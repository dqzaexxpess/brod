// Package store — SQLite-хранилище результатов проверок с привязкой к сети (P2).
//
// Результат действителен только для сети, в которой получен: при смене сети (другой шлюз/интерфейс) старые данные
// не используются как «свежие». Узел идентифицируется sha1 ключа дедупликации (не хостом и не UUID).
package store

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Store — открытая база.
type Store struct {
	db       *writeDB      // единственное соединение: все записи (и миграции); считает записи для метки списка узлов (viewgen.go)
	rd       *sql.DB       // пул только для чтения (WAL: читатели не ждут писателя и друг друга); для ":memory:" совпадает с db
	hc       histCache     // сводка проб по сетям (histcache.go)
	lc       liteCache     // лёгкие записи узлов (nodecache.go)
	nodesGen atomic.Uint64 // версия таблиц nodes/node_sources
	viewGen  atomic.Uint64 // версия всего, что видно в списке узлов (viewgen.go)
	epoch    int64         // момент открытия: метки прежнего запуска демона не совпадут с новыми
	sc       settingsCache // таблица настроек целиком (state.go)
	cc       countCache    // число узлов для статуса (пересчёт только после записи)
}

// Open открывает (создаёт) базу и применяет миграции. Файл создаётся с правами 0600: в нём ссылки узлов и ключи.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
		_ = os.Chmod(path, 0o600) // файл мог существовать с другими правами; -wal/-shm наследуют права основного файла
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if runtime.GOOS == "android" {
		// на Android нет /tmp: без этого SQLite падает с «disk I/O error» (код 6410, SQLITE_IOERR_GETTEMPPATH) на запросах с временными таблицами
		dsn += "&_pragma=temp_store(MEMORY)"
	}
	// в режиме WAL synchronous(NORMAL) не портит базу при сбое питания (теряется лишь хвост последних записей, все они
	// воспроизводимы обновлением источников), зато убирает fsync с каждой фиксации. Кэш страниц по умолчанию 2 МБ: полное обновление
	// 10,6 → 8,0 с на ПК. Кэш растёт по мере надобности, поэтому у читающих соединений он меньше, а на телефоне (RSS) скромнее.
	cacheKB, readCacheKB := 65536, 8192
	if runtime.GOOS == "android" {
		cacheKB, readCacheKB = 16384, 4096
	}
	wdsn := dsn + "&_pragma=synchronous(NORMAL)&_pragma=cache_size(-" + strconv.Itoa(cacheKB) + ")"
	db, err := sql.Open("sqlite", wdsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: одна запись за раз, без блокировок на уровне приложения
	if err := migrateWithBackup(db, path); err != nil {
		db.Close()
		return nil, err
	}
	rd := db
	if path != ":memory:" {
		// читающие обращения (статус, настройки, список узлов, история) идут отдельными соединениями и не стоят в очереди за
		// долгой записью или чтением. Запись по-прежнему одна, целостность та же. Читатель видит все зафиксированные записи (WAL).
		rd, err = sql.Open("sqlite", strings.Replace(dsn, "journal_mode(WAL)", "query_only(1)", 1)+"&_pragma=cache_size(-"+strconv.Itoa(readCacheKB)+")")
		if err != nil {
			db.Close()
			return nil, err
		}
		rd.SetMaxOpenConns(readConns)
	}
	st := &Store{rd: rd, epoch: time.Now().UnixNano()}
	st.db = &writeDB{DB: db, gen: &st.viewGen}
	return st, nil
}

// readConns — число соединений пула чтения.
const readConns = 3

// Close закрывает базу.
func (s *Store) Close() error {
	var err error
	if s.rd != s.db.DB {
		err = s.rd.Close()
	}
	if e := s.db.Close(); err == nil {
		err = e
	}
	return err
}

// NodeID — устойчивый идентификатор узла по ключу дедупликации.
func NodeID(key string) string {
	h := sha1.Sum([]byte(key))
	return hex.EncodeToString(h[:])
}

// Row — одна запись проверки.
type Row struct {
	Node, Net, Target, Class, NodeClass string
	Level                               int
	OK                                  bool
	LatencyMs, Bytes                    int64
	SpeedKBps                           float64
	At                                  time.Time
}

// Record записывает пачку результатов одной транзакцией.
func (s *Store) Record(rows []Row) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO probes(node,cls,net,level,target,ok,class,latency_ms,bytes,speed_kbps,at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer st.Close()
	for _, r := range rows {
		ok := 0
		if r.OK {
			ok = 1
		}
		if _, err := st.Exec(r.Node, r.NodeClass, r.Net, r.Level, r.Target, ok, r.Class, r.LatencyMs, r.Bytes, r.SpeedKBps, r.At.Unix()); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.histRecord(rows)
	return nil
}

// History — сводка по узлу в данной сети.
type History struct {
	L2OK, L4OK time.Time // последний успех (нулевое время = не было)
	L2Ms       int64     // задержка последнего успешного L2 (рукопожатие и HTTPS через узел), мс — это «пинг» узла
	GoogleOK   time.Time // последний успех цели google-api
	LastFail   time.Time
	FailedLast bool // последний результат L2 — провал, и он свежий (≤30 мин)
}

// PruneProbes удаляет результаты проверок старше before. Окна Histories и ClassPriors — 7 суток, поэтому хранить дольше 8–14 не нужно:
// без чистки таблица растёт на сотни тысяч строк в сутки.
//
// Удаление идёт порциями по pruneChunk строк: после простоя в несколько дней одним запросом набирается секунда-две, и всё это время
// единственное соединение записи занято. Между порциями его забирают ожидающие.
func (s *Store) PruneProbes(before time.Time) (int, error) {
	defer s.histReset() // чистка могла укоротить окно сводки
	total := 0
	for {
		res, err := s.db.Exec(`DELETE FROM probes WHERE rowid IN (SELECT rowid FROM probes WHERE at < ? LIMIT ?)`, before.Unix(), pruneChunk)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
		if int(n) < pruneChunk {
			return total, nil
		}
	}
}

// pruneChunk — размер порции чистки проб (переменная ради теста).
var pruneChunk = 20000

// Score — приоритет узла в очереди проверки (больше = раньше). Свежесть важнее давнего успеха; недавний провал уводит в конец.
func (h History) Score(now time.Time) int {
	sc := 0
	if !h.L4OK.IsZero() {
		sc += 1000 - clampMinutes(now.Sub(h.L4OK))
	}
	if !h.L2OK.IsZero() {
		sc += 500 - clampMinutes(now.Sub(h.L2OK))/2
	}
	if !h.GoogleOK.IsZero() { // Play Игры, PWA и сертификация Play без него не работают (P7): такие узлы предпочтительнее
		sc += 300 - clampMinutes(now.Sub(h.GoogleOK))/2
	}
	if h.FailedLast { // штраф не накапливается: узел, мигнувший один раз, не должен утонуть из-за числа прогонов
		sc -= 200
	}
	return sc
}

func clampMinutes(d time.Duration) int {
	m := int(d.Minutes() / 10) // шаг 10 минут, чтобы свежесть не перекрывала разницу между классами
	if m > 400 {
		m = 400
	}
	return m
}

// NetID определяет идентификатор текущей сети: интерфейс и шлюз маршрута по умолчанию плюс набор активных туннельных
// интерфейсов (чужих VPN/TUN). Последнее важно: включённый VPN/TUN меняет путь наружу, не меняя шлюз по умолчанию, и результаты
// «через чужой туннель» не должны смешиваться с прямыми. Как определяются интерфейс и туннели — netBase (netid_*.go).
// SSID/оператор здесь не определяются (на Android будет из ConnectivityManager).
func NetID() string {
	base := netBase()
	if base == "unknown" {
		return base
	}
	// явная метка сети (например, mobile-tether): шлюз раздачи с телефона может совпадать между оператором и домашним роутером
	if label := strings.TrimSpace(os.Getenv("VPNCLIENT_NETLABEL")); label != "" {
		base = label + ":" + base
	}
	return base
}

// ClassPriors возвращает долю успешных L2 по классам узлов в сети netID за 7 суток (со сглаживанием Лапласа):
// априорная вероятность для узлов без собственной истории.
func (s *Store) ClassPriors(netID string, now time.Time) (map[string]float64, error) {
	rows, err := s.rd.Query(`SELECT cls, SUM(ok), COUNT(*) FROM probes WHERE net=? AND level=2 AND cls<>'' AND at>=? GROUP BY cls`,
		netID, now.Add(-7*24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var cls string
		var ok, n int
		if err := rows.Scan(&cls, &ok, &n); err != nil {
			return nil, err
		}
		out[cls] = (float64(ok) + 1) / (float64(n) + 20)
	}
	return out, rows.Err()
}
