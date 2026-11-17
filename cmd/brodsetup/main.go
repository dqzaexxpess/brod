// Команда brodsetup — загрузчик установщика Brod для Windows (setup.exe). Несёт архив сборки (payload.zip), распаковывает его во временную
// папку и запускает оттуда `Brod.exe --setup` (окно установки в оформлении Brod). Права администратора окно не получает: привилегированную работу
// (install.ps1) делает само окно отдельным запросом UAC, поэтому приложение после установки запускается без повышения прав.
//
// Тихая установка (без окон; код возврата 0 — успех): setup.exe /S [/USER=ДОМЕН\Имя] [/D=каталог]. Нужны права администратора: если их нет,
// загрузчик перезапускает себя с запросом UAC. Прежние ключи установщика Inno Setup (/VERYSILENT, /SILENT, /SUPPRESSMSGBOXES) принимаются.
// Файл payload.zip в репозитории пуст: настоящий архив подкладывает сборка (CI, deploy/windows/build-setup.sh) перед компиляцией.
package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed payload.zip
var payload []byte

// Коды возврата тихой установки.
const (
	exitOK        = 0
	exitInstall   = 1    // install.ps1 завершился с ошибкой
	exitUsage     = 2    // неверные аргументы
	exitPayload   = 3    // в загрузчике нет архива или он повреждён
	exitElevation = 1223 // пользователь отказал в повышении прав
)

type options struct {
	silent bool
	user   string
	dir    string
}

func parseArgs(args []string) options {
	var o options
	for _, a := range args {
		up := strings.ToUpper(a)
		switch {
		case up == "/S", up == "/SILENT", up == "/VERYSILENT", up == "-S", up == "--silent":
			o.silent = true
		case strings.HasPrefix(up, "/USER="):
			o.user = a[len("/USER="):]
		case strings.HasPrefix(up, "/D="):
			o.dir = a[len("/D="):]
		}
	}
	return o
}

// extract распаковывает архив в dest; пути вне dest (абсолютные и с «..») отвергаются.
func extract(data []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		// Compress-Archive в Windows PowerShell 5.1 пишет имена с обратной косой чертой («data\\flutter_assets\\»): приводим к прямой, иначе
		// запись каталога принимается за файл, а вложенный путь создаётся раньше родительского
		name := strings.ReplaceAll(f.Name, `\`, "/")
		target := filepath.Join(root, filepath.FromSlash(name))
		if rel, err := filepath.Rel(root, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
			return fmt.Errorf("недопустимый путь в архиве: %q", f.Name)
		}
		if strings.HasSuffix(name, "/") || f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ссылка в архиве: %q", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeEntry(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

var errNoPayload = errors.New("установщик собран без архива программы")

func unpack() (string, error) {
	if len(payload) == 0 {
		return "", errNoPayload
	}
	dir, err := os.MkdirTemp("", "brod-setup-")
	if err != nil {
		return "", err
	}
	if err := extract(payload, dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// defaultUser — учётная запись, которой выдаётся доступ к службе, если /USER= не указан: ДОМЕН\Имя из окружения.
func defaultUser(env func(string) string) string {
	u := env("USERNAME")
	if u == "" {
		return ""
	}
	if d := env("USERDOMAIN"); d != "" {
		return d + `\` + u
	}
	return u
}
