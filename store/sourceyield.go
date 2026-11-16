package store

import "time"

// SourceYield — отдача источника за окно: сколько его присутствующих узлов проверялось (L2) и сколько из них хоть раз ответило ни в какой сети.
type SourceYield struct {
	Present int // присутствующих узлов источника
	Checked int // из них проверялось за окно
	Alive   int // из проверенных ответило хотя бы раз
}

// SourceYields считает отдачу всех источников: по одному проходу таблицы проб с отбором по времени (на базе телефона порядка секунды),
// поэтому вызывать редко (ядро — не чаще раза в час).
func (s *Store) SourceYields(since time.Time) (map[string]SourceYield, error) {
	rows, err := s.rd.Query(`
WITH recent AS (SELECT node, MAX(ok) AS ok FROM probes WHERE level=2 AND at>=? GROUP BY node)
SELECT ns.source, COUNT(*), COUNT(r.node), COALESCE(SUM(r.ok),0)
FROM node_sources ns LEFT JOIN recent r ON r.node=ns.node
WHERE ns.present=1 GROUP BY ns.source`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SourceYield{}
	for rows.Next() {
		var src string
		var y SourceYield
		if err := rows.Scan(&src, &y.Present, &y.Checked, &y.Alive); err != nil {
			return nil, err
		}
		out[src] = y
	}
	return out, rows.Err()
}
