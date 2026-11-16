package supervisor

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var tunNameRe = regexp.MustCompile(`^(tun|tap|wg|ppp|utun)|tun$`)

// foreignTunnels возвращает имена активных туннельных интерфейсов (TUN/TAP, WireGuard, PPP), кроме нашего. Два туннеля с политикой
// маршрутов «весь трафик» мешают друг другу (так выглядит запущенный параллельно Throne): Brod при этом молча «не подключается».
func foreignTunnels(ours string) []string {
	ents, err := filepath.Glob("/sys/class/net/*")
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range ents {
		name := filepath.Base(p)
		if name == ours || name == "lo" {
			continue
		}
		_, noTunFlags := os.Stat(filepath.Join(p, "tun_flags")) // есть у любого TUN/TAP, как бы он ни назывался (throne-tun, utun…)
		if noTunFlags == nil || tunNameRe.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
