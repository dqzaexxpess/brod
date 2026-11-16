package store

import "time"

// SpeedRec — замер скорости скачивания через узел.
type SpeedRec struct {
	Node string
	KBps float64
	At   time.Time
}

// PutSpeed сохраняет (заменяет) замер скорости узла.
func (s *Store) PutSpeed(r SpeedRec) error {
	_, err := s.db.Exec(`INSERT INTO node_speed(node, kbps, at) VALUES(?,?,?)
ON CONFLICT(node) DO UPDATE SET kbps=excluded.kbps, at=excluded.at`, r.Node, r.KBps, r.At.Unix())
	return err
}

// Speeds возвращает замеры скорости всех узлов, у которых они есть.
func (s *Store) Speeds() (map[string]SpeedRec, error) {
	rows, err := s.rd.Query(`SELECT node, kbps, at FROM node_speed`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SpeedRec{}
	for rows.Next() {
		var r SpeedRec
		var at int64
		if err := rows.Scan(&r.Node, &r.KBps, &at); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0)
		out[r.Node] = r
	}
	return out, rows.Err()
}
