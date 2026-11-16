// Package paths — расположение каталога состояния и сокета демона (общее для brodd и brodctl).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// dirExists подменяется в тестах.
var dirExists = func(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// firstEnv возвращает первое непустое значение: BROD_* сильнее прежних VPNCLIENT_* (они остаются как запасные имена).
func firstEnv(env func(string) string, names ...string) string {
	for _, n := range names {
		if v := env(n); v != "" {
			return v
		}
	}
	return ""
}

// StateDir — каталог состояния: $BROD_STATE (прежнее имя $VPNCLIENT_STATE), иначе на Windows %ProgramData%\Brod (если каталог есть:
// так живёт служба), иначе %LOCALAPPDATA%\vpnclient, иначе $XDG_DATA_HOME/brod, иначе ~/.local/share/brod (прежний …/vpnclient
// переносит MigrateLegacyState).
func StateDir() string { return stateDir(runtime.GOOS, os.Getenv, os.UserHomeDir) }

// Socket — unix-сокет API (на Windows 10+ Go и Dart тоже умеют AF_UNIX): $BROD_SOCK (прежнее имя $VPNCLIENT_SOCK), иначе (не на Windows)
// $XDG_RUNTIME_DIR/brod.sock, иначе в каталоге состояния; на Windows — в подкаталоге run каталога состояния (сам каталог состояния
// доступен только SYSTEM и администраторам, пользователю install.ps1 даёт право записи только на run: сокет и api-access.json).
func Socket() string { return socketPath(runtime.GOOS, os.Getenv, os.UserHomeDir) }

func stateDir(goos string, env func(string) string, home func() (string, error)) string {
	if v := firstEnv(env, "BROD_STATE", "VPNCLIENT_STATE"); v != "" {
		return v
	}
	if goos == "windows" {
		// служба Brod (LocalSystem) хранит состояние в %ProgramData%\Brod: у неё нет профиля пользователя. Каталог создаёт install.ps1,
		// он же переносит туда прежнее состояние; пока его нет, работает прежнее расположение (запуск без службы, разработка)
		if pd := env("ProgramData"); pd != "" {
			if d := filepath.Join(pd, "Brod"); dirExists(d) {
				return d
			}
		}
		if v := env("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "vpnclient")
		}
		if h, err := home(); err == nil {
			return filepath.Join(h, "AppData", "Local", "vpnclient")
		}
		return filepath.Join(os.TempDir(), "vpnclient")
	}
	if v := env("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "brod")
	}
	h, err := home()
	if err != nil {
		return filepath.Join(os.TempDir(), "brod")
	}
	return filepath.Join(h, ".local", "share", "brod")
}

// legacyStateDir — прежний каталог состояния не-Windows (до переименования в Brod).
func legacyStateDir(env func(string) string, home func() (string, error)) string {
	if v := env("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "vpnclient")
	}
	h, err := home()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".local", "share", "vpnclient")
}

// MigrateLegacyState переносит прежний каталог состояния (…/vpnclient) в новый (…/brod) переименованием: узлы, выбор, WARP-аккаунт и
// настройки остаются. Только когда новый каталог ещё не существует и пути не заданы переменными окружения. Windows переносит установщик.
func MigrateLegacyState() (string, error) {
	return migrateLegacyState(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func migrateLegacyState(goos string, env func(string) string, home func() (string, error)) (string, error) {
	if goos == "windows" || firstEnv(env, "BROD_STATE", "VPNCLIENT_STATE") != "" {
		return "", nil
	}
	old, cur := legacyStateDir(env, home), stateDir(goos, env, home)
	if old == "" || old == cur || !dirExists(old) || dirExists(cur) {
		return "", nil
	}
	if err := os.Rename(old, cur); err != nil {
		return "", fmt.Errorf("перенос состояния %s → %s: %w", old, cur, err)
	}
	return fmt.Sprintf("состояние перенесено: %s → %s", old, cur), nil
}

func socketPath(goos string, env func(string) string, home func() (string, error)) string {
	if v := firstEnv(env, "BROD_SOCK", "VPNCLIENT_SOCK"); v != "" {
		return v
	}
	if goos != "windows" {
		if v := env("XDG_RUNTIME_DIR"); v != "" {
			return filepath.Join(v, "brod.sock")
		}
	}
	if goos == "windows" {
		return filepath.Join(stateDir(goos, env, home), WindowsRunSubdir, "brodd.sock")
	}
	return filepath.Join(stateDir(goos, env, home), "brodd.sock")
}

// WindowsRunSubdir — подкаталог состояния на Windows, куда пользователь приложения имеет доступ (сокет API, api-access.json).
// Дублируется в deploy/windows/install.ps1 и app/lib/src/platform_paths.dart.
const WindowsRunSubdir = "run"
