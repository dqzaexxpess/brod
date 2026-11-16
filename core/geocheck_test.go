package core

import (
	"strings"
	"testing"
	"time"
)

func TestParseGeo(t *testing.T) {
	g, err := parseGeo("ipinfo", []byte(`{"ip":"203.0.113.9","city":"Amsterdam","country":"nl","org":"AS14061 DigitalOcean","timezone":"Europe/Amsterdam"}`))
	if err != nil || g.Country != "NL" || g.City != "Amsterdam" || g.Timezone != "Europe/Amsterdam" || !strings.Contains(g.Org, "DigitalOcean") {
		t.Fatalf("ipinfo: %+v %v", g, err)
	}
	g, err = parseGeo("ipwho.is", []byte(`{"success":true,"ip":"203.0.113.9","country_code":"DE","city":"Frankfurt","timezone":{"id":"Europe/Berlin"},"connection":{"org":"Hetzner","isp":"Hetzner Online"}}`))
	if err != nil || g.Country != "DE" || g.Org != "Hetzner" || g.Timezone != "Europe/Berlin" {
		t.Fatalf("ipwho.is: %+v %v", g, err)
	}
	g, err = parseGeo("ip-api", []byte(`{"status":"success","query":"203.0.113.9","countryCode":"NL","city":"Amsterdam","timezone":"Europe/Amsterdam","isp":"DO","org":"DO LLC","as":"AS14061 DigitalOcean","hosting":true,"proxy":false}`))
	if err != nil || g.Hosting == nil || !*g.Hosting || g.Proxy == nil || *g.Proxy || g.Org != "AS14061 DigitalOcean" {
		t.Fatalf("ip-api: %+v %v", g, err)
	}
	g, err = parseGeo("ifconfig.co", []byte(`{"ip":"203.0.113.9","country_iso":"nl","city":"Amsterdam","time_zone":"Europe/Amsterdam","asn":"AS14061","asn_org":"DigitalOcean"}`))
	if err != nil || g.Country != "NL" || g.Org != "AS14061 DigitalOcean" || g.Timezone != "Europe/Amsterdam" {
		t.Fatalf("ifconfig.co: %+v %v", g, err)
	}
	for name, body := range map[string]string{"ipinfo": `{"bogon":true}`, "ipwho.is": `{"success":false,"message":"limit"}`, "ip-api": `{"status":"fail","message":"private range"}`, "x": `{}`, "ipinfo ": `not json`} {
		if _, err := parseGeo(strings.TrimSpace(name), []byte(body)); err == nil {
			t.Errorf("%s: плохой ответ принят", name)
		}
	}
}

func gs(name, cc, city, tz string, hosting, proxy *bool) GeoSource {
	return GeoSource{Name: name, Country: cc, City: city, Timezone: tz, Hosting: hosting, Proxy: proxy, Org: "AS1 Org"}
}

func hasFinding(fs []DiagFinding, level, titlePart string) bool {
	for _, f := range fs {
		if f.Level == level && strings.Contains(f.Title, titlePart) {
			return true
		}
	}
	return false
}

func TestGeoFindings(t *testing.T) {
	tr, fl := true, false
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	moscow, _ := time.LoadLocation("Europe/Moscow")
	base := geoInput{CFCountry: "NL", VPNOn: true, Mode: ModeAll, Now: now, Local: moscow, ResolverCountry: "NL"}

	// всё согласно, обычный адрес, но пояс устройства не тот
	in := base
	in.Sources = []GeoSource{gs("ipinfo", "NL", "Amsterdam", "Europe/Amsterdam", nil, nil), gs("ipwho.is", "NL", "Amsterdam", "Europe/Amsterdam", nil, nil), gs("ip-api", "NL", "Amsterdam", "Europe/Amsterdam", &fl, &fl)}
	fs := geoFindings(in)
	for _, w := range [][2]string{{"ok", "согласны по стране"}, {"ok", "не помечен"}, {"warn", "Часовой пояс устройства не совпадает"}, {"ok", "DNS в той же стране"}, {"info", "Чего эта проверка не видит"}} {
		if !hasFinding(fs, w[0], w[1]) {
			t.Errorf("нет вывода %v: %+v", w, fs)
		}
	}
	if hasFinding(fs, "warn", "расходятся по стране") {
		t.Error("ложное расхождение стран")
	}

	// расхождение стран + дата-центр + DNS из другой страны
	in = base
	in.ResolverCountry = "RU"
	in.Sources = []GeoSource{gs("ipinfo", "DE", "Frankfurt", "Europe/Berlin", nil, nil), gs("ipwho.is", "NL", "Amsterdam", "Europe/Amsterdam", nil, nil), gs("ip-api", "NL", "Amsterdam", "Europe/Amsterdam", &tr, &fl)}
	fs = geoFindings(in)
	for _, w := range [][2]string{{"warn", "расходятся по стране"}, {"warn", "из дата-центра"}, {"warn", "DNS-запросы уходят из другой страны"}} {
		if !hasFinding(fs, w[0], w[1]) {
			t.Errorf("нет вывода %v: %+v", w, fs)
		}
	}
	// прокси важнее дата-центра
	in.Sources[2].Proxy = &tr
	if fs = geoFindings(in); !hasFinding(fs, "warn", "прокси или VPN") || hasFinding(fs, "warn", "из дата-центра") {
		t.Errorf("прокси: %+v", fs)
	}

	// режим «только заблокированное»: обычные сайты видят настоящий адрес — пояс и DNS не сравниваем
	in = base
	in.Mode = ModeBlocked
	in.Sources = []GeoSource{gs("ipinfo", "NL", "Amsterdam", "Europe/Amsterdam", nil, nil), gs("ip-api", "NL", "Amsterdam", "Europe/Amsterdam", &fl, &fl)}
	fs = geoFindings(in)
	if hasFinding(fs, "warn", "Часовой пояс") || hasFinding(fs, "ok", "Часовой пояс") || hasFinding(fs, "warn", "DNS") {
		t.Errorf("в режиме blocked пояс и DNS не сравниваются: %+v", fs)
	}

	// пояс совпал
	in = base
	in.Local, _ = time.LoadLocation("Europe/Berlin")
	in.Sources = []GeoSource{gs("ipinfo", "NL", "", "Europe/Amsterdam", nil, nil), gs("ip-api", "NL", "", "Europe/Amsterdam", &fl, &fl)}
	if fs = geoFindings(in); !hasFinding(fs, "ok", "Часовой пояс устройства совпадает") {
		t.Errorf("пояс: %+v", fs)
	}

	// никто не ответил / ответила одна база
	in = base
	in.Sources = []GeoSource{{Name: "ipinfo", Err: "timeout"}, {Name: "ipwho.is", Err: "timeout"}, {Name: "ip-api", Err: "timeout"}}
	in.CFCountry = ""
	if fs = geoFindings(in); len(fs) != 1 || !hasFinding(fs, "info", "не ответили") {
		t.Errorf("никто не ответил: %+v", fs)
	}
	in.CFCountry = "NL"
	in.Sources = []GeoSource{{Name: "ipinfo", Err: "timeout"}, gs("ip-api", "NL", "", "", nil, nil)}
	if fs = geoFindings(in); !hasFinding(fs, "ok", "согласны") || !hasFinding(fs, "info", "Не ответили базы") {
		t.Errorf("частично: %+v", fs)
	}
}

func TestOffString(t *testing.T) {
	for sec, want := range map[int]string{3 * 3600: "UTC+3", 0: "UTC+0", -5 * 3600: "UTC-5", 19800: "UTC+5:30"} {
		if got := offString(sec); got != want {
			t.Errorf("%d: %s", sec, got)
		}
	}
}
