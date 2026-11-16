//go:build !windows

package store

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// netBase (Linux/Android): маршрут по умолчанию из /proc/net/route и туннели из /sys/class/net (tun*, tap*, wg*, ppp*, *tun).
func netBase() string {
	base := "no-default-route"
	if f, err := os.Open("/proc/net/route"); err == nil {
		sc := bufio.NewScanner(f)
		sc.Scan() // заголовок
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) >= 3 && fs[1] == "00000000" {
				base = fmt.Sprintf("%s/%s", fs[0], fs[2])
				break
			}
		}
		f.Close()
	} else {
		return "unknown"
	}
	var tuns []string
	if ents, err := os.ReadDir("/sys/class/net"); err == nil {
		for _, e := range ents {
			n := e.Name()
			if n == "lo" || strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "veth") {
				continue
			}
			for _, p := range []string{"tun", "tap", "wg", "ppp", "utun"} {
				if strings.HasPrefix(n, p) || strings.HasSuffix(n, "tun") || strings.HasSuffix(n, "-tun") {
					tuns = append(tuns, n)
					break
				}
			}
		}
	}
	sort.Strings(tuns)
	if len(tuns) > 0 {
		base += "+" + strings.Join(tuns, ",")
	}
	return base
}
