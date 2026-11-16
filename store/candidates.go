package store

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"time"
)

// CandidateRec — кандидат в источники, найденный автопоиском. Хранятся только счётчики и схемы: ни ссылок узлов, ни секретов.
type CandidateRec struct {
	URL        string
	Name       string
	Repo       string
	FirstSeen  time.Time
	LastSeen   time.Time
	LastDay    string
	DaysSeen   int
	Links      int
	Schemes    map[string]int
	Sampled    int // проверено ссылок (накопительно)
	SampleOK   int // из них ответило (накопительно)
	SampleDays int
	LastSample time.Time
	Dismissed  bool
	Added      bool
	Sketch     []byte // отпечаток набора ссылок (хэши, см. discover/sketch.go); пусто — нет
}

// CandidateID — стабильный короткий идентификатор кандидата (хэш адреса).
func CandidateID(url string) string {
	sum := sha1.Sum([]byte(url))
	return hex.EncodeToString(sum[:6])
}

// UpsertCandidate записывает наблюдение: новый кандидат создаётся, у известного обновляются ссылки, схемы и время; день наблюдения
// засчитывается один раз за календарный день (day). Отметки «скрыт» и «добавлен», статистика проверок сохраняются.
func (s *Store) UpsertCandidate(r CandidateRec, day string, now time.Time) error {
	sch, err := json.Marshal(r.Schemes)
	if err != nil {
		return err
	}
	var sk any // пустой отпечаток не затирает прежний
	if len(r.Sketch) > 0 {
		sk = r.Sketch
	}
	_, err = s.db.Exec(`
INSERT INTO source_candidates(url,name,repo,first_seen,last_seen,last_day,days_seen,links,schemes,sketch) VALUES(?,?,?,?,?,?,1,?,?,?)
ON CONFLICT(url) DO UPDATE SET name=excluded.name, repo=excluded.repo, last_seen=excluded.last_seen, links=excluded.links, schemes=excluded.schemes,
  sketch=COALESCE(excluded.sketch, sketch),
  days_seen = days_seen + CASE WHEN last_day = excluded.last_day THEN 0 ELSE 1 END, last_day = excluded.last_day`,
		r.URL, r.Name, r.Repo, now.Unix(), now.Unix(), day, r.Links, string(sch), sk)
	return err
}

// RecordCandidateSample добавляет итог проверки выборки: checked проверено, ok ответило. Один день выборки засчитывается за вызов.
func (s *Store) RecordCandidateSample(url string, checked, ok int, now time.Time) error {
	res, err := s.db.Exec(`UPDATE source_candidates SET sampled=sampled+?, sample_ok=sample_ok+?, sample_days=sample_days+1, last_sample=? WHERE url=?`,
		checked, ok, now.Unix(), url)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Candidates возвращает всех кандидатов (включая скрытых и добавленных: они нужны, чтобы не предлагать их заново).
func (s *Store) Candidates() ([]CandidateRec, error) {
	rows, err := s.rd.Query(`SELECT url,name,repo,first_seen,last_seen,last_day,days_seen,links,schemes,sampled,sample_ok,sample_days,last_sample,dismissed,added,sketch FROM source_candidates ORDER BY url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CandidateRec
	for rows.Next() {
		var r CandidateRec
		var fs, ls, lsm int64
		var sch string
		var dis, add int
		if err := rows.Scan(&r.URL, &r.Name, &r.Repo, &fs, &ls, &r.LastDay, &r.DaysSeen, &r.Links, &sch, &r.Sampled, &r.SampleOK, &r.SampleDays, &lsm, &dis, &add, &r.Sketch); err != nil {
			return nil, err
		}
		r.FirstSeen, r.LastSeen, r.LastSample = unixOrZero(fs), unixOrZero(ls), unixOrZero(lsm)
		r.Dismissed, r.Added = dis == 1, add == 1
		_ = json.Unmarshal([]byte(sch), &r.Schemes)
		out = append(out, r)
	}
	return out, rows.Err()
}

// DismissCandidate скрывает кандидата: заново он не предлагается.
func (s *Store) DismissCandidate(url string) error { return s.markCandidate(url, "dismissed") }

// MarkCandidateAdded помечает кандидата добавленным в источники.
func (s *Store) MarkCandidateAdded(url string) error { return s.markCandidate(url, "added") }

func (s *Store) markCandidate(url, col string) error {
	res, err := s.db.Exec(`UPDATE source_candidates SET `+col+`=1 WHERE url=?`, url)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneCandidates удаляет нескрытых и недобавленных кандидатов, которых не видели с момента before (README перестал их называть).
func (s *Store) PruneCandidates(before time.Time) (int, error) {
	res, err := s.db.Exec(`DELETE FROM source_candidates WHERE dismissed=0 AND added=0 AND last_seen < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
