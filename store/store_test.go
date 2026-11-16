package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryAndPriors(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	good, flaky, dead := NodeID("good"), NodeID("flaky"), NodeID("dead")
	rows := []Row{
		{Node: good, NodeClass: "vless/tcp/reality", Net: "n1", Level: 2, OK: true, At: now.Add(-5 * time.Minute)},
		{Node: good, NodeClass: "vless/tcp/reality", Net: "n1", Level: 4, Target: "youtube", OK: true, At: now.Add(-5 * time.Minute)},
		{Node: flaky, NodeClass: "vless/ws/tls", Net: "n1", Level: 2, OK: true, At: now.Add(-40 * time.Minute)},
		{Node: flaky, NodeClass: "vless/ws/tls", Net: "n1", Level: 2, OK: false, Class: "timeout", At: now.Add(-10 * time.Minute)},
		{Node: dead, NodeClass: "vless/ws/tls", Net: "n1", Level: 2, OK: false, Class: "auth", At: now.Add(-1 * time.Minute)},
		{Node: good, NodeClass: "vless/tcp/reality", Net: "OTHER", Level: 2, OK: false, At: now}, // чужая сеть не должна влиять
	}
	if err := s.Record(rows); err != nil {
		t.Fatal(err)
	}
	h, err := s.Histories("n1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !(h[good].Score(now) > h[flaky].Score(now) && h[flaky].Score(now) > h[dead].Score(now)) {
		t.Fatalf("порядок приоритетов неверен: good=%d flaky=%d dead=%d", h[good].Score(now), h[flaky].Score(now), h[dead].Score(now))
	}
	if h[good].FailedLast {
		t.Fatal("результат из другой сети повлиял на историю")
	}
	// штраф не накапливается: три провала подряд штрафуют так же, как один
	if err := s.Record([]Row{
		{Node: dead, NodeClass: "vless/ws/tls", Net: "n1", Level: 2, Class: "auth", At: now.Add(-30 * time.Second)},
		{Node: dead, NodeClass: "vless/ws/tls", Net: "n1", Level: 2, Class: "auth", At: now.Add(-20 * time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	h2, _ := s.Histories("n1", now)
	if h2[dead].Score(now) != h[dead].Score(now) {
		t.Fatalf("штраф накапливается: %d != %d", h2[dead].Score(now), h[dead].Score(now))
	}
	pr, err := s.ClassPriors("n1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !(pr["vless/tcp/reality"] > pr["vless/ws/tls"]) {
		t.Fatalf("априор reality должен быть выше ws/tls: %v", pr)
	}
}

func TestNetIDNotEmpty(t *testing.T) {
	if NetID() == "" {
		t.Fatal("пустой NetID")
	}
}

func TestNetIDIncludesTunnelInterfaces(t *testing.T) {
	id := NetID()
	t.Logf("NetID=%s", id)
	if _, err := os.Stat("/sys/class/net/throne-tun"); err == nil && !strings.Contains(id, "throne-tun") {
		t.Fatalf("активный throne-tun не отражён в NetID: %s", id)
	}
}

func TestGoogleAPIHistoryRaisesScore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	g, plain := NodeID("g"), NodeID("p")
	_ = s.Record([]Row{
		{Node: g, Net: "n", Level: 2, OK: true, At: now.Add(-time.Minute)},
		{Node: g, Net: "n", Level: 4, Target: "google-api", OK: true, At: now.Add(-time.Minute)},
		{Node: plain, Net: "n", Level: 2, OK: true, At: now.Add(-time.Minute)},
		{Node: plain, Net: "n", Level: 4, Target: "google-api", OK: false, At: now.Add(-time.Minute)},
	})
	h, _ := s.Histories("n", now)
	if h[g].GoogleOK.IsZero() || !h[plain].GoogleOK.IsZero() {
		t.Fatalf("история google-api: %+v", h)
	}
	if h[g].Score(now) <= h[plain].Score(now) {
		t.Fatal("узел с google-api должен стоять выше")
	}
}
