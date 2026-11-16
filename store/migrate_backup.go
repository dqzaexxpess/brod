package store

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// Копия базы перед миграцией схемы. Откат версии программы без неё превращается в «база от более новой версии»: старая сборка
// не стартует, пока базу не удалят. С копией `state.db.bak-v<версия>` откат делается подменой файла. Хранится одна копия: после
// успешной миграции прежние удаляются, при сбое остаются все. База бывает сотни мегабайт, поэтому свободное место проверяется заранее:
// не хватает места — миграции нет и понятная ошибка (демон не стартует, но и базу не ломает), а не тихий отказ от копии.

const backupMargin = 64 << 20 // запас сверх размера базы: файловая система, журнал, запись самой базы после миграции

var backupName = regexp.MustCompile(`\.bak-v[0-9]+(\.tmp)?$`)

// freeBytes — свободное место в каталоге (подменяется в тестах).
var freeBytes = diskFree

// migrateWithBackup — migrate с копией файла базы перед миграцией (только когда версия схемы действительно меняется).
func migrateWithBackup(db *sql.DB, path string) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if path == ":memory:" || v >= len(migrations) {
		return migrate(db) // новая база в памяти или миграций нет (или база от более новой версии: ошибку вернёт migrate)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		return migrate(db) // пустая база: копировать нечего
	}
	bak, err := backupBeforeMigrate(db, path, v)
	if err != nil {
		return err
	}
	if err := migrate(db); err != nil {
		return fmt.Errorf("%w (копия базы прежней версии: %s)", err, bak)
	}
	pruneBackups(path, bak)
	return nil
}

// backupBeforeMigrate копирует файл базы в <path>.bak-v<v> (через временный файл и переименование) и возвращает путь копии.
func backupBeforeMigrate(db *sql.DB, path string, v int) (string, error) {
	// содержимое журнала WAL переносим в основной файл: копируется один файл, и он целостный (соединение единственное, читателей нет)
	var busy, logFrames, moved int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &moved); err != nil {
		return "", fmt.Errorf("копия базы перед миграцией: %w", err)
	}
	if busy != 0 {
		return "", fmt.Errorf("копия базы перед миграцией: база занята другим процессом (возможно, уже запущен второй демон)")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("копия базы перед миграцией: %w", err)
	}
	bak := path + ".bak-v" + strconv.Itoa(v)
	need := uint64(fi.Size()) + backupMargin
	if old, err := os.Stat(bak); err == nil {
		need -= min(uint64(old.Size()), need) // копия той же версии заменяется: её место освободится
	}
	if free, err := freeBytes(filepath.Dir(path)); err == nil && free < need {
		return "", fmt.Errorf("недостаточно места для копии базы перед обновлением схемы (v%d → v%d): нужно около %d МБ, свободно %d МБ. Освободите место и запустите снова (база не тронута); прежняя копия, если есть, лежит рядом: %s.bak-v*", v, len(migrations), need>>20, free>>20, path)
	}
	tmp := bak + ".tmp"
	if err := copyFile(path, tmp); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("копия базы перед миграцией не создана (база не тронута): %w", err)
	}
	if err := os.Rename(tmp, bak); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("копия базы перед миграцией: %w", err)
	}
	return bak, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // в базе ссылки узлов и ключи
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pruneBackups удаляет все копии <path>.bak-v*, кроме keep (вызывается только после успешной миграции).
func pruneBackups(path, keep string) {
	m, _ := filepath.Glob(path + ".bak-v*")
	for _, f := range m {
		if f != keep && backupName.MatchString(f) {
			_ = os.Remove(f)
		}
	}
}
