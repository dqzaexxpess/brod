package cli

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func TestDecodeTextHandlesWindowsEncodings(t *testing.T) {
	const doc = `{"rules":[{"action":"direct","match":{"domain":["пример.рф"]}}]}`
	if got := string(decodeText([]byte(doc))); got != doc {
		t.Fatalf("обычный UTF-8 изменён: %q", got)
	}
	if got := string(decodeText(append([]byte{0xEF, 0xBB, 0xBF}, doc...))); got != doc {
		t.Fatalf("метка UTF-8 не снята: %q", got)
	}
	enc := func(bom []byte, order binary.AppendByteOrder) []byte {
		out := append([]byte{}, bom...)
		for _, u := range utf16.Encode([]rune(doc)) {
			out = order.AppendUint16(out, u)
		}
		return out
	}
	if got := string(decodeText(enc([]byte{0xFF, 0xFE}, binary.LittleEndian))); got != doc {
		t.Fatalf("UTF-16 LE не прочитан: %q", got)
	}
	if got := string(decodeText(enc([]byte{0xFE, 0xFF}, binary.BigEndian))); got != doc {
		t.Fatalf("UTF-16 BE не прочитан: %q", got)
	}
	// нечётная длина после метки UTF-16 — не наш случай: оставляем как есть, пусть разбор JSON сообщит ошибку
	if got := decodeText([]byte{0xFF, 0xFE, 'a'}); len(got) != 3 {
		t.Fatalf("битый UTF-16 изменён: %v", got)
	}
}
