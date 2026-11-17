package store

import (
	"testing"
	"time"
)

func TestUDPRoundTripAndReplace(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	now := time.Unix(1893456000, 0)
	if err := s.PutUDP(UDPRec{Node: "a", Net: "net1", Grade: "none", LossPct: 100, At: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUDP(UDPRec{Node: "a", Net: "net2", Grade: "good", LossPct: 0, MedianMs: 74, At: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	m, err := s.UDPs()
	if err != nil || len(m) != 1 || m["a"].Grade != "good" || m["a"].Net != "net2" || m["a"].MedianMs != 74 || !m["a"].At.Equal(now.Add(time.Hour)) {
		t.Fatalf("%v %v", m, err)
	}
}
