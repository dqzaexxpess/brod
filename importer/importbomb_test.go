package importer

import (
	"bytes"
	"runtime"
	"testing"
	"time"
)

// документ в пределах 8 МБ с миллионами пустых серверов; предел числа узлов проверяется после построения списка.
// Допустимый рост кучи на документе в 8 МБ (до исправления: более 2000 МиБ; остаток ~260 МиБ — json.Unmarshal самого документа, предел узлов на него не влияет).
const maxHeapGrowthMiB = 512

func TestImporterNodeLimitBoundsMemory(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(`{"outbounds":[{"protocol":"vless","settings":{"vnext":[`)
	n := 0
	for b.Len() < maxDocBytes-64 {
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString("{}")
		n++
	}
	b.WriteString(`]}}]}`)
	body := b.Bytes()
	var peak uint64
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		var m runtime.MemStats
		for {
			select {
			case <-done:
				return
			default:
			}
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peak {
				peak = m.HeapInuse
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	t0 := time.Now()
	links, ok := Links(body)
	d := time.Since(t0)
	close(done)
	<-stopped
	growth := (peak - m0.HeapInuse) >> 20
	t.Logf("тело %d байт, пустых серверов %d → ссылок %d, ok=%v, время %v, пик кучи +%d МиБ", len(body), n, len(links), ok, d, growth)
	if growth > maxHeapGrowthMiB {
		t.Errorf("пик кучи +%d МиБ при пределе %d МиБ: предел числа узлов не работает внутри цикла", growth, maxHeapGrowthMiB)
	}
}
