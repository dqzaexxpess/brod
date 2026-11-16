package translator

import (
	"encoding/base64"
	"testing"
)

// vmess: поля allowInsecure/pcs/vcn из JSON ссылки не читаются, поэтому проверка сертификата остаётся включённой (узел с самоподписанным
// сертификатом просто не подключится), а не отключается молча. Тест закрепляет это поведение: если разбор insecure у vmess появится,
// он обязан идти вместе с отказом при закреплении, как у остальных схем (Parse).
func TestVMessInsecureAndPinFieldsDoNotWeakenCertCheck(t *testing.T) {
	js := `{"v":"2","add":"example.org","port":"443","id":"11111111-1111-1111-1111-111111111111","aid":"0","net":"tcp","tls":"tls","sni":"example.org","allowInsecure":"1","pcs":"deadbeef","vcn":"example.org"}`
	link := "vmess://" + base64.StdEncoding.EncodeToString([]byte(js))
	o, err := Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if o.Flags.Insecure {
		t.Fatal("vmess: allowInsecure из JSON не должен отключать проверку сертификата молча")
	}
	tls, _ := o.Opts["tls"].(map[string]any)
	if tls == nil {
		t.Fatalf("нет блока tls: %v", o.Opts)
	}
	if v, ok := tls["insecure"]; ok && v == true {
		t.Fatalf("tls.insecure выставлен: %v", tls)
	}
}
