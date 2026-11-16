package rulesets

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"runtime"
	"testing"
)

func srsFile(raw []byte) []byte {
	var file bytes.Buffer
	file.Write([]byte{0x53, 0x52, 0x53, 1})
	zw, _ := zlib.NewWriterLevel(&file, zlib.BestCompression)
	zw.Write(raw)
	zw.Close()
	return file.Bytes()
}

// список-«бомба» распаковки (несколько килобайт → миллионы пустых правил) отклоняется Validate с ошибкой предела, память не растёт.
func TestValidateRejectsUnpackBomb(t *testing.T) {
	cases := map[string][]byte{}
	for _, n := range []uint64{1_000_000, 4_000_000} {
		raw := binary.AppendUvarint(nil, n)
		raw = append(raw, bytes.Repeat([]byte{0x00, 0xFF, 0x00}, int(n))...) // правило по умолчанию без условий
		cases[fmt.Sprintf("правил %d", n)] = srsFile(raw)
	}
	// одно правило, но огромный распакованный хвост
	raw := binary.AppendUvarint(nil, 1)
	raw = append(raw, 0x00, 0xFF, 0x00)
	raw = append(raw, make([]byte, 64<<20)...)
	cases["огромный хвост"] = srsFile(raw)

	for name, file := range cases {
		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		err := Validate(file)
		runtime.ReadMemStats(&m1)
		grown := (m1.TotalAlloc - m0.TotalAlloc) >> 20
		if err == nil {
			t.Errorf("%s: файл %d байт принят", name, len(file))
		}
		if grown > 64 {
			t.Errorf("%s: Validate выделил %d МиБ", name, grown)
		}
	}
}

// Реальные встроенные списки пределы проходят.
func TestValidateAcceptsEmbeddedSnapshot(t *testing.T) {
	for _, s := range Sets {
		b, err := snapshot.ReadFile("data/" + s.Name + ".srs")
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(b); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
	}
}
