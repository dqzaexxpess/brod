package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// NodeRec — узел в базе. Opts — JSON outbound'а sing-box без поля tag; содержит секреты и наружу не отдаётся.
type NodeRec struct {
	ID         string // NodeID
	Class      string // протокол/транспорт/безопасность
	Proto      string
	Endpoint   bool // endpoint sing-box (WireGuard/AWG)
	Server     string
	Port       int
	Opts       []byte // заполняется только если запрошено (WithOpts) либо при UpsertNodes
	Notes      []string
	Insecure   bool
	NoSecurity bool
	Favorite   bool
	Pinned     bool
	Present    bool // хотя бы один источник ещё отдаёт узел
	FirstSeen  time.Time
	LastSeen   time.Time
}

// UpsertNodes записывает узлы, пришедшие из источника в обновлении с меткой stamp (возрастающее число, например
// UnixNano начала обновления). Новые добавляются, известные получают свежие опции и last_seen, привязка к источнику
// помечается присутствующей. Пользовательские флаги (favorite/pinned) не затираются.
//
// Параметры известного узла меняет не любой источник: собственный сервер (OwnSource) другим источникам не
// перезаписывается вовсе; для остальных узлов опции меняет только источник, давший узел первым из тех, что его ещё
// отдают. Остальные лишь привязываются к узлу.
func (s *Store) UpsertNodes(sourceID string, stamp int64, recs []NodeRec, now time.Time) error {
	defer s.nodesChanged()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	nst, err := tx.Prepare(`
INSERT INTO nodes(id,cls,proto,endpoint,server,port,opts,notes,insecure,no_security,first_seen,last_seen) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET cls=excluded.cls, proto=excluded.proto, endpoint=excluded.endpoint, server=excluded.server, port=excluded.port,
  opts=excluded.opts, notes=excluded.notes, insecure=excluded.insecure, no_security=excluded.no_security, last_seen=excluded.last_seen
WHERE ?13=?14 OR NOT EXISTS (
  SELECT 1 FROM node_sources o WHERE o.node=nodes.id AND o.present=1 AND o.source<>?13 AND
    (o.source=?14 OR o.rowid < COALESCE((SELECT m.rowid FROM node_sources m WHERE m.node=nodes.id AND m.source=?13), 9223372036854775807)))`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer nst.Close()
	sst, err := tx.Prepare(`
INSERT INTO node_sources(node,source,present,stamp) VALUES(?,?,1,?)
ON CONFLICT(node,source) DO UPDATE SET present=1, stamp=excluded.stamp`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer sst.Close()
	for _, r := range recs {
		notes, _ := json.Marshal(r.Notes)
		if r.Notes == nil {
			notes = []byte("[]")
		}
		if _, err := nst.Exec(r.ID, r.Class, r.Proto, b2i(r.Endpoint), r.Server, r.Port, r.Opts, string(notes), b2i(r.Insecure), b2i(r.NoSecurity), now.Unix(), now.Unix(), sourceID, OwnSource); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := sst.Exec(r.ID, sourceID, stamp); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// TouchSource подтверждает присутствие узлов источника, который не менялся (ответ 304); метки обновления у его строк не меняются
// (stamp нужен только EndRefresh изменившегося источника, см. ниже; параметр оставлен для совместимости), а узлам с устаревшим last_seen (старше bumpOlderThan) он обновляется — чистка
// «пропавших» узлов отсчитывает срок от last_seen. Возвращает число узлов источника, отмеченных присутствующими.
func (s *Store) TouchSource(sourceID string, stamp int64, now, bumpOlderThan time.Time) (int, error) {
	defer s.nodesChanged()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	// метки stamp не перезаписываем (UPDATE по ≈478 тыс. строк и индексу занимал секунды): stamp читает только EndRefresh того же
	// прохода, а для неизменившегося источника он не вызывается; когда источник изменится, UpsertNodes поставит встреченным узлам
	// новую метку, и сравнение stamp<новой для старых меток остаётся верным
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM node_sources WHERE source=? AND present=1`, sourceID).Scan(&n); err != nil {
		tx.Rollback()
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE nodes SET last_seen=? WHERE last_seen<? AND id IN (SELECT node FROM node_sources WHERE source=? AND present=1)`,
		now.Unix(), bumpOlderThan.Unix(), sourceID); err != nil {
		tx.Rollback()
		return 0, err
	}
	return n, tx.Commit()
}

// EndRefresh завершает успешное обновление источника: узлы, не встреченные в нём с меткой stamp, помечаются
// пропавшими из источника (они остаются в базе — активный и закреплённые не должны исчезать). Возвращает число пропавших.
// Вызывать только после полной успешной загрузки: при ошибке сети «пропали» все узлы источника, и это ложь.
func (s *Store) EndRefresh(sourceID string, stamp int64) (int, error) {
	defer s.nodesChanged()
	res, err := s.db.Exec(`UPDATE node_sources SET present=0 WHERE source=? AND present=1 AND stamp<?`, sourceID, stamp)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// NodeFilter — выборка узлов.
type NodeFilter struct {
	IDs               []string // только эти (пусто = все)
	OnlyPresent       bool     // только отдаваемые источниками
	FavoritesOnly     bool
	Flagged           bool   // избранные или закреплённые
	Source            string // только присутствующие в этом источнике
	Class             string
	IncludeNoSecurity bool // security=none по умолчанию скрыты (P6)
	HideInsecure      bool // allowInsecure по умолчанию показываются с предупреждением; true скрывает их
	WithOpts          bool // загружать Opts (тяжёлое поле)
	Limit             int  // 0 = без ограничения
}

const nodeCols = `n.id,n.cls,n.proto,n.endpoint,n.server,n.port,n.notes,n.insecure,n.no_security,n.favorite,n.pinned,n.first_seen,n.last_seen,
 EXISTS(SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1)`

func scanNode(sc interface{ Scan(...any) error }, withOpts bool) (NodeRec, error) {
	var r NodeRec
	var ep, ins, nosec, fav, pin, present int
	var notes string
	var fs, ls int64
	dest := []any{&r.ID, &r.Class, &r.Proto, &ep, &r.Server, &r.Port, &notes, &ins, &nosec, &fav, &pin, &fs, &ls, &present}
	if withOpts {
		dest = append(dest, &r.Opts)
	}
	if err := sc.Scan(dest...); err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(notes), &r.Notes)
	r.Endpoint, r.Insecure, r.NoSecurity, r.Favorite, r.Pinned, r.Present = ep == 1, ins == 1, nosec == 1, fav == 1, pin == 1, present == 1
	r.FirstSeen, r.LastSeen = time.Unix(fs, 0), time.Unix(ls, 0)
	return r, nil
}

// Nodes возвращает узлы по фильтру, свежие первыми.
func (s *Store) Nodes(f NodeFilter) ([]NodeRec, error) {
	cols := nodeCols
	if f.WithOpts {
		cols += ",n.opts"
	}
	var where []string
	var args []any
	if len(f.IDs) > 0 {
		where = append(where, "n.id IN ("+placeholders(len(f.IDs))+")")
		for _, id := range f.IDs {
			args = append(args, id)
		}
	}
	if f.OnlyPresent {
		where = append(where, "EXISTS(SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1)")
	}
	if f.Source != "" {
		where = append(where, "EXISTS(SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1 AND ns.source=?)")
		args = append(args, f.Source)
	}
	if f.FavoritesOnly {
		where = append(where, "n.favorite=1")
	}
	if f.Flagged {
		where = append(where, "(n.favorite=1 OR n.pinned=1)")
	}
	if f.Class != "" {
		where = append(where, "n.cls=?")
		args = append(args, f.Class)
	}
	if !f.IncludeNoSecurity {
		where = append(where, "(n.no_security=0 OR n.favorite=1 OR n.pinned=1)") // явно выбранное пользователем не прячем
	}
	if f.HideInsecure {
		where = append(where, "(n.insecure=0 OR n.favorite=1 OR n.pinned=1)")
	}
	q := "SELECT " + cols + " FROM nodes n"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY n.last_seen DESC, n.id"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := s.rd.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRec
	for rows.Next() {
		r, err := scanNode(rows, f.WithOpts)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Node возвращает один узел с опциями.
func (s *Store) Node(id string) (NodeRec, error) {
	r, err := scanNode(s.rd.QueryRow(`SELECT `+nodeCols+`,n.opts FROM nodes n WHERE n.id=?`, id), true)
	if err == sql.ErrNoRows {
		return r, ErrNotFound
	}
	return r, err
}

// NodeIDsByPrefix возвращает до limit ID узлов, начинающихся с prefix (для коротких идентификаторов в CLI).
func (s *Store) NodeIDsByPrefix(prefix string, limit int) ([]string, error) {
	rows, err := s.rd.Query(`SELECT id FROM nodes WHERE id LIKE ? ESCAPE '\' ORDER BY id LIMIT ?`, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// NodeFlag — пользовательский флаг узла.
type NodeFlag string

const (
	FlagFavorite NodeFlag = "favorite"
	FlagPinned   NodeFlag = "pinned"
)

// SetNodeFlag ставит или снимает флаг. Узел с флагом не удаляется PurgeNodes.
func (s *Store) SetNodeFlag(id string, flag NodeFlag, on bool) error {
	defer s.nodesChanged()
	var col string
	switch flag {
	case FlagFavorite:
		col = "favorite"
	case FlagPinned:
		col = "pinned"
	default:
		return ErrNotFound
	}
	return s.mustAffect(s.db.Exec(`UPDATE nodes SET `+col+`=? WHERE id=?`, b2i(on), id))
}

// PurgeNodes удаляет узлы, которых нет ни в одном источнике, нет в keep (активный, закреплённые под целями), не отмечены
// избранными/закреплёнными и не встречались дольше olderThan. Возвращает число удалённых.
func (s *Store) PurgeNodes(olderThan time.Time, keep []string) (int, error) {
	defer s.nodesChanged()
	args := []any{olderThan.Unix()}
	q := `DELETE FROM nodes WHERE favorite=0 AND pinned=0 AND last_seen<?
 AND NOT EXISTS(SELECT 1 FROM node_sources ns WHERE ns.node=nodes.id AND ns.present=1)`
	if len(keep) > 0 {
		q += " AND id NOT IN (" + placeholders(len(keep)) + ")"
		for _, k := range keep {
			args = append(args, k)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(q, args...)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM node_sources WHERE node NOT IN (SELECT id FROM nodes)`); err != nil {
		tx.Rollback()
		return 0, err
	}
	return int(n), tx.Commit()
}

// EvictNodes держит общее число узлов в базе не выше limit: лишние удаляются, сначала не отдаваемые никаким источником, затем
// давнее всех виденные. Избранные, закреплённые и узлы из keep (активный, запасные, закреплённые под целями) не удаляются никогда,
// поэтому при слишком большом числе защищённых узлов потолок может не достигаться. limit <= 0 отключает потолок. Возвращает число удалённых.
func (s *Store) EvictNodes(limit int, keep []string) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&total); err != nil || total <= limit {
		return 0, err
	}
	defer s.nodesChanged()
	args := []any{}
	q := `DELETE FROM nodes WHERE id IN (SELECT n.id FROM nodes n WHERE n.favorite=0 AND n.pinned=0`
	if len(keep) > 0 {
		q += " AND n.id NOT IN (" + placeholders(len(keep)) + ")"
		for _, k := range keep {
			args = append(args, k)
		}
	}
	q += ` ORDER BY EXISTS(SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1), n.last_seen LIMIT ?)`
	args = append(args, total-limit)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(q, args...)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM node_sources WHERE node NOT IN (SELECT id FROM nodes)`); err != nil {
		tx.Rollback()
		return 0, err
	}
	return int(n), tx.Commit()
}

// CountNodes возвращает число узлов: всего и отдаваемых источниками.
func (s *Store) CountNodes() (total, present int, err error) {
	s.cc.mu.Lock()
	defer s.cc.mu.Unlock()
	gen := s.nodesGen.Load() // до запроса, как в PresentLites: запись во время подсчёта оставит кэш устаревшим и он пересчитается
	if s.cc.ok && s.cc.gen == gen {
		return s.cc.total, s.cc.present, nil
	}
	defer func() {
		if err == nil {
			s.cc.total, s.cc.present, s.cc.gen, s.cc.ok = total, present, gen, true
		}
	}()
	err = s.rd.QueryRow(`SELECT COUNT(*), COALESCE(SUM(EXISTS(SELECT 1 FROM node_sources ns WHERE ns.node=n.id AND ns.present=1)),0) FROM nodes n`).Scan(&total, &present)
	return
}
