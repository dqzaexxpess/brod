package store

import (
	"time"
)

// TGProxyRec — прокси Telegram в базе.
type TGProxyRec struct {
	ID        string
	Kind      string // mtproto | socks5
	Server    string
	Port      int
	Secret    string
	User      string
	Pass      string
	Source    string
	Present   bool
	Favorite  bool
	Own       bool
	OK        bool // результат последней проверки
	LatencyMs int64
	Checked   time.Time // нулевое — ещё не проверялся
	Fails     int
	FirstSeen time.Time
	LastSeen  time.Time
}

const tgCols = `id,kind,server,port,secret,user,pass,source,present,favorite,own,ok,latency_ms,checked_at,fails,first_seen,last_seen`

func scanTG(sc interface{ Scan(...any) error }) (TGProxyRec, error) {
	var r TGProxyRec
	var present, fav, own, ok int
	var chk, first, last int64
	if err := sc.Scan(&r.ID, &r.Kind, &r.Server, &r.Port, &r.Secret, &r.User, &r.Pass, &r.Source, &present, &fav, &own, &ok, &r.LatencyMs, &chk, &r.Fails, &first, &last); err != nil {
		return r, err
	}
	r.Present, r.Favorite, r.Own, r.OK = present == 1, fav == 1, own == 1, ok == 1
	if chk > 0 {
		r.Checked = time.Unix(chk, 0)
	}
	r.FirstSeen, r.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
	return r, nil
}

// UpsertTGProxies записывает прокси источника: новые добавляются, известные получают метку обновления и присутствие.
// Результат проверки и избранное известных не затираются.
func (s *Store) UpsertTGProxies(source string, stamp int64, recs []TGProxyRec, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare(`
INSERT INTO tg_proxies(id,kind,server,port,secret,user,pass,source,stamp,present,first_seen,last_seen) VALUES(?,?,?,?,?,?,?,?,?,1,?,?)
ON CONFLICT(id) DO UPDATE SET source=CASE WHEN tg_proxies.own=1 THEN tg_proxies.source ELSE excluded.source END,
  stamp=excluded.stamp, present=1, last_seen=excluded.last_seen`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer st.Close()
	for _, r := range recs {
		if _, err := st.Exec(r.ID, r.Kind, r.Server, r.Port, r.Secret, r.User, r.Pass, source, stamp, now.Unix(), now.Unix()); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// TouchTGSource подтверждает присутствие прокси источника, который не менялся (304). Возвращает число отмеченных.
func (s *Store) TouchTGSource(source string, stamp int64, now time.Time) (int, error) {
	res, err := s.db.Exec(`UPDATE tg_proxies SET stamp=?, last_seen=? WHERE source=? AND present=1 AND own=0`, stamp, now.Unix(), source)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// EndTGRefresh помечает пропавшими прокси источника, не встреченные в обновлении stamp (только после полной удачной загрузки).
func (s *Store) EndTGRefresh(source string, stamp int64) (int, error) {
	res, err := s.db.Exec(`UPDATE tg_proxies SET present=0 WHERE source=? AND own=0 AND present=1 AND stamp<?`, source, stamp)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// TGFilter — выборка прокси.
type TGFilter struct {
	AliveOnly     bool
	FavoritesOnly bool
	Kind          string
	Limit         int
}

// TGProxies возвращает прокси: избранные и свои первыми, затем живые по возрастанию задержки, затем остальные. Пропавшие из источников
// (кроме избранных и своих) не показываются.
func (s *Store) TGProxies(f TGFilter) ([]TGProxyRec, error) {
	q := `SELECT ` + tgCols + ` FROM tg_proxies WHERE (present=1 OR own=1 OR favorite=1)`
	var args []any
	if f.AliveOnly {
		q += ` AND ok=1`
	}
	if f.FavoritesOnly {
		q += ` AND favorite=1`
	}
	if f.Kind != "" {
		q += ` AND kind=?`
		args = append(args, f.Kind)
	}
	q += ` ORDER BY favorite DESC, own DESC, ok DESC, CASE WHEN ok=1 THEN latency_ms ELSE 0 END ASC, last_seen DESC, id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.rd.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TGProxyRec
	for rows.Next() {
		r, err := scanTG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TGProxy возвращает один прокси.
func (s *Store) TGProxy(id string) (TGProxyRec, error) {
	r, err := scanTG(s.rd.QueryRow(`SELECT `+tgCols+` FROM tg_proxies WHERE id=?`, id))
	if err != nil {
		return r, ErrNotFound
	}
	return r, nil
}

// TGPickForCheck отбирает прокси для проверки: избранные и свои, затем ни разу не проверенные, затем самые давно проверенные; прокси,
// провалившиеся несколько раз подряд, перепроверяются редко (чтобы не тратить круги на мёртвые).
func (s *Store) TGPickForCheck(limit int, now time.Time) ([]TGProxyRec, error) {
	rows, err := s.rd.Query(`SELECT `+tgCols+` FROM tg_proxies
WHERE (present=1 OR own=1 OR favorite=1)
  AND NOT (fails>=3 AND favorite=0 AND own=0 AND checked_at>?)
ORDER BY (favorite=1 OR own=1) DESC, (checked_at=0) DESC, ok DESC, checked_at ASC
LIMIT ?`, now.Add(-6*time.Hour).Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TGProxyRec
	for rows.Next() {
		r, err := scanTG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordTGProbe сохраняет результат проверки.
func (s *Store) RecordTGProbe(id string, ok bool, latencyMs int64, at time.Time) error {
	if ok {
		return s.mustAffect(s.db.Exec(`UPDATE tg_proxies SET ok=1, latency_ms=?, checked_at=?, fails=0 WHERE id=?`, latencyMs, at.Unix(), id))
	}
	return s.mustAffect(s.db.Exec(`UPDATE tg_proxies SET ok=0, checked_at=?, fails=fails+1 WHERE id=?`, at.Unix(), id))
}

// SetTGFavorite — избранное.
func (s *Store) SetTGFavorite(id string, on bool) error {
	return s.mustAffect(s.db.Exec(`UPDATE tg_proxies SET favorite=? WHERE id=?`, b2i(on), id))
}

// AddOwnTGProxy добавляет прокси пользователя (из базы не пропадает, пока его не удалят).
func (s *Store) AddOwnTGProxy(r TGProxyRec, now time.Time) error {
	_, err := s.db.Exec(`
INSERT INTO tg_proxies(id,kind,server,port,secret,user,pass,source,stamp,present,own,favorite,first_seen,last_seen) VALUES(?,?,?,?,?,?,?,'',0,1,1,1,?,?)
ON CONFLICT(id) DO UPDATE SET own=1, favorite=1, present=1`, r.ID, r.Kind, r.Server, r.Port, r.Secret, r.User, r.Pass, now.Unix(), now.Unix())
	return err
}

// DeleteOwnTGProxy удаляет прокси пользователя.
func (s *Store) DeleteOwnTGProxy(id string) error {
	return s.mustAffect(s.db.Exec(`DELETE FROM tg_proxies WHERE id=? AND own=1`, id))
}

// PurgeTGProxies удаляет давно пропавшие из источников прокси (кроме избранных и своих).
func (s *Store) PurgeTGProxies(olderThan time.Time) (int, error) {
	res, err := s.db.Exec(`DELETE FROM tg_proxies WHERE present=0 AND own=0 AND favorite=0 AND last_seen<?`, olderThan.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CountTGProxies — число показываемых прокси и живых среди них.
func (s *Store) CountTGProxies() (total, alive int, err error) {
	err = s.rd.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ok),0) FROM tg_proxies WHERE present=1 OR own=1 OR favorite=1`).Scan(&total, &alive)
	return
}
