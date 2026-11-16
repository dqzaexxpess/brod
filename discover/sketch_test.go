package discover

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

var testSalt = []byte("соль-теста")

func sketchOf(salt []byte, links []string) []uint64 {
	b := NewSketchBuilder(salt)
	for _, l := range links {
		b.Add(l)
	}
	return b.Sum()
}

func linkSet(n int, tag string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("vless://11111111-2222-3333-4444-%012d@%s%d.example.org:443?security=tls#%s%d", i, tag, i, tag, i)
	}
	return out
}

func TestNormalizeLinkIgnoresFragmentCaseAndEntities(t *testing.T) {
	a := NormalizeLink("  VLESS://u@h:1?a=1&amp;b=2#имя ")
	b := NormalizeLink("vless://u@h:1?a=1&b=2#другое")
	if a != b || a != "vless://u@h:1?a=1&b=2" {
		t.Errorf("%q %q", a, b)
	}
}

func TestSketchIdenticalFilesAndOrder(t *testing.T) {
	links := linkSet(3000, "a")
	rev := make([]string, len(links))
	for i, l := range links {
		rev[len(links)-1-i] = l
	}
	// комментарии «#…» в зеркале другие: на отпечаток не влияют
	renamed := make([]string, len(links))
	for i, l := range links {
		renamed[i] = l[:strings.IndexByte(l, '#')] + "#зеркало"
	}
	a, b, c := sketchOf(testSalt, links), sketchOf(testSalt, rev), sketchOf(testSalt, renamed)
	if len(a) != SketchK {
		t.Fatalf("длина %d", len(a))
	}
	if Similarity(a, b) != 1 || Similarity(a, c) != 1 {
		t.Errorf("одинаковые наборы: %v %v", Similarity(a, b), Similarity(a, c))
	}
	if len(EncodeSketch(a)) != 512 {
		t.Errorf("размер blob %d", len(EncodeSketch(a)))
	}
	back := DecodeSketch(EncodeSketch(a))
	if Similarity(a, back) != 1 || len(back) != len(a) {
		t.Error("кодирование не обратимо")
	}
	if DecodeSketch([]byte{1, 2, 3}) != nil || DecodeSketch(make([]byte, 8*SketchK+8)) != nil {
		t.Error("битая длина должна давать пустой отпечаток")
	}
}

func TestSketchMirrorWithFivePercentDiffer(t *testing.T) {
	base := linkSet(2000, "m")
	mirror := append([]string(nil), base...)
	rng := rand.New(rand.NewSource(7))
	for _, i := range rng.Perm(len(mirror))[:100] { // 5% строк заменены
		mirror[i] = fmt.Sprintf("trojan://p%d@other%d.example.net:443", i, i)
	}
	s := Similarity(sketchOf(testSalt, base), sketchOf(testSalt, mirror))
	if s < SimilarThreshold {
		t.Errorf("зеркало с 5%% отличий: оценка %.3f < %.2f", s, SimilarThreshold)
	}
}

func TestSketchDifferentFiles(t *testing.T) {
	s := Similarity(sketchOf(testSalt, linkSet(2000, "x")), sketchOf(testSalt, linkSet(2000, "y")))
	if s >= 0.1 {
		t.Errorf("разные файлы: оценка %.3f", s)
	}
	if Similarity(nil, sketchOf(testSalt, linkSet(100, "x"))) != 0 {
		t.Error("пустой отпечаток должен давать 0")
	}
}

func TestSketchSaltChangesHashes(t *testing.T) {
	l := linkSet(500, "s")
	a, b := sketchOf([]byte("one"), l), sketchOf([]byte("two"), l)
	if Similarity(a, b) > 0.1 {
		t.Error("с другой солью хэши должны отличаться")
	}
}

func TestSketchSmallSetsExact(t *testing.T) {
	a := sketchOf(testSalt, linkSet(30, "e"))
	b := sketchOf(testSalt, append(linkSet(24, "e"), linkSet(6, "z")...))
	// 24 общих, объединение 36 -> 24/36
	if s := Similarity(a, b); s < 0.66 || s > 0.67 {
		t.Errorf("точная оценка малых наборов: %.4f", s)
	}
}

// Граница порога: хэши заданы напрямую. 52 общих из 64 — 0.8125 (группа), 51 — 0.797 (нет).
func TestSimilarityThresholdBoundary(t *testing.T) {
	seq := func(from, n int) []uint64 {
		o := make([]uint64, n)
		for i := range o {
			o[i] = uint64(from + i)
		}
		return o
	}
	a := seq(1, 64)
	for _, tc := range []struct {
		shared int
		group  bool
	}{{52, true}, {51, false}, {64, true}, {30, false}} {
		b := append(seq(1, tc.shared), seq(1000, 64-tc.shared)...)
		s := Similarity(a, b)
		comp := Group([][]uint64{a, b}, SimilarThreshold)
		if (comp[0] == comp[1]) != tc.group {
			t.Errorf("общих %d: оценка %.4f, в одной группе %v, ждали %v", tc.shared, s, comp[0] == comp[1], tc.group)
		}
	}
	// ровно на пороге (>=): k=10, общих 8 из 10 -> 0.8
	a10, b10 := seq(1, 10), append(seq(1, 8), seq(500, 2)...)
	if s := similarityK(a10, b10, 10); s != 0.8 || s < SimilarThreshold {
		t.Errorf("на пороге: %v", s)
	}
}

func TestGroupComponentsAndEmpty(t *testing.T) {
	x, y := sketchOf(testSalt, linkSet(500, "g1")), sketchOf(testSalt, linkSet(500, "g2"))
	comp := Group([][]uint64{x, y, x, nil, nil, y}, SimilarThreshold)
	if comp[0] != comp[2] || comp[1] != comp[5] || comp[0] == comp[1] || comp[3] == comp[4] || comp[3] == comp[0] {
		t.Errorf("компоненты: %v", comp)
	}
}
