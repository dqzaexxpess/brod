package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SwapDir ставит распакованный каталог stage на место target переименованием: прежний target остаётся рядом как target+".prev" (одна копия,
// более старая удаляется). Оба каталога должны лежать в одной файловой системе. При сбое прежнее возвращается на место.
func SwapDir(stage, target string) error {
	prev := target + ".prev"
	if err := os.RemoveAll(prev); err != nil {
		return err
	}
	if err := os.Rename(target, prev); err != nil {
		return fmt.Errorf("убрать прежнюю версию: %w", err)
	}
	if err := os.Rename(stage, target); err != nil {
		if rerr := os.Rename(prev, target); rerr != nil {
			return fmt.Errorf("поставить новую версию: %v; вернуть прежнюю не удалось (она в %s): %v", err, prev, rerr)
		}
		return fmt.Errorf("поставить новую версию: %w", err)
	}
	return nil
}

// Rollback возвращает прежнюю версию (target+".prev") на место target; текущая уходит в target+".failed".
func Rollback(target string) error {
	prev, failed := target+".prev", target+".failed"
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("прежней версии нет: %w", err)
	}
	_ = os.RemoveAll(failed)
	if err := os.Rename(target, failed); err != nil {
		return err
	}
	if err := os.Rename(prev, target); err != nil {
		_ = os.Rename(failed, target)
		return err
	}
	return nil
}

// Result — итог установки: его пишет помощник, читает ядро (UpdateInfo.Last) уже после перезапуска.
type Result struct {
	At    time.Time `json:"at"`
	From  string    `json:"from"`
	To    string    `json:"to"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

// WriteResult записывает итог (временный файл и переименование).
func WriteResult(file string, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// ReadResult читает итог последней установки (false — файла нет или он не разобрался).
func ReadResult(file string) (Result, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return Result{}, false
	}
	var r Result
	if json.Unmarshal(b, &r) != nil || r.To == "" {
		return Result{}, false
	}
	return r, true
}

// ResultFile — где лежит итог последней установки.
func ResultFile(stateDir string) string {
	return filepath.Join(stateDir, "updates", "last-install.json")
}
