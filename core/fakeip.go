package core

import (
	"fmt"
	"log"
	"strings"
)

// SetFakeIP — настройка «Быстрый DNS (FakeIP)», эксперимент: 1 или 0, по умолчанию 0. Меняется перезапуском ядра (как tun.*).
//
// Что делает: в режиме «всё» домены, которые идут через туннель, получают от DNS демона синтетические адреса 198.18.0.0/15; при соединении
// по такому адресу движок подставляет домен назад, и настоящий запрос DoH через выход не нужен (для цепочки через WARP это ~0,15–0,4 с на
// каждый новый домен). Экспериментальная настройка; по умолчанию выключена.
//
// Где отключается само: в режимах «блокировки» и «всё, кроме РФ» (там маршрут зависит от настоящего адреса: geoip-ru, ip_is_private), и когда у
// пользователя есть включённое правило, которому нужен настоящий адрес назначения (ip_cidr, частные адреса, версия IP, свои списки и geoip).
const SetFakeIP = "dns.fakeip"

// fakeIPLocalSuffixes — суффиксы имён, которые резолвятся в частные и локальные адреса (сервисы «адрес в имени», локальные зоны): они идут в «local»,
// получают настоящий адрес, и правило «частные адреса напрямую» работает как без FakeIP: иначе 192-168-10-1.sslip.io
// получало бы синтетический адрес и уходило в туннель вместо локальной сети. Свои домены пользователя с частными адресами решаются правилом «напрямую».
var fakeIPLocalSuffixes = []string{"sslip.io", "nip.io", "localtest.me", "lvh.me", "plex.direct", "local", "lan", "home.arpa", "internal", "localhost", "localdomain"}

// fakeIPActive: включать ли FakeIP в этом конфиге.
func fakeIPActive(t TUNConfig) bool {
	if !t.FakeIP {
		return false
	}
	if t.Mode != ModeAll {
		return false
	}
	if why := fakeIPBlockedBy(t.UserRules); why != "" {
		log.Printf("FakeIP отключён: правило %s требует настоящий адрес назначения", why)
		return false
	}
	return true
}

// fakeIPBlockedBy возвращает название первого включённого правила, которому нужен настоящий адрес назначения (после подмены домена адреса нет).
func fakeIPBlockedBy(rules []RouteRule) string {
	for _, r := range rules {
		if !r.Enabled || !r.Match.needsRealIP() {
			continue
		}
		if r.Title != "" {
			return fmt.Sprintf("«%s»", r.Title)
		}
		return r.ID
	}
	return ""
}

// needsRealIP: условие смотрит на адрес назначения, а не на домен. Списки, кроме доменных geosite-*, считаются адресными (в своих списках и geoip-* бывают подсети).
func (m RouteMatch) needsRealIP() bool {
	if len(m.IPCIDR) > 0 || m.IPIsPrivate || m.IPVersion != 0 {
		return true
	}
	for _, s := range m.RuleSet {
		if !strings.HasPrefix(s, "geosite-") {
			return true
		}
	}
	return false
}
