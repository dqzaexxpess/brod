package ota

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func sign(priv ed25519.PrivateKey, doc string) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(doc))) + "\n")
}

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	doc := `{"version":1}`
	if err := Verify(pub, []byte(doc), sign(priv, doc)); err != nil {
		t.Fatalf("верная подпись отвергнута: %v", err)
	}
	if Verify(pub, []byte(doc+" "), sign(priv, doc)) == nil {
		t.Fatal("изменённый документ принят")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if Verify(pub, []byte(doc), sign(other, doc)) == nil {
		t.Fatal("подпись чужого ключа принята")
	}
	for _, bad := range []string{"", "не base64", base64.StdEncoding.EncodeToString([]byte("коротко"))} {
		if Verify(pub, []byte(doc), []byte(bad)) == nil {
			t.Fatalf("повреждённая подпись %q принята", bad)
		}
	}
}

func TestDefaultKeyIsValid(t *testing.T) {
	if k := DefaultKey(); len(k) != ed25519.PublicKeySize {
		t.Fatalf("вшитый ключ неверной длины: %d", len(k))
	}
}

func TestParseSources(t *testing.T) {
	good := `{"version":3,"sources":[{"name":"a/b","url":"https://x.example/a.txt","mirror":"https://m.example/a.txt","priority":1},{"name":"c","url":"https://x.example/c"}],"retired":["old"]}`
	d, err := ParseSources([]byte(good))
	if err != nil || d.Version != 3 || len(d.Sources) != 2 || d.Retired[0] != "old" {
		t.Fatalf("корректный список: %+v %v", d, err)
	}
	cases := map[string]string{
		"нет версии":      `{"sources":[{"name":"a","url":"https://x.example/a"}]}`,
		"пустой список":   `{"version":1,"sources":[]}`,
		"http":            `{"version":1,"sources":[{"name":"a","url":"http://x.example/a"}]}`,
		"логин в адресе":  `{"version":1,"sources":[{"name":"a","url":"https://u:p@x.example/a"}]}`,
		"file":            `{"version":1,"sources":[{"name":"a","url":"file:///etc/passwd"}]}`,
		"http-зеркало":    `{"version":1,"sources":[{"name":"a","url":"https://x.example/a","mirror":"http://m.example/a"}]}`,
		"дубль":           `{"version":1,"sources":[{"name":"a","url":"https://x.example/a"},{"name":"a","url":"https://x.example/b"}]}`,
		"пустое имя":      `{"version":1,"sources":[{"name":"","url":"https://x.example/a"}]}`,
		"приоритет":       `{"version":1,"sources":[{"name":"a","url":"https://x.example/a","priority":101}]}`,
		"лишнее поле":     `{"version":1,"sources":[{"name":"a","url":"https://x.example/a"}],"exec":"rm"}`,
		"снят и в списке": `{"version":1,"sources":[{"name":"a","url":"https://x.example/a"}],"retired":["a"]}`,
		"не JSON":         `источники`,
		"слишком длинное": `{"version":1,"sources":[{"name":"` + strings.Repeat("я", MaxNameLen+1) + `","url":"https://x.example/a"}]}`,
	}
	for name, doc := range cases {
		if _, err := ParseSources([]byte(doc)); err == nil {
			t.Errorf("%s: документ принят", name)
		}
	}
	if _, err := ParseSources(make([]byte, MaxDocBytes+1)); err == nil {
		t.Error("слишком большой документ принят")
	}
	var many strings.Builder
	many.WriteString(`{"version":1,"sources":[`)
	for i := 0; i <= MaxSources; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"name":"s` + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)) + `","url":"https://x.example/a"}`)
	}
	many.WriteString(`]}`)
	if _, err := ParseSources([]byte(many.String())); err == nil {
		t.Error("список из более чем MaxSources принят")
	}
}
