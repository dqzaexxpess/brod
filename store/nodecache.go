package store

import (
	"encoding/binary"
	"net/netip"
	"sync"
)

// Кэш лёгких записей узлов. Отбор кандидатов (проверки, запасные, цели) не читает всю таблицу nodes с подзапросом присутствия
// (сотни тысяч узлов: секунды процессора на вызов), ему нужны только идентификатор, класс и флаги. Список строится один раз
// и пересобирается только после записи в nodes/node_sources (nodesGen растёт в каждой такой функции).

// NodeLite — узел без тяжёлых полей, для отбора и ранжирования.
type NodeLite struct {
	ID, Class                                       string
	Favorite, Pinned, Insecure, NoSecurity, Present bool
	LastSeen                                        int64  // unix-секунды
	IP4                                             uint32 // адрес сервера, если это IPv4-литерал (0 — домен или IPv6): для оценки по белым спискам мобильной сети
}

type liteCache struct {
	mu   sync.Mutex
	gen  uint64
	list []NodeLite
	ok   bool
}

// nodesChanged помечает кэш устаревшим: вызывать (через defer) из каждой функции, пишущей в nodes или node_sources.
func (s *Store) nodesChanged() { s.nodesGen.Add(1) }

// PresentLites возвращает отдаваемые источниками узлы (флаги безопасности и избранное — для фильтров вызывающего). Срез общий:
// менять его нельзя. Порядок не задан.
func (s *Store) PresentLites() ([]NodeLite, error) {
	s.lc.mu.Lock()
	defer s.lc.mu.Unlock()
	gen := s.nodesGen.Load() // до запроса: запись во время чтения оставит кэш с прежней версией, и он пересоберётся
	if s.lc.ok && s.lc.gen == gen {
		return s.lc.list, nil
	}
	rows, err := s.rd.Query(`
SELECT n.id, n.cls, n.favorite, n.pinned, n.insecure, n.no_security, n.last_seen, n.server FROM nodes n
WHERE EXISTS(SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]NodeLite, 0, len(s.lc.list)+1024)
	classes := map[string]string{} // классов десятки: одна строка на класс, а не по строке на узел
	for rows.Next() {
		var l NodeLite
		var fav, pin, ins, nosec int
		var server string
		if err := rows.Scan(&l.ID, &l.Class, &fav, &pin, &ins, &nosec, &l.LastSeen, &server); err != nil {
			return nil, err
		}
		if a, err := netip.ParseAddr(server); err == nil && a.Is4() {
			b := a.As4()
			l.IP4 = binary.BigEndian.Uint32(b[:])
		}
		if c, ok := classes[l.Class]; ok {
			l.Class = c
		} else {
			classes[l.Class] = l.Class
		}
		l.Favorite, l.Pinned, l.Insecure, l.NoSecurity, l.Present = fav == 1, pin == 1, ins == 1, nosec == 1, true
		list = append(list, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.lc.list, s.lc.gen, s.lc.ok = list, gen, true
	return list, nil
}

// countCache — результат CountNodes для версии nodesGen: статус запрашивают каждые пару секунд, а подсчёт обходит всю таблицу узлов.
type countCache struct {
	mu             sync.Mutex
	gen            uint64
	total, present int
	ok             bool
}
