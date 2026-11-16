package warp

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Без SNI встроенная проверка имени отключена, поэтому verifyAPICert обязана отклонять всё, что не является настоящим сертификатом apiHost.
func TestVerifyAPICertRejectsUntrusted(t *testing.T) {
	if err := verifyAPICert(tls.ConnectionState{}); err == nil {
		t.Fatal("пустая цепочка принята")
	}
	srv := httptest.NewTLSServer(http.NotFoundHandler()) // самоподписанный сертификат (не в системных корнях, имя example.com/127.0.0.1)
	defer srv.Close()
	cert, err := x509.ParseCertificate(srv.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAPICert(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err == nil {
		t.Fatal("самоподписанный сертификат принят")
	}
}
