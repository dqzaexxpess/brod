package core

import (
	"testing"
	"time"

	"brod/store"
)

// Метка «для звонков» в списке узлов: замер UDP показывается, только если снят в текущей сети и не устарел.
func TestUDPLabelFreshAndSameNetwork(t *testing.T) {
	c := startCore(t, t.TempDir())
	id := addNode(t, c, "calls")
	_ = c.st.Record([]store.Row{{Node: id, Net: c.netKey(), NodeClass: "direct/tcp/none", Level: 2, OK: true, LatencyMs: 100, At: time.Now()}})

	view := func() NodeView {
		v, err := c.NodeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := view(); v.UDP != "" {
		t.Fatalf("без замера метки быть не должно: %+v", v)
	}
	put := func(net string, at time.Time) {
		if err := c.st.PutUDP(store.UDPRec{Node: id, Net: net, Grade: "good", MedianMs: 74, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	put(c.netKey(), time.Now())
	if v := view(); v.UDP != "good" || v.UDPMs != 74 {
		t.Fatalf("свежий замер в текущей сети не показан: %+v", v)
	}
	put("другая-сеть", time.Now())
	if v := view(); v.UDP != "" {
		t.Fatalf("замер из другой сети показан: %+v", v)
	}
	put(c.netKey(), time.Now().Add(-c.cfg.UDPTTL-time.Minute))
	if v := view(); v.UDP != "" {
		t.Fatalf("устаревший замер показан: %+v", v)
	}
	// список узлов отдаёт ту же метку
	put(c.netKey(), time.Now())
	list, err := c.Nodes(NodesQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range list {
		if n.ID == id {
			found = n.UDP == "good"
		}
	}
	if !found {
		t.Fatal("в списке узлов метка не показана")
	}
}
