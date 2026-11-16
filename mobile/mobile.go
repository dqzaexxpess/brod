// Package mobile оценивает пригодность узла для мобильного интернета РФ в режиме «белых списков».
//
// Модель (README hxehex/russia-mobile-internet-whitelist): оператор пропускает только разрешённые адреса и/или SNI; всё чаще
// проверяется связка «IP + SNI». Поэтому лучше всего узел, у которого и адрес сервера входит в белый список, и SNI — разрешённый домен.
// Хосты-домены без резолва не оцениваются (резолвить сотни тысяч имён нельзя): Evaluate их пропускает, EvaluateResolved резолвит лениво
// только домены с разрешённым SNI и в пределах бюджета.
package mobile

import (
	"bufio"
	"compress/gzip"
	"context"
	"embed"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Lists — загруженные белые списки.
type Lists struct {
	domains  map[string]struct{}          // домены, под которые подходит и поддомен
	ips      []netip.Addr                 // отсортирован: сто тысяч адресов в map съедают заметную часть памяти телефона
	prefixes [129]map[netip.Addr]struct{} // [длина префикса] → сетевые адреса
	hasLen   []int
}

// Load читает каталог с файлами whitelist.txt (домены), cidrwhitelist.txt, ipwhitelist.txt (префиксы hx_ допускается).
func Load(dir string) (*Lists, error) {
	return load(func(names []string) (io.ReadCloser, error) {
		for _, n := range names {
			if f, err := os.Open(filepath.Join(dir, n)); err == nil {
				return f, nil
			}
		}
		return nil, os.ErrNotExist
	})
}

//go:embed data/*.txt.gz
var embedded embed.FS

var (
	embOnce  sync.Once
	embLists *Lists
	embErr   error
)

// Embedded возвращает встроенный снимок белых списков (hxehex/russia-mobile-internet-whitelist, 2026-11-03; обновление — scripts/update-mobile-lists.sh).
// Читается один раз при первом обращении: списки нужны только в мобильной сети.
func Embedded() (*Lists, error) {
	embOnce.Do(func() {
		embLists, embErr = load(func(names []string) (io.ReadCloser, error) {
			f, err := embedded.Open("data/" + names[len(names)-1] + ".gz") // имя без префикса hx_: последнее в списке
			if err != nil {
				return nil, err
			}
			return gzip.NewReader(f)
		})
	})
	return embLists, embErr
}

func load(open func(names []string) (io.ReadCloser, error)) (*Lists, error) {
	l := &Lists{domains: map[string]struct{}{}}
	for i := range l.prefixes {
		l.prefixes[i] = map[netip.Addr]struct{}{}
	}
	read := func(names []string, fn func(string)) error {
		f, err := open(names)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if s := strings.TrimSpace(sc.Text()); s != "" && !strings.HasPrefix(s, "#") {
				fn(s)
			}
		}
		return nil
	}
	if err := read([]string{"hx_whitelist.txt", "whitelist.txt"}, func(s string) { l.domains[strings.ToLower(strings.TrimPrefix(s, "*."))] = struct{}{} }); err != nil {
		return nil, err
	}
	_ = read([]string{"hx_ipwhitelist.txt", "ipwhitelist.txt"}, func(s string) {
		if a, err := netip.ParseAddr(s); err == nil {
			l.ips = append(l.ips, a)
		}
	})
	slices.SortFunc(l.ips, func(a, b netip.Addr) int { return a.Compare(b) })
	l.ips = slices.Compact(l.ips)
	seen := map[int]bool{}
	_ = read([]string{"hx_cidrwhitelist.txt", "cidrwhitelist.txt"}, func(s string) {
		if p, err := netip.ParsePrefix(s); err == nil {
			p = p.Masked()
			l.prefixes[p.Bits()][p.Addr()] = struct{}{}
			if !seen[p.Bits()] {
				seen[p.Bits()] = true
				l.hasLen = append(l.hasLen, p.Bits())
			}
		}
	})
	return l, nil
}

// Sizes возвращает размеры списков (домены, IP, CIDR).
func (l *Lists) Sizes() (domains, ips, cidrs int) {
	for _, m := range l.prefixes {
		cidrs += len(m)
	}
	return len(l.domains), len(l.ips), cidrs
}

// IP сообщает, входит ли адрес в белый список (точно или через CIDR).
func (l *Lists) IP(a netip.Addr) bool {
	if _, ok := slices.BinarySearchFunc(l.ips, a, func(x, t netip.Addr) int { return x.Compare(t) }); ok {
		return true
	}
	for _, bits := range l.hasLen {
		if bits > a.BitLen() {
			continue
		}
		p, err := a.Prefix(bits)
		if err != nil {
			continue
		}
		if _, ok := l.prefixes[bits][p.Addr()]; ok {
			return true
		}
	}
	return false
}

// Domain сообщает, разрешён ли домен (сам или как поддомен записи списка).
func (l *Lists) Domain(d string) bool {
	d = strings.ToLower(strings.TrimSuffix(d, "."))
	for d != "" {
		if _, ok := l.domains[d]; ok {
			return true
		}
		i := strings.Index(d, ".")
		if i < 0 {
			return false
		}
		d = d[i+1:]
	}
	return false
}

// Verdict — результат оценки узла.
type Verdict struct {
	IPListed  bool // адрес сервера в белом списке
	SNIListed bool // SNI/Host — разрешённый домен
	HostIsIP  bool // адрес сервера задан IP (иначе IP-признак не оценивался без резолва)
	Resolved  bool // хост — домен, и IP-признак получен резолвом (EvaluateResolved)
}

// Score — приоритет для мобильной сети: IP+SNI лучше всего; только IP — следом; только SNI — слабее; иначе 0.
func (v Verdict) Score() int {
	switch {
	case v.IPListed && v.SNIListed:
		return 3
	case v.IPListed:
		return 2
	case v.SNIListed:
		return 1
	}
	return 0
}

// Evaluate оценивает outbound в формате translator (map): server, tls.server_name, transport.host / headers.Host.
func (l *Lists) Evaluate(opts map[string]any) Verdict {
	var v Verdict
	if host, _ := opts["server"].(string); host != "" {
		if a, err := netip.ParseAddr(host); err == nil {
			v.HostIsIP = true
			v.IPListed = l.IP(a)
		}
	}
	var names []string
	if t, ok := opts["tls"].(map[string]any); ok {
		if s, _ := t["server_name"].(string); s != "" {
			names = append(names, s)
		}
	}
	if tr, ok := opts["transport"].(map[string]any); ok {
		if s, _ := tr["host"].(string); s != "" {
			names = append(names, s)
		}
		if hs, ok := tr["headers"].(map[string]any); ok {
			if s, _ := hs["Host"].(string); s != "" {
				names = append(names, s)
			}
		}
	}
	for _, n := range names {
		if l.Domain(n) {
			v.SNIListed = true
			break
		}
	}
	return v
}

// Resolver лениво резолвит домены в адресах узлов: ответы кэшируются, число настоящих запросов ограничено бюджетом (миллионы узлов
// массово резолвить нельзя: трафик и подозрительная активность), поэтому резолвится верх очереди, пока бюджет не исчерпан.
type Resolver struct {
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	Budget int // сколько запросов разрешено в сумме; <=0 — не резолвить

	mu    sync.Mutex
	cache map[string][]netip.Addr
	used  int
}

func (r *Resolver) addrs(ctx context.Context, host string) []netip.Addr {
	if r == nil || r.Lookup == nil {
		return nil
	}
	r.mu.Lock()
	if a, ok := r.cache[host]; ok {
		r.mu.Unlock()
		return a
	}
	if r.used >= r.Budget {
		r.mu.Unlock()
		return nil
	}
	r.used++
	r.mu.Unlock()
	a, _ := r.Lookup(ctx, host)
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string][]netip.Addr{}
	}
	r.cache[host] = a
	r.mu.Unlock()
	return a
}

// EvaluateResolved — Evaluate, который для хоста-домена с признаком SNI или Host из списка дополнительно резолвит имя и проверяет адреса
// по спискам. Домен без разрешённого SNI не резолвится: выше балла 2 он не поднимется, а запросов на него уйдёт больше всего.
func (l *Lists) EvaluateResolved(ctx context.Context, opts map[string]any, r *Resolver) Verdict {
	v := l.Evaluate(opts)
	if v.HostIsIP || !v.SNIListed {
		return v
	}
	host, _ := opts["server"].(string)
	if host == "" {
		return v
	}
	for _, a := range r.addrs(ctx, host) {
		v.Resolved = true
		if l.IP(a) {
			v.IPListed = true
			break
		}
	}
	return v
}
