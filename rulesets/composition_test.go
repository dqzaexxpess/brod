package rulesets

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// buildSRS собирает бинарный srs из заданных правил (как это делает источник списков).
func buildSRS(t *testing.T, rules ...option.HeadlessRule) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := srs.Write(&buf, option.PlainRuleSet{Rules: rules}, C.RuleSetVersionCurrent); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func def(r option.DefaultHeadlessRule) option.HeadlessRule {
	return option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: r}
}

// встроенные списки (реальные файлы снимка) проходят проверку состава.
func TestValidateSetAcceptsEmbeddedSnapshot(t *testing.T) {
	for _, s := range Sets {
		b, err := snapshot.ReadFile("data/" + s.Name + ".srs")
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateSet(b, s.Name); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
		// и тот же файл как «свой» список: вид определяется по содержимому
		if err := ValidateUser(b); err != nil {
			t.Errorf("%s как свой: %v", s.Name, err)
		}
	}
}

// Образцы того же вида, что выдают встроенные списки (одно правило: домены и суффиксы / подсети), проходят.
func TestValidateSetAcceptsRealShapedSamples(t *testing.T) {
	site := buildSRS(t, def(option.DefaultHeadlessRule{
		Domain: []string{"sber.ru"}, DomainSuffix: []string{"sberbank.ru", "gosuslugi.ru"},
		DomainKeyword: []string{"bank"}, DomainRegex: []string{`^m\d+\.example\.ru$`}}))
	if err := ValidateSet(site, "geosite-category-bank-ru"); err != nil {
		t.Fatal(err)
	}
	ip := buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"5.8.0.0/13", "91.108.4.0/22", "2a00::/29", "8.0.0.0/8"}}))
	if err := ValidateSet(ip, "geoip-ru"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUser(site); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUser(ip); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSetRejectsBadComposition(t *testing.T) {
	logical := option.HeadlessRule{Type: C.RuleTypeLogical, LogicalOptions: option.LogicalHeadlessRule{
		Mode: C.LogicalTypeOr, Rules: []option.HeadlessRule{def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}})}}}
	cases := []struct {
		name string
		set  string
		file []byte
	}{
		{"geosite с ip_cidr 0.0.0.0/0", "geosite-category-bank-ru", buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"0.0.0.0/0"}}))},
		{"geosite: домены и подсети в одном правиле", "geosite-youtube", buildSRS(t, def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}, IPCIDR: []string{"10.0.0.0/8"}}))},
		{"geosite: домены в одном правиле, подсеть в другом", "geosite-youtube", buildSRS(t,
			def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}}), def(option.DefaultHeadlessRule{IPCIDR: []string{"10.0.0.0/8"}}))},
		{"geosite: domain_regex .*", "geosite-category-bank-ru", buildSRS(t, def(option.DefaultHeadlessRule{DomainRegex: []string{".*"}}))},
		{"geosite: domain_regex .+", "geosite-category-bank-ru", buildSRS(t, def(option.DefaultHeadlessRule{DomainRegex: []string{".+"}}))},
		{"geosite: пустой domain_keyword", "geosite-category-bank-ru", buildSRS(t, def(option.DefaultHeadlessRule{DomainKeyword: []string{""}}))},
		{"geosite: network", "geosite-category-bank-ru", buildSRS(t, def(option.DefaultHeadlessRule{Network: []string{"tcp", "udp"}}))},
		{"geosite: порт", "geosite-category-bank-ru", buildSRS(t, def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}, Port: []uint16{443}}))},
		{"geosite: invert", "geosite-category-bank-ru", buildSRS(t, def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}, Invert: true}))},
		{"geosite: логическое правило", "geosite-category-bank-ru", buildSRS(t, logical)},
		{"geoip с доменами", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}}))},
		{"geoip: /7", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"4.0.0.0/7"}}))},
		{"geoip: 0.0.0.0/0", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"0.0.0.0/0"}}))},
		{"geoip: ::/0", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"::/0"}}))},
		{"geoip: ::/8", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"::/8"}}))},
		{"geoip: мост IPv4 ::ffff:0:0/96", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"::ffff:0.0.0.0/96"}}))},
		{"geoip: invert", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"5.8.0.0/13"}, Invert: true}))},
		{"geoip: логическое", "geoip-ru", buildSRS(t, logical)},
		{"geoip: source_ip_cidr", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{SourceIPCIDR: []string{"5.8.0.0/13"}}))},
		{"geoip: network", "geoip-ru", buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"5.8.0.0/13"}, Network: []string{"tcp"}}))},
		{"geoip: два правила, второе широкое", "geoip-ru", buildSRS(t,
			def(option.DefaultHeadlessRule{IPCIDR: []string{"5.8.0.0/13"}}), def(option.DefaultHeadlessRule{IPCIDR: []string{"0.0.0.0/1"}}))},
	}
	for _, c := range cases {
		if err := ValidateSet(c.file, c.set); err == nil {
			t.Errorf("%s: принят в %s", c.name, c.set)
		}
	}
}

func TestValidateUserRejectsBadComposition(t *testing.T) {
	cases := map[string][]byte{
		"смесь доменов и подсетей":   buildSRS(t, def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}, IPCIDR: []string{"10.0.0.0/8"}})),
		"смесь в разных правилах":    buildSRS(t, def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}}), def(option.DefaultHeadlessRule{IPCIDR: []string{"10.0.0.0/8"}})),
		"всё: 0.0.0.0/0":             buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"0.0.0.0/0"}})),
		"всё: domain_regex .*":       buildSRS(t, def(option.DefaultHeadlessRule{DomainRegex: []string{".*"}})),
		"сеть tcp,udp":               buildSRS(t, def(option.DefaultHeadlessRule{Network: []string{"tcp", "udp"}})),
		"подсеть короче /8":          buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"64.0.0.0/2"}})),
		"invert":                     buildSRS(t, def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.ru"}, Invert: true})),
		"процесс":                    buildSRS(t, def(option.DefaultHeadlessRule{ProcessName: []string{"curl"}})),
		"пустое правило без условий": buildSRS(t, def(option.DefaultHeadlessRule{})),
	}
	for name, b := range cases {
		err := ValidateUser(b)
		if err == nil {
			t.Errorf("%s: принят", name)
		} else if !strings.Contains(err.Error(), "список") {
			t.Errorf("%s: сообщение непонятно: %v", name, err)
		}
	}
}

// Список по URL (JSON-источник) с широким правилом отклоняется целиком, а не молча урезается.
func TestFetchRemoteRejectsCatchAll(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/all":
			w.Write([]byte(`{"version":3,"rules":[{"domain_suffix":["example.org"]},{"ip_cidr":["0.0.0.0/0"]}]}`))
		case "/ok":
			w.Write([]byte(`{"version":3,"rules":[{"domain_suffix":["example.org"]}]}`))
		}
	}))
	defer srv.Close()
	if _, err := FetchRemote(context.Background(), srv.Client(), srv.URL+"/all", 0); err == nil {
		t.Fatal("список со смесью и 0.0.0.0/0 принят")
	}
	if _, err := FetchRemote(context.Background(), srv.Client(), srv.URL+"/ok", 0); err != nil {
		t.Fatal(err)
	}
}

// Update не принимает файл с неверным составом и оставляет прежний.
func TestUpdateRejectsBadComposition(t *testing.T) {
	// широкое адресное правило под именем geosite-ru-blocked
	catchAll := buildSRS(t, def(option.DefaultHeadlessRule{IPCIDR: []string{"0.0.0.0/0"}}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "geosite-ru-blocked.srs") {
			w.Write(catchAll)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, srv.URL+r.URL.Path, nil)
		return http.DefaultClient.Do(req)
	})
	dir := t.TempDir()
	_, _ = Install(dir)
	// прежний файл пустой: проверка «втрое меньше прежнего» не мешает, отказ даёт именно проверка состава
	_ = os.WriteFile(Path(dir, "geosite-ru-blocked"), nil, 0o600)
	res, _ := Update(context.Background(), &http.Client{Transport: rt}, dir)
	if _, bad := res.Failed["geosite-ru-blocked"]; !bad {
		t.Fatalf("файл с неверным составом принят: %+v", res)
	}
	for _, n := range res.Updated {
		if n == "geosite-ru-blocked" {
			t.Fatal("файл записан")
		}
	}
}
