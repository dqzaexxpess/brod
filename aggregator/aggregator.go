// Package aggregator загружает подписки параллельно (P3): у каждого источника свой таймаут,
// ETag/If-None-Match, кэш на диске и запасное зеркало (jsDelivr для GitHub raw).
package aggregator

import (
	"brod/importer"
	"brod/tgproxy"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unsafe"
)

const (
	// maxBodyBytes — предел тела ответа источника (после распаковки gzip). Самый большой из реальных источников: 46,6 МБ
	// (base64-список на 135 тыс. ссылок), поэтому 64 МиБ с запасом, но не «до исчерпания памяти и диска».
	maxBodyBytes = 64 << 20
	// maxLinksPerSource — предел числа ссылок из одного источника. Самый большой из реальных: около 135 тыс.
	maxLinksPerSource = 200_000
	// maxLinkBytes — предел длины одной ссылки. Самые длинные параметры узла в рабочей базе (JSON после разбора) — 11 КБ, ссылка не длиннее;
	// 16 КиБ с запасом, но не мегабайтная «ссылка» из одной строки.
	maxLinkBytes = 16 << 10
)

// ErrBodyTooLarge — тело ответа источника больше maxBodyBytes: оно не читается дальше и не пишется в кэш.
var ErrBodyTooLarge = errors.New("тело источника больше предела")

// CacheBase — путь файлов кэша источника без расширения («.txt» тело, «.etag» заголовок): хэш имени, а не само имя. Имя приходит
// от пользователя, из резервной копии и по OTA, и на Windows «..\..\x» вывело бы запись за каталог кэша. Кэш прежнего вида
// («/» заменён на «_») переносится под хэш при первом обращении.
func CacheBase(cacheDir, name string) string {
	sum := sha256.Sum256([]byte(name))
	base := filepath.Join(cacheDir, hex.EncodeToString(sum[:16]))
	if _, err := os.Stat(base + ".txt"); err != nil && ValidName(name) == nil {
		legacy := filepath.Join(cacheDir, strings.ReplaceAll(name, "/", "_"))
		for _, ext := range []string{".txt", ".etag"} {
			_ = os.Rename(legacy+ext, base+ext)
		}
	}
	return base
}

// ValidName проверяет имя источника: буквы, цифры, пробел и «._/@-», до 128 знаков, без «..» и без начальной «/».
func ValidName(name string) error {
	if name == "" || len(name) > 128 || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return errors.New("имя источника: 1–128 знаков, без «..» и начальной «/»")
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" ._/@-", r) {
			return fmt.Errorf("имя источника: недопустимый знак %q (разрешены буквы, цифры, пробел и «._/@-»)", r)
		}
	}
	return nil
}

// ValidURL проверяет адрес источника или зеркала: только https; http допускается на адрес самой машины (loopback).
func ValidURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return errors.New("адрес источника: нужен https-адрес")
	}
	if u.Scheme == "https" {
		return nil
	}
	if h := u.Hostname(); u.Scheme == "http" && (h == "localhost" || isLoopbackIP(h)) {
		return nil
	}
	return errors.New("адрес источника: нужен https-адрес (http только для этой машины)")
}

func isLoopbackIP(host string) bool {
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// Source — источник подписки.
type Source struct {
	Name     string
	URL      string
	Mirror   string // запасной путь, если основной недоступен (источники GitHub могут быть заблокированы)
	Priority int    // меньше = раньше: курируемые небольшие списки проверяются первыми, большие дампы — следом
	// Known — база уже содержит последнее содержимое источника (прошлый разбор удался): при ответе «не изменилось» (304) тело не
	// читается и не разбирается, результат приходит с NotModified и без ссылок.
	Known bool
	// SkipStaleParse — при сбое загрузки устаревший кэш только отмечается (FromCache, Bytes), но не читается и не разбирается: вызывающий
	// такой результат всё равно отбрасывает (на телефоне разбор кэшей 72 источников при сбое сети давал сотни МБ и секунды процессора).
	SkipStaleParse bool
	// Lazy — при удачной загрузке (и записи тела в кэш) ссылки не извлекаются: результат несёт CacheFile, а вызывающий читает и
	// разбирает файл сам, по одному источнику за раз (все скачанные источники разом в памяти давали пик ≈600 МиБ).
	// Если тело в кэш записать не удалось, результат приходит обычным, со ссылками.
	Lazy bool
}

// Result — итог загрузки одного источника.
type Result struct {
	Source      Source
	Links       []string
	TGLinks     []string // ссылки Telegram-прокси (tg://proxy, t.me/proxy, tg://socks)
	Err         error
	Took        time.Duration
	Bytes       int
	NotModified bool // ответ 304, взяты данные из кэша
	FromMirror  bool
	FromCache   bool // сеть недоступна, использован устаревший кэш
	ViaActive   bool // загружено через прокси активного узла (прямой путь не сработал)
	Meta        Meta // название, период обновления, квота (если источник их сообщает)
	// CacheFile — файл кэша с телом ответа: Links и TGLinks пусты, их достаёт LoadLinks (только при Source.Lazy).
	CacheFile string
}

// LoadLinks возвращает ссылки узлов и прокси Telegram результата: готовые (Links/TGLinks) либо извлечённые из CacheFile.
// Тело читается заново при каждом вызове и не удерживается: вызывающий держит у себя одну копию, пока разбирает источник.
func (r Result) LoadLinks() (links, tg []string, err error) {
	if r.CacheFile == "" {
		return r.Links, r.TGLinks, nil
	}
	body, err := os.ReadFile(r.CacheFile)
	if err != nil {
		return nil, nil, err
	}
	return ExtractLinks(body), tgproxy.ExtractLinks(body), nil
}

// Defaults — источники из приложения A плана.
func Defaults() []Source {
	raw := "https://raw.githubusercontent.com/"
	cdn := "https://cdn.jsdelivr.net/gh/"
	return []Source{
		{Name: "igareck/BLACK_VLESS_RUS", URL: raw + "igareck/vpn-configs-for-russia/main/BLACK_VLESS_RUS.txt", Mirror: cdn + "igareck/vpn-configs-for-russia@main/BLACK_VLESS_RUS.txt", Priority: 0},
		{Name: "igareck/BLACK_VLESS_RUS_mobile", URL: raw + "igareck/vpn-configs-for-russia/main/BLACK_VLESS_RUS_mobile.txt", Mirror: cdn + "igareck/vpn-configs-for-russia@main/BLACK_VLESS_RUS_mobile.txt", Priority: 0},
		{Name: "igareck/Vless-Reality-White-Lists-Rus-Mobile", URL: raw + "igareck/vpn-configs-for-russia/main/Vless-Reality-White-Lists-Rus-Mobile.txt", Mirror: cdn + "igareck/vpn-configs-for-russia@main/Vless-Reality-White-Lists-Rus-Mobile.txt", Priority: 0},
		{Name: "zieng2/vless_universal", URL: raw + "zieng2/wl/main/vless_universal.txt", Mirror: cdn + "zieng2/wl@main/vless_universal.txt", Priority: 0},
		{Name: "zieng2/vless_lite", URL: raw + "zieng2/wl/main/vless_lite.txt", Mirror: cdn + "zieng2/wl@main/vless_lite.txt", Priority: 0},
		{Name: "kort0881/vless", URL: raw + "kort0881/vpn-vless-configs-russia/main/output/vless.txt", Mirror: cdn + "kort0881/vpn-vless-configs-russia@main/output/vless.txt", Priority: 1},
		// только прокси Telegram (MTProto), не узлы: вкладка «Прокси»; обновляется раз в 12 часов
		{Name: "SoliSpirit/mtproto", URL: raw + "SoliSpirit/mtproto/master/all_proxies.txt", Mirror: cdn + "SoliSpirit/mtproto@master/all_proxies.txt", Priority: 5},
		// публичные каналы Telegram через веб-просмотр t.me/s/<канал> (последние ~20 сообщений, прокси меняются раз в час): выбраны по доле живых прокси;
		// каналы-воронки VPN и повторяющие один прокси (ProxyFree_Ru, tgmtproxylol, memtproxy, telega_proxies) не взяты
		{Name: "t.me/mtp4tg", URL: "https://t.me/s/mtp4tg", Priority: 5},
		{Name: "t.me/ProxyMTProto", URL: "https://t.me/s/ProxyMTProto", Priority: 5},
		{Name: "t.me/TProxyRU", URL: "https://t.me/s/TProxyRU", Priority: 5},
	}
}

// Fetch качает все источники параллельно и отдаёт результаты по мере готовности.
// Один зависший источник не задерживает остальные: у каждого свой perSource-таймаут.
func Fetch(ctx context.Context, srcs []Source, perSource time.Duration, cacheDir string) <-chan Result {
	return FetchVia(ctx, srcs, perSource, cacheDir, nil)
}

// FetchVia — Fetch через указанный HTTP-клиент (например, через прокси активного узла); nil = http.DefaultClient.
func FetchVia(ctx context.Context, srcs []Source, perSource time.Duration, cacheDir string, client *http.Client) <-chan Result {
	if client == nil {
		client = http.DefaultClient
	}
	out := make(chan Result, len(srcs))
	_ = os.MkdirAll(cacheDir, 0o700) // в кэше ссылки узлов (у пользовательских подписок — личные)
	done := make(chan struct{}, len(srcs))
	sem := make(chan struct{}, 16) // не больше 16 одновременных загрузок: сотни источников разом — нагрузка на хостинг и риск бана
	for _, s := range srcs {
		go func(s Source) {
			sem <- struct{}{}
			defer func() { <-sem }()
			out <- fetchOne(ctx, s, perSource, cacheDir, client)
			done <- struct{}{}
		}(s)
	}
	go func() {
		for range srcs {
			<-done
		}
		close(out)
	}()
	return out
}

func fetchOne(ctx context.Context, s Source, timeout time.Duration, cacheDir string, client *http.Client) Result {
	start := time.Now()
	r := Result{Source: s}
	base := CacheBase(cacheDir, s.Name)
	etag, _ := os.ReadFile(base + ".etag")
	var hdr http.Header
	inCache := false // тело лежит в файле кэша (для Lazy)
	body, notMod, err := get(ctx, client, s.URL, timeout, string(etag), base+".etag", &hdr)
	if err != nil && s.Mirror != "" {
		body, notMod, err = get(ctx, client, s.Mirror, timeout, "", "", &hdr)
		r.FromMirror = err == nil
	}
	switch {
	case err == nil && notMod && s.Known:
		r.NotModified = true
		r.Took = time.Since(start)
		return r // быстрый путь: источник не менялся, ничего читать и разбирать не нужно
	case err == nil && notMod:
		r.NotModified = true
		body, err = os.ReadFile(base + ".txt")
		inCache = err == nil
		if err != nil { // etag есть, а файла кэша нет (запись тела не удалась): сбрасываем etag и качаем заново, иначе источник застрянет на 304
			_ = os.Remove(base + ".etag")
			r.NotModified = false
			body, _, err = get(ctx, client, s.URL, timeout, "", base+".etag", &hdr)
			if err == nil {
				inCache = os.WriteFile(base+".txt", body, 0o600) == nil
			}
		}
	case err == nil:
		inCache = os.WriteFile(base+".txt", body, 0o600) == nil
	case s.SkipStaleParse: // сеть не помогла; кэш нужен лишь как признак «есть старая копия»
		if fi, serr := os.Stat(base + ".txt"); serr == nil {
			r.FromCache, r.Bytes, err = true, int(fi.Size()), nil
			r.Took = time.Since(start)
			return r
		}
	default: // сеть не помогла — берём устаревший кэш, если он есть
		if cached, cerr := os.ReadFile(base + ".txt"); cerr == nil {
			body, r.FromCache, err = cached, true, nil
		}
	}
	r.Err, r.Took, r.Bytes = err, time.Since(start), len(body)
	if err == nil {
		if s.Lazy && inCache && !r.FromCache {
			r.CacheFile = base + ".txt" // тело в память не удерживается: ссылки достанет вызывающий (LoadLinks)
		} else {
			r.Links = ExtractLinks(body)
			r.TGLinks = tgproxy.ExtractLinks(body)
		}
		r.Meta = ParseMeta(hdr, body)
	}
	return r
}

// ExtractLinks достаёт ссылки узлов из тела подписки: список строк «схема://…», весь список в base64 (стандартный формат
// пользовательских подписок) либо полный конфиг (через importer). Комментарии («#…») пропускаются.
func ExtractLinks(body []byte) []string {
	// полный конфиг (sing-box JSON, Clash YAML, Xray JSON): берём только узлы, остальное отбрасывается (P6)
	if links, ok := importer.Links(body); ok {
		return capLinks(links)
	}
	// ссылки — подстроки тела: копия string(body) удвоила бы пик на самом большом источнике (47 МБ), поэтому строка делит память с body.
	// Вызывающие тело после этого не меняют (оно только читается из ответа или файла).
	links := linesWithLinks(unsafe.String(unsafe.SliceData(body), len(body)))
	if len(links) > 0 {
		return links
	}
	trimmed := strings.Join(strings.Fields(string(body)), "")
	if trimmed == "" {
		return nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if dec, err := enc.DecodeString(trimmed); err == nil {
			if links := linesWithLinks(string(dec)); len(links) > 0 {
				return links
			}
		}
	}
	return nil
}

// capLinks отбрасывает слишком длинные ссылки и обрезает список до maxLinksPerSource.
func capLinks(in []string) []string {
	out := in[:0:0]
	for _, l := range in {
		if len(l) <= maxLinkBytes {
			out = append(out, l)
			if len(out) >= maxLinksPerSource {
				break
			}
		}
	}
	return out
}

func linesWithLinks(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); len(l) <= maxLinkBytes && strings.Contains(l, "://") && !strings.HasPrefix(l, "#") {
			out = append(out, l)
			if len(out) >= maxLinksPerSource {
				break
			}
		}
	}
	return out
}

func get(ctx context.Context, client *http.Client, url string, timeout time.Duration, etag, etagPath string, hdr *http.Header) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if hdr != nil {
		*hdr = resp.Header.Clone()
	}
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, true, nil
	case http.StatusOK:
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
		if err == nil && len(b) > maxBodyBytes {
			return nil, false, ErrBodyTooLarge
		}
		if err == nil && etagPath != "" {
			if e := resp.Header.Get("ETag"); e != "" {
				_ = os.WriteFile(etagPath, []byte(e), 0o600)
			}
		}
		return b, false, err
	}
	return nil, false, &httpError{resp.StatusCode}
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "HTTP " + http.StatusText(e.code) }

// LoadCached читает последнюю сохранённую копию каждого источника с диска (без сети): нужна для мгновенного старта (P3).
func LoadCached(srcs []Source, cacheDir string) []Result {
	var out []Result
	for _, s := range srcs {
		b, err := os.ReadFile(CacheBase(cacheDir, s.Name) + ".txt")
		if err != nil {
			continue
		}
		r := Result{Source: s, FromCache: true, Bytes: len(b), Links: ExtractLinks(b)}
		out = append(out, r)
	}
	return out
}

// ParseSourcesFile читает дополнительные источники: строки «имя|url|приоритет» (приоритет по умолчанию 1), «#» — комментарий.
func ParseSourcesFile(path string) ([]Source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Source
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Split(l, "|")
		if len(f) < 2 {
			continue
		}
		s := Source{Name: strings.TrimSpace(f[0]), URL: strings.TrimSpace(f[1]), Priority: 1}
		if len(f) > 2 {
			if n, err := strconv.Atoi(strings.TrimSpace(f[2])); err == nil {
				s.Priority = n
			}
		}
		out = append(out, s)
	}
	return out, nil
}
