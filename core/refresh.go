package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"brod/aggregator"
	"brod/store"
	"brod/tgproxy"
	"brod/translator"
)

// ErrBusy — обновление уже идёт.
var ErrBusy = errors.New("обновление уже идёт")

// ErrMetered — файл обновления не скачан: сеть платная, а согласия пользователя нет (allow_metered).
var ErrMetered = errors.New("сеть мобильная или с оплатой за трафик: скачивание не начато без согласия")

// SourceReport — итог по одному источнику.
type SourceReport struct {
	Name        string
	Links       int    // ссылок в источнике
	TGProxies   int    // сколько прокси Telegram дал источник
	Nodes       int    // узлов после разбора и дедупликации внутри источника
	Lost        int    // узлов, пропавших из источника
	Err         string // пусто — загрузка удалась
	NotModified bool
	FromMirror  bool
	ViaActive   bool // загружен через активный узел (прямой путь не сработал)
}

// RefreshReport — итог обновления.
type RefreshReport struct {
	Sources []SourceReport
	Added   int // новых узлов в базе
	Total   int
	Present int
	Took    time.Duration
}

// defaultFetch: сначала напрямую (с зеркалом и ETag), а источники, которые так не загрузились, — повторно через прокси активного
// узла (GitHub и CDN из РФ бывают режутся, а узел за пределами). Нет активного узла — остаётся результат прямой попытки.
func (c *Core) defaultFetch(ctx context.Context, srcs []aggregator.Source, perSource time.Duration, cacheDir string) <-chan aggregator.Result {
	direct, via := c.directFetch, c.viaFetch
	if direct == nil {
		direct = aggregator.Fetch
	}
	if via == nil {
		via = aggregator.FetchVia
	}
	// устаревший кэш при сбое загрузки ingest всё равно отбрасывает (case res.FromCache): не читаем и не разбираем его
	srcs = append([]aggregator.Source(nil), srcs...)
	// удачно скачанный источник приходит без ссылок (только файл кэша), их читает и разбирает ingestResult по одному источнику
	for i := range srcs {
		srcs[i].SkipStaleParse = true
		srcs[i].Lazy = true
	}
	out := make(chan aggregator.Result, len(srcs))
	go func() {
		defer close(out)
		held := map[string]aggregator.Result{}
		var failed []aggregator.Source
		for r := range direct(ctx, srcs, perSource, cacheDir) {
			if r.Err != nil || r.FromCache {
				held[r.Source.Name] = r
				failed = append(failed, r.Source)
				continue
			}
			out <- r
		}
		c.mu.Lock()
		hasActive := c.selected[store.ScopeActive] != ""
		c.mu.Unlock()
		if len(failed) > 0 && hasActive && ctx.Err() == nil {
			for r := range via(ctx, failed, perSource, cacheDir, c.proxyClient(perSource)) {
				if r.Err == nil && !r.FromCache {
					r.ViaActive = true
					delete(held, r.Source.Name)
					out <- r
				}
			}
		}
		for _, r := range held {
			out <- r
		}
	}()
	return out
}

// proxyClient — HTTP-клиент через собственный вход для приложений (дальше dispatch → активный узел).
func (c *Core) proxyClient(timeout time.Duration) *http.Client {
	u := &url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", c.cfg.AppHost, c.cfg.AppPort), User: url.UserPassword(c.appUser, c.appPass)}
	return &http.Client{Timeout: timeout * 2, Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}}
}

// seedSources записывает источники по умолчанию: включённость и статистика существующих не сбрасываются.
func (c *Core) seedSources() error { return c.SeedSources(c.cfg.SeedSources) }

// SeedSources добавляет или обновляет источники из списка (адрес, зеркало, приоритет); включённость и статистика сохраняются.
// Вызывается при старте и по SIGHUP после перечитывания файла источников.
func (c *Core) SeedSources(srcs []aggregator.Source) error {
	now := c.cfg.Now()
	for _, s := range srcs {
		if err := c.st.UpsertSource(store.SourceRec{ID: s.Name, URL: s.URL, Mirror: s.Mirror, Priority: s.Priority, Enabled: true}, now); err != nil {
			return err
		}
	}
	return nil
}

// Версия разбора подписок: при её изменении (например, улучшен транслятор) все источники один раз разбираются заново, даже если
// они не менялись; иначе быстрый путь для «не изменилось» оставил бы в базе узлы, разобранные старой версией.
const (
	ingestRev        = "1"
	settingIngestRev = "ingest.rev"
	// время начала последнего завершённого обновления (unix, с): перезапуск ядра (включение VPN, смена режима, перезапуск службы)
	// не запускает полное обновление заново, если прошлое было недавно
	settingRefreshLast = "refresh.last"
)

// firstRefreshDelay — через сколько после старта ядра запускать первое автоматическое обновление: сразу (0,5 с), если прошлого
// не было или оно старше периода, иначе — когда период истечёт. Полное обновление — загрузка и разбор сотни тысяч узлов,
// десятки секунд процессора; повтор при каждом включении VPN нагружал ПК и задерживал остановку.
func (c *Core) firstRefreshDelay() time.Duration {
	const soon = 500 * time.Millisecond
	v, ok, _ := c.st.Setting(settingRefreshLast)
	if !ok {
		return soon
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return soon
	}
	age := c.cfg.Now().Sub(time.Unix(sec, 0))
	if age < 0 || age >= c.cfg.RefreshInterval {
		return soon
	}
	return max(c.cfg.RefreshInterval-age, soon)
}

// Refresh обновляет подписки: параллельная загрузка (ETag, зеркала, кэш), разбор, дедупликация, запись.
// Активный узел и выбор по целям не затрагиваются никогда (P1): обновление только пополняет базу и отмечает, что узел
// пропал из источника. Сбой источника не меняет присутствие его узлов. initiator: user | auto | api:<приложение>.
func (c *Core) Refresh(ctx context.Context, initiator string) (RefreshReport, error) {
	if !c.refreshMu.TryLock() {
		return RefreshReport{}, ErrBusy
	}
	defer c.refreshMu.Unlock()
	c.act.update(func(a *Activity) { a.Refreshing = true })
	defer c.act.update(func(a *Activity) { a.Refreshing = false })
	start := c.cfg.Now()
	rep := RefreshReport{}
	before, _, err := c.st.CountNodes()
	if err != nil {
		return rep, err
	}
	recs, err := c.st.Sources()
	if err != nil {
		return rep, err
	}
	var srcs []aggregator.Source
	skipped := 0
	revOK := false // версия разбора не менялась: источники, не изменившиеся с прошлого раза, можно не разбирать заново
	if v, ok, _ := c.st.Setting(settingIngestRev); ok && v == ingestRev {
		revOK = true
	}
	lastCount := map[string]int{}
	var yields map[string]store.SourceYield
	if initiator == "auto" {
		yields = c.sourceYields(start)
	}
	skippedDead := 0
	for _, r := range recs {
		if !r.Enabled {
			continue
		}
		// добавленные пользователем источники, сохранённые до проверок имени и адреса (или записанные в базу мимо AddSource),
		// перепроверяются при каждом проходе; встроенные источники задаёт код, их не проверяем
		if r.UserAdded {
			if err := aggregator.ValidName(r.ID); err != nil {
				log.Printf("источник %q пропущен: %v", r.ID, err)
				continue
			}
			if err := aggregator.ValidURL(r.URL); err != nil || (r.Mirror != "" && aggregator.ValidURL(r.Mirror) != nil) {
				log.Printf("источник %q пропущен: адрес не проходит проверку (https, http только на адрес этой машины)", r.ID)
				continue
			}
		}
		if initiator == "auto" && sourceIsDeadWeight(r, yields[r.ID], start) {
			skippedDead++
			continue
		}
		// автоматическое обновление уважает период, который просит сам источник (profile-update-interval); ручное — нет
		if initiator == "auto" && r.UpdateHours > 0 && !r.LastOK.IsZero() && start.Sub(r.LastOK) < time.Duration(r.UpdateHours)*time.Hour {
			skipped++
			continue
		}
		srcs = append(srcs, aggregator.Source{Name: r.ID, URL: r.URL, Mirror: r.Mirror, Priority: r.Priority,
			Known: revOK && !r.LastOK.IsZero() && r.LastErr == "" && r.LastCount > 0})
		lastCount[r.ID] = r.LastCount
	}
	stamp := start.UnixNano()
	fetch := c.cfg.Fetch
	if fetch == nil {
		fetch = c.defaultFetch
	}
	// источники обрабатываются по одному и потоком (ingestResult): ссылки читаются из кэша, разбираются и пишутся порциями,
	// так что в памяти одновременно один источник, а не все ≈478 тыс. ссылок. Загрузка при этом идёт вперёд параллельно
	// (результаты Lazy — без ссылок, очередь готовых ничего не весит); разбор перекрывается с записью внутри источника.
	fetched := fetch(ctx, srcs, c.cfg.FetchTimeout, c.cfg.CacheDir)
	var redo []aggregator.Source // источники, чьё «не изменилось» не подтвердилось базой: разбираем полностью
	for res := range fetched {
		if ctx.Err() != nil {
			// демон останавливается: недописанные источники обновятся при следующем запуске (как при сбое загрузки),
			// а запись в базу сотен тысяч узлов задержала бы остановку на секунды (службе Windows на это дано 10 с)
			continue
		}
		sr := c.ingestResult(ctx, res, stamp, lastCount[res.Source.Name])
		if sr.needFull {
			s := res.Source
			s.Known = false
			redo = append(redo, s)
			continue
		}
		rep.Sources = append(rep.Sources, sr.SourceReport)
	}
	if len(redo) > 0 && ctx.Err() == nil {
		for res := range fetch(ctx, redo, c.cfg.FetchTimeout, c.cfg.CacheDir) {
			rep.Sources = append(rep.Sources, c.ingestResult(ctx, res, stamp, 0).SourceReport)
		}
	}
	if err := ctx.Err(); err != nil {
		// прерванное обновление не отмечает версию разбора и не чистит базу: часть источников не записана
		return rep, err
	}
	_ = c.st.SetSetting(settingIngestRev, ingestRev)
	_ = c.st.SetSetting(settingRefreshLast, strconv.FormatInt(start.Unix(), 10))
	sort.Slice(rep.Sources, func(i, j int) bool { return rep.Sources[i].Name < rep.Sources[j].Name })
	c.kickTG() // новые прокси Telegram — на проверку
	c.purge()
	c.housekeeping()
	c.invalidateCounts()
	after, present, err := c.counts()
	if err != nil {
		return rep, err
	}
	rep.Added, rep.Total, rep.Present, rep.Took = after-before, after, present, c.cfg.Now().Sub(start)
	failed := 0
	for _, s := range rep.Sources {
		if s.Err != "" {
			failed++
		}
	}
	c.bus.Publish(store.Event{Kind: EvRefresh, Initiator: initiator,
		Detail: fmt.Sprintf("источников %d (сбоев %d, пропущено по периоду источника %d, без живых узлов за неделю %d), новых узлов %d, всего %d, доступно %d", len(rep.Sources), failed, skipped, skippedDead, rep.Added, rep.Total, rep.Present)})
	return rep, ctx.Err()
}

type ingested struct {
	SourceReport
	needFull bool // быстрый путь 304 не подтвердился базой — источник нужно загрузить и разобрать полностью
}

// ingestChunk — сколько узлов источника пишется одной транзакцией (между порциями проверяется отмена); переменная — для теста.
var ingestChunk = 5000

// errIngestCanceled — запись источника прервана отменой (остановка ядра, выключение VPN).
var errIngestCanceled = errors.New("прервано")

// ingestResult записывает один источник в базу (последовательная часть обновления). lastCount — сколько ссылок было в источнике
// при прошлом разборе (для проверки быстрого пути). Ссылки читаются из кэша (aggregator.Result.LoadLinks), разбираются и пишутся
// порциями по ingestChunk: в памяти не весь срез записей источника, а порция плюс множество идентификаторов для дедупликации.
func (c *Core) ingestResult(ctx context.Context, res aggregator.Result, stamp int64, lastCount int) ingested {
	name := res.Source.Name
	rep := SourceReport{Name: name, Links: len(res.Links), NotModified: res.NotModified, FromMirror: res.FromMirror, ViaActive: res.ViaActive}
	now := c.cfg.Now()
	fail := func(msg string) ingested {
		rep.Err = msg
		_ = c.st.RecordFetch(name, 0, msg, now)
		return ingested{SourceReport: rep}
	}
	switch {
	case res.NotModified && res.Links == nil && res.CacheFile == "" && res.Err == nil: // быстрый путь: источник не менялся с прошлого разбора
		n, err := c.st.TouchSource(name, stamp, now, now.Add(-24*time.Hour))
		if err != nil {
			return fail("отметка присутствия: " + err.Error())
		}
		nt, err := c.st.TouchTGSource(name, stamp, now)
		if err != nil {
			return fail("отметка присутствия прокси: " + err.Error())
		}
		// в базе нет ни одного присутствующего узла источника (база заменена или восстановлена): быстрому пути верить нельзя.
		// Сравнивать с числом ссылок прошлого разбора нельзя: у дампов с дубликатами узлов в разы меньше, чем ссылок.
		if n == 0 && nt == 0 {
			return ingested{SourceReport: rep, needFull: true}
		}
		rep.Nodes, rep.Links, rep.TGProxies = n, n, nt
		_ = c.st.RecordFetch(name, lastCount, "", now)
		return ingested{SourceReport: rep}
	case res.Err != nil:
		return fail(res.Err.Error())
	case res.FromCache:
		return fail("источник недоступен, использована старая копия") // старые данные присутствие не обновляют
	}
	links, tgLinks, err := res.LoadLinks()
	if err != nil {
		return fail("чтение кэша источника: " + err.Error())
	}
	tg := parseTGLinks(tgLinks)
	rep.Links, rep.TGProxies = len(links), len(tg)
	if len(links) == 0 && len(tg) == 0 {
		return fail("в источнике нет ссылок") // пустой ответ не должен «убивать» все узлы
	}
	nodes, err := c.ingestNodes(ctx, name, stamp, links, now)
	rep.Nodes = nodes
	links, tgLinks = nil, nil // тело источника больше не нужно: освобождается до записи метаданных и следующего источника
	switch {
	case errors.Is(err, errIngestCanceled):
		// остановка или выключение VPN не ждут записи остатка большого источника. Записанные порции без EndRefresh
		// ничего не меняют (присутствие узлов не пересчитано), недописанный источник обновится в следующий раз.
		rep.Err = errIngestCanceled.Error()
		return ingested{SourceReport: rep}
	case err != nil:
		return fail("запись в базу: " + err.Error())
	case nodes == 0 && len(tg) == 0:
		return fail("ни одна ссылка не разобрана")
	}
	if m := res.Meta; !m.IsZero() {
		if err := c.st.RecordSourceMeta(name, m.Title, m.UpdateHours, m.Upload+m.Download, m.Total, m.Expire); err != nil {
			c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "сведения источника: " + err.Error()})
		}
	}
	if nodes > 0 { // источник, отдавший только прокси Telegram, не должен «убивать» свои прежние узлы
		lost, err := c.st.EndRefresh(name, stamp)
		if err != nil {
			return fail("отметка пропавших: " + err.Error())
		}
		rep.Lost = lost
	}
	if len(tg) > 0 {
		if err := c.st.UpsertTGProxies(name, stamp, tg, now); err != nil {
			return fail("запись прокси Telegram: " + err.Error())
		}
		if _, err := c.st.EndTGRefresh(name, stamp); err != nil {
			return fail("отметка пропавших прокси: " + err.Error())
		}
	}
	_ = c.st.RecordFetch(name, rep.Links+len(tg), "", now)
	return ingested{SourceReport: rep}
}

// ingestNodes разбирает ссылки источника и пишет узлы в базу порциями: разбор идёт в отдельной горутине и перекрывается с записью
// (в очереди не больше одной готовой порции). Возвращает число разных узлов, разобранных к моменту возврата, и ошибку записи либо
// errIngestCanceled. Порядок записи узлов и правила владения те же, что при записи целым срезом.
func (c *Core) ingestNodes(ctx context.Context, name string, stamp int64, links []string, now time.Time) (int, error) {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan []store.NodeRec, 1)
	var parsed int
	var complete bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(ch)
		parsed, complete = parseLinksChunked(pctx, links, ingestChunk, func(recs []store.NodeRec) bool {
			select {
			case ch <- recs:
				return true
			case <-pctx.Done():
				return false
			}
		})
	}()
	var werr error
	for recs := range ch {
		if ctx.Err() != nil {
			werr = errIngestCanceled
			break
		}
		if err := c.st.UpsertNodes(name, stamp, recs, now); err != nil {
			werr = err
			break
		}
	}
	cancel()
	for range ch { // производитель мог ждать места в очереди
	}
	<-done
	switch {
	case werr != nil:
		return parsed, werr
	case !complete || ctx.Err() != nil:
		return parsed, errIngestCanceled
	}
	return parsed, nil
}

// parseTGLinks переводит ссылки Telegram-прокси в записи (дубликаты схлопываются, нераспознанные пропускаются).
func parseTGLinks(links []string) []store.TGProxyRec {
	seen := make(map[string]bool, len(links))
	var out []store.TGProxyRec
	for _, l := range links {
		p, err := tgproxy.Parse(l)
		if err != nil {
			continue
		}
		id := p.ID()
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, store.TGProxyRec{ID: id, Kind: p.Kind, Server: p.Server, Port: p.Port, Secret: p.Secret, User: p.User, Pass: p.Pass})
	}
	return out
}

// parseLinks переводит ссылки в записи узлов; дубликаты внутри источника схлопываются по ключу дедупликации.
func parseLinks(links []string) []store.NodeRec { return parseLinksCtx(context.Background(), links) }

// parseLinksCtx — parseLinks, который бросает разбор при отмене ctx (большой источник разбирается секунды; результат
// прерванного обновления всё равно не записывается).
func parseLinksCtx(ctx context.Context, links []string) []store.NodeRec {
	var out []store.NodeRec
	_, ok := parseLinksChunked(ctx, links, max(len(links), 1), func(recs []store.NodeRec) bool { out = append(out, recs...); return true })
	if !ok {
		return nil
	}
	if out == nil {
		out = []store.NodeRec{}
	}
	return out
}

// parseLinksChunked разбирает ссылки порциями до chunk записей и передаёт каждую в emit (false — остановить). Дубликаты схлопываются
// по ключу дедупликации во всём источнике, а не в пределах порции: множество идентификаторов живёт всё время разбора (≈100 байт
// на узел), сами записи — только порция. Возвращает число разных узлов и признак «разобрано до конца» (нет — отмена или emit false).
func parseLinksChunked(ctx context.Context, links []string, chunk int, emit func([]store.NodeRec) bool) (int, bool) {
	seen := make(map[string]struct{}, len(links))
	buf := make([]store.NodeRec, 0, min(chunk, len(links)))
	n := 0
	for i, l := range links {
		if i%256 == 0 && ctx.Err() != nil {
			return n, false
		}
		o, err := translator.Parse(l)
		if err != nil {
			continue
		}
		id := store.NodeID(o.Key)
		if _, dup := seen[id]; dup {
			continue
		}
		raw, err := json.Marshal(o.Opts)
		if err != nil {
			continue
		}
		server, port := endpointOf(o)
		if localServerAddr(server) {
			continue
		}
		seen[id] = struct{}{}
		n++
		buf = append(buf, store.NodeRec{
			ID: id, Class: translator.Class(o.Opts), Proto: o.Proto, Endpoint: o.Endpoint, Server: server, Port: port,
			Opts: raw, Notes: o.Notes, Insecure: o.Flags.Insecure, NoSecurity: o.Flags.NoSecurity,
		})
		if len(buf) >= chunk {
			if !emit(buf) {
				return n, false
			}
			buf = make([]store.NodeRec, 0, min(chunk, len(links)-i-1))
		}
	}
	if len(buf) > 0 && !emit(buf) {
		return n, false
	}
	return n, true
}

// localServerAddr — адрес сервера из подписки указывает на саму машину или локальную сеть (loopback, частные,
// link-local, неуказанные адреса, localhost). Такой «узел» чужой источник подсунуть может только ради
// сканирования локальной сети или подмены трафика локальным прокси. Собственные серверы пользователя сюда
// не попадают: они не идут через parseLinks.
func localServerAddr(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if i := strings.IndexByte(h, '%'); i >= 0 { // зона IPv6
		h = h[:i]
	}
	a, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified()
}

// endpointOf достаёт адрес сервера для показа: у outbound'ов это server/server_port, у WireGuard-endpoint'ов — первый пир.
func endpointOf(o *translator.Outbound) (string, int) {
	num := func(v any) int {
		switch n := v.(type) {
		case int:
			return n
		case uint16:
			return int(n)
		case float64:
			return int(n)
		}
		return 0
	}
	if s, ok := o.Opts["server"].(string); ok {
		return s, num(o.Opts["server_port"])
	}
	switch peers := o.Opts["peers"].(type) {
	case []any:
		if len(peers) > 0 {
			if p, ok := peers[0].(map[string]any); ok {
				a, _ := p["address"].(string)
				return a, num(p["port"])
			}
		}
	case []map[string]any:
		if len(peers) > 0 {
			a, _ := peers[0]["address"].(string)
			return a, num(peers[0]["port"])
		}
	}
	return "", 0
}

// purge вычищает давно пропавшие из источников узлы, кроме нужных (выбранные, запасные, избранные/закреплённые).
func (c *Core) purge() {
	keep := map[string]bool{}
	c.mu.Lock()
	for id := range c.needed() {
		keep[id] = true
	}
	c.mu.Unlock()
	if sels, err := c.st.Selections(); err == nil {
		for _, s := range sels {
			keep[s.Node] = true
			for _, f := range s.Fallbacks {
				keep[f] = true
			}
		}
	}
	ids := make([]string, 0, len(keep))
	for id := range keep {
		ids = append(ids, id)
	}
	if _, err := c.st.PurgeNodes(c.cfg.Now().Add(-c.cfg.NodeRetention), ids); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "очистка узлов: " + err.Error()})
	}
	if n, err := c.st.EvictNodes(c.cfg.MaxNodes, ids); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "потолок узлов: " + err.Error()})
	} else if n > 0 {
		c.bus.Publish(store.Event{Kind: EvNode, Initiator: "auto", Detail: fmt.Sprintf("в базе больше %d узлов: вытеснено %d давно не виденных", c.cfg.MaxNodes, n)})
	}
}

// ---- управление источниками и узлами ----

// AddSource добавляет пользовательский источник (адрес может содержать токен).
func (c *Core) AddSource(name, url, mirror string, priority int, initiator string) error {
	if name == "" || url == "" {
		return fmt.Errorf("%w: нужны имя и адрес источника", ErrInvalid)
	}
	// имя идёт в файлы кэша и в журнал, адрес — в сеть: проверка белым списком
	if err := aggregator.ValidName(name); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := aggregator.ValidURL(url); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mirror != "" {
		if err := aggregator.ValidURL(mirror); err != nil {
			return fmt.Errorf("%w: зеркало: %v", ErrInvalid, err)
		}
	}
	if err := c.st.UpsertSource(store.SourceRec{ID: name, URL: url, Mirror: mirror, Priority: priority, Enabled: true, UserAdded: true}, c.cfg.Now()); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvSource, Initiator: initiator, Detail: "добавлен " + name})
	return nil
}

// SetSourceEnabled включает/выключает источник.
func (c *Core) SetSourceEnabled(name string, on bool, initiator string) error {
	if err := c.st.SetSourceEnabled(name, on); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvSource, Initiator: initiator, Detail: fmt.Sprintf("%s: включён=%v", name, on)})
	return nil
}

// RemoveSource удаляет источник.
func (c *Core) RemoveSource(name, initiator string) error {
	if err := c.st.DeleteSource(name); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvSource, Initiator: initiator, Detail: "удалён " + name})
	return nil
}

// SetFavorite / SetPinned ставят пользовательские флаги узла (такой узел не чистится и не прячется политикой P6).
func (c *Core) SetFavorite(nodeID string, on bool, initiator string) error {
	return c.setFlag(nodeID, store.FlagFavorite, on, initiator)
}
func (c *Core) SetPinned(nodeID string, on bool, initiator string) error {
	return c.setFlag(nodeID, store.FlagPinned, on, initiator)
}

func (c *Core) setFlag(nodeID string, f store.NodeFlag, on bool, initiator string) error {
	if err := c.st.SetNodeFlag(nodeID, f, on); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvNode, Initiator: initiator, Node: nodeID, Detail: fmt.Sprintf("%s=%v", f, on)})
	return nil
}

// housekeeping ограничивает рост хранилища: старые результаты проверок, журнал событий, лог движка.
func (c *Core) housekeeping() {
	if _, err := c.st.PruneProbes(c.cfg.Now().Add(-c.cfg.ProbeRetention)); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "очистка проверок: " + err.Error()})
	}
	if err := c.st.TrimEvents(c.cfg.EventRetention); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "очистка журнала: " + err.Error()})
	}
	if err := c.rotateLog(); err != nil {
		c.bus.Publish(store.Event{Kind: EvError, Initiator: "auto", Detail: "ротация лога: " + err.Error()})
	}
}

// rotateLog: лог движка превысил предел — прежняя копия заменяется текущим содержимым, лог очищается (движок пишет в режиме
// O_APPEND, поэтому усечение безопасно). Хранится одна старая копия, права 0600.
func (c *Core) rotateLog() error {
	path := filepath.Join(c.cfg.StateDir, "engine.log")
	st, err := os.Stat(path)
	if err != nil || st.Size() <= c.cfg.LogMaxBytes {
		return nil
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(path+".1", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Truncate(path, 0)
}
