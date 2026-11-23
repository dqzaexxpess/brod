package store

import "time"

// protoSQL — протокол узла для отчёта: первая часть класса «протокол/транспорт/безопасность». Так же названы строки таблицы проб (probes.cls),
// поэтому счётчики узлов и проверок сходятся; поле nodes.proto называет протоколы иначе (ss, hy2) и годится только как запасное.
const protoSQL = `CASE WHEN instr(n.cls,'/')>0 THEN substr(n.cls,1,instr(n.cls,'/')-1) WHEN n.cls<>'' THEN n.cls ELSE n.proto END`

// ProtoCount — сколько узлов с таким протоколом в базе.
type ProtoCount struct {
	Proto   string
	Total   int
	Present int // хотя бы один источник ещё отдаёт узел
	Domain  int // адрес сервера — имя, а не IP (такому узлу нужен DNS)
}

// NodeCounts считает узлы по протоколам одним запросом, не поднимая записи в память (в базе сотни тысяч узлов).
func (s *Store) NodeCounts() ([]ProtoCount, error) {
	rows, err := s.rd.Query(`SELECT ` + protoSQL + ` AS p, COUNT(*),
		SUM(CASE WHEN EXISTS (SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1) THEN 1 ELSE 0 END),
		SUM(CASE WHEN n.server NOT LIKE '%:%' AND n.server GLOB '*[A-Za-z]*' THEN 1 ELSE 0 END)
		FROM nodes n GROUP BY p ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProtoCount
	for rows.Next() {
		var p ProtoCount
		if err := rows.Scan(&p.Proto, &p.Total, &p.Present, &p.Domain); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProbeAgg — сводка проверок: сколько проб и узлов дали такой исход.
type ProbeAgg struct {
	Net    string // ключ сети как в базе (содержит адрес шлюза: наружу не выдаётся)
	Cls    string // протокол/транспорт/безопасность
	Level  int
	OK     bool
	Class  string // класс провала; для успеха пусто
	Probes int
	Nodes  int
}

// ProbeAggregates группирует результаты проверок за период по сети, классу узла, уровню и исходу.
func (s *Store) ProbeAggregates(since time.Time) ([]ProbeAgg, error) {
	rows, err := s.rd.Query(`SELECT net, cls, level, ok, class, COUNT(*), COUNT(DISTINCT node) FROM probes WHERE at>=? GROUP BY net, cls, level, ok, class`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProbeAgg
	for rows.Next() {
		var a ProbeAgg
		var ok int
		if err := rows.Scan(&a.Net, &a.Cls, &a.Level, &ok, &a.Class, &a.Probes, &a.Nodes); err != nil {
			return nil, err
		}
		a.OK = ok != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// SampleNode — узел для выборочной пробы отчёта.
type SampleNode struct {
	ID     string
	Proto  string
	Domain bool
}

// SampleNodes выбирает случайные присутствующие узлы: не больше perProto на протокол и не больше protos самых частых протоколов. WireGuard
// берётся всегда, если он есть: сравнение «живы только WireGuard» без него теряет смысл, а по числу узлов он обычно не в первой шестёрке.
func (s *Store) SampleNodes(perProto, protos int) ([]SampleNode, error) {
	ps, err := s.NodeCounts()
	if err != nil {
		return nil, err
	}
	var pick []string
	hasWG := false
	for _, p := range ps {
		if p.Present == 0 || p.Proto == "" || p.Proto == "chain" { // цепочки в выборку не берём: это не протокол, а составной узел
			continue
		}
		if p.Proto == "wireguard" {
			hasWG = true
		}
		if len(pick) < protos {
			pick = append(pick, p.Proto)
		}
	}
	if hasWG {
		in := false
		for _, p := range pick {
			in = in || p == "wireguard"
		}
		if !in {
			pick = append(pick, "wireguard")
		}
	}
	var out []SampleNode
	for _, proto := range pick {
		rows, err := s.rd.Query(`SELECT n.id, n.server NOT LIKE '%:%' AND n.server GLOB '*[A-Za-z]*' FROM nodes n
			WHERE `+protoSQL+`=? AND EXISTS (SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1) ORDER BY RANDOM() LIMIT ?`, proto, perProto)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			x := SampleNode{Proto: proto}
			var dom int
			if err := rows.Scan(&x.ID, &dom); err != nil {
				rows.Close()
				return nil, err
			}
			x.Domain = dom != 0
			out = append(out, x)
		}
		rows.Close()
	}
	return out, nil
}

// SampleDomains возвращает до n случайных доменных адресов серверов присутствующих узлов (для проверки DNS; наружу имена не выдаются).
func (s *Store) SampleDomains(n int) ([]string, error) {
	rows, err := s.rd.Query(`SELECT DISTINCT n.server FROM nodes n WHERE n.server NOT LIKE '%:%' AND n.server GLOB '*[A-Za-z]*' AND n.server LIKE '%.%'
		AND EXISTS (SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1) ORDER BY RANDOM() LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Stability — как вёл себя узел на проверках уровня L2 за период в одной сети.
type Stability struct {
	OK, Fail int
	Flips    int // сколько раз исход менялся между соседними проверками (успех↔провал): «мигающий» узел
	Recent   int // провалов за последние recent
}

// Ratio — доля успехов; 0, false — данных нет.
func (s Stability) Ratio() (float64, bool) {
	if s.OK+s.Fail == 0 {
		return 0, false
	}
	return float64(s.OK) / float64(s.OK+s.Fail), true
}

// NodeStability считает Stability по проверкам L2 для перечисленных узлов в сети net за период since; провалы после recent идут в Recent.
// Узлы без проверок в результат не попадают.
func (s *Store) NodeStability(net string, ids []string, since, recent time.Time) (map[string]Stability, error) {
	out := make(map[string]Stability, len(ids))
	const chunk = 400 // предел переменных SQLite — 999
	for i := 0; i < len(ids); i += chunk {
		part := ids[i:min(i+chunk, len(ids))]
		args := make([]any, 0, len(part)+2)
		args = append(args, net, since.Unix())
		ph := make([]byte, 0, len(part)*2)
		for j, id := range part {
			if j > 0 {
				ph = append(ph, ',')
			}
			ph = append(ph, '?')
			args = append(args, id)
		}
		rows, err := s.rd.Query(`SELECT node, ok, at FROM probes WHERE net=? AND level=2 AND at>=? AND node IN (`+string(ph)+`) ORDER BY node, at`, args...)
		if err != nil {
			return nil, err
		}
		prev, prevNode := -1, ""
		for rows.Next() {
			var node string
			var ok int
			var at int64
			if err := rows.Scan(&node, &ok, &at); err != nil {
				rows.Close()
				return nil, err
			}
			st := out[node]
			if node != prevNode {
				prev = -1
			}
			if ok != 0 {
				st.OK++
			} else {
				st.Fail++
				if at >= recent.Unix() {
					st.Recent++
				}
			}
			if prev >= 0 && prev != min(ok, 1) {
				st.Flips++
			}
			prev, prevNode = min(ok, 1), node
			out[node] = st
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
