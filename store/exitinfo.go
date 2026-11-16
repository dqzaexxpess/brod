package store

import "time"

// ExitInfoRec — сведения о выходе узла (что видят сайты).
type ExitInfoRec struct {
	Node     string
	IP       string
	Country  string
	Colo     string
	GoogleOK int // 1 / 0 / -1 (не проверялось)
	SiteOK   int
	Detail   string
	At       time.Time
}

// PutExitInfo сохраняет (заменяет) сведения о выходе узла.
func (s *Store) PutExitInfo(r ExitInfoRec) error {
	_, err := s.db.Exec(`
INSERT INTO exit_info(node, ip, country, colo, google_ok, site_ok, detail, at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(node) DO UPDATE SET ip=excluded.ip, country=excluded.country, colo=excluded.colo,
  google_ok=excluded.google_ok, site_ok=excluded.site_ok, detail=excluded.detail, at=excluded.at`,
		r.Node, r.IP, r.Country, r.Colo, r.GoogleOK, r.SiteOK, r.Detail, r.At.Unix())
	return err
}

// ExitInfos возвращает сведения обо всех узлах, для которых они есть.
func (s *Store) ExitInfos() (map[string]ExitInfoRec, error) {
	rows, err := s.rd.Query(`SELECT node, ip, country, colo, google_ok, site_ok, detail, at FROM exit_info`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ExitInfoRec{}
	for rows.Next() {
		var r ExitInfoRec
		var at int64
		if err := rows.Scan(&r.Node, &r.IP, &r.Country, &r.Colo, &r.GoogleOK, &r.SiteOK, &r.Detail, &at); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0)
		out[r.Node] = r
	}
	return out, rows.Err()
}

// ExitCountry возвращает код страны выхода узла; пусто — сведений нет.
func (s *Store) ExitCountry(node string) string {
	var c string
	if err := s.rd.QueryRow(`SELECT country FROM exit_info WHERE node=?`, node).Scan(&c); err != nil {
		return ""
	}
	return c
}
