// Package killswitch — файрвольный «киллсвитч»: пока VPN включён, трафик, который не идёт через туннель и не принадлежит самому движку,
// блокируется. Если демон упадёт (SIGKILL, паника), интерфейс TUN исчезнет, а правила останутся в ядре — и трафик не утечёт напрямую,
// а будет заблокирован, пока демон не запустят снова (при старте без VPN правила снимаются) или пока их не снимут вручную:
//
//	brodd --release-killswitch      (или: sudo nft delete table inet brod_ks)
//
// Схема (nftables, таблица inet brod_ks, базовые цепочки output и forward, политика accept, в конце — drop):
//
//	lo → accept;  метка пакета = Mark (сокеты движка: route.default_mark) → accept;  выходной интерфейс = TUN → accept;
//	DNS (tcp/udp, порты 53 и 853) → drop;  локальная сеть (RFC1918, link-local, multicast; можно отключить) → accept;  DHCP → accept;
//	всё остальное → drop.
//
// Метка сокетов движка нужна потому, что его соединения к узлам привязаны к физическому интерфейсу (auto_detect_interface) и по
// определению идут мимо TUN. На Linux sing-tun метки для маршрутизации не использует (только для Android и auto_redirect), так что
// метка на маршруты не влияет.
//
// Правила применяются одной транзакцией (add/delete/create таблицы в одном файле `nft -f`): либо новый набор целиком, либо прежний.
// Снимается только своя таблица по имени, весь набор правил системы никогда не сбрасывается.
package killswitch

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"brod/paths"
)

// Table — имя таблицы nftables.
const Table = "brod_ks"

// DefaultMark — метка сокетов движка («brod» в ASCII).
const DefaultMark uint32 = 0x62726f64

// Config — параметры набора правил.
type Config struct {
	Interface string // имя TUN-интерфейса
	Mark      uint32 // метка сокетов движка; 0 — DefaultMark
	AllowLAN  bool   // пропускать локальную сеть (по умолчанию да: принтеры, NAS, роутер, Docker)
	// RequireInterface — интерфейс уже должен существовать (набор после запуска ядра). Нужно реализациям, которые ссылаются на экземпляр
	// адаптера (WFP); nftables сравнивает имя и это поле не использует.
	RequireInterface bool
}

var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

const (
	lan4 = "10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4, 255.255.255.255"
	lan6 = "fc00::/7, fe80::/10, ff00::/8"

	// DNS наружу (в том числе на шлюз локальной сети) идёт только через lo, метку движка и TUN: правило стоит после их accept и
	// до исключения «локальная сеть». Без него после падения демона имена уходят через роутер провайдеру.
	dnsDrop = `meta l4proto { tcp, udp } th dport { 53, 853 } counter drop comment "dns-leak"`
)

// Ruleset возвращает текст для `nft -f -`. Имя интерфейса проверяется (в правило подставляется как строка).
func Ruleset(c Config) (string, error) {
	if !ifaceRe.MatchString(c.Interface) {
		return "", fmt.Errorf("недопустимое имя интерфейса %q", c.Interface)
	}
	mark := c.Mark
	if mark == 0 {
		mark = DefaultMark
	}
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	// add + delete + create: атомарная замена (если таблицы не было, add её создаёт, чтобы delete не упал)
	w("add table inet %s", Table)
	w("delete table inet %s", Table)
	w("table inet %s {", Table)
	w("\tchain output {")
	w("\t\ttype filter hook output priority 0; policy accept;")
	w("\t\toifname \"lo\" accept")
	w("\t\tmeta mark 0x%x counter accept comment \"engine\"", mark)
	w("\t\toifname \"%s\" accept", c.Interface)
	w("\t\t%s", dnsDrop)
	if c.AllowLAN {
		w("\t\tip daddr { %s } accept", lan4)
		w("\t\tip6 daddr { %s } accept", lan6)
	}
	w("\t\tudp sport 68 udp dport 67 accept")
	w("\t\tudp sport 546 udp dport 547 accept")
	w("\t\tcounter drop comment \"killswitch\"")
	w("\t}")
	// транзит (контейнеры, виртуалки): наружу только через туннель; между локальными сетями — свободно
	w("\tchain forward {")
	w("\t\ttype filter hook forward priority 0; policy accept;")
	w("\t\toifname \"%s\" accept", c.Interface)
	w("\t\t%s", dnsDrop)
	w("\t\tip daddr { %s } accept", lan4)
	w("\t\tip6 daddr { %s } accept", lan6)
	w("\t\tcounter drop comment \"killswitch\"")
	w("\t}")
	w("}")
	return b.String(), nil
}

// Marker — файл-признак «правила киллсвитча стоят»: по нему приложение узнаёт, что при остановленном демоне трафик заблокирован
// намеренно. Лежит в каталоге рантайма пользователя (исчезает при выходе из сессии, как и сами правила — при перезагрузке); без
// XDG_RUNTIME_DIR — в каталоге состояния пользователя (0700), а не в общем /tmp, где чужой пользователь мог бы занять имя заранее
//. На Windows демон — служба, и признак лежит в её каталоге состояния (%ProgramData%\Brod).
func Marker() string {
	return markerPath(runtime.GOOS, os.Getenv, paths.StateDir())
}

func markerPath(goos string, env func(string) string, stateDir string) string {
	if goos == "windows" {
		if d := env("ProgramData"); d != "" {
			return filepath.Join(d, "Brod", "killswitch.active")
		}
	}
	if d := env("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "brod-killswitch.active")
	}
	return filepath.Join(stateDir, "brod-killswitch.active")
}

// legacyMarker — прежнее расположение без XDG_RUNTIME_DIR: общий /tmp. Пусто там, где прежнего расположения не было.
// Нужно на момент обновления: правила, поставленные прежним демоном, могут остаться с признаком в /tmp.
func legacyMarker() string {
	if runtime.GOOS == "windows" || os.Getenv("XDG_RUNTIME_DIR") != "" {
		return ""
	}
	return filepath.Join(os.TempDir(), "brod-killswitch-"+strconv.Itoa(os.Getuid())+".active")
}

// legacyMarkerPresent — прежний признак есть и создан нами (владелец — текущий пользователь, обычный файл): чужой файл с этим именем
// в общем /tmp признаком не считается.
func legacyMarkerPresent() bool {
	p := legacyMarker()
	if p == "" {
		return false
	}
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular() && ownedByCurrentUser(fi)
}

// Active — есть ли признак установленных правил (новый или прежний).
func Active() bool {
	if _, err := os.Stat(Marker()); err == nil {
		return true
	}
	return legacyMarkerPresent()
}

func writeMarker(c Config) {
	body := fmt.Sprintf("interface=%s\npid=%d\nsince=%s\n", c.Interface, os.Getpid(), time.Now().Format(time.RFC3339))
	m := Marker()
	_ = os.MkdirAll(filepath.Dir(m), 0o700)
	_ = os.WriteFile(m, []byte(body), 0o600)
	// признак теперь один: прежний (если остался от старой версии) больше не нужен
	if legacyMarkerPresent() {
		_ = os.Remove(legacyMarker())
	}
}

func removeMarker() {
	_ = os.Remove(Marker())
	if legacyMarkerPresent() {
		_ = os.Remove(legacyMarker())
	}
}

// Nft — реализация поверх команды nft (нужно право CAP_NET_ADMIN: у brodd оно есть после deploy/tun-setup.sh; дочернему процессу nft
// оно передаётся как ambient-право).
type Nft struct {
	Bin string // путь к nft; пусто — поиск в стандартных местах и PATH
}

// Install ставит (или заменяет) правила.
func (n *Nft) Install(c Config) error {
	text, err := Ruleset(c)
	if err != nil {
		return err
	}
	writeMarker(c) // до установки: при частичном сбое признак не должен отсутствовать
	if _, err := n.run(text, "-f", "-"); err != nil {
		removeMarker()
		return fmt.Errorf("не удалось поставить правила: %w", err)
	}
	return nil
}

// Remove снимает свою таблицу; отсутствие таблицы — не ошибка. Если признака установленных правил нет, а nft отказал (нет прав),
// это тоже не ошибка: ставить было нечего (демон без прав стартует без VPN и не должен ругаться при каждом запуске).
func (n *Nft) Remove() error {
	had := Active()
	if _, err := n.run("", "delete", "table", "inet", Table); err != nil {
		if !isNoTable(err) {
			if !had {
				return nil
			}
			return fmt.Errorf("не удалось снять правила: %w", err)
		}
	}
	removeMarker()
	return nil
}

// Release снимает таблицу принудительно и всегда сообщает о неудаче: аварийная команда `brodd --release-killswitch`.
func (n *Nft) Release() error {
	if _, err := n.run("", "delete", "table", "inet", Table); err != nil && !isNoTable(err) {
		return fmt.Errorf("не удалось снять правила: %w", err)
	}
	removeMarker()
	return nil
}

// Status возвращает вывод `nft list table inet brod_ks` (со счётчиками).
func (n *Nft) Status() (string, error) { return n.run("", "list", "table", "inet", Table) }

// Probe проверяет, что nft запускается с нужным правом (ничего не меняя): `nft list tables`.
func (n *Nft) Probe() (string, error) { return n.run("", "list", "tables") }

func isNoTable(err error) bool {
	s := err.Error()
	return strings.Contains(s, "No such file or directory") || strings.Contains(s, "does not exist")
}
