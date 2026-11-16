package store

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ownTUNWindows — имя собственного TUN-интерфейса на Windows (core.DefaultTUNInterface). Он в набор туннелей не входит:
// проверки узлов идут мимо него, а его включение и выключение не должно менять ключ истории проверок.
const ownTUNWindows = "Brod"

// netAdapter и defaultRoute — то, что netBase берёт из GetAdaptersAddresses и GetIpForwardTable2 (отдельно, чтобы правила
// выбора проверялись тестом без системы).
type netAdapter struct {
	Index             uint32
	Name, Description string
	Up                bool
	Metric            uint32 // метрика IPv4 интерфейса
}

type defaultRoute struct {
	Index   uint32
	Gateway string
	Metric  uint32 // метрика маршрута; действующая = она + метрика интерфейса
}

// netBase (Windows): интерфейс и шлюз IPv4-маршрута 0.0.0.0/0 с наименьшей действующей метрикой среди работающих нетуннельных
// адаптеров плюс работающие чужие туннели (wintun/sing-tun, TAP, WireGuard и т. п.). Сбой API — «unknown», как на Linux без /proc.
func netBase() string {
	ads, err := adapters()
	if err != nil {
		return "unknown"
	}
	routes, err := defaultRoutes()
	if err != nil {
		return "unknown"
	}
	return netBaseFrom(ads, routes)
}

func netBaseFrom(ads []netAdapter, routes []defaultRoute) string {
	byIndex := map[uint32]netAdapter{}
	var tuns []string
	for _, a := range ads {
		if !a.Up {
			continue
		}
		if isTunnelAdapter(a.Name, a.Description) {
			if a.Name != ownTUNWindows {
				tuns = append(tuns, a.Name)
			}
			continue
		}
		byIndex[a.Index] = a
	}
	base := "no-default-route"
	best := ^uint64(0)
	for _, r := range routes {
		a, ok := byIndex[r.Index]
		if !ok {
			continue
		}
		if m := uint64(r.Metric) + uint64(a.Metric); m < best {
			best, base = m, fmt.Sprintf("%s/%s", a.Name, r.Gateway)
		}
	}
	sort.Strings(tuns)
	if len(tuns) > 0 {
		base += "+" + strings.Join(tuns, ",")
	}
	return base
}

// isTunnelAdapter — туннель VPN по имени или описанию драйвера. Виртуальные сети, не меняющие путь в интернет (Radmin VPN,
// Hyper-V, VirtualBox), и системные псевдоинтерфейсы IPv6 (Teredo, ISATAP, 6to4, IP-HTTPS) сюда не попадают.
func isTunnelAdapter(name, desc string) bool {
	n, d := strings.ToLower(name), strings.ToLower(desc)
	for _, p := range []string{"pseudo-interface", "teredo", "isatap", "6to4", "ip-https"} {
		if strings.Contains(d, p) || strings.Contains(n, p) {
			return false
		}
	}
	if strings.HasPrefix(n, "tun") || strings.HasSuffix(n, "tun") || strings.HasPrefix(n, "wg") {
		return true
	}
	for _, p := range []string{"wintun", "sing-tun", "tap-windows", "wireguard", "openvpn", "tunnel"} {
		if strings.Contains(d, p) {
			return true
		}
	}
	return false
}

func adapters() ([]netAdapter, error) {
	size := uint32(16 << 10)
	var buf []byte
	for range 3 {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil, err
		}
		buf = nil
	}
	if buf == nil {
		return nil, windows.ERROR_BUFFER_OVERFLOW
	}
	var out []netAdapter
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		out = append(out, netAdapter{
			Index: a.IfIndex, Name: windows.UTF16PtrToString(a.FriendlyName), Description: windows.UTF16PtrToString(a.Description),
			Up: a.OperStatus == windows.IfOperStatusUp, Metric: a.Ipv4Metric,
		})
	}
	return out, nil
}

func defaultRoutes() ([]defaultRoute, error) {
	var t *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &t); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(t))
	var out []defaultRoute
	for _, r := range t.Rows() {
		if r.DestinationPrefix.PrefixLength != 0 || r.NextHop.Family != windows.AF_INET {
			continue
		}
		gw := netip.AddrFrom4(*(*[4]byte)(unsafe.Pointer(&r.NextHop.Data[0])))
		out = append(out, defaultRoute{Index: r.InterfaceIndex, Gateway: gw.String(), Metric: r.Metric})
	}
	return out, nil
}
