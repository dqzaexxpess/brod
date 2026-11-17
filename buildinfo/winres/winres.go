// Package winres строит ресурсы Windows-файлов демона: VERSIONINFO (имя, описание, версия в свойствах файла) и значок. Результат —
// COFF-объект .syso рядом с main-пакетом: go build подхватывает его сам (имя с суффиксом _windows_amd64 ограничивает его этой
// платформой), отдельный шаг в сборке не нужен. Пересборка: go run ./buildinfo/winres/gen (из корня репозитория); тест следит, чтобы
// закоммиченные файлы совпадали с тем, что строится сейчас.
package winres

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"brod/buildinfo"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

// Binary — исполняемый файл с ресурсами.
type Binary struct {
	Name        string // имя файла без .exe
	Description string // FileDescription
	Dir         string // каталог main-пакета от корня репозитория
	Original    string // OriginalFilename; пусто — Name + ".exe"
	Manifest    bool   // добавить манифест приложения (установщик: права как у запустившего, Windows 10/11, длинные пути)
}

// Binaries — файлы, которым нужны ресурсы (загрузчик установщика и демон с CLI) (Brod.exe получает их из Flutter, app/windows/runner/Runner.rc).
var Binaries = []Binary{
	{Name: "brodd", Description: "Brod daemon", Dir: "cmd/brodd"},
	{Name: "brodctl", Description: "Brod command-line control", Dir: "cmd/brodctl"},
	{Name: "brodsetup", Description: "Brod installer", Dir: "cmd/brodsetup", Original: "setup.exe", Manifest: true},
}

// IconPath — значок приложения (тот же, что у Brod.exe), от корня репозитория.
const IconPath = "app/windows/runner/resources/app_icon.ico"

// ObjectName — имя .syso в каталоге main-пакета.
const ObjectName = "rsrc_windows_amd64.syso"

// Build возвращает COFF-объект (amd64) с ресурсами файла. root — корень репозитория.
func Build(root string, b Binary) ([]byte, error) {
	rs := winres.ResourceSet{}
	f, err := os.Open(filepath.Join(root, IconPath))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	icon, err := winres.LoadICO(f)
	if err != nil {
		return nil, fmt.Errorf("значок: %w", err)
	}
	if err := rs.SetIcon(winres.ID(1), icon); err != nil {
		return nil, err
	}

	vi := version.Info{} // без отметки времени: объект воспроизводим байт в байт
	vi.SetFileVersion(buildinfo.Version)
	vi.SetProductVersion(buildinfo.Version)
	for k, v := range map[string]string{
		version.ProductName:      "Brod",
		version.FileDescription:  b.Description,
		version.InternalName:     b.Name,
		version.OriginalFilename: firstNonEmpty(b.Original, b.Name+".exe"),
		version.CompanyName:      "Brod",
		version.LegalCopyright:   "Copyright (C) 2026 Brod. GPL-3.0",
	} {
		if err := vi.Set(version.LangDefault, k, v); err != nil {
			return nil, err
		}
	}
	rs.SetVersionInfo(vi)
	if b.Manifest {
		rs.SetManifest(winres.AppManifest{
			Description:    b.Description,
			Compatibility:  winres.Win10AndAbove,
			ExecutionLevel: winres.AsInvoker,
			DPIAwareness:   winres.DPIPerMonitorV2,
			LongPathAware:  true,
		})
	}

	var out bytes.Buffer
	if err := rs.WriteObject(&out, winres.ArchAMD64); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
