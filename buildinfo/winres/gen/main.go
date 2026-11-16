// Команда gen пересобирает ресурсы Windows-файлов демона (VERSIONINFO и значок) в cmd/*/rsrc_windows_amd64.syso.
// Запуск из корня репозитория: go run ./buildinfo/winres/gen
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"brod/buildinfo/winres"
)

func main() {
	for _, b := range winres.Binaries {
		obj, err := winres.Build(".", b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", b.Name, err)
			os.Exit(1)
		}
		path := filepath.Join(b.Dir, winres.ObjectName)
		if err := os.WriteFile(path, obj, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s (%d байт)\n", path, len(obj))
	}
}
