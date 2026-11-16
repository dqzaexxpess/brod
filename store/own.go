package store

// OwnSource — служебный «источник» собственных серверов пользователя: их никто не обновляет и не удаляет автоматически.
const OwnSource = "own"

// OwnIDs возвращает идентификаторы собственных узлов.
func (s *Store) OwnIDs() (map[string]bool, error) {
	rows, err := s.rd.Query(`SELECT node FROM node_sources WHERE source=? AND present=1`, OwnSource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DeleteOwn убирает узел из собственных. Если он больше нигде не присутствует, узел удаляется целиком вместе со сведениями о выходе и скорости;
// если его отдаёт и обычный источник — остаётся (снимаются только флаги «избранный/закреплённый»).
func (s *Store) DeleteOwn(id string) error {
	defer s.nodesChanged()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM node_sources WHERE node=? AND source=?`, id, OwnSource); err != nil {
		tx.Rollback()
		return err
	}
	var other int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM node_sources WHERE node=?`, id).Scan(&other); err != nil {
		tx.Rollback()
		return err
	}
	if other == 0 {
		for _, q := range []string{`DELETE FROM nodes WHERE id=?`, `DELETE FROM exit_info WHERE node=?`, `DELETE FROM node_speed WHERE node=?`} {
			if _, err := tx.Exec(q, id); err != nil {
				tx.Rollback()
				return err
			}
		}
	} else if _, err := tx.Exec(`UPDATE nodes SET favorite=0, pinned=0 WHERE id=?`, id); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

const labelPrefix = "node.label."

// NodeLabels возвращает пользовательские названия узлов (id → название).
func (s *Store) NodeLabels() (map[string]string, error) {
	rows, err := s.rd.Query(`SELECT k, v FROM settings WHERE k LIKE ?`, labelPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k[len(labelPrefix):]] = v
	}
	return out, rows.Err()
}

// SetNodeLabel задаёт название узла (пустое — снимает).
func (s *Store) SetNodeLabel(id, label string) error {
	if label == "" {
		return s.DeleteSetting(labelPrefix + id)
	}
	return s.SetSetting(labelPrefix+id, label)
}

// UpdateNodeAddr меняет адрес и порт узла и его опции на месте (идентификатор остаётся прежним: для собственных узлов он только метка).
func (s *Store) UpdateNodeAddr(id, server string, port int, opts []byte) error {
	defer s.nodesChanged()
	res, err := s.db.Exec(`UPDATE nodes SET server=?, port=?, opts=? WHERE id=?`, server, port, opts, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
