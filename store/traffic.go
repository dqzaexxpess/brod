package store

import "time"

// DayTraffic — трафик за день в байтах (всего и через VPN).
type DayTraffic struct {
	Day            string
	Up, Down       int64
	VPNUp, VPNDown int64
}

// AddDailyTraffic прибавляет приращения к счётчикам дня.
func (s *Store) AddDailyTraffic(day string, up, down, vpnUp, vpnDown int64) error {
	_, err := s.db.Exec(`INSERT INTO traffic_daily(day, up, down, vpn_up, vpn_down) VALUES(?,?,?,?,?)
ON CONFLICT(day) DO UPDATE SET up=up+excluded.up, down=down+excluded.down, vpn_up=vpn_up+excluded.vpn_up, vpn_down=vpn_down+excluded.vpn_down`,
		day, up, down, vpnUp, vpnDown)
	return err
}

// DailyTraffic возвращает последние n дней (старые первыми).
func (s *Store) DailyTraffic(n int) ([]DayTraffic, error) {
	rows, err := s.rd.Query(`SELECT day, up, down, vpn_up, vpn_down FROM traffic_daily ORDER BY day DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayTraffic
	for rows.Next() {
		var d DayTraffic
		if err := rows.Scan(&d.Day, &d.Up, &d.Down, &d.VPNUp, &d.VPNDown); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// PruneDailyTraffic удаляет записи старше before (по строке даты).
func (s *Store) PruneDailyTraffic(before time.Time) error {
	_, err := s.db.Exec(`DELETE FROM traffic_daily WHERE day < ?`, before.Format("2006-01-02"))
	return err
}
