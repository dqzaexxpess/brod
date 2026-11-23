package api_test

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Права на каждый маршрут API. Каждый маршрут из routes.go обязан быть помечен явно: accessAny — доступен и токену приложения,
// accessOwner — только владельцу (токен приложения получает 403). Новый маршрут без пометки валит тест: сначала решить, кому он открыт.
const (
	accessAny   = "любой"
	accessOwner = "только владелец"
)

var routeAccess = map[string]string{
	"GET /v1/status":                                accessAny,
	"GET /v1/nodes":                                 accessOwner,
	"GET /v1/nodes/{id}":                            accessOwner,
	"GET /v1/nodes/{id}/link":                       accessOwner,
	"POST /v1/nodes/{id}/connect":                   accessOwner,
	"GET /v1/connections":                           accessOwner,
	"DELETE /v1/connections/{id}":                   accessOwner,
	"GET /v1/backup":                                accessOwner,
	"POST /v1/backup":                               accessOwner,
	"POST /v1/diagnostics":                          accessOwner,
	"POST /v1/report":                               accessOwner,
	"POST /v1/chains":                               accessOwner,
	"GET /v1/report":                                accessOwner,
	"GET /v1/traffic":                               accessOwner,
	"GET /v1/traffic/daily":                         accessOwner,
	"POST /v1/connect-best":                         accessOwner,
	"GET /v1/tg-proxies":                            accessOwner,
	"POST /v1/tg-proxies":                           accessOwner,
	"POST /v1/tg-proxies/check":                     accessOwner,
	"DELETE /v1/tg-proxies/{id}":                    accessOwner,
	"PUT /v1/tg-proxies/{id}/favorite":              accessOwner,
	"POST /v1/own-nodes":                            accessOwner,
	"DELETE /v1/own-nodes/{id}":                     accessOwner,
	"PATCH /v1/own-nodes/{id}":                      accessOwner,
	"PUT /v1/nodes/{id}/label":                      accessOwner,
	"POST /v1/measure":                              accessOwner,
	"POST /v1/ui":                                   accessOwner,
	"GET /v1/measure":                               accessOwner,
	"DELETE /v1/measure":                            accessOwner,
	"POST /v1/nodes/{id}/exit-info":                 accessOwner,
	"POST /v1/nodes/{id}/speed":                     accessOwner,
	"POST /v1/nodes/{id}/favorite":                  accessOwner,
	"POST /v1/nodes/{id}/pin":                       accessOwner,
	"POST /v1/refresh":                              accessOwner,
	"POST /v1/check":                                accessOwner,
	"POST /v1/probe-active":                         accessOwner,
	"GET /v1/alternatives":                          accessOwner,
	"POST /v1/warp/probe":                           accessOwner,
	"GET /v1/warp":                                  accessOwner,
	"POST /v1/warp/scan":                            accessOwner,
	"POST /v1/warp/reset":                           accessOwner,
	"PUT /v1/warp/params":                           accessOwner,
	"POST /v1/warp/register":                        accessOwner,
	"POST /v1/ota/sources":                          accessOwner,
	"POST /v1/warp/import":                          accessOwner,
	"POST /v1/notify-test":                          accessOwner,
	"GET /v1/notifications":                         accessOwner,
	"POST /v1/notifications/{id}/action":            accessOwner,
	"POST /v1/rulesets/update":                      accessOwner,
	"GET /v1/route-sets":                            accessOwner,
	"POST /v1/route-sets":                           accessOwner,
	"DELETE /v1/route-sets/{id}":                    accessOwner,
	"GET /v1/route-rules":                           accessOwner,
	"PUT /v1/route-rules":                           accessOwner,
	"GET /v1/update":                                accessOwner,
	"POST /v1/update/check":                         accessOwner,
	"POST /v1/update/download":                      accessOwner,
	"POST /v1/update/install":                       accessOwner,
	"GET /v1/route-profiles":                        accessOwner,
	"POST /v1/route-profiles":                       accessOwner,
	"POST /v1/route-profiles/import":                accessOwner,
	"PUT /v1/route-profiles/{id}":                   accessOwner,
	"DELETE /v1/route-profiles/{id}":                accessOwner,
	"POST /v1/route-profiles/{id}/activate":         accessOwner,
	"GET /v1/route-profiles/{id}/share":             accessOwner,
	"POST /v1/killswitch/release":                   accessOwner,
	"GET /v1/vpn":                                   accessOwner,
	"POST /v1/vpn":                                  accessOwner,
	"POST /v1/vpn/selftest":                         accessOwner,
	"POST /v1/nodes/{id}/check":                     accessOwner,
	"POST /v1/active/check":                         accessOwner,
	"POST /v1/nodes/{id}/udp":                       accessOwner,
	"POST /v1/active/udp":                           accessOwner,
	"GET /v1/sources":                               accessOwner,
	"POST /v1/sources":                              accessOwner,
	"POST /v1/sources/{name}/enable":                accessOwner,
	"DELETE /v1/sources/{name}":                     accessOwner,
	"GET /v1/source-candidates":                     accessOwner,
	"POST /v1/source-candidates/scan":               accessOwner,
	"POST /v1/source-candidates/{id}/add":           accessOwner,
	"POST /v1/source-candidates/{id}/dismiss":       accessOwner,
	"GET /v1/source-candidate-groups":               accessOwner,
	"POST /v1/source-candidate-groups/{id}/dismiss": accessOwner,
	"GET /v1/targets":                               accessAny,
	"GET /v1/targets/{id}":                          accessAny,
	"PUT /v1/targets/{id}":                          accessAny,
	"DELETE /v1/targets/{id}":                       accessAny,
	"POST /v1/targets/{id}/probe-url":               accessAny,
	"POST /v1/targets/{id}/probe":                   accessAny,
	"POST /v1/targets/{id}/switch":                  accessAny,
	"GET /v1/events":                                accessAny,
	"GET /v1/settings":                              accessOwner,
	"PUT /v1/settings/{key}":                        accessOwner,
	"PUT /v1/tun/apps":                              accessOwner,
	"GET /v1/logs":                                  accessOwner,
	"GET /v1/app-proxy":                             accessOwner,
	"GET /v1/tokens":                                accessOwner,
	"POST /v1/tokens":                               accessOwner,
	"DELETE /v1/tokens/{name}":                      accessOwner,
}

func TestEveryRouteHasExplicitAccess(t *testing.T) {
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]bool{}
	for _, m := range regexp.MustCompile(`m\.HandleFunc\("([A-Z]+ /[^"]*)"`).FindAllStringSubmatch(string(src), -1) {
		routes[m[1]] = true
	}
	if len(routes) < 80 {
		t.Fatalf("в routes.go найдено %d маршрутов: разбор сломан или маршруты переехали", len(routes))
	}
	for r := range routes {
		if _, ok := routeAccess[r]; !ok {
			t.Errorf("маршрут %q не помечен в routeAccess (любой или только владелец)", r)
		}
	}
	for r := range routeAccess {
		if !routes[r] {
			t.Errorf("в routeAccess есть %q, а в routes.go такого маршрута нет", r)
		}
	}
}

// Токен приложения: маршруты «только владелец» отвечают 403 до всякой работы, маршруты «любой» — не 403 и не 401.
func TestAppTokenAccessPerRoute(t *testing.T) {
	c, srv := newEnv(t)
	user := client{t: t, base: srv.URL, tok: c.APIToken()}
	tok := user.ok("POST", "/v1/tokens", map[string]string{"name": "routecheck"})["token"].(string)
	param := regexp.MustCompile(`\{[a-z]+\}`)
	for route, access := range routeAccess {
		method, pattern, _ := strings.Cut(route, " ")
		path := param.ReplaceAllString(pattern, "x")
		// /v1/events — поток: достаточно заголовков ответа
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Errorf("%s: %v", route, err)
			continue
		}
		code := resp.StatusCode
		resp.Body.Close()
		cancel()
		switch access {
		case accessOwner:
			if code != http.StatusForbidden {
				t.Errorf("%s помечен «%s», а токен приложения получил HTTP %d (ожидался 403)", route, access, code)
			}
		case accessAny:
			if code == http.StatusForbidden || code == http.StatusUnauthorized {
				t.Errorf("%s помечен «%s», а токен приложения получил HTTP %d", route, access, code)
			}
		}
	}
}
