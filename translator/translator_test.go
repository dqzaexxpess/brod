package translator

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Фикстуры обезличены: UUID, ключи и хосты вымышленные (RFC 5737 / example.*).
const (
	uuid1 = "11111111-2222-4333-8444-555555555555"
	pbk   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 символа base64url = 32 байта
)

func mustParse(t *testing.T, link string) *Outbound {
	t.Helper()
	o, err := Parse(link)
	if err != nil {
		t.Fatalf("Parse(%q): %v", link, err)
	}
	return o
}

func reasonOf(t *testing.T, link string) Reason {
	t.Helper()
	_, err := Parse(link)
	var te *Error
	if !errors.As(err, &te) {
		t.Fatalf("ожидалась *Error, получили %v", err)
	}
	return te.Reason
}

func TestVLESSRealityVision(t *testing.T) {
	o := mustParse(t, "vless://"+uuid1+"@192.0.2.1:443?type=tcp&security=reality&sni=example.com&fp=chrome&pbk="+pbk+"&sid=ab12&flow=xtls-rprx-vision#n")
	tls := o.Opts["tls"].(map[string]any)
	if tls["reality"].(map[string]any)["public_key"] != pbk || o.Opts["flow"] != "xtls-rprx-vision" {
		t.Fatalf("неверный разбор: %v", o.Opts)
	}
	if _, has := o.Opts["transport"]; has {
		t.Fatal("для tcp транспорта быть не должно")
	}
}

func TestHTMLEscapedLink(t *testing.T) {
	o := mustParse(t, "vless://"+uuid1+"@192.0.2.1:443?security=reality&amp;pbk="+pbk+"&amp;sid=ab&amp;type=tcp&amp;sni=example.com")
	if o.Opts["tls"].(map[string]any)["reality"] == nil {
		t.Fatal("&amp; не нормализован")
	}
}

func TestRealityWithoutKeyRejected(t *testing.T) {
	if r := reasonOf(t, "vless://"+uuid1+"@192.0.2.1:443?security=reality&sni=example.com"); r != ReasonMissingField {
		t.Fatalf("reason=%s", r)
	}
}

func TestCustomIDAndUDP443Flow(t *testing.T) {
	o := mustParse(t, "vless://my-custom-id@192.0.2.1:443?security=tls&flow=xtls-rprx-vision-udp443")
	if o.Opts["flow"] != "xtls-rprx-vision" {
		t.Fatalf("flow=%v", o.Opts["flow"])
	}
	if !strings.Contains(strings.Join(o.Notes, ","), "custom-id") {
		t.Fatalf("нет пометки custom-id: %v", o.Notes)
	}
}

func TestXHTTPExtra(t *testing.T) {
	extra := `%7B%22xPaddingBytes%22%3A%7B%22from%22%3A100%2C%22to%22%3A1000%7D%2C%22scMaxEachPostBytes%22%3A1000000%2C%22xmux%22%3A%7B%22maxConcurrency%22%3A%2216-32%22%7D%2C%22unknownKey%22%3A1%7D`
	o := mustParse(t, "vless://"+uuid1+"@192.0.2.1:443?type=xhttp&security=tls&path=/p&mode=packet-up&extra="+extra)
	tr := o.Opts["transport"].(map[string]any)
	if tr["x_padding_bytes"] != "100-1000" || tr["sc_max_each_post_bytes"] != "1000000" {
		t.Fatalf("диапазоны должны быть строками: %v", tr)
	}
	if tr["xmux"].(map[string]any)["max_concurrency"] != "16-32" {
		t.Fatalf("xmux: %v", tr["xmux"])
	}
	if _, bad := tr["unknownKey"]; bad || !strings.Contains(strings.Join(o.Notes, ","), "dropped:unknownKey") {
		t.Fatalf("белый список не сработал: %v %v", tr, o.Notes)
	}
}

func TestXHTTPDownloadSettingsFlagged(t *testing.T) {
	extra := `%7B%22downloadSettings%22%3A%7B%22address%22%3A%22example.net%22%7D%7D`
	o := mustParse(t, "vless://"+uuid1+"@192.0.2.1:443?type=xhttp&security=tls&extra="+extra)
	if !o.Flags.DownloadSplit {
		t.Fatal("downloadSettings должен помечать узел")
	}
	if _, has := o.Opts["transport"].(map[string]any)["download"]; has {
		t.Fatal("downloadSettings не должен попадать в выход")
	}
}

func TestXHTTPNullExtraAndEmptySessionTable(t *testing.T) {
	o := mustParse(t, "vless://"+uuid1+"@192.0.2.1:443?type=xhttp&security=tls&extra=null")
	if o.Opts["transport"].(map[string]any)["mode"] != "auto" {
		t.Fatal("mode по умолчанию должен быть auto")
	}
	extra := `%7B%22sessionIDTable%22%3A%22%22%2C%22sessionIDLength%22%3A0%7D`
	o = mustParse(t, "vless://"+uuid1+"@192.0.2.1:443?type=xhttp&security=tls&extra="+extra)
	tr := o.Opts["transport"].(map[string]any)
	if _, has := tr["session_table"]; has {
		t.Fatalf("пустая таблица должна считаться незаданной: %v", tr)
	}
}

func TestWSEarlyDataAndBadPercentPath(t *testing.T) {
	o := mustParse(t, "vless://"+uuid1+"@192.0.2.1:443?type=ws&security=tls&host=example.com&path=%2Fws%3Fed%3D2048")
	tr := o.Opts["transport"].(map[string]any)
	if tr["max_early_data"] != 2048 || tr["path"] != "/ws" {
		t.Fatalf("ed: %v", tr)
	}
	if got := fixPath("/a%zz/b%"); got != "/a%25zz/b%25" {
		t.Fatalf("fixPath=%q", got)
	}
}

func TestRejections(t *testing.T) {
	cases := map[string]Reason{
		"vless://" + uuid1 + "@192.0.2.1:443?type=kcp":                   ReasonBadTransport,
		"vless://" + uuid1 + "@192.0.2.1:443?security=tl...":             ReasonTruncated,
		"vless://" + uuid1 + "@192.0.2.1:99999?security=tls":             ReasonBadPort,
		"vless://@192.0.2.1:443":                                         ReasonBadUUID,
		"mieru://x@192.0.2.1:1":                                          ReasonUnknownScheme,
		"vless://" + uuid1 + "@192.0.2.1:443?type=xhttp&extra=%7Bbroken": ReasonBadExtra,
	}
	for link, want := range cases {
		if got := reasonOf(t, link); got != want {
			t.Errorf("%s: got %s want %s", link, got, want)
		}
	}
}

func TestOtherProtocols(t *testing.T) {
	// vmess: {"v":"2","add":"192.0.2.2","port":"443","id":"<uuid>","aid":"0","net":"ws","tls":"tls","path":"/x","host":"example.com"}
	vm := "vmess://eyJ2IjoiMiIsImFkZCI6IjE5Mi4wLjIuMiIsInBvcnQiOiI0NDMiLCJpZCI6IjExMTExMTExLTIyMjItNDMzMy04NDQ0LTU1NTU1NTU1NTU1NSIsImFpZCI6IjAiLCJuZXQiOiJ3cyIsInRscyI6InRscyIsInBhdGgiOiIveCIsImhvc3QiOiJleGFtcGxlLmNvbSJ9"
	if o := mustParse(t, vm); o.Opts["type"] != "vmess" || o.Opts["transport"].(map[string]any)["type"] != "ws" {
		t.Fatalf("vmess: %v", o.Opts)
	}
	if o := mustParse(t, "trojan://secret@192.0.2.3:443?sni=example.com"); o.Opts["tls"] == nil {
		t.Fatal("у trojan TLS по умолчанию")
	}
	if o := mustParse(t, "hysteria2://auth@192.0.2.4:443?sni=example.com&insecure=1&obfs=salamander&obfs-password=p"); !o.Flags.Insecure || o.Opts["obfs"] == nil {
		t.Fatalf("hy2: %v", o.Opts)
	}
}

func TestBadURLDetailHasNoLink(t *testing.T) {
	_, err := Parse("vless://bad host@192.0.2.9:443?x=1")
	if err != nil && strings.Contains(err.Error(), "192.0.2.9") {
		t.Fatalf("деталь ошибки содержит хост: %v", err)
	}
}

func TestShadowsocks(t *testing.T) {
	// SIP002: base64("aes-256-gcm:pw123")@host:port
	o := mustParse(t, "ss://YWVzLTI1Ni1nY206cHcxMjM@192.0.2.5:8388#tag")
	if o.Opts["method"] != "aes-256-gcm" || o.Opts["password"] != "pw123" || o.Opts["server_port"] != 8388 {
		t.Fatalf("SIP002: %v", o.Opts)
	}
	// старый вид: base64("aes-256-gcm:pw123@192.0.2.5:8388")
	o = mustParse(t, "ss://YWVzLTI1Ni1nY206cHcxMjNAMTkyLjAuMi41OjgzODg=#tag")
	if o.Opts["server"] != "192.0.2.5" || o.Opts["method"] != "aes-256-gcm" {
		t.Fatalf("legacy: %v", o.Opts)
	}
	// не-base64 userinfo
	o = mustParse(t, "ss://chacha20-ietf-poly1305:secret@192.0.2.6:443")
	if o.Opts["password"] != "secret" {
		t.Fatalf("plain: %v", o.Opts)
	}
	// простой obfs-плагин
	o = mustParse(t, "ss://YWVzLTI1Ni1nY206cHcxMjM@192.0.2.5:8388?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dexample.com")
	if o.Opts["plugin"] != "obfs-local" {
		t.Fatalf("plugin: %v", o.Opts)
	}
	if r := reasonOf(t, "ss://rc5-cfb:pw@192.0.2.7:1"); r != ReasonSSMethod {
		t.Fatalf("reason=%s", r)
	}
}

func TestMoreProtocols(t *testing.T) {
	// TUIC
	o := mustParse(t, "tuic://"+uuid1+":pw@192.0.2.8:443?congestion_control=bbr&udp_relay_mode=native&alpn=h3&sni=example.com&allow_insecure=1")
	if o.Opts["type"] != "tuic" || o.Opts["congestion_control"] != "bbr" || !o.Flags.Insecure {
		t.Fatalf("tuic: %v", o.Opts)
	}
	if r := reasonOf(t, "tuic://not-a-uuid:pw@192.0.2.8:443"); r != ReasonBadUUID {
		t.Fatalf("tuic uuid: %s", r)
	}
	// Hysteria v1
	o = mustParse(t, "hysteria://192.0.2.9:443?auth=secret&peer=example.com&insecure=1&upmbps=20&downmbps=100&obfsParam=salt")
	if o.Opts["type"] != "hysteria" || o.Opts["auth_str"] != "secret" || o.Opts["up_mbps"] != 20 || o.Opts["obfs"] != "salt" {
		t.Fatalf("hysteria: %v", o.Opts)
	}
	if r := reasonOf(t, "hysteria://192.0.2.9:443?auth=x&protocol=faketcp"); r != ReasonBadTransport {
		t.Fatalf("hysteria faketcp: %s", r)
	}
	// AnyTLS
	o = mustParse(t, "anytls://pw@192.0.2.10:443?sni=example.com&insecure=1")
	if o.Opts["type"] != "anytls" || o.Opts["password"] != "pw" {
		t.Fatalf("anytls: %v", o.Opts)
	}
	// SOCKS
	o = mustParse(t, "socks5://user:pass@192.0.2.11:1080")
	if o.Opts["type"] != "socks" || o.Opts["username"] != "user" || !o.Flags.NoSecurity {
		t.Fatalf("socks: %v", o.Opts)
	}
	o = mustParse(t, "socks5://dXNlcjpwYXNz@192.0.2.11:1080") // base64("user:pass")
	if o.Opts["username"] != "user" || o.Opts["password"] != "pass" {
		t.Fatalf("socks b64: %v", o.Opts)
	}
	// WireGuard (+ AmneziaWG-параметры)
	o = mustParse(t, "wireguard://cHJpdmF0ZUtleUJhc2U2NFZhbHVlMDEyMzQ1Njc4OTA%3D@192.0.2.12:2408?publickey=cHVibGljS2V5QmFzZTY0VmFsdWVBQUFBQUFBQUFBQUE%3D&address=172.16.0.2/32,fd00::2/128&mtu=1280&reserved=43,144,144&jc=4&jmin=40&jmax=70#n")
	if !o.Endpoint || o.Opts["type"] != "wireguard" || o.Opts["jc"] != uint64(4) {
		t.Fatalf("wireguard: %v", o.Opts)
	}
	if peers := o.Opts["peers"].([]any); peers[0].(map[string]any)["reserved"] == nil {
		t.Fatalf("reserved потерян: %v", peers)
	}
	if r := reasonOf(t, "wireguard://k@192.0.2.12:2408"); r != ReasonMissingField {
		t.Fatalf("wg без ключа/адреса: %s", r)
	}
}

func TestSchemeFixedSS(t *testing.T) {
	o := mustParse(t, "ss://"+uuid1+"@192.0.2.20:443?security=tls&type=ws&path=%2Fw&sni=example.com&encryption=none")
	if o.Opts["type"] != "vless" || !strings.Contains(strings.Join(o.Notes, ","), "scheme-fixed") {
		t.Fatalf("ss->vless: %v %v", o.Opts, o.Notes)
	}
	o = mustParse(t, "ss://secretpass@192.0.2.21:443?security=tls&type=tcp&sni=example.com")
	if o.Opts["type"] != "trojan" {
		t.Fatalf("ss->trojan: %v", o.Opts)
	}
	// настоящий ss не затрагивается
	if o := mustParse(t, "ss://YWVzLTI1Ni1nY206cHcxMjM@192.0.2.5:8388"); o.Opts["type"] != "shadowsocks" {
		t.Fatalf("ss сломан: %v", o.Opts)
	}
	// headerType не http — не причина отказа
	mustParse(t, "vless://"+uuid1+"@192.0.2.22:443?type=tcp&security=tls&headerType=vmess")
}

func TestTCPHTTPObfs(t *testing.T) {
	o := mustParse(t, "vless://"+uuid1+"@192.0.2.30:80?type=tcp&security=none&headerType=http&host=a.example,b.example&path=%2Fp1,%2Fp2&flow=xtls-rprx-vision")
	tr := o.Opts["transport"].(map[string]any)
	if tr["type"] != "raw" || tr["header_type"] != "http" {
		t.Fatalf("transport: %v", tr)
	}
	if h := tr["host"].([]string); len(h) != 2 || h[0] != "a.example" {
		t.Fatalf("host: %v", tr["host"])
	}
	if _, has := o.Opts["flow"]; has {
		t.Fatal("flow vision несовместим с обфускацией и должен быть отброшен")
	}
	// без обфускации raw не нужен
	if o := mustParse(t, "vless://"+uuid1+"@192.0.2.30:443?type=tcp&security=tls"); o.Opts["transport"] != nil {
		t.Fatal("для обычного tcp транспорта быть не должно")
	}
}

func TestSSR(t *testing.T) {
	// base64("192.0.2.40:8388:auth_aes128_md5:aes-256-cfb:tls1.2_ticket_auth:cGFzcw/?obfsparam=ZXhhbXBsZS5jb20&protoparam=MTIzOnBhc3M&remarks=eA")
	link := "ssr://MTkyLjAuMi40MDo4Mzg4OmF1dGhfYWVzMTI4X21kNTphZXMtMjU2LWNmYjp0bHMxLjJfdGlja2V0X2F1dGg6Y0dGemN3Lz9vYmZzcGFyYW09WlhoaGJYQnNaUzVqYjIwJnByb3RvcGFyYW09TVRJek9uQmhjM00mcmVtYXJrcz1lQQ"
	o := mustParse(t, link)
	if o.Opts["type"] != "shadowsocksr" || o.Opts["server"] != "192.0.2.40" || o.Opts["password"] != "pass" ||
		o.Opts["protocol"] != "auth_aes128_md5" || o.Opts["obfs"] != "tls1.2_ticket_auth" || o.Opts["obfs_param"] != "example.com" || o.Opts["protocol_param"] != "123:pass" {
		t.Fatalf("ssr: %v", o.Opts)
	}
	if r := reasonOf(t, "ssr://bm9wZQ"); r != ReasonMissingField {
		t.Fatalf("ssr мусор: %s", r)
	}
}

// Ссылка «поделиться»: Parse(ToLink(opts)) даёт те же параметры, что и исходный разбор.
func TestToLinkRoundTrip(t *testing.T) {
	links := []string{
		"vless://0b0a0c0d-1111-2222-3333-444455556666@example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.apple.com&fp=chrome&pbk=" + pbk + "&sid=ab12&type=tcp#r",
		"vless://0b0a0c0d-1111-2222-3333-444455556666@example.com:443?security=tls&sni=cdn.example.com&type=ws&path=%2Fws%3Fed%3D2048&host=cdn.example.com&alpn=h2,http/1.1&fp=firefox#ws",
		"vless://0b0a0c0d-1111-2222-3333-444455556666@example.com:8443?security=tls&sni=a.example.com&type=grpc&serviceName=svc#grpc",
		"vless://0b0a0c0d-1111-2222-3333-444455556666@example.com:443?security=tls&sni=a.example.com&type=httpupgrade&path=%2Fu&host=a.example.com#hu",
		"trojan://pa55word@example.com:443?security=tls&sni=t.example.com&type=tcp#tr",
		"hysteria2://secretpw@example.com:443?sni=h.example.com&insecure=1&obfs=salamander&obfs-password=ob#hy",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:passw0rd")) + "@example.com:8388#ss",
	}
	for _, l := range links {
		o1, err := Parse(l)
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		out, err := ToLink(o1.Opts, "имя")
		if err != nil {
			t.Fatalf("%s: экспорт: %v", l, err)
		}
		o2, err := Parse(out)
		if err != nil {
			t.Fatalf("%s: повторный разбор %q: %v", l, out, err)
		}
		if !reflect.DeepEqual(o1.Opts, o2.Opts) {
			t.Errorf("%s\n  экспорт: %s\n  было:  %v\n  стало: %v", l, out, o1.Opts, o2.Opts)
		}
	}
	// vmess: через собственный формат
	vm := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"x","add":"v.example.com","port":"443","id":"0b0a0c0d-1111-2222-3333-444455556666","aid":"0","scy":"auto","net":"ws","type":"none","host":"v.example.com","path":"/vm","tls":"tls","sni":"v.example.com"}`))
	o1, err := Parse(vm)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ToLink(o1.Opts, "v")
	if err != nil {
		t.Fatal(err)
	}
	if o2, err := Parse(out); err != nil || !reflect.DeepEqual(o1.Opts, o2.Opts) {
		t.Errorf("vmess: %v\n  было:  %v\n  стало: %v", err, o1.Opts, func() any {
			if o2 != nil {
				return o2.Opts
			}
			return nil
		}())
	}
	if _, err := ToLink(map[string]any{"type": "wireguard", "server": "x", "server_port": 1}, ""); err == nil {
		t.Error("для wireguard ссылка не поддерживается")
	}
}

// cert=/key= в опциях плагина v2ray-plugin из ссылки ss:// заставили бы демон читать файл на диске.
func TestSSPluginOptsDropFilePaths(t *testing.T) {
	o, err := Parse("ss://YWVzLTEyOC1nY206cGFzcw@example.org:8388?plugin=v2ray-plugin%3Btls%3Bhost%3Dx.example%3Bcert%3D%2Fetc%2Fpasswd%3Bkey%3D%2Fk")
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Opts["plugin_opts"]; got != "tls;host=x.example" {
		t.Fatalf("plugin_opts = %q", got)
	}
	if !strings.Contains(strings.Join(o.Notes, ","), "plugin:cert") {
		t.Errorf("потеря cert= не отмечена в заметках: %v", o.Notes)
	}
}
