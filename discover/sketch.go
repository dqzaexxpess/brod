package discover

import (
	"container/heap"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"
)

// Отпечаток набора ссылок (группировка кандидатов-зеркал по содержимому).
//
// Bottom-k sketch: из набора ссылок узлов берутся SketchK наименьших 64-битных хэшей. Для двух наборов доля общих хэшей среди SketchK
// наименьших хэшей их объединения — несмещённая оценка коэффициента Жаккара (точная, пока в наборах меньше SketchK различных ссылок).
//
// Нормализация ссылки (NormalizeLink): обрезка пробелов, «&amp;» -> «&», схема в нижнем регистре, фрагмент «#…» отброшен. Это дешёвая
// строковая нормализация, а не translator.Parse→Key: проход читает до сотен файлов по 10000 ссылок (миллионы ссылок), а полный разбор
// на телефоне — лишние процессор и батарея. Цена выбора: два файла с одним узлом, записанным с другим порядком параметров, считаются
// разными ссылками; для зеркал (копии одного набора, различия только в комментарии «#…» или в нескольких строках) это не мешает.
//
// В базу идёт только сам отпечаток (хэши). Хэш — усечённый до 64 бит SHA-256 от соли установки и нормализованной ссылки: по отпечатку
// ссылку не подобрать (без соли догадку не проверить, а усечение не оставляет ссылки в хэше), сами ссылки в базу не попадают.
const (
	SketchK = 64
	// SimilarThreshold — оценка Жаккара, начиная с которой (включительно) два кандидата считаются копиями друг друга.
	SimilarThreshold = 0.8
)

// NormalizeLink — ключ ссылки для отпечатка (см. выше).
func NormalizeLink(l string) string {
	l = strings.TrimSpace(l)
	if i := strings.IndexByte(l, '#'); i >= 0 {
		l = l[:i]
	}
	l = strings.ReplaceAll(l, "&amp;", "&")
	if i := strings.Index(l, "://"); i > 0 {
		l = strings.ToLower(l[:i]) + l[i:]
	}
	return l
}

// linkHash — 64-битный хэш нормализованной ссылки с солью.
func linkHash(salt []byte, l string) uint64 {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte{0})
	h.Write([]byte(NormalizeLink(l)))
	var sum [sha256.Size]byte
	return binary.BigEndian.Uint64(h.Sum(sum[:0])[:8])
}

// maxHeap — куча наибольших из хранимых хэшей: корень вытесняется, когда приходит меньший.
type maxHeap []uint64

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(x any)        { *h = append(*h, x.(uint64)) }
func (h *maxHeap) Pop() any          { o := *h; v := o[len(o)-1]; *h = o[:len(o)-1]; return v }

// SketchBuilder копит отпечаток потоком ссылок (память O(SketchK)).
type SketchBuilder struct {
	salt []byte
	h    maxHeap
	in   map[uint64]struct{}
}

// NewSketchBuilder — salt: соль установки (одна на все проходы, иначе отпечатки несравнимы).
func NewSketchBuilder(salt []byte) *SketchBuilder {
	return &SketchBuilder{salt: salt, in: make(map[uint64]struct{}, SketchK+1)}
}

// Add учитывает ссылку (повторы одной нормализованной ссылки считаются один раз).
func (b *SketchBuilder) Add(link string) {
	v := linkHash(b.salt, link)
	if _, dup := b.in[v]; dup {
		return
	}
	if len(b.h) < SketchK {
		heap.Push(&b.h, v)
		b.in[v] = struct{}{}
		return
	}
	if v >= b.h[0] {
		return
	}
	delete(b.in, b.h[0])
	b.h[0] = v
	heap.Fix(&b.h, 0)
	b.in[v] = struct{}{}
}

// Sum — отпечаток: хэши по возрастанию.
func (b *SketchBuilder) Sum() []uint64 {
	out := append([]uint64(nil), b.h...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// EncodeSketch — компактный вид для базы: 8 байт на хэш (до 512 байт).
func EncodeSketch(s []uint64) []byte {
	if len(s) == 0 {
		return nil
	}
	b := make([]byte, 8*len(s))
	for i, v := range s {
		binary.BigEndian.PutUint64(b[8*i:], v)
	}
	return b
}

// DecodeSketch разбирает вид из базы; битая длина — пустой отпечаток.
func DecodeSketch(b []byte) []uint64 {
	if len(b) == 0 || len(b)%8 != 0 || len(b) > 8*SketchK {
		return nil
	}
	s := make([]uint64, len(b)/8)
	for i := range s {
		s[i] = binary.BigEndian.Uint64(b[8*i:])
	}
	return s
}

// Similarity — оценка коэффициента Жаккара двух отпечатков (оба по возрастанию). Пустой отпечаток ни на что не похож (0).
func Similarity(a, b []uint64) float64 { return similarityK(a, b, SketchK) }

func similarityK(a, b []uint64, k int) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// Полный отпечаток (k хэшей) знает набор только до своего наибольшего хэша: выше этой границы сравнивать нельзя.
	limit := ^uint64(0)
	if len(a) >= k && a[len(a)-1] < limit {
		limit = a[len(a)-1]
	}
	if len(b) >= k && b[len(b)-1] < limit {
		limit = b[len(b)-1]
	}
	i, j, union, both := 0, 0, 0, 0
	for union < k && (i < len(a) || j < len(b)) {
		var v uint64
		inA, inB := false, false
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			v, inA, inB = a[i], true, true
		case i < len(a) && (j >= len(b) || a[i] < b[j]):
			v, inA = a[i], true
		default:
			v, inB = b[j], true
		}
		if v > limit {
			break
		}
		if inA {
			i++
		}
		if inB {
			j++
		}
		union++
		if inA && inB {
			both++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(both) / float64(union)
}

// Group — связные компоненты графа «оценка Жаккара >= threshold» над отпечатками sk. Возвращает номер компоненты для каждого элемента
// (0..n-1, по порядку первого появления). Элемент с пустым отпечатком — своя компонента.
func Group(sk [][]uint64, threshold float64) []int {
	n := len(sk)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := 0; i < n; i++ {
		if len(sk[i]) == 0 {
			continue
		}
		for j := i + 1; j < n; j++ {
			if len(sk[j]) > 0 && Similarity(sk[i], sk[j]) >= threshold {
				parent[find(j)] = find(i)
			}
		}
	}
	comp := make([]int, n)
	ids := map[int]int{}
	for i := range comp {
		r := find(i)
		if _, ok := ids[r]; !ok {
			ids[r] = len(ids)
		}
		comp[i] = ids[r]
	}
	return comp
}
