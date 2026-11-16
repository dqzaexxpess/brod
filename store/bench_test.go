package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Объём реального корпуса — сотни тысяч узлов: массовая запись и выборка должны укладываться в секунды.
func BenchmarkUpsertAndList(b *testing.B) {
	for i := 0; i < b.N; i++ {
		s, err := Open(filepath.Join(b.TempDir(), "b.db"))
		if err != nil {
			b.Fatal(err)
		}
		recs := make([]NodeRec, 100000)
		for j := range recs {
			recs[j] = rec(fmt.Sprintf("k%d", j), "vless")
			recs[j].Opts = make([]byte, 600)
		}
		t0 := time.Now()
		if err := s.UpsertNodes("A", 1, recs, time.Now()); err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(time.Since(t0).Seconds(), "s/100k-upsert")
		t0 = time.Now()
		l, _ := s.Nodes(NodeFilter{OnlyPresent: true})
		b.ReportMetric(time.Since(t0).Seconds(), "s/100k-list")
		if len(l) != 100000 {
			b.Fatal(len(l))
		}
		s.Close()
	}
}
