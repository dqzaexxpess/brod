package core

import (
	"encoding/json"
	"testing"
)

func TestBackupRoundTrip(t *testing.T) {
	a := startCore(t, t.TempDir())
	if _, err := a.AddOwn(ownSS+"\n"+ownVLESS, "user"); err != nil {
		t.Fatal(err)
	}
	v, _ := a.Nodes(NodesQuery{})
	label := "Мой сервер"
	_ = a.SetNodeLabel(v[0].ID, label, "user")
	_ = a.PutRouteRules([]RouteRule{{Enabled: true, Action: ActionDirect, Match: RouteMatch{DomainSuffix: []string{"example.com"}}}}, "user")
	_ = a.SetSetting(SetDegradeAction, "auto", "user")
	_ = a.SetSetting(SetNotifyLevel, "off", "user")
	b, err := a.ExportBackup()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)

	// восстановление в чистую базу
	c := startCore(t, t.TempDir())
	var b2 Backup
	if err := json.Unmarshal(raw, &b2); err != nil {
		t.Fatal(err)
	}
	rep, err := c.ImportBackup(b2, "user")
	if err != nil || rep.Own != 2 || rep.Rules != 1 || rep.Settings < 2 || rep.Labels != 1 || len(rep.Skipped) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if c.DegradeAction() != "auto" || c.NotifyLevel() != "off" {
		t.Fatal("настройки не восстановились")
	}
	rules, _ := c.RouteRules()
	if len(rules) != 1 || rules[0].Match.DomainSuffix[0] != "example.com" {
		t.Fatalf("правила: %+v", rules)
	}
	nodes, _ := c.Nodes(NodesQuery{})
	own, labelled := 0, 0
	for _, n := range nodes {
		if n.Own && n.Favorite && n.Pinned {
			own++
		}
		if n.Label == label {
			labelled++
		}
	}
	if own != 2 || labelled != 1 {
		t.Fatalf("серверы после восстановления: own=%d labelled=%d", own, labelled)
	}
	// негодное пропускается, остальное применяется; чужая версия отклоняется
	bad := Backup{Version: BackupVersion, Own: []BackupNode{{ID: "x", Server: "h", Opts: json.RawMessage(`{"нет":"типа"}`)}}, Settings: map[string]string{"нет.такой": "1"}}
	rep, err = c.ImportBackup(bad, "user")
	if err != nil || rep.Own != 0 || len(rep.Skipped) != 2 {
		t.Fatalf("негодное: %+v %v", rep, err)
	}
	if _, err := c.ImportBackup(Backup{Version: 99}, "user"); err == nil {
		t.Fatal("неизвестная версия должна отклоняться")
	}
}
