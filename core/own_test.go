package core

import (
	"strings"
	"testing"
)

const (
	ownSS    = "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNzd29yZDEyMw@203.0.113.7:8388#my-ss"
	ownVLESS = "vless://5b0e5c2e-0000-4000-8000-000000000001@vps.example:443?security=tls&sni=vps.example&type=tcp#my-vps"
)

func TestOwnServers(t *testing.T) {
	c := startCore(t, t.TempDir())
	if _, err := c.AddOwn("тут нет ссылок", "user"); err == nil {
		t.Fatal("без ссылок должна быть ошибка")
	}
	rep, err := c.AddOwn(ownSS+"\n"+ownVLESS+"\nmuсор://битая ссылка без хоста", "user")
	if err != nil || rep.Added != 2 || rep.Duplicates != 0 || len(rep.IDs) != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	// повторное добавление — дубликаты, не новые
	rep2, _ := c.AddOwn(ownSS, "user")
	if rep2.Added != 0 || rep2.Duplicates != 1 {
		t.Fatalf("повтор: %+v", rep2)
	}
	views, _ := c.Nodes(NodesQuery{})
	own := 0
	for _, v := range views {
		if v.Own {
			own++
			if !v.Favorite || !v.Pinned {
				t.Fatalf("собственный сервер должен быть избранным и закреплённым: %+v", v)
			}
		}
	}
	if own != 2 {
		t.Fatalf("собственных в списке: %d", own)
	}
	if err := c.RemoveOwn(rep.IDs[0], "user"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Nodes(NodesQuery{}); len(v) != 1 || !v[0].Own {
		t.Fatalf("после удаления: %+v", v)
	}
	// узел, который сейчас активен, удалить нельзя
	if err := c.Connect(rep.IDs[1], "user"); err != nil {
		t.Fatalf("подключение не удалось в тесте: %v", err)
	}
	if err := c.RemoveOwn(rep.IDs[1], "user"); err == nil || !strings.Contains(err.Error(), "используется") {
		t.Fatalf("активный сервер удалять нельзя: %v", err)
	}
	// чужой узел собственным не является
	other := addNode(t, c, "sub-node")
	if err := c.RemoveOwn(other, "user"); err == nil {
		t.Fatal("узел из подписки не собственный")
	}
}

func TestEditOwnAndLabels(t *testing.T) {
	c := startCore(t, t.TempDir())
	rep, err := c.AddOwn(ownVLESS, "user")
	if err != nil || rep.Added != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	id := rep.IDs[0]
	label := "Мой VPS в Хельсинки"
	srv, port := "new.vps.example", 8443
	if err := c.EditOwn(id, EditOwnReq{Label: &label, Server: &srv, Port: &port}, "user"); err != nil {
		t.Fatal(err)
	}
	v, _ := c.Nodes(NodesQuery{})
	if len(v) != 1 || v[0].Label != label || v[0].Server != srv || v[0].Port != port {
		t.Fatalf("после правки: %+v", v)
	}
	rec, _ := c.st.Node(id)
	if !strings.Contains(string(rec.Opts), `"server":"new.vps.example"`) || !strings.Contains(string(rec.Opts), `"server_port":8443`) {
		t.Fatalf("опции не обновились: %s", rec.Opts)
	}
	bad := "bad host"
	if err := c.EditOwn(id, EditOwnReq{Server: &bad}, "user"); err == nil {
		t.Fatal("недопустимый адрес должен отклоняться")
	}
	zero := 0
	if err := c.EditOwn(id, EditOwnReq{Port: &zero}, "user"); err == nil {
		t.Fatal("порт 0 должен отклоняться")
	}
	// замена ссылкой: новый id, название переезжает, старый сервер исчезает
	newID, err := c.ReplaceOwn(id, ownSS, "user")
	if err != nil || newID == id {
		t.Fatalf("замена: %v %s", err, newID)
	}
	v, _ = c.Nodes(NodesQuery{})
	if len(v) != 1 || v[0].ID != newID || v[0].Label != label || !v[0].Own {
		t.Fatalf("после замены: %+v", v)
	}
	// название любому узлу
	other := addNode(t, c, "sub")
	if err := c.SetNodeLabel(other, "  Чужой  ", "user"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetNodeLabel(other, strings.Repeat("я", 61), "user"); err == nil {
		t.Fatal("слишком длинное название")
	}
	// активный сервер править нельзя
	if err := c.Connect(newID, "user"); err == nil {
		if err := c.EditOwn(newID, EditOwnReq{Server: &srv}, "user"); err == nil {
			t.Fatal("активный сервер править нельзя")
		}
	}
	if err := c.EditOwn(other, EditOwnReq{Label: &label}, "user"); err == nil {
		t.Fatal("чужой узел собственным не является")
	}
}

// Файл wg-quick (WireGuard или AmneziaWG) добавляется как свой сервер: вместе с параметрами AmneziaWG, без лишних полей и ключа в ответе.
func TestOwnAcceptsWGQuickConfig(t *testing.T) {
	c := newCore(t, t.TempDir())
	defer c.Close()
	conf := "[Interface]\nAddress = 172.16.0.2\nMTU = 1380\nPrivateKey = AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=\nJc = 4\nJmin = 40\nJmax = 70\nPostUp = reboot\n" +
		"[Peer]\nPublicKey = bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=\nEndpoint = 8.34.146.9:878\n"
	rep, err := c.AddOwn(conf, "test")
	if err != nil || rep.Added != 1 || rep.Failed != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	v, err := c.NodeByID(rep.IDs[0])
	if err != nil || v.Server != "8.34.146.9" || v.Port != 878 || !v.Own {
		t.Fatalf("узел: %+v %v", v, err)
	}
	if rec, _ := c.st.Node(rep.IDs[0]); !strings.Contains(string(rec.Opts), `"jc":4`) || strings.Contains(string(rec.Opts), "reboot") {
		t.Fatalf("параметры узла: %s", rec.Opts)
	}
	if _, err := c.AddOwn("просто текст без серверов", "test"); err == nil {
		t.Fatal("текст без серверов должен отклоняться")
	}
}
