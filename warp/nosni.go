package warp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"time"
)

// Часть сетей (замер, Wi-Fi и мобильная в РФ) режет TLS-рукопожатие по SNI api.cloudflareclient.com: TCP проходит, ответ на
// ClientHello не приходит. Без расширения SNI Cloudflare отдаёт на тот же адрес настоящий сертификат *.cloudflareclient.com и принимает
// запросы, поэтому запасной путь — соединение без SNI с обычной проверкой цепочки и имени (VerifyConnection). Подставной SNI (www.cloudflare.com)
// не годится: API отвечает 403.
const apiHost = "api.cloudflareclient.com"

// apiAddrs — адреса api.cloudflareclient.com (anycast Cloudflare); имя не резолвим: у приложений на Android нет resolv.conf, а адреса стабильны.
var apiAddrs = []string{"104.16.24.84:443", "104.16.192.82:443"}

// noSNIClient — HTTP-клиент, который подключается к apiAddrs без SNI и проверяет сертификат на имя apiHost.
func noSNIClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var last error
			for _, addr := range apiAddrs {
				d := net.Dialer{Timeout: 5 * time.Second}
				c, err := d.DialContext(ctx, "tcp", addr)
				if err != nil {
					last = err
					continue
				}
				tc := tls.Client(c, &tls.Config{
					// без SNI встроенная проверка имени невозможна: проверяем сами, точно так же, как проверил бы стандартная
					InsecureSkipVerify: true,
					VerifyConnection:   verifyAPICert,
				})
				if err := tc.HandshakeContext(ctx); err != nil {
					c.Close()
					last = err
					continue
				}
				return tc, nil
			}
			if last == nil {
				last = errors.New("нет адресов")
			}
			return nil, last
		},
		ForceAttemptHTTP2: false,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// verifyAPICert проверяет цепочку по системным корням и имя сертификата (apiHost).
func verifyAPICert(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("сервер не прислал сертификат")
	}
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: apiHost, Intermediates: inter})
	return err
}
