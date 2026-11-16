package translator

import (
	"fmt"
	"net/url"
	"testing"
)

// числа из ссылки без верхней границы уходили в движок (strings.Repeat("X", n), make([]byte, n), make([][]byte, jc)).
// Теперь значение выше предела отклоняет ссылку с причиной value-out-of-range; реальные максимумы из рабочей базы проходят.

func xhttpLink(extra string) string {
	return "vless://" + uuid1 + "@192.0.2.1:443?type=xhttp&security=tls&path=/p&mode=packet-up&extra=" + url.QueryEscape(extra)
}

const wgBase = "wireguard://cHJpdmF0ZUtleUJhc2U2NFZhbHVlMDEyMzQ1Njc4OTA%3D@192.0.2.12:2408?publickey=cHVibGljS2V5QmFzZTY0VmFsdWVBQUFBQUFBQUFBQUE%3D&address=172.16.0.2/32"

func TestXHTTPNumbersOutOfRangeRejected(t *testing.T) {
	bad := []string{
		`{"xPaddingBytes":"100-2000000000"}`,
		`{"xPaddingBytes":2000000000}`,
		`{"xPaddingBytes":1e30}`,
		`{"xPaddingBytes":{"from":100,"to":9000000000}}`,
		`{"xPaddingBytes":[100,9000000000]}`,
		`{"scMaxEachPostBytes":"1000000-9000000000"}`,
		`{"scMinPostsIntervalMs":"30-99999999"}`,
		`{"uplinkChunkSize":"4000-9000000000"}`,
		`{"sessionIDTable":"base62","sessionIDLength":"32-2000000000"}`,
		`{"scMaxConcurrentPosts":9000000000}`,
		`{"scMaxBufferedPosts":9000000000}`,
		`{"serverMaxHeaderBytes":9000000000}`,
		`{"scStreamUpServerSecs":"180-99999999"}`,
		`{"xmux":{"maxConcurrency":"16-99999999"}}`,
		`{"xmux":{"maxConnections":99999999}}`,
		`{"xmux":{"cMaxReuseTimes":9000000000}}`,
		`{"xmux":{"hMaxRequestTimes":"600-9000000000"}}`,
		`{"xmux":{"hMaxReusableSecs":"1800-9000000000"}}`,
		`{"xmux":{"hKeepAlivePeriod":9000000000}}`,
	}
	for _, e := range bad {
		if r := reasonOf(t, xhttpLink(e)); r != ReasonOutOfRange {
			t.Errorf("%s: причина %q, ожидалась %q", e, r, ReasonOutOfRange)
		}
	}
}

// Реальные максимумы по полям из рабочей базы (агрегаты, 164 тыс. узлов) проходят.
func TestXHTTPRealMaximaAccepted(t *testing.T) {
	ok := []string{
		`{"xPaddingBytes":"100-4000"}`,
		`{"scMaxEachPostBytes":"1000000-20000000"}`,
		`{"scMinPostsIntervalMs":"10-200"}`,
		`{"scMaxConcurrentPosts":1024,"scMaxBufferedPosts":1000,"serverMaxHeaderBytes":3932160}`,
		`{"scStreamUpServerSecs":"20-180"}`,
		`{"uplinkChunkSize":"1000-614400"}`,
		`{"sessionIDTable":"base62","sessionIDLength":"32"}`,
		`{"xmux":{"maxConcurrency":"16-100","maxConnections":16,"cMaxReuseTimes":2000,"hMaxRequestTimes":"600-10000","hMaxReusableSecs":"1800-86400","hKeepAlivePeriod":600}}`,
	}
	for _, e := range ok {
		if _, err := Parse(xhttpLink(e)); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
}

func TestAWGNumbersOutOfRangeRejected(t *testing.T) {
	for _, q := range []string{
		"jc=4000000000", "jc=1000", "jmin=4000000000", "jmax=4000000000", "jmax=65535", "s1=4000000000", "s1=65535", "s2=5000", "s3=5000", "s4=5000",
		"h1=4294967296", "h2=1-4294967296", "h3=9999999999999999999", "jc=abc", "h4=x",
	} {
		if r := reasonOf(t, wgBase+"&"+q); r != ReasonOutOfRange {
			t.Errorf("%s: причина %q", q, r)
		}
	}
	for _, q := range []string{
		"jc=3&jmin=1&jmax=3", "jc=128&jmin=50&jmax=1280&s1=1132&s2=1132&s3=64&s4=32",
		"h1=4294967295&h2=1&h3=3&h4=4", "h1=100-200&h2=300-400&h3=500-600&h4=700-800", "jc=0&s1=0&s2=0",
	} {
		if _, err := Parse(wgBase + "&" + q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
}

func TestHysteriaSpeedOutOfRangeRejected(t *testing.T) {
	for _, q := range []string{"upmbps=100000", "downmbps=100000", "upmbps=99999999999999999999", "up=20000 mbps"} {
		link := "hysteria://192.0.2.9:443?auth=secret&peer=example.com&" + q
		if r := reasonOf(t, link); r != ReasonOutOfRange {
			t.Errorf("%s: причина %q", q, r)
		}
	}
	for _, q := range []string{"upmbps=500&downmbps=1000", "up=10000&down=10000", "upmbps=0"} {
		link := "hysteria://192.0.2.9:443?auth=secret&peer=example.com&" + q
		if _, err := Parse(link); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	_ = fmt.Sprint
}
