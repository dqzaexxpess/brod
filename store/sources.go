package store

import (
	"database/sql"
	"errors"
	"time"
)

// SourceRec — источник подписки в базе.
type SourceRec struct {
	ID        string // имя
	URL       string
	Mirror    string
	Priority  int
	Enabled   bool
	UserAdded bool
	LastFetch time.Time
	LastOK    time.Time
	LastErr   string
	LastCount int
	AddedAt   time.Time
	// Сведения, которые сообщает сам источник.
	Title       string
	UpdateHours int
	UsedBytes   int64
	TotalBytes  int64
	Expire      time.Time
}

// ErrNotFound — записи нет.
var ErrNotFound = errors.New("не найдено")

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func unixOrZero(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

func unixSec(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// UpsertSource добавляет источник или обновляет его адрес/зеркало/приоритет; включённость и статистика сохраняются.
func (s *Store) UpsertSource(r SourceRec, now time.Time) error {
	_, err := s.db.Exec(`
INSERT INTO sources(id,url,mirror,priority,enabled,user_added,added_at) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET url=excluded.url, mirror=excluded.mirror, priority=excluded.priority`,
		r.ID, r.URL, r.Mirror, r.Priority, b2i(r.Enabled), b2i(r.UserAdded), now.Unix())
	return err
}

// Sources возвращает все источники по приоритету и имени.
func (s *Store) Sources() ([]SourceRec, error) {
	rows, err := s.rd.Query(`SELECT id,url,mirror,priority,enabled,user_added,last_fetch,last_ok,last_err,last_count,added_at,title,update_hours,used_bytes,total_bytes,expire FROM sources ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceRec
	for rows.Next() {
		var r SourceRec
		var en, ua int
		var lf, lo, aa, ex int64
		if err := rows.Scan(&r.ID, &r.URL, &r.Mirror, &r.Priority, &en, &ua, &lf, &lo, &r.LastErr, &r.LastCount, &aa, &r.Title, &r.UpdateHours, &r.UsedBytes, &r.TotalBytes, &ex); err != nil {
			return nil, err
		}
		r.Enabled, r.UserAdded = en == 1, ua == 1
		r.LastFetch, r.LastOK, r.AddedAt, r.Expire = unixOrZero(lf), unixOrZero(lo), unixOrZero(aa), unixOrZero(ex)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetSourceEnabled включает/выключает источник.
func (s *Store) SetSourceEnabled(id string, enabled bool) error {
	return s.mustAffect(s.db.Exec(`UPDATE sources SET enabled=? WHERE id=?`, b2i(enabled), id))
}

// RecordFetch записывает итог загрузки источника: при успехе — число ссылок и время, при ошибке — её текст (счётчик не трогается).
func (s *Store) RecordFetch(id string, count int, fetchErr string, at time.Time) error {
	if fetchErr != "" {
		return s.mustAffect(s.db.Exec(`UPDATE sources SET last_fetch=?, last_err=? WHERE id=?`, at.Unix(), fetchErr, id))
	}
	return s.mustAffect(s.db.Exec(`UPDATE sources SET last_fetch=?, last_ok=?, last_err='', last_count=? WHERE id=?`, at.Unix(), at.Unix(), count, id))
}

// RecordSourceMeta сохраняет сведения, которые сообщил источник (пустые значения не затирают прежние: заголовки приходят не каждый раз).
func (s *Store) RecordSourceMeta(id, title string, updateHours int, used, total, expire int64) error {
	return s.mustAffect(s.db.Exec(`UPDATE sources SET
 title=CASE WHEN ?<>'' THEN ? ELSE title END,
 update_hours=CASE WHEN ?>0 THEN ? ELSE update_hours END,
 used_bytes=CASE WHEN ?>0 THEN ? ELSE used_bytes END,
 total_bytes=CASE WHEN ?>0 THEN ? ELSE total_bytes END,
 expire=CASE WHEN ?>0 THEN ? ELSE expire END WHERE id=?`,
		title, title, updateHours, updateHours, used, used, total, total, expire, expire, id))
}

// DeleteSource удаляет источник и его привязки к узлам (сами узлы остаются, пока их не вычистит PurgeNodes).
func (s *Store) DeleteSource(id string) error {
	defer s.nodesChanged()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := s.mustAffect(tx.Exec(`DELETE FROM sources WHERE id=?`, id)); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM node_sources WHERE source=?`, id); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) mustAffect(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
