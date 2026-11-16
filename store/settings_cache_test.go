package store

import "testing"

// Settings() отдаёт копию из памяти; запись и удаление сбрасывают её.
func TestSettingsCached(t *testing.T) {
	s, _ := open(t)
	if err := s.SetSetting("a", "1"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Settings()
	if err != nil || m["a"] != "1" {
		t.Fatalf("m=%v err=%v", m, err)
	}
	m["a"] = "испорчено" // вызывающий меняет свою копию: кэш не страдает
	// запись мимо кэша: если второй вызов идёт в базу, он увидит "2"
	if _, err := s.db.Exec(`UPDATE settings SET v='2' WHERE k='a'`); err != nil {
		t.Fatal(err)
	}
	m2, _ := s.Settings()
	if m2["a"] != "1" {
		t.Fatalf("второй вызов ходил в базу или кэш испорчен: %v", m2)
	}
	if err := s.SetSetting("a", "3"); err != nil {
		t.Fatal(err)
	}
	if m3, _ := s.Settings(); m3["a"] != "3" {
		t.Fatalf("SetSetting не сбросил кэш: %v", m3)
	}
	if err := s.DeleteSetting("a"); err != nil {
		t.Fatal(err)
	}
	if m4, _ := s.Settings(); len(m4) != 0 {
		t.Fatalf("DeleteSetting не сбросил кэш: %v", m4)
	}
}
