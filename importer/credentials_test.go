package importer_test

import (
	"testing"

	"brod/importer"
)

// логин и пароль с «:» и «@» доходят до ссылки целиком (url.PathEscape эти знаки не экранирует, и ссылка усекала пароль).
func TestCredentialsWithReservedChars(t *testing.T) {
	doc := `{"outbounds":[
	 {"type":"trojan","tag":"a","server":"t.example","server_port":443,"password":"p:a@ss/w#rd?","tls":{"enabled":true,"server_name":"t.example"}},
	 {"type":"tuic","tag":"b","server":"tu.example","server_port":4444,"uuid":"` + uid + `","password":"x:y@z","tls":{"enabled":true,"server_name":"tu.example"}},
	 {"type":"socks","tag":"c","server":"so.example","server_port":1080,"username":"u:1@h","password":"p:2@q"}
	]}`
	links, ok := importer.Links([]byte(doc))
	if !ok || len(links) != 3 {
		t.Fatalf("ссылки: %v", links)
	}
	outs := parseAll(t, links)
	if got := outs[0].Opts["password"]; got != "p:a@ss/w#rd?" {
		t.Fatalf("trojan: пароль %q", got)
	}
	if outs[1].Opts["password"] != "x:y@z" || outs[1].Opts["uuid"] != uid {
		t.Fatalf("tuic: %v", outs[1].Opts)
	}
	if outs[2].Opts["username"] != "u:1@h" || outs[2].Opts["password"] != "p:2@q" {
		t.Fatalf("socks: %v", outs[2].Opts)
	}
}
