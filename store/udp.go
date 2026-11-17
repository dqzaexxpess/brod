package store

import "time"

// UDPRec — последний замер UDP через узел.
type UDPRec struct {
	Node     string
	Net      string // сеть, в которой снят замер
	Grade    string // good | unstable | none
	LossPct  float64
	MedianMs int64
	At       time.Time
}

// PutUDP сохраняет (заменяет) замер UDP узла.
func (s *Store) PutUDP(r UDPRec) error {
	_, err := s.db.Exec(`INSERT INTO node_udp(node, net, grade, loss_pct, median_ms, at) VALUES(?,?,?,?,?,?)
ON CONFLICT(node) DO UPDATE SET net=excluded.net, grade=excluded.grade, loss_pct=excluded.loss_pct, median_ms=excluded.median_ms, at=excluded.at`,
		r.Node, r.Net, r.Grade, r.LossPct, r.MedianMs, r.At.Unix())
	return err
}

// UDPs возвращает замеры UDP всех узлов, у которых они есть.
func (s *Store) UDPs() (map[string]UDPRec, error) {
	rows, err := s.rd.Query(`SELECT node, net, grade, loss_pct, median_ms, at FROM node_udp`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]UDPRec{}
	for rows.Next() {
		var r UDPRec
		var at int64
		if err := rows.Scan(&r.Node, &r.Net, &r.Grade, &r.LossPct, &r.MedianMs, &at); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0)
		out[r.Node] = r
	}
	return out, rows.Err()
}
