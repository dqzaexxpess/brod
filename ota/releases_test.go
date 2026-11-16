package ota

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func relDoc(mut func(*ReleasesDoc)) []byte {
	d := ReleasesDoc{Version: 3, Created: "2026-11-10T12:00:00Z", Release: Release{App: "0.4.0", Notes: "правила маршрутизации", Assets: map[string]Asset{
		"android-arm64": {URL: "https://example.org/brod-0.4.0.apk", Mirror: "https://cdn.example.net/brod-0.4.0.apk", SHA256: strings.Repeat("ab", 32), Size: 91_000_000},
		"linux-amd64":   {URL: "https://example.org/brod-0.4.0-linux.tar.gz", SHA256: strings.Repeat("cd", 32), Size: 40_000_000},
	}}}
	if mut != nil {
		mut(&d)
	}
	b, _ := jsonMarshal(d)
	return b
}

func TestParseReleases(t *testing.T) {
	d, err := ParseReleases(relDoc(nil))
	if err != nil || d.Version != 3 || len(d.Release.Assets) != 2 {
		t.Fatalf("корректный манифест: %v, %+v", err, d)
	}
	bad := map[string]func(*ReleasesDoc){
		"версия публикации":  func(d *ReleasesDoc) { d.Version = 0 },
		"версия приложения":  func(d *ReleasesDoc) { d.Release.App = "latest" },
		"версия с суффиксом": func(d *ReleasesDoc) { d.Release.App = "0.4.0-beta" },
		"длинное описание":   func(d *ReleasesDoc) { d.Release.Notes = strings.Repeat("я", MaxNotes+1) },
		"нет файлов":         func(d *ReleasesDoc) { d.Release.Assets = nil },
		"чужая платформа":    func(d *ReleasesDoc) { d.Release.Assets["macos-arm64"] = d.Release.Assets["linux-amd64"] },
		"http вместо https": func(d *ReleasesDoc) {
			a := d.Release.Assets["linux-amd64"]
			a.URL = "http://example.org/x"
			d.Release.Assets["linux-amd64"] = a
		},
		"логин в адресе": func(d *ReleasesDoc) {
			a := d.Release.Assets["linux-amd64"]
			a.URL = "https://u:p@example.org/x"
			d.Release.Assets["linux-amd64"] = a
		},
		"зеркало не https": func(d *ReleasesDoc) {
			a := d.Release.Assets["linux-amd64"]
			a.Mirror = "ftp://example.org/x"
			d.Release.Assets["linux-amd64"] = a
		},
		"sha256 заглавными": func(d *ReleasesDoc) {
			a := d.Release.Assets["linux-amd64"]
			a.SHA256 = strings.ToUpper(a.SHA256)
			d.Release.Assets["linux-amd64"] = a
		},
		"sha256 короткий": func(d *ReleasesDoc) {
			a := d.Release.Assets["linux-amd64"]
			a.SHA256 = "abcd"
			d.Release.Assets["linux-amd64"] = a
		},
		"размер ноль": func(d *ReleasesDoc) {
			a := d.Release.Assets["linux-amd64"]
			a.Size = 0
			d.Release.Assets["linux-amd64"] = a
		},
		"размер больше лимита": func(d *ReleasesDoc) {
			a := d.Release.Assets["linux-amd64"]
			a.Size = MaxAssetBytes + 1
			d.Release.Assets["linux-amd64"] = a
		},
	}
	for name, mut := range bad {
		if _, err := ParseReleases(relDoc(mut)); err == nil {
			t.Errorf("%s: манифест должен отклоняться", name)
		}
	}
	for name, raw := range map[string]string{
		"пусто":            "",
		"не json":          "привет",
		"неизвестное поле": strings.Replace(string(relDoc(nil)), `"version":3`, `"version":3,"evil":1`, 1),
	} {
		if _, err := ParseReleases([]byte(raw)); err == nil {
			t.Errorf("%s: манифест должен отклоняться", name)
		}
	}
	if _, err := ParseReleases(bytes.Repeat([]byte(" "), MaxDocBytes+1)); err == nil {
		t.Error("слишком большой документ должен отклоняться")
	}
}

func TestReleaseNewerThan(t *testing.T) {
	r := Release{App: "0.4.0"}
	for cur, want := range map[string]bool{
		"0.3.9": true, "0.3.0-android": true, "0.3.0-linux": true, "0.2.9-windows": true, "0.4.0": false, "0.4.0-linux": false, "0.4.1": false, "1.0.0": false, "0.10.0": false,
		"": false, "dev": false, "0.3": false,
	} {
		if got := r.NewerThan(cur); got != want {
			t.Errorf("0.4.0 новее %q: %v, ожидалось %v", cur, got, want)
		}
	}
	if !(Release{App: "0.10.0"}).NewerThan("0.9.9") {
		t.Error("0.10.0 новее 0.9.9 (сравнение по числам, не по строкам)")
	}
}

func TestVerifyAsset(t *testing.T) {
	data := []byte(strings.Repeat("brod", 1000))
	sum := sha256.Sum256(data)
	a := Asset{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	if err := VerifyAsset(bytes.NewReader(data), a); err != nil {
		t.Fatalf("честный файл: %v", err)
	}
	tampered := append([]byte(nil), data...)
	tampered[10] ^= 1
	if err := VerifyAsset(bytes.NewReader(tampered), a); err == nil {
		t.Error("подменённый файл должен отклоняться")
	}
	if err := VerifyAsset(bytes.NewReader(data[:len(data)-1]), a); err == nil {
		t.Error("обрезанный файл должен отклоняться")
	}
	if err := VerifyAsset(bytes.NewReader(append(append([]byte(nil), data...), 0)), a); err == nil {
		t.Error("файл больше заявленного должен отклоняться")
	}
	// «бесконечный» ответ: читается не больше размера + 1 байта
	var read int64
	inf := readerFunc(func(p []byte) (int, error) { read += int64(len(p)); return len(p), nil })
	if err := VerifyAsset(inf, a); err == nil || read > a.Size+int64(1<<20) {
		t.Errorf("бесконечный поток: ошибка %v, прочитано %d (заявлено %d)", err, read, a.Size)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// Подпись над точными байтами манифеста: подмена любого байта ломает проверку.
func TestReleasesSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	doc := relDoc(nil)
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, doc)))
	if err := Verify(pub, doc, sig); err != nil {
		t.Fatal(err)
	}
	evil := bytes.Replace(doc, []byte("91000000"), []byte("91000001"), 1)
	if err := Verify(pub, evil, sig); err == nil {
		t.Error("изменённый манифест не должен проходить проверку подписи")
	}
	_ = io.EOF
}

// Ключ релизов вшит, корректен и отличается от ключа списка источников; подпись манифеста проверяется именно им.
func TestReleasesKeyIsSeparate(t *testing.T) {
	rk, sk := ReleasesKey(), DefaultKey()
	if len(rk) != ed25519.PublicKeySize {
		t.Fatalf("ключ релизов: %d байт", len(rk))
	}
	if bytes.Equal(rk, sk) {
		t.Fatal("ключ релизов не должен совпадать с ключом списка источников")
	}
}
