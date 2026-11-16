package importer_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"brod/importer"
	"brod/translator"
)

func parseAll(t *testing.T, links []string) []*translator.Outbound {
	t.Helper()
	var out []*translator.Outbound
	for _, l := range links {
		o, err := translator.Parse(l)
		if err != nil {
			t.Fatalf("translator не принял ссылку %q: %v", l, err)
		}
		out = append(out, o)
	}
	return out
}

const uid = "5b0e5c2e-0000-4000-8000-0000000000b1"

const singBoxDoc = `{
 "log":{"level":"info"},
 "dns":{"servers":[{"address":"https://evil.example/dns"}]},
 "inbounds":[{"type":"tun","tag":"tun-in","auto_route":true},{"type":"mixed","listen":"0.0.0.0","listen_port":7890}],
 "route":{"rules":[{"domain":["bank.example"],"outbound":"proxy"}],"final":"proxy"},
 "experimental":{"clash_api":{"external_controller":"0.0.0.0:9090","secret":""}},
 "outbounds":[
  {"type":"selector","tag":"proxy","outbounds":["a","b"]},
  {"type":"urltest","tag":"auto","outbounds":["a"]},
  {"type":"direct","tag":"direct"},
  {"type":"block","tag":"block"},
  {"type":"vless","tag":"a","server":"v1.example","server_port":443,"uuid":"` + uid + `","flow":"xtls-rprx-vision",
   "detour":"direct","bind_interface":"eth0","routing_mark":1,"inet4_bind_address":"10.0.0.1","tcp_fast_open":true,
   "tls":{"enabled":true,"server_name":"www.cdn.example","utls":{"enabled":true,"fingerprint":"chrome"},
          "reality":{"enabled":true,"public_key":"bmXOC-F1FxEMF9dyiK2H5_1SUtzH0JuVo51h2wPfgyo","short_id":"ab12"}}},
  {"type":"vmess","tag":"b","server":"v2.example","server_port":8443,"uuid":"` + uid + `","alter_id":0,"security":"auto",
   "tls":{"enabled":true,"server_name":"v2.example","insecure":true},
   "transport":{"type":"ws","path":"/ws","headers":{"Host":"h.example"}}},
  {"type":"trojan","tag":"c","server":"t.example","server_port":443,"password":"pw","tls":{"enabled":true,"server_name":"t.example"},
   "transport":{"type":"grpc","service_name":"svc"}},
  {"type":"shadowsocks","tag":"d","server":"s.example","server_port":8388,"method":"chacha20-ietf-poly1305","password":"pw"},
  {"type":"hysteria2","tag":"e","server":"h2.example","server_port":4443,"password":"pw","tls":{"enabled":true,"server_name":"h2.example"},
   "obfs":{"type":"salamander","password":"op"}},
  {"type":"tuic","tag":"f","server":"tu.example","server_port":4444,"uuid":"` + uid + `","password":"pw","congestion_control":"bbr","tls":{"enabled":true,"server_name":"tu.example"}},
  {"type":"anytls","tag":"g","server":"an.example","server_port":4445,"password":"pw","tls":{"enabled":true,"server_name":"an.example"}},
  {"type":"socks","tag":"h","server":"so.example","server_port":1080,"username":"u","password":"p"},
  {"type":"wireguard","tag":"w","server":"wg.example","server_port":51820}
 ]}`

// Из полного конфига sing-box берутся только узлы; всё остальное (inbounds, route, dns, clash_api, detour, bind_interface…) не доходит до результата.
func TestSingBoxDocumentKeepsOnlyNodes(t *testing.T) {
	links, ok := importer.Links([]byte(singBoxDoc))
	if !ok {
		t.Fatal("конфиг sing-box не распознан")
	}
	outs := parseAll(t, links)
	protos := map[string]int{}
	for _, o := range outs {
		protos[o.Proto]++
	}
	// 8 узлов: vless, vmess, trojan, ss, hysteria2, tuic, anytls, socks; selector/urltest/direct/block/wireguard пропущены
	if len(outs) != 8 {
		t.Fatalf("узлов %d: %v\n%s", len(outs), protos, strings.Join(links, "\n"))
	}
	for _, o := range outs {
		raw, _ := json.Marshal(o.Opts)
		for _, bad := range []string{"detour", "bind_interface", "routing_mark", "inet4_bind_address", "tcp_fast_open", "0.0.0.0", "evil.example", "clash_api", "external_controller"} {
			if strings.Contains(string(raw)+strings.Join(links, " "), bad) {
				t.Fatalf("в результат просочилось %q: %s", bad, raw)
			}
		}
	}
	// содержимое сохранилось: reality и транспорт
	var v1 *translator.Outbound
	for _, o := range outs {
		if o.Proto == "vless" {
			v1 = o
		}
	}
	tls, _ := v1.Opts["tls"].(map[string]any)
	if v1.Opts["server"] != "v1.example" || tls["reality"] == nil || v1.Opts["flow"] != "xtls-rprx-vision" {
		t.Fatalf("vless: %+v", v1.Opts)
	}
}

// Одна и та же нода из ссылки и из конфига получает один ключ дедупликации.
func TestSameNodeDedupsAcrossFormats(t *testing.T) {
	link := "vless://" + uid + "@v1.example:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.cdn.example&fp=chrome&pbk=bmXOC-F1FxEMF9dyiK2H5_1SUtzH0JuVo51h2wPfgyo&sid=ab12&type=tcp#a"
	direct, err := translator.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	links, _ := importer.Links([]byte(singBoxDoc))
	for _, o := range parseAll(t, links) {
		if o.Proto == "vless" && o.Key == direct.Key {
			return
		}
	}
	t.Fatalf("ключ из конфига не совпал с ключом ссылки (%s)", direct.Key)
}

const clashDoc = `
port: 7890
mixed-port: 7891
allow-lan: true
external-controller: 0.0.0.0:9090
secret: ""
dns: {enable: true, nameserver: [evil.example]}
rules: ["DOMAIN,bank.example,PROXY"]
proxy-groups: [{name: PROXY, type: select, proxies: [a]}]
proxies:
  - {name: a, type: vless, server: v1.example, port: 443, uuid: ` + uid + `, tls: true, servername: www.cdn.example, network: ws, ws-opts: {path: /w, headers: {Host: h.example}}, client-fingerprint: chrome}
  - {name: b, type: trojan, server: t.example, port: 443, password: pw, sni: t.example, skip-cert-verify: true}
  - {name: c, type: ss, server: s.example, port: 8388, cipher: aes-256-gcm, password: pw}
  - {name: d, type: hysteria2, server: h2.example, port: 4443, password: pw, sni: h2.example}
  - {name: e, type: vmess, server: v2.example, port: 443, uuid: ` + uid + `, alterId: 0, cipher: auto, tls: true, network: grpc, grpc-opts: {grpc-service-name: svc}}
  - {name: r, type: ssr, server: r.example, port: 1, cipher: aes-256-cfb, password: pw}
  - {name: w, type: wireguard, server: wg.example, port: 51820}
`

func TestClashDocument(t *testing.T) {
	links, ok := importer.Links([]byte(clashDoc))
	if !ok {
		t.Fatal("Clash не распознан")
	}
	outs := parseAll(t, links)
	if len(outs) != 5 {
		t.Fatalf("узлов %d (ssr и wireguard не выражаются ссылками): %v", len(outs), links)
	}
	all := strings.Join(links, " ")
	for _, bad := range []string{"evil.example", "bank.example", "9090", "7890"} {
		if strings.Contains(all, bad) {
			t.Fatalf("просочилось %q", bad)
		}
	}
	for _, o := range outs {
		if o.Proto == "trojan" && !o.Flags.Insecure {
			t.Fatal("skip-cert-verify потерян (должен давать пометку insecure)")
		}
	}
}

const xrayDoc = `{
 "log":{"loglevel":"debug"},
 "inbounds":[{"port":1080,"listen":"0.0.0.0","protocol":"socks"}],
 "routing":{"rules":[{"type":"field","outboundTag":"direct","domain":["bank.example"]}]},
 "api":{"tag":"api","services":["HandlerService"]},
 "outbounds":[
  {"tag":"p","protocol":"vless","settings":{"vnext":[{"address":"v1.example","port":443,"users":[{"id":"` + uid + `","flow":"xtls-rprx-vision","encryption":"none"}]}]},
   "streamSettings":{"network":"tcp","security":"reality","sockopt":{"mark":255,"interface":"eth0"},
     "realitySettings":{"serverName":"www.cdn.example","publicKey":"bmXOC-F1FxEMF9dyiK2H5_1SUtzH0JuVo51h2wPfgyo","shortId":"ab12","fingerprint":"chrome"}}},
  {"protocol":"vmess","settings":{"vnext":[{"address":"v2.example","port":443,"users":[{"id":"` + uid + `","alterId":0,"security":"auto"}]}]},
   "streamSettings":{"network":"ws","security":"tls","tlsSettings":{"serverName":"v2.example"},"wsSettings":{"path":"/ws","headers":{"Host":"h.example"}}}},
  {"protocol":"trojan","settings":{"servers":[{"address":"t.example","port":443,"password":"pw"}]},"streamSettings":{"security":"tls","tlsSettings":{"serverName":"t.example"}}},
  {"protocol":"shadowsocks","settings":{"servers":[{"address":"s.example","port":8388,"method":"aes-256-gcm","password":"pw"}]}},
  {"protocol":"vless","settings":{"vnext":[{"address":"x.example","port":80,"users":[{"id":"` + uid + `"}]}]},
   "streamSettings":{"network":"tcp","tcpSettings":{"header":{"type":"http","request":{"path":["/x","/y"],"headers":{"Host":["a.example"]}}}}}},
  {"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"block"},{"protocol":"dns","tag":"dns-out"}
 ]}`

func TestXrayDocumentAndList(t *testing.T) {
	links, ok := importer.Links([]byte(xrayDoc))
	if !ok {
		t.Fatal("Xray не распознан")
	}
	outs := parseAll(t, links)
	if len(outs) != 5 {
		t.Fatalf("узлов %d: %v", len(outs), links)
	}
	all := strings.Join(links, " ")
	for _, bad := range []string{"0.0.0.0", "bank.example", "sockopt", "eth0", "255", "HandlerService"} {
		if strings.Contains(all, bad) {
			t.Fatalf("просочилось %q", bad)
		}
	}
	// HTTP-обфускация tcp (плагин rawhttp) сохранилась
	obfs := false
	for _, o := range outs {
		if tr, _ := o.Opts["transport"].(map[string]any); tr["type"] == "raw" {
			obfs = true
		}
	}
	if !obfs {
		t.Fatal("tcp с HTTP-обфускацией не распознан")
	}
	// массив конфигов (v2rayN): объединение
	list := "[" + xrayDoc + "," + xrayDoc + "]"
	again, ok := importer.Links([]byte(list))
	if !ok || len(again) != len(links) { // одинаковые узлы схлопываются
		t.Fatalf("массив конфигов: ok=%v узлов %d (ожидали %d после дедупликации)", ok, len(again), len(links))
	}
}

func TestNotADocument(t *testing.T) {
	for name, body := range map[string]string{
		"список ссылок":    "vless://a@h:1\ntrojan://b@h:2",
		"пусто":            "",
		"html":             "<html>404</html>",
		"json без узлов":   `{"hello":"world"}`,
		"битый json":       `{"outbounds":[`,
		"yaml без proxies": "foo: bar\nbaz: [1,2]",
		"массив чисел":     `[1,2,3]`,
	} {
		if links, ok := importer.Links([]byte(body)); ok || len(links) != 0 {
			t.Errorf("%s: распознан как конфиг (%v)", name, links)
		}
	}
}

// Мусорные и враждебные значения не ломают разбор и не дают ссылок с управляющими символами.
func TestHostileValues(t *testing.T) {
	doc := `{"outbounds":[
	 {"type":"vless","server":"evil.example/../x","server_port":443,"uuid":"` + uid + `"},
	 {"type":"vless","server":"a b.example","server_port":443,"uuid":"` + uid + `"},
	 {"type":"vless","server":"ok.example","server_port":0,"uuid":"` + uid + `"},
	 {"type":"vless","server":"ok.example","server_port":70000,"uuid":"` + uid + `"},
	 {"type":"vless","server":"ok.example","server_port":443,"uuid":"` + uid + `\nINJECT"},
	 {"type":"trojan","server":"ok.example","server_port":443,"password":""},
	 {"type":"vless","server":"user@ok.example","server_port":443,"uuid":"` + uid + `"},
	 {"type":"vless","server":"good.example","server_port":443,"uuid":"` + uid + `","tag":"line1\r\nline2"},
	 {"type":"vless","server":"[2001:db8::1]","server_port":443,"uuid":"` + uid + `"}
	]}`
	links, ok := importer.Links([]byte(doc))
	if !ok {
		t.Fatal("документ не распознан")
	}
	for _, l := range links {
		if strings.ContainsAny(l, "\r\n") || strings.Contains(l, "evil.example") || strings.Contains(l, "INJECT") {
			t.Fatalf("враждебная ссылка пропущена: %q", l)
		}
	}
	// корректным остаётся лишь узел без мусора (tag с переводом строки отбрасывает узел целиком)
	if len(links) != 1 || !strings.Contains(links[0], "@[2001:db8::1]:443") || strings.Contains(links[0], "[[") {
		t.Fatalf("ожидался один безопасный узел: %v", links)
	}
}

// Пределы: слишком большие документы и бесконечное число узлов не раздувают результат; бомба алиасов YAML не вешает разбор.
func TestLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"outbounds":[`)
	for i := 0; i < 25000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type":"trojan","server":"h%d.example","server_port":443,"password":"p%d"}`, i, i)
	}
	b.WriteString(`]}`)
	links, ok := importer.Links([]byte(b.String()))
	if !ok || len(links) != 20000 {
		t.Fatalf("предел узлов: ok=%v узлов=%d", ok, len(links))
	}
	if _, ok := importer.Links([]byte(strings.Repeat("a", 9<<20))); ok {
		t.Fatal("слишком большое тело разобрано")
	}
	bomb := "proxies:\n  - &a {name: a, type: trojan, server: h.example, port: 443, password: p}\n"
	bomb += "x: &l1 [*a,*a,*a,*a,*a,*a,*a,*a,*a]\ny: &l2 [*l1,*l1,*l1,*l1,*l1,*l1,*l1,*l1,*l1]\nz: &l3 [*l2,*l2,*l2,*l2,*l2,*l2,*l2,*l2,*l2]\n"
	bomb += "w: &l4 [*l3,*l3,*l3,*l3,*l3,*l3,*l3,*l3,*l3]\nv: &l5 [*l4,*l4,*l4,*l4,*l4,*l4,*l4,*l4,*l4]\n"
	done := make(chan struct{})
	go func() { importer.Links([]byte(bomb)); close(done) }()
	select {
	case <-done:
	case <-timeAfter(5):
		t.Fatal("разбор бомбы алиасов YAML завис")
	}
}
