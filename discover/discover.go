// Package discover ищет в README известных GitHub-репозиториев ссылки на файлы подписок (автопоиск источников, пункт A6 дорожной карты).
//
// Только GET на raw.githubusercontent.com (без GitHub API, без лимитов API): README репозитория, затем найденные в нём файлы подписок.
// У каждого запроса таймаут и предел размера, у прохода — предел числа репозиториев и файлов; отмена контекста прерывает всё.
// Ничего из скачанного не исполняется. Ссылки узлов наружу не отдаются: результат — счётчики, схемы и небольшая случайная выборка
// ссылок для проверки (в памяти вызывающего, в базу не пишется).
package discover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"brod/aggregator"
)

const (
	// RawBase — единственный хост, откуда берутся README и файлы подписок.
	RawBase = "https://raw.githubusercontent.com/"
	// AggregatorRepo — README этого репозитория перечисляет много репозиториев-раздатчиков (то же зерно, что у scripts/discover-sources.py).
	AggregatorRepo = "mehdirzfx/v2ray-sub"
)

// Schemes — схемы ссылок узлов, которые считаются (как в scripts/discover-sources.py).
var Schemes = map[string]bool{"vless": true, "vmess": true, "trojan": true, "ss": true, "hysteria2": true, "hy2": true, "hysteria": true,
	"tuic": true, "ssr": true, "wireguard": true, "anytls": true, "socks": true, "mieru": true}

// Options — параметры прохода; нулевые значения заменяются умолчаниями.
type Options struct {
	Client       *http.Client // nil — клиент с таймаутом и запретом редиректа на чужой хост
	Base         string       // база raw-адресов (по умолчанию RawBase; тесты указывают локальный сервер)
	Aggregator   string       // репозиторий-агрегатор для расширения зерна; "-" — не использовать (по умолчанию AggregatorRepo)
	MaxRepos     int          // репозиториев за проход (150)
	MaxFiles     int          // файлов за проход (300)
	FilesPerRepo int          // файлов из одного README (40)
	ReadmeMax    int64        // предел размера README, байт (2 МиБ)
	FileMax      int64        // предел размера файла подписки, байт (12 МиБ)
	Timeout      time.Duration
	Parallel     int                   // одновременных загрузок (4: проход идёт в фоне, в том числе на телефоне)
	MinLinks     int                   // файлы с меньшим числом ссылок узлов не кандидаты (20)
	SampleSize   int                   // размер случайной выборки ссылок (20)
	Skip         func(url string) bool // true — адрес уже известен (источник или скрытый кандидат): файл не качается
	Rand         *rand.Rand            // nil — свой генератор
	Salt         []byte                // соль установки для отпечатка набора ссылок (sketch.go); пусто — отпечаток не считается
}

func (o *Options) defaults() {
	if o.Base == "" {
		o.Base = RawBase
	}
	if o.Aggregator == "" {
		o.Aggregator = AggregatorRepo
	}
	if o.MaxRepos <= 0 {
		o.MaxRepos = 150
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 300
	}
	if o.FilesPerRepo <= 0 {
		o.FilesPerRepo = 40
	}
	if o.ReadmeMax <= 0 {
		o.ReadmeMax = 2 << 20
	}
	if o.FileMax <= 0 {
		o.FileMax = 12 << 20
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.Parallel <= 0 {
		o.Parallel = 4
	}
	if o.MinLinks <= 0 {
		o.MinLinks = 20
	}
	if o.SampleSize <= 0 {
		o.SampleSize = 20
	}
	if o.Rand == nil {
		o.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: o.Timeout, CheckRedirect: SameHostRedirect(o.Base)}
	}
}

// SameHostRedirect — правило редиректов клиента поиска: не больше двух и только на тот же хост, что у базового адреса
// (README и файлы подписок не должны уводить поиск на чужие серверы).
func SameHostRedirect(baseURL string) func(*http.Request, []*http.Request) error {
	base, _ := url.Parse(baseURL)
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || base == nil || req.URL.Host != base.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}
}

// Found — найденный файл подписки. Sample — случайная выборка его ссылок (секреты): только для проверки в памяти.
type Found struct {
	URL     string
	Repo    string
	Name    string
	Links   int
	Schemes map[string]int
	Sample  []string
	Sketch  []uint64 // отпечаток набора ссылок (bottom-k хэши, sketch.go); nil без соли
}

// Stats — итог прохода.
type Stats struct {
	Repos    int // репозиториев просмотрено
	Readmes  int // README прочитано
	Files    int // файлов скачано
	TooLarge int // файлов больше предела
	Failed   int // файлов, которые не скачались
}

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	urlRe    = regexp.MustCompile(`https?://[^\s)"'<>\]]+`)
	githubRe = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:[/)\s"'<>]|$)`)
	extRe    = regexp.MustCompile(`(?i)\.(txt|list|conf|sub)(\?|$)|/(sub|subscription|v2ray|vless|configs?|proxies)([/?]|$)`)
	badRe    = regexp.MustCompile(`(?i)(\.png|\.jpg|\.svg|\.md$|\.yaml$|\.yml$|\.json$|\.sh$|\.py$|license|shields\.io|badge)`)
	ghFileRe = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/(?:raw|blob)/(.+)$`)
)

// ValidRepo — вид «владелец/репозиторий» из безопасных знаков.
func ValidRepo(r string) bool {
	return repoRe.MatchString(r) && !strings.HasPrefix(r, ".") && !strings.Contains(r, "..") &&
		!strings.HasSuffix(strings.ToLower(r), ".md") && !strings.HasSuffix(strings.ToLower(r), ".git")
}

// RepoOfURL достаёт «владелец/репозиторий» из адреса вида <base>/<owner>/<repo>/…; base — RawBase или локальная база в тестах.
func RepoOfURL(base, raw string) (string, bool) {
	if base == "" {
		base = RawBase
	}
	rest, ok := strings.CutPrefix(raw, base)
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		return "", false
	}
	r := parts[0] + "/" + parts[1]
	return r, ValidRepo(r)
}

// ReadmeRepos — репозитории GitHub, названные в тексте README агрегатора, без повторов.
func ReadmeRepos(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range githubRe.FindAllStringSubmatch(text, -1) {
		r := strings.TrimRight(m[1], ".")
		if ValidRepo(r) && !seen[strings.ToLower(r)] {
			seen[strings.ToLower(r)] = true
			out = append(out, r)
		}
	}
	return out
}

// normalize приводит ссылку на файл GitHub к raw-адресу.
func normalize(u string) string {
	u = strings.TrimRight(u, ".,;")
	u = ghFileRe.ReplaceAllString(u, "https://raw.githubusercontent.com/$1/$2/$3")
	return strings.ReplaceAll(u, "/refs/heads/", "/")
}

// ReadmeLinks находит в тексте README ссылки на файлы подписок (только на хосте raw-адресов base), без повторов, не больше limit.
func ReadmeLinks(base, text string, limit int) []string {
	if base == "" {
		base = RawBase
	}
	var out []string
	seen := map[string]bool{}
	for _, u := range urlRe.FindAllString(text, -1) {
		u = normalize(u)
		if !strings.HasPrefix(u, base) || !extRe.MatchString(u) || badRe.MatchString(u) || seen[u] {
			continue
		}
		if aggregator.ValidURL(u) != nil || len(u) > 512 {
			continue
		}
		seen[u] = true
		out = append(out, u)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// CandidateName — имя будущего источника: «владелец/репозиторий/файл» без расширения.
func CandidateName(repo, rawURL string) string {
	u, err := url.Parse(rawURL)
	file := rawURL
	if err == nil {
		file = u.Path[strings.LastIndex(u.Path, "/")+1:]
	}
	if i := strings.LastIndex(file, "."); i > 0 {
		file = file[:i]
	}
	if file == "" {
		file = "sub"
	}
	name := repo + "/" + file
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-', r == '/':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name = strings.ReplaceAll(b.String(), "..", "_")
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

// fetch читает ответ с пределом размера; больше предела — aggregator.ErrBodyTooLarge.
func fetch(ctx context.Context, cl *http.Client, rawURL string, limit int64, timeout time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 brod-discover")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if int64(len(b)) > limit {
		return nil, resp.StatusCode, aggregator.ErrBodyTooLarge
	}
	return b, resp.StatusCode, nil
}

// readme читает README репозитория (ветки main, master; имена README.md, readme.md, Readme.md).
func readme(ctx context.Context, o *Options, repo string) (string, bool) {
	for _, br := range []string{"main", "master"} {
		for _, name := range []string{"README.md", "readme.md", "Readme.md"} {
			if ctx.Err() != nil {
				return "", false
			}
			b, _, err := fetch(ctx, o.Client, o.Base+repo+"/"+br+"/"+name, o.ReadmeMax, o.Timeout)
			if err == nil {
				return string(b), true
			}
		}
	}
	return "", false
}

// Analyze считает ссылки узлов и схемы в теле подписки (список строк, base64 или полный конфиг) и берёт случайную выборку до n ссылок
// (равномерно, резервуаром: тело целиком не держится).
func Analyze(body []byte, n int, rng *rand.Rand) (links int, schemes map[string]int, sample []string) {
	links, schemes, sample, _ = analyze(body, n, rng, nil)
	return
}

// analyze — Analyze плюс отпечаток набора ссылок, если передан sb.
func analyze(body []byte, n int, rng *rand.Rand, sb *SketchBuilder) (links int, schemes map[string]int, sample []string, sketch []uint64) {
	schemes = map[string]int{}
	for _, l := range aggregator.ExtractLinks(body) {
		i := strings.Index(l, "://")
		if i <= 0 || !Schemes[strings.ToLower(l[:i])] {
			continue
		}
		schemes[strings.ToLower(l[:i])]++
		links++
		if sb != nil {
			sb.Add(l)
		}
		if len(sample) < n {
			sample = append(sample, strings.Clone(l))
		} else if j := rng.Intn(links); j < n {
			sample[j] = strings.Clone(l)
		}
	}
	if sb != nil {
		sketch = sb.Sum()
	}
	return links, schemes, sample, sketch
}

// Run делает проход: зерно (seeds плюс репозитории из README агрегатора) → README → файлы подписок → найденные кандидаты по убыванию числа ссылок.
// Ошибка — только отмена контекста; недоступные репозитории и файлы пропускаются.
func Run(ctx context.Context, opt Options, seeds []string) ([]Found, Stats, error) {
	opt.defaults()
	o := &opt
	var st Stats
	var stMu sync.Mutex

	var repos []string
	seen := map[string]bool{}
	add := func(r string) {
		if ValidRepo(r) && !seen[strings.ToLower(r)] {
			seen[strings.ToLower(r)] = true
			repos = append(repos, r)
		}
	}
	for _, r := range seeds {
		add(r)
	}
	if o.Aggregator != "-" {
		if text, ok := readme(ctx, o, o.Aggregator); ok {
			extra := ReadmeRepos(text)
			o.Rand.Shuffle(len(extra), func(i, j int) { extra[i], extra[j] = extra[j], extra[i] }) // не помещающиеся в предел репозитории за дни обходятся по очереди
			for _, r := range extra {
				add(r)
			}
		}
	}
	if len(repos) > o.MaxRepos {
		repos = repos[:o.MaxRepos]
	}
	st.Repos = len(repos)

	type fileRef struct{ url, repo string }
	var (
		files []fileRef
		fMu   sync.Mutex
		fSeen = map[string]bool{}
	)
	run := func(n int, f func(i int)) {
		sem := make(chan struct{}, o.Parallel)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			if ctx.Err() != nil {
				break
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(i int) {
				defer func() { <-sem; wg.Done() }()
				f(i)
			}(i)
		}
		wg.Wait()
	}
	run(len(repos), func(i int) {
		text, ok := readme(ctx, o, repos[i])
		if !ok {
			return
		}
		links := ReadmeLinks(o.Base, text, o.FilesPerRepo)
		fMu.Lock()
		defer fMu.Unlock()
		stMu.Lock()
		st.Readmes++
		stMu.Unlock()
		for _, u := range links {
			if fSeen[u] || (o.Skip != nil && o.Skip(u)) {
				continue
			}
			fSeen[u] = true
			files = append(files, fileRef{u, repos[i]})
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, st, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].url < files[j].url }) // порядок не зависит от гонки потоков
	if len(files) > o.MaxFiles {
		o.Rand.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
		files = files[:o.MaxFiles]
	}

	var out []Found
	var oMu sync.Mutex
	run(len(files), func(i int) {
		f := files[i]
		body, _, err := fetch(ctx, o.Client, f.url, o.FileMax, o.Timeout)
		if err != nil {
			if ctx.Err() == nil {
				stMu.Lock()
				if errors.Is(err, aggregator.ErrBodyTooLarge) {
					st.TooLarge++
				} else {
					st.Failed++
				}
				stMu.Unlock()
			}
			return
		}
		oMu.Lock()
		rng := rand.New(rand.NewSource(o.Rand.Int63())) // rand.Rand не потокобезопасен
		oMu.Unlock()
		var sb *SketchBuilder
		if len(o.Salt) > 0 {
			sb = NewSketchBuilder(o.Salt)
		}
		n, sch, sample, sketch := analyze(body, o.SampleSize, rng, sb)
		body = nil
		stMu.Lock()
		st.Files++
		stMu.Unlock()
		if n < o.MinLinks {
			return
		}
		oMu.Lock()
		out = append(out, Found{URL: f.url, Repo: f.repo, Name: CandidateName(f.repo, f.url), Links: n, Schemes: sch, Sample: sample, Sketch: sketch})
		oMu.Unlock()
	})
	if err := ctx.Err(); err != nil {
		return nil, st, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Links != out[j].Links {
			return out[i].Links > out[j].Links
		}
		return out[i].URL < out[j].URL
	})
	return out, st, nil
}
