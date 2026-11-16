// Команда probe: полный конвейер «обновить» (Ф2): загрузка источников → разбор → L1 → L2–L4,
// с замером времени до первых N узлов, прошедших L4 (метрика P3).
//
//	probe -budget 150s -stop-after 3 -out probe-run1.json
//
// В вывод и файл узлы попадают только как анонимный id (sha1 ключа, 8 символов): хосты и UUID не печатаются.
package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"brod/aggregator"
	"brod/engine"
	"brod/mobile"
	"brod/prober"
	"brod/store"
	"brod/translator"
	"brod/warp"
)

type node struct {
	mob   int    // мобильный балл (0–3): IP+SNI из белых списков = 3
	link  string // исходная ссылка (для экспорта живых узлов)
	cls   string // протокол/транспорт/безопасность
	nid   string // полный sha1 ключа (для хранилища)
	prio  int    // приоритет источника: 0 = курируемый
	id    string
	src   string
	proto string
	hp    string
	opts  []byte
}

type nodeResult struct {
	ID       string          `json:"id"`
	Source   string          `json:"source"`
	Mobile   int             `json:"mobile,omitempty"`
	Proto    string          `json:"proto"`
	StartSec float64         `json:"start_sec"`
	AtSec    float64         `json:"at_sec"`
	Results  []prober.Result `json:"results"`
	L4OK     bool            `json:"l4_ok"`
}

func main() {
	budget := flag.Duration("budget", 150*time.Second, "общий бюджет времени на прогон")
	stopAfter := flag.Int("stop-after", 3, "остановиться после N узлов с пройденным L4 (0 = работать весь бюджет)")
	l1n := flag.Int("l1", 300, "параллелизм L1")
	l2n := flag.Int("l2", 64, "параллелизм L2–L4")
	perSrc := flag.Duration("src-timeout", 10*time.Second, "таймаут на источник")
	cache := flag.String("cache", "work/cache", "каталог кэша подписок")
	out := flag.String("out", "", "куда писать JSON с итогами (без хостов)")
	dbPath := flag.String("db", "work/vpn.db", "SQLite с историей проверок (\"\" = не вести)")
	sourcesFile := flag.String("sources-file", "", "файл с дополнительными источниками («имя|url|приоритет»)")
	sourcesOnly := flag.Bool("sources-only", false, "использовать только источники из -sources-file")
	offline := flag.Bool("offline", false, "не ходить в сеть за подписками, только кэш на диске")
	perCap := flag.Int("per-source-cap", 0, "брать с источника не больше N случайных узлов (0 = все): оценка качества источников выборкой")
	l2only := flag.Bool("l2-only", false, "только L2 (без L3/L4): быстрая оценка живости")
	viaWarp := flag.String("via-warp", "", "файл аккаунта WARP: проверять каждый узел ЧЕРЕЗ WARP (цепочка WARP → узел)")
	warpMask := flag.String("warp-mask", "quic", "маскировка AWG для -via-warp: quic|dns|stun|sip|none (none = обычный WG)")
	warpEndpoint := flag.String("warp-endpoint", "", "host пира WARP (по умолчанию engage.cloudflareclient.com)")
	warpPort := flag.Uint("warp-port", 0, "порт пира WARP (по умолчанию 2408)")
	mobileMode := flag.Bool("mobile", false, "мобильный режим: приоритет узлам с адресом/SNI из белых списков мобильного интернета")
	mobileOnly := flag.Bool("mobile-only", false, "проверять только узлы с мобильным баллом ≥ 1")
	mobileResolve := flag.Int("mobile-resolve", 0, "сколько доменов-хостов с разрешённым SNI резолвить для IP-признака (0 — не резолвить, такие узлы оцениваются без адреса)")
	mobileLists := flag.String("mobile-lists", "work/mobile", "каталог со списками whitelist/cidrwhitelist/ipwhitelist")
	exportAlive := flag.String("export-alive", "", "файл для экспорта ссылок узлов, прошедших L2 (содержит секреты, права 0600)")
	onlyNote := flag.String("only-note", "", "проверять только узлы с такой пометкой транслятора (например, http-obfs-ignored)")
	skipL1 := flag.Bool("skip-l1", false, "принудительно отключить L1 (для сравнения пользы предфильтра)")
	debugQueue := flag.Bool("debug-queue", false, "печатать распределение приоритетов очереди")
	noHistory := flag.Bool("no-history", false, "не использовать историю для ранжирования очереди")
	showNone := flag.Bool("include-none", false, "не скрывать узлы с security=none")
	l2t := flag.Duration("l2-timeout", 6*time.Second, "таймаут L2")
	forceL1 := flag.Bool("force-l1", false, "не отключать L1 даже если контроль показал, что TCP «успешен» всегда")
	flag.Parse()

	var mlists *mobile.Lists
	if *mobileMode || *mobileOnly {
		var merr error
		if mlists, merr = mobile.Load(*mobileLists); merr != nil {
			fmt.Fprintln(os.Stderr, "мобильные списки:", merr)
			os.Exit(1)
		}
		d, i, c := mlists.Sizes()
		fmt.Printf("мобильный режим: списки загружены (доменов %d, IP %d, CIDR %d)\n", d, i, c)
	}
	mresolver := &mobile.Resolver{Budget: *mobileResolve, Lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
	srcs := aggregator.Defaults()
	if *sourcesOnly {
		srcs = nil
	}
	if *sourcesFile != "" {
		extra, err := aggregator.ParseSourcesFile(*sourcesFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sources-file:", err)
			os.Exit(1)
		}
		srcs = append(srcs, extra...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()

	var st *store.Store
	var hist map[string]store.History
	var priors map[string]float64
	netID := store.NetID()
	if *dbPath != "" {
		var derr error
		if st, derr = store.Open(*dbPath); derr != nil {
			fmt.Fprintln(os.Stderr, "store:", derr)
			os.Exit(1)
		}
		defer st.Close()
		if !*noHistory {
			hist, _ = st.Histories(netID, time.Now())
			priors, _ = st.ClassPriors(netID, time.Now())
		}
	}
	fmt.Printf("сеть: %s; история: %d узлов\n", netID, len(hist))

	var engCfg []byte
	if *viaWarp != "" {
		b, rerr := os.ReadFile(*viaWarp)
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "via-warp:", rerr)
			os.Exit(1)
		}
		var acc warp.Account
		if json.Unmarshal(b, &acc) != nil {
			fmt.Fprintln(os.Stderr, "via-warp: неверный файл аккаунта")
			os.Exit(1)
		}
		m := *warpMask
		if m == "none" {
			m = ""
		}
		ep := acc.Endpoint(warp.Options{Mask: m, Endpoint: *warpEndpoint, Port: uint16(*warpPort)})
		engCfg, _ = json.Marshal(map[string]any{"endpoints": []any{ep}})
		fmt.Printf("цепочка: каждый узел проверяется через WARP (mask=%s)\n", *warpMask)
	}
	eng, err := engine.NewFromJSON("error", engCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer eng.Close()

	prober.L2Timeout = *l2t

	// контроль L1: адрес из TEST-NET-1 (RFC 5737) заведомо недостижим. Если connect «успешен», путь процесса терминирует TCP
	// локально (прозрачный прокси/TUN, как Throne) и L1 ничего не фильтрует — тогда он отключается.
	l1Off := *skipL1 || *viaWarp != "" // при цепочке через WARP прямой TCP-предфильтр бессмыслен: узел может быть недоступен напрямую, но доступен через WARP
	if c := prober.L1(ctx, "192.0.2.1:443", 2*time.Second); c.OK && !*forceL1 {
		l1Off = true
		fmt.Println("ВНИМАНИЕ: контроль L1 (заведомо мёртвый адрес) прошёл — путь процесса терминирует TCP локально, L1 отключён")
	}

	t0 := time.Now()
	el := func() float64 { return time.Since(t0).Seconds() }

	var (
		parsed, hidden, split, parseFail, sourcesOK, sourcesFail                    int64
		l1Tested, l1Pass, l2Tested, l2Pass, l3Pass, l4Pass, l4Telegram, l4GoogleAPI int64
		firstSrcAt                                                                  = -1.0
	)
	nodes := make(chan node, 1<<16)
	ranked := make(chan node, 1<<16)
	// планировщик (P3): первые 2 с копит узлы, затем отдаёт по убыванию приоритета (история в этой сети + курируемые источники);
	// опоздавшие источники идут в порядке поступления
	go func() {
		now := time.Now()
		score := func(n node) int {
			sc := hist[n.nid].Score(now) + int(priors[n.cls]*400) // априор по классу: порядок среди «неизвестных» не произвольный
			if *mobileMode {
				sc += 3000 * n.mob // для мобильной сети важнее всего совпадение с белыми списками
			}
			if n.prio == 0 {
				sc += 100
			}
			return sc
		}
		var buf []node
		flush := func() {
			if len(buf) == 0 {
				return
			}
			sort.SliceStable(buf, func(i, j int) bool { return score(buf[i]) > score(buf[j]) })
			if *debugQueue {
				buckets := map[int]int{}
				for _, n := range buf {
					buckets[score(n)]++
				}
				fmt.Printf("[+%5.1fs] [очередь] пачка %d узлов, приоритеты %v, первые:", el(), len(buf), buckets)
				for i := 0; i < 5 && i < len(buf); i++ {
					fmt.Printf(" %s(%d)", buf[i].id, score(buf[i]))
				}
				fmt.Println()
			}
			for _, n := range buf {
				ranked <- n
			}
			buf = nil
		}
		// пачка = всё, что пришло без пауз дольше 60 мс (разбор источника идёт сплошным потоком); так каждый источник ранжируется целиком
		quiet := time.NewTimer(time.Hour)
		for {
			select {
			case n, ok := <-nodes:
				if !ok {
					flush()
					close(ranked)
					return
				}
				buf = append(buf, n)
				quiet.Reset(60 * time.Millisecond)
			case <-quiet.C:
				flush()
			}
		}
	}()
	l2q := make(chan node, 1<<16)

	// 1) загрузка и разбор — по мере поступления источников
	var parseWG sync.WaitGroup
	parseWG.Add(1)
	go func() {
		defer parseWG.Done()
		seen := map[string]bool{}
		seenLink := map[string]bool{}
		ingest := func(r aggregator.Result, note string) {
			n0 := 0
			links := r.Links
			if *perCap > 0 { // случайная выборка, детерминированная по имени источника (повторяемость прогонов)
				links = append([]string(nil), links...)
				h := fnv.New64a()
				h.Write([]byte(r.Source.Name))
				rand.New(rand.NewSource(int64(h.Sum64()))).Shuffle(len(links), func(i, j int) { links[i], links[j] = links[j], links[i] })
			}
			for _, l := range links {
				if *perCap > 0 && n0 >= *perCap {
					break
				}
				if seenLink[l] {
					continue
				}
				seenLink[l] = true
				o, err := translator.Parse(l)
				if err != nil {
					atomic.AddInt64(&parseFail, 1)
					continue
				}
				if seen[o.Key] {
					continue
				}
				seen[o.Key] = true
				if *onlyNote != "" {
					found := false
					for _, nt := range o.Notes {
						if nt == *onlyNote {
							found = true
						}
					}
					if !found {
						continue
					}
				}
				mscore := 0
				if mlists != nil {
					mscore = mlists.EvaluateResolved(context.Background(), o.Opts, mresolver).Score()
					if *mobileOnly && mscore == 0 {
						continue
					}
				}
				switch {
				case o.Flags.NoSecurity && !*showNone:
					atomic.AddInt64(&hidden, 1)
					continue
				case o.Flags.DownloadSplit:
					atomic.AddInt64(&split, 1)
					continue
				}
				if *viaWarp != "" {
					cp := make(map[string]any, len(o.Opts)+1)
					for k, v := range o.Opts {
						cp[k] = v
					}
					cp["detour"] = "warp"
					o.Opts = cp
				}
				raw, _ := json.Marshal(o.Opts)
				h := sha1.Sum([]byte(o.Key))
				nodes <- node{mob: mscore, link: l, cls: nodeClass(o.Opts), nid: store.NodeID(o.Key), prio: r.Source.Priority, id: hex.EncodeToString(h[:4]), src: r.Source.Name, proto: o.Proto,
					hp: fmt.Sprintf("%v:%v", o.Opts["server"], o.Opts["server_port"]), opts: raw}
				atomic.AddInt64(&parsed, 1)
				n0++
			}
			fmt.Printf("[+%5.1fs] источник %-45s ссылок %6d, новых узлов в очередь %6d%s\n", el(), r.Source.Name, len(r.Links), n0, note)
		}
		// 1) копия подписок с диска: проверки лучших по истории узлов стартуют сразу, не ожидая сети (P3: кэш виден сразу)
		for _, r := range aggregator.LoadCached(srcs, *cache) {
			if firstSrcAt < 0 {
				firstSrcAt = el()
			}
			ingest(r, " [кэш диска]")
		}
		// 2) сеть добавляет только новое
		if *offline {
			close(nodes)
			return
		}
		for r := range aggregator.Fetch(ctx, srcs, *perSrc, *cache) {
			if r.Err != nil {
				atomic.AddInt64(&sourcesFail, 1)
				fmt.Printf("[+%5.1fs] источник %-45s ОШИБКА %v\n", el(), r.Source.Name, r.Err)
				continue
			}
			atomic.AddInt64(&sourcesOK, 1)
			if firstSrcAt < 0 {
				firstSrcAt = el()
			}
			note := fmt.Sprintf(", загрузка %v", r.Took.Round(time.Millisecond))
			if r.NotModified {
				note += " 304"
			}
			if r.FromMirror {
				note += " зеркало"
			}
			if r.FromCache {
				note += " кэш"
			}
			ingest(r, note)
		}
		close(nodes)
	}()

	// 2) L1: TCP connect, результат кэшируется по host:port (на один адрес приходится несколько ссылок)
	var l1WG sync.WaitGroup
	type l1entry struct {
		done chan struct{}
		ok   bool
	}
	var l1mu sync.Mutex
	l1cache := map[string]*l1entry{}
	for i := 0; i < *l1n; i++ {
		l1WG.Add(1)
		go func() {
			defer l1WG.Done()
			for n := range ranked {
				if udpProto[n.proto] || l1Off { // UDP: TCP-предфильтр неприменим; либо L1 отключён контролем
					l2q <- n
					continue
				}
				l1mu.Lock()
				e, had := l1cache[n.hp]
				if !had {
					e = &l1entry{done: make(chan struct{})}
					l1cache[n.hp] = e
				}
				l1mu.Unlock()
				if !had {
					r := prober.L1(ctx, n.hp, 3*time.Second)
					atomic.AddInt64(&l1Tested, 1)
					e.ok = r.OK
					close(e.done)
				} else {
					<-e.done
				}
				if e.ok {
					atomic.AddInt64(&l1Pass, 1)
					l2q <- n
				}
			}
		}()
	}
	go func() { l1WG.Wait(); close(l2q) }()

	// 3) L2–L4 через пробные outbound'ы движка
	var (
		mu       sync.Mutex
		results  []nodeResult
		rows     []store.Row
		alive    []aliveItem
		classes  = map[string]int{}
		okTimes  []float64
		l4Times  []float64
		stopOnce sync.Once
	)
	var l2WG sync.WaitGroup
	for i := 0; i < *l2n; i++ {
		l2WG.Add(1)
		go func() {
			defer l2WG.Done()
			for n := range l2q {
				if ctx.Err() != nil {
					return
				}
				p, err := eng.NewProbe(n.opts)
				if err != nil {
					mu.Lock()
					classes["L0:движок не принял конфиг"]++
					mu.Unlock()
					continue
				}
				nr := nodeResult{ID: n.id, Source: n.src, Mobile: n.mob, Proto: n.proto, StartSec: el()}
				r2 := prober.L2(ctx, p)
				atomic.AddInt64(&l2Tested, 1)
				nr.Results = append(nr.Results, r2)
				if r2.OK {
					mu.Lock()
					alive = append(alive, aliveItem{mob: n.mob, link: n.link})
					mu.Unlock()
					atomic.AddInt64(&l2Pass, 1)
					if *l2only {
						p.Close()
						nr.AtSec = el()
						mu.Lock()
						results = append(results, nr)
						mu.Unlock()
						continue
					}
					var wg sync.WaitGroup
					var r3, r4, rt, rg prober.Result
					wg.Add(4)
					go func() { defer wg.Done(); r3 = prober.L3(ctx, p, 256*1024) }()
					go func() { defer wg.Done(); r4 = prober.L4YouTube(ctx, p) }()
					go func() { defer wg.Done(); rt = prober.L4Telegram(ctx, p) }()
					go func() { defer wg.Done(); rg = prober.L4GoogleAPI(ctx, p) }()
					wg.Wait()
					nr.Results = append(nr.Results, r3, r4, rt, rg)
					if rg.OK {
						atomic.AddInt64(&l4GoogleAPI, 1)
					}
					if r3.OK {
						atomic.AddInt64(&l3Pass, 1)
					}
					if rt.OK {
						atomic.AddInt64(&l4Telegram, 1)
					}
					if r4.OK {
						atomic.AddInt64(&l4Pass, 1)
						mu.Lock()
						l4Times = append(l4Times, el())
						mu.Unlock()
					}
					if r3.OK && r4.OK {
						nr.L4OK = true
						mu.Lock()
						okTimes = append(okTimes, el())
						k := len(okTimes)
						mu.Unlock()
						fmt.Printf("[+%5.1fs] узел #%d прошёл L2+L3+L4(YouTube): id=%s src=%s  скорость %.0f КБ/с  задержка L2 %d мс\n",
							el(), k, n.id, n.src, r3.SpeedKBps, r2.LatencyMs)
						if *stopAfter > 0 && k >= *stopAfter {
							stopOnce.Do(cancel)
						}
					}
				}
				p.Close()
				nr.AtSec = el()
				mu.Lock()
				for _, r := range nr.Results {
					if !r.OK && r.Class != prober.ClassCanceled {
						classes[fmt.Sprintf("L%d/%s:%s", r.Level, r.Target, r.Class)]++
					}
				}
				results = append(results, nr)
				for _, r := range nr.Results {
					if r.Class != prober.ClassCanceled {
						rows = append(rows, store.Row{Node: n.nid, NodeClass: n.cls, Net: netID, Level: r.Level, Target: r.Target, OK: r.OK, Class: string(r.Class),
							LatencyMs: r.LatencyMs, Bytes: r.Bytes, SpeedKBps: r.SpeedKBps, At: time.Now()})
					}
				}
				mu.Unlock()
			}
		}()
	}
	l2WG.Wait()
	parseWG.Wait()

	total := el()
	if st != nil {
		if err := st.Record(rows); err != nil {
			fmt.Fprintln(os.Stderr, "store.Record:", err)
		}
	}
	fmt.Printf("\n== итоги (прошло %.1f с)\n", total)
	fmt.Printf("источники: ок %d, ошибок %d; первый источник получен на +%.1f с\n", sourcesOK, sourcesFail, firstSrcAt)
	fmt.Printf("узлов в работе %d (скрыто security=none %d, downloadSettings %d, не разобрано ссылок %d)\n", parsed, hidden, split, parseFail)
	fmt.Printf("L1 проверено адресов %d, узлов прошло %d | L2 проверено %d, прошло %d | L3 прошло %d | L4 YouTube прошло %d, Telegram %d, google-api %d\n",
		l1Tested, l1Pass, l2Tested, l2Pass, l3Pass, l4Pass, l4Telegram, l4GoogleAPI)
	if *mobileMode || *mobileOnly {
		var tested, l2ok, gapi, yt [4]int
		for _, nr := range results {
			tested[nr.Mobile]++
			for _, r := range nr.Results {
				switch {
				case r.Level == 2 && r.OK:
					l2ok[nr.Mobile]++
				case r.Level == 4 && r.Target == "google-api" && r.OK:
					gapi[nr.Mobile]++
				case r.Level == 4 && r.Target == "youtube" && r.OK:
					yt[nr.Mobile]++
				}
			}
		}
		fmt.Println("== мобильные баллы (3 = IP и SNI в белых списках, 2 = только IP, 1 = только SNI, 0 = нет)")
		for sc := 3; sc >= 0; sc-- {
			fmt.Printf("  балл %d: проверено %6d, L2 ок %5d, google-api %4d, YouTube %3d\n", sc, tested[sc], l2ok[sc], gapi[sc], yt[sc])
		}
	}
	if *exportAlive != "" && len(alive) > 0 {
		sort.SliceStable(alive, func(i, j int) bool { return alive[i].mob > alive[j].mob })
		var sb strings.Builder
		for _, a := range alive {
			sb.WriteString(a.link + "\n")
		}
		if err := os.WriteFile(*exportAlive, []byte(sb.String()), 0o600); err == nil {
			fmt.Printf("экспортировано живых узлов (L2): %d → %s (0600, содержит секреты)\n", len(alive), *exportAlive)
		}
	}
	sort.Float64s(okTimes)
	sort.Float64s(l4Times)
	if len(l4Times) >= 3 {
		fmt.Printf("МЕТРИКА P3 (буквально, только L4 YouTube): 3 узла за %.1f с (цель ≤60 с): %v\n", l4Times[2], l4Times[2] <= 60)
	} else {
		fmt.Printf("МЕТРИКА P3 (буквально, только L4 YouTube): найдено %d узлов за %.1f с\n", len(l4Times), total)
	}
	for i, t := range okTimes {
		if i < 3 {
			fmt.Printf("узел с L2+L3+L4 №%d найден на +%.1f с\n", i+1, t)
		}
	}
	if len(okTimes) >= 3 {
		fmt.Printf("МЕТРИКА P3: 3 узла с L4 за %.1f с (цель ≤60 с): %v\n", okTimes[2], okTimes[2] <= 60)
	} else {
		fmt.Printf("МЕТРИКА P3: найдено только %d узлов с L4 за %.1f с — цель не достигнута\n", len(okTimes), total)
	}
	type kv struct {
		k string
		v int
	}
	var ks []kv
	for k, v := range classes {
		ks = append(ks, kv{k, v})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].v > ks[j].v })
	fmt.Println("классы отказов:")
	for i, e := range ks {
		if i >= 14 {
			break
		}
		fmt.Printf("  %6d  %s\n", e.v, e.k)
	}
	if *out != "" {
		b, _ := json.MarshalIndent(map[string]any{
			"total_sec": total, "ok_times_sec": okTimes, "l4_only_times_sec": l4Times, "classes": classes,
			"counts": map[string]int64{"parsed": parsed, "hidden_none": hidden, "download_split": split, "l1_tested": l1Tested, "l1_pass": l1Pass,
				"l2_tested": l2Tested, "l2_pass": l2Pass, "l3_pass": l3Pass, "l4_youtube_pass": l4Pass, "l4_telegram_pass": l4Telegram, "l4_google_api_pass": l4GoogleAPI},
			"nodes": results,
		}, "", " ")
		_ = os.WriteFile(*out, b, 0o644)
	}
	_ = strings.TrimSpace
}

func nodeClass(o map[string]any) string { return translator.Class(o) }

// udpProto — протоколы поверх UDP: TCP-предфильтр L1 к ним неприменим.
var udpProto = map[string]bool{"hysteria2": true, "hysteria": true, "tuic": true, "wireguard": true}

type aliveItem struct {
	mob  int
	link string
}
