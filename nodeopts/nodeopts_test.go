package nodeopts_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"brod/nodeopts"
	"brod/translator"
	"brod/warp"
)

var linkRe = regexp.MustCompile(`"((?:vless|vmess|trojan|hy2|hysteria2|hysteria|tuic|anytls|socks[45a]?|ss|ssr|wireguard|wg)://[^"\s]+)"`)

// Всё, что выдаёт translator на ссылках из его же тестов, должно проходить белый список без потерь:
// иначе рабочие узлы перестанут подключаться.
func TestWhitelistAcceptsTranslatorOutput(t *testing.T) {
	var links []string
	for _, f := range []string{"../translator/translator_test.go", "../translator/wgquick_test.go", "../translator/coldstart_test.go", "../importer/importer_test.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range linkRe.FindAllStringSubmatch(string(b), -1) {
			links = append(links, m[1])
		}
	}
	ok := 0
	for _, l := range links {
		o, err := translator.Parse(l)
		if err != nil {
			continue
		}
		raw, _ := json.Marshal(o.Opts)
		clean, err := nodeopts.Sanitize(raw, nodeopts.Flags{})
		if err != nil {
			t.Errorf("%.40s…: %v", l, err)
			continue
		}
		var a, b any
		_ = json.Unmarshal(raw, &a)
		_ = json.Unmarshal(clean, &b)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%.40s…: белый список изменил параметры:\n%s\n%s", l, ja, jb)
		}
		ok++
	}
	if ok < 20 {
		t.Fatalf("разобрано всего %d ссылок из тестов: корпус потерян", ok)
	}
	t.Logf("проверено ссылок: %d", ok)
}

func TestWhitelistAcceptsWarpEndpoint(t *testing.T) {
	acc := &warp.Account{AddrV4: "172.16.0.2", AddrV6: "fd01::2", PeerPublicKey: "pk", PrivateKey: "sk"}
	for _, o := range []warp.Options{{}, {Mask: "quic"}, {Mask: "dns", MaskHost: "x.example"}, {IPv4Only: true}} {
		raw, _ := json.Marshal(acc.Endpoint(o))
		if _, err := nodeopts.Sanitize(raw, nodeopts.Flags{Tag: true}); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

func TestWhitelistRejects(t *testing.T) {
	bad := map[string]string{
		"tor":                  `{"type":"tor","executable_path":"/bin/sh"}`,
		"ssh":                  `{"type":"ssh","server":"a","private_key_path":"/etc/shadow"}`,
		"bridge":               `{"type":"bridge"}`,
		"direct":               `{"type":"direct"}`,
		"selector":             `{"type":"selector","outbounds":["a"]}`,
		"нет типа":             `{"server":"a"}`,
		"путь к сертификату":   `{"type":"vless","server":"a","server_port":1,"uuid":"u","tls":{"enabled":true,"certificate_path":"/etc/passwd"}}`,
		"сертификат строкой":   `{"type":"trojan","server":"a","server_port":1,"password":"p","tls":{"enabled":true,"certificate":["x"]}}`,
		"ech":                  `{"type":"vless","server":"a","server_port":1,"uuid":"u","tls":{"ech":{"enabled":true,"config_path":"/x"}}}`,
		"detour":               `{"type":"vless","server":"a","server_port":1,"uuid":"u","detour":"direct"}`,
		"bind_interface":       `{"type":"vless","server":"a","server_port":1,"uuid":"u","bind_interface":"eth0"}`,
		"protect_path":         `{"type":"vless","server":"a","server_port":1,"uuid":"u","protect_path":"/run/x.sock"}`,
		"регистр имени":        `{"type":"vless","server":"a","server_port":1,"uuid":"u","TLS":{"enabled":true}}`,
		"регистр типа":         `{"Type":"tor","type":"vless","server":"a"}`,
		"повтор поля":          `{"type":"vless","type":"tor","server":"a","server_port":1,"uuid":"u"}`,
		"повтор во вложенном":  `{"type":"vless","server":"a","server_port":1,"uuid":"u","tls":{"enabled":true,"enabled":false}}`,
		"transport свой тип":   `{"type":"vless","server":"a","server_port":1,"uuid":"u","transport":{"type":"quic"}}`,
		"transport лишнее":     `{"type":"vless","server":"a","server_port":1,"uuid":"u","transport":{"type":"ws","executable":"x"}}`,
		"порт строкой":         `{"type":"vless","server":"a","server_port":"1","uuid":"u"}`,
		"пир с listen_port":    `{"type":"wireguard","address":["10.0.0.2/32"],"private_key":"k","listen_port":51820,"peers":[]}`,
		"пир: лишнее поле":     `{"type":"wireguard","address":["10.0.0.2/32"],"private_key":"k","peers":[{"address":"a","port":1,"public_key":"p","allowed_ips":["0.0.0.0/0"],"x":1}]}`,
		"плагин неизвестный":   `{"type":"shadowsocks","server":"a","server_port":1,"method":"aes-128-gcm","password":"p","plugin":"/bin/sh"}`,
		"хвост после объекта":  `{"type":"vless","server":"a","server_port":1,"uuid":"u"} {"type":"tor"}`,
		"не объект":            `["tor"]`,
		"слишком глубоко":      `{"type":"vless","server":"a","server_port":1,"uuid":"u","tls":{"utls":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{}}}}}}}}}}`,
		"headers не строки":    `{"type":"vless","server":"a","server_port":1,"uuid":"u","transport":{"type":"ws","headers":{"Host":["a"]}}}`,
		"объект вместо строки": `{"type":"vless","server":{"a":1},"server_port":1,"uuid":"u"}`,
	}
	for name, raw := range bad {
		if _, err := nodeopts.Sanitize([]byte(raw), nodeopts.Flags{}); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
	// tag разрешён только когда его просят (ставит ядро), detour — тоже
	tagged := `{"type":"vless","tag":"x","detour":"warp","server":"a","server_port":1,"uuid":"u"}`
	if _, err := nodeopts.Sanitize([]byte(tagged), nodeopts.Flags{}); err == nil {
		t.Error("tag/detour приняты без разрешения")
	}
	if _, err := nodeopts.Sanitize([]byte(tagged), nodeopts.Flags{Tag: true, Detour: true}); err != nil {
		t.Errorf("tag/detour от ядра отклонены: %v", err)
	}
}

func TestPluginOptsWhitelist(t *testing.T) {
	got, dropped := nodeopts.PluginOpts(`tls;host=a.example;cert=/etc/ssl/x;key=/k;certRaw=zz;path=/p\;q;mux=4;fast-open`)
	if got != `tls;host=a.example;path=/p\;q;mux=4` {
		t.Errorf("got %q", got)
	}
	if strings.Join(dropped, ",") != "cert,certRaw,fast-open,key" {
		t.Errorf("dropped %v", dropped)
	}
	// экранированный разделитель не должен прятать ключ внутри значения
	got, _ = nodeopts.PluginOpts(`host=a\;cert=/etc/passwd`)
	if got != `host=a\;cert=/etc/passwd` {
		t.Errorf("значение с экранированным ; разобрано как два ключа: %q", got)
	}
	clean, err := nodeopts.Sanitize([]byte(`{"type":"shadowsocks","server":"a","server_port":1,"method":"aes-128-gcm","password":"p","plugin":"v2ray-plugin","plugin_opts":"tls;cert=/etc/passwd"}`), nodeopts.Flags{})
	if err != nil || strings.Contains(string(clean), "cert") {
		t.Errorf("cert в итоговых параметрах: %s %v", clean, err)
	}
}
