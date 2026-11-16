package api

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Счётчик запросов к API по путям: раз в минуту (по первому запросу после минуты) одна строка в журнал ядра. Нужен, чтобы по журналу было видно,
// кто и как часто опрашивает ядро (на телефоне фоновые опросы приложения оказались заметной долей расхода батареи); пустая минута строки не пишет.
var hits struct {
	mu    sync.Mutex
	since time.Time
	n     map[string]int
}

var idSeg = regexp.MustCompile(`^[0-9a-fA-F]{12,}$`)

func hitKey(method, path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if idSeg.MatchString(p) || len(p) > 40 {
			parts[i] = "{id}"
		}
	}
	return method + " " + strings.Join(parts, "/")
}

func countHit(method, path string, now time.Time) {
	hits.mu.Lock()
	defer hits.mu.Unlock()
	if hits.n == nil {
		hits.n, hits.since = map[string]int{}, now
	}
	hits.n[hitKey(method, path)]++
	if now.Sub(hits.since) < time.Minute {
		return
	}
	type kv struct {
		k string
		v int
	}
	var l []kv
	total := 0
	for k, v := range hits.n {
		l = append(l, kv{k, v})
		total += v
	}
	sort.Slice(l, func(i, j int) bool { return l[i].v > l[j].v })
	var sb strings.Builder
	for i, e := range l {
		if i == 8 {
			sb.WriteString(", …")
			break
		}
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%s ×%d", e.k, e.v)
	}
	log.Printf("api за %d с: %d запросов: %s", int(now.Sub(hits.since).Seconds()), total, sb.String())
	hits.n, hits.since = map[string]int{}, now
}
