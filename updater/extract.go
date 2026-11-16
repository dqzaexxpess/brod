// Package updater устанавливает скачанное и проверенное обновление на Linux и Windows: безопасная распаковка архива релиза, проверка
// раскладки файлов и замена каталога программы с откатом. Подпись и SHA-256 архива проверяет ядро по манифесту (пакет ota) до вызова сюда;
// здесь защита от плохого содержимого самого архива (выход за каталог, ссылки, «бомба», не та раскладка).
package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	MaxFiles     = 5000
	MaxTotalSize = 1 << 30 // 1 ГиБ после распаковки: сборка Flutter с движком занимает порядка 300 МБ
	releaseFile  = "BROD-RELEASE"
)

// Extract распаковывает архив (.tar.gz, .tgz или .zip — по имени файла) в пустой каталог dest. Отвергает: абсолютные пути и «..», ссылки
// и устройства, повторы имён, больше MaxFiles файлов или MaxTotalSize байт. Права файлов: исполняемые остаются исполняемыми (0755), прочие 0644.
func Extract(archive, dest string) error {
	ents, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	if len(ents) != 0 {
		return fmt.Errorf("каталог распаковки %s не пуст", dest)
	}
	name := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return extractTarGz(archive, dest)
	case strings.HasSuffix(name, ".zip"):
		return extractZip(archive, dest)
	}
	return fmt.Errorf("неизвестный формат архива: %s", filepath.Base(archive))
}

// cleanName приводит имя из архива к относительному пути без выхода из каталога ("" — корень, пропускается).
func cleanName(n string) (string, error) {
	n = strings.ReplaceAll(n, `\`, "/") // zip с Windows
	if n == "" || strings.ContainsRune(n, 0) || strings.HasPrefix(n, "/") || (len(n) >= 2 && n[1] == ':') {
		return "", fmt.Errorf("недопустимое имя в архиве: %q", n)
	}
	c := path.Clean(n)
	if c == "." {
		return "", nil
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("имя выходит за каталог: %q", n)
	}
	return c, nil
}

type limiter struct {
	files int
	total int64
	seen  map[string]bool
}

func (l *limiter) add(name string, size int64) error {
	l.files++
	l.total += size
	if l.files > MaxFiles {
		return fmt.Errorf("в архиве больше %d файлов", MaxFiles)
	}
	if size < 0 || l.total > MaxTotalSize {
		return fmt.Errorf("архив распаковывается больше чем в %d МБ", MaxTotalSize>>20)
	}
	key := strings.ToLower(name) // регистронезависимо: Windows и macOS склеили бы такие имена
	if l.seen[key] {
		return fmt.Errorf("имя повторяется в архиве: %s", name)
	}
	l.seen[key] = true
	return nil
}

func writeFile(dest, rel string, r io.Reader, exec bool, limit int64) error {
	full := filepath.Join(dest, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%s: размер больше заявленного", rel)
	}
	return os.Chmod(full, mode) // umask мог срезать биты
}

func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	l := &limiter{seen: map[string]bool{}}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := cleanName(h.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(rel)), 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := l.add(rel, h.Size); err != nil {
				return err
			}
			if err := writeFile(dest, rel, tr, h.Mode&0o111 != 0, h.Size); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("%s: тип записи %q не допускается (ссылки и устройства в релизе не нужны)", rel, string(h.Typeflag))
		}
	}
}

func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	l := &limiter{seen: map[string]bool{}}
	for _, zf := range zr.File {
		rel, err := cleanName(zf.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		if zf.Mode()&os.ModeSymlink != 0 || (!zf.Mode().IsRegular() && !zf.FileInfo().IsDir()) {
			return fmt.Errorf("%s: ссылки и устройства в релизе не допускаются", rel)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(rel)), 0o755); err != nil {
				return err
			}
			continue
		}
		if err := l.add(rel, int64(zf.UncompressedSize64)); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = writeFile(dest, rel, rc, zf.Mode()&0o111 != 0 || strings.HasSuffix(strings.ToLower(rel), ".exe"), int64(zf.UncompressedSize64))
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Файлы, без которых релиз платформы нельзя ставить. BROD-RELEASE — одна строка «версия платформа»: распакованное не принимается за другую
// версию или платформу, даже если архив с верным SHA-256 подсунули вместо нужного (манифест подписан, но защита не должна на этом кончаться).
var required = map[string][]string{
	"linux-amd64":   {"brod", "brodd", "brodctl", "data/flutter_assets/AssetManifest.bin", "lib/libflutter_linux_gtk.so"},
	"windows-amd64": {"Brod.exe", "brodd.exe", "brodctl.exe", "wintun.dll", "flutter_windows.dll", "install.ps1", "data/flutter_assets/AssetManifest.bin"},
}

// CheckLayout проверяет распакованный релиз: метка версии и платформы и обязательные файлы.
func CheckLayout(dir, platform, version string) error {
	req, ok := required[platform]
	if !ok {
		return fmt.Errorf("платформа %q не поддерживает установку из приложения", platform)
	}
	b, err := os.ReadFile(filepath.Join(dir, releaseFile))
	if err != nil {
		return fmt.Errorf("в релизе нет %s", releaseFile)
	}
	if got, want := strings.TrimSpace(string(b)), version+" "+platform; got != want {
		return fmt.Errorf("метка релиза %q, ожидалось %q", got, want)
	}
	for _, f := range req {
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return fmt.Errorf("в релизе нет файла %s", f)
		}
	}
	return nil
}

// ReleaseMark — содержимое файла BROD-RELEASE для сборки релиза.
func ReleaseMark(version, platform string) string { return version + " " + platform + "\n" }
