package supervisor

import (
	"net"
	"sort"
	"strings"
)

// foreignTunnels возвращает имена поднятых туннельных адаптеров, кроме нашего. Два туннеля с маршрутом «весь трафик» гасят сеть
// (Throne рядом с Brod): без этой проверки автоподключение при старте службы включало VPN поверх чужого туннеля.
func foreignTunnels(ours string) []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, i := range ifs {
		if strings.EqualFold(i.Name, ours) || i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		if isWinTunName(i.Name) {
			out = append(out, i.Name)
		}
	}
	sort.Strings(out)
	return out
}
