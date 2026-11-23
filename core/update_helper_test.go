package core

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"testing"
)

// подписанный манифест выпуска сохраняется рядом со скачиванием байт в байт (его проверяет помощник от root), задание помощнику несёт
// пути архива, манифеста и подписи; после «обновлений нет» файлы убираются вместе со скачанным.
func TestUpdateManifestSavedForHelper(t *testing.T) {
	r := newUpdRig(t, "0.3.0")
	data := []byte("release-bytes")
	doc, sig := r.manifest(3, "0.4.0", data)
	if _, err := r.c.applyUpdateManifest(doc, sig, []ed25519.PublicKey{r.pub}, "test"); err != nil {
		t.Fatal(err)
	}
	dp, sp := r.c.releaseManifestPaths()
	if b, err := os.ReadFile(dp); err != nil || !bytes.Equal(b, doc) {
		t.Fatalf("манифест: %v", err)
	}
	if b, err := os.ReadFile(sp); err != nil || !bytes.Equal(b, sig) {
		t.Fatalf("подпись: %v", err)
	}
	if st, err := os.Stat(dp); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("права манифеста: %v", err)
	}
	// обновлений больше нет: файлы убираются
	if _, err := r.apply(4, "0.3.0", data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dp); err == nil {
		t.Fatal("манифест остался после «обновлений нет»")
	}
}

func TestInstallUpdatePassesReleaseFilesToHelper(t *testing.T) {
	r := newInstRig(t, "linux-amd64", linuxReleaseFiles("0.4.0"))
	if _, err := r.c.InstallUpdate("test", 0); err != nil {
		t.Fatal(err)
	}
	s := <-r.specs
	dp, sp := r.c.releaseManifestPaths()
	if s.Archive != r.archive || s.Manifest != dp || s.Sig != sp {
		t.Fatalf("задание помощнику: архив %q, манифест %q, подпись %q", s.Archive, s.Manifest, s.Sig)
	}
}
