package tgproxy

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Предфильтр не меняет результат: на случайных телах (с маркерами в разном регистре и без них) совпадает с полным разбором.
func TestExtractLinksPrefilterEquivalent(t *testing.T) {
	pieces := []string{
		"vless://uuid@host:443?type=tcp#name\n", "vmess://eyJ2IjoiMiJ9\n", "<div>", "</div>", "<br/>", "&amp;", "&nbsp;",
		"Server: 1.2.3.4\nPort: 443\nSecret: eeee1234567890abcdef1234567890abcdef\n", "СЕРВЕР: host.example\nПОРТ: 8443\nСЕКРЕТ: dd1234567890abcdef1234567890abcdef\n",
		"server: a.b.c, port: 80, SeCrEt: ABCDEFGHIJKLMNOPQRSTUV\n", "TG://proxy?server=x.y&port=443&secret=ee11223344556677889900aabbccddeeff\n",
		"https://T.ME/proxy?server=h&port=1&secret=abcdefabcdefabcdefabcdef\n", "ссылка: http://t.me/socks?server=s&port=1080&user=u&pass=p\n",
		"tg:/ /proxy", "secre", "секре", "t.me", "Server: Unknown\nPort: 1\nSecret: 0000000000000000", strings.Repeat("noise ", 50),
	}
	rng := rand.New(rand.NewSource(7))
	nonEmpty := 0
	for i := 0; i < 600; i++ {
		var sb strings.Builder
		for k := rng.Intn(12); k > 0; k-- {
			sb.WriteString(pieces[rng.Intn(len(pieces))])
		}
		body := []byte(sb.String())
		want, got := extractLinks(body), ExtractLinks(body)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("тело %q:\nполный разбор: %v\nс предфильтром: %v", body, want, got)
		}
		if len(want) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty < 100 {
		t.Fatalf("тест вырожден: ссылки нашлись только в %d телах", nonEmpty)
	}
}

// 20 МБ подписки с узлами и без прокси Telegram проходят предфильтр быстро (раньше — десятки секунд процессора на телефоне).
func TestExtractLinksSkipsHugeNodeList(t *testing.T) {
	body := []byte(strings.Repeat("vless://11111111-2222-3333-4444-555555555555@host.example:443?type=tcp&security=reality#node\n", 220_000))
	t0 := time.Now()
	if got := ExtractLinks(body); got != nil {
		t.Fatalf("неожиданные ссылки: %v", got)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("предфильтр слишком медленный: %v на %d МБ", d, len(body)>>20)
	}
}
