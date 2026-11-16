package killswitch

// Набор фильтров WFP (Windows Filtering Platform) — киллсвитч Windows. Здесь только описание набора, не зависящее от платформы (его же
// печатает сухой прогон и проверяют тесты); установка — wfp_windows.go.
//
// Схема та же, что у nftables на Linux, в терминах WFP. Все фильтры — в своём подуровне, на слоях ALE (установка соединений: исходящие
// AUTH_CONNECT и входящие AUTH_RECV_ACCEPT) и IPFORWARD (транзит: WSL, Hyper-V, общий доступ к подключению):
//
//	движок (brodd.exe по app-id, «жёсткое» разрешение) → permit;  loopback → permit;  интерфейс TUN → permit;
//	DHCP, NDP → permit;  локальная сеть (можно отключить) → permit;  всё остальное → block.
//
// Движок разрешается по пути исполняемого файла: его соединения к узлам привязаны к физическому интерфейсу и по определению идут мимо TUN
// (аналог метки сокетов на Linux). Транзит наружу разрешён только через TUN и в локальную сеть.

import (
	"fmt"
	"net/netip"
	"strings"
)

// WFPLayer — слой WFP.
type WFPLayer int

const (
	LayerConnect4 WFPLayer = iota // FWPM_LAYER_ALE_AUTH_CONNECT_V4
	LayerRecv4                    // FWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4
	LayerConnect6                 // FWPM_LAYER_ALE_AUTH_CONNECT_V6
	LayerRecv6                    // FWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6
	LayerForward4                 // FWPM_LAYER_IPFORWARD_V4
	LayerForward6                 // FWPM_LAYER_IPFORWARD_V6
)

var layerNames = [...]string{"connect4", "recv4", "connect6", "recv6", "forward4", "forward6"}

func (l WFPLayer) String() string { return layerNames[l] }

// IPv6 сообщает, что слой для IPv6.
func (l WFPLayer) IPv6() bool { return l == LayerConnect6 || l == LayerRecv6 || l == LayerForward6 }

// Forward сообщает, что слой транзитный (там другие поля условий: адрес назначения и интерфейс пересылки).
func (l WFPLayer) Forward() bool { return l == LayerForward4 || l == LayerForward6 }

// WFPCondKind — поле условия фильтра.
type WFPCondKind int

const (
	CondAppID        WFPCondKind = iota // FWPM_CONDITION_ALE_APP_ID: Path
	CondLoopback                        // FWPM_CONDITION_FLAGS & FWP_CONDITION_FLAG_IS_LOOPBACK
	CondLocalIface                      // FWPM_CONDITION_IP_LOCAL_INTERFACE: LUID в Num
	CondForwardIface                    // FWPM_CONDITION_IP_FORWARD_INTERFACE: LUID в Num
	CondProto                           // FWPM_CONDITION_IP_PROTOCOL: Num
	CondLocalPort                       // FWPM_CONDITION_IP_LOCAL_PORT: Num (для ICMP — тип)
	CondRemotePort                      // FWPM_CONDITION_IP_REMOTE_PORT: Num (для ICMP — код)
	CondRemoteAddr                      // FWPM_CONDITION_IP_REMOTE_ADDRESS: Prefix
	CondLocalAddr                       // FWPM_CONDITION_IP_LOCAL_ADDRESS: Prefix
	CondDestAddr                        // FWPM_CONDITION_IP_DESTINATION_ADDRESS (транзит): Prefix
)

// WFPCond — условие фильтра. Условия с одинаковым полем WFP объединяет по ИЛИ, с разными — по И.
type WFPCond struct {
	Kind   WFPCondKind
	Num    uint64
	Prefix netip.Prefix
	Path   string
}

func (c WFPCond) String() string {
	switch c.Kind {
	case CondAppID:
		return "app=" + c.Path
	case CondLoopback:
		return "loopback"
	case CondLocalIface:
		return fmt.Sprintf("iface=%#x", c.Num)
	case CondForwardIface:
		return fmt.Sprintf("fwd-iface=%#x", c.Num)
	case CondProto:
		return fmt.Sprintf("proto=%d", c.Num)
	case CondLocalPort:
		return fmt.Sprintf("lport=%d", c.Num)
	case CondRemotePort:
		return fmt.Sprintf("rport=%d", c.Num)
	case CondRemoteAddr:
		return "raddr=" + c.Prefix.String()
	case CondLocalAddr:
		return "laddr=" + c.Prefix.String()
	case CondDestAddr:
		return "daddr=" + c.Prefix.String()
	}
	return "?"
}

// WFPRule — один фильтр.
type WFPRule struct {
	Name   string
	Layer  WFPLayer
	Conds  []WFPCond
	Permit bool
	Hard   bool  // FWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT: блок из подуровня с меньшим весом это разрешение не отменит
	Weight uint8 // больший вес проверяется раньше
}

func (r WFPRule) String() string {
	act := "block"
	if r.Permit {
		act = "permit"
		if r.Hard {
			act = "permit!"
		}
	}
	cs := make([]string, len(r.Conds))
	for i, c := range r.Conds {
		cs[i] = c.String()
	}
	return fmt.Sprintf("%-8s w=%-2d %-7s %-24s %s", r.Layer, r.Weight, act, r.Name, strings.Join(cs, " "))
}

// WFPConfig — параметры набора.
type WFPConfig struct {
	AppPath  string // исполняемый файл движка (brodd.exe)
	TunLUID  uint64 // LUID интерфейса TUN; 0 — адаптера ещё нет (набор ставится до запуска ядра), разрешения TUN не будет
	AllowLAN bool
}

// Веса фильтров внутри подуровня.
const (
	weightApp      = 15
	weightLoopback = 14
	weightTun      = 13
	weightDNSBlock = 12 // выше разрешений DHCP/NDP/«локальная сеть», ниже движка, loopback и TUN
	weightPermit   = 11
	weightBlock    = 0
)

var (
	wfpLAN4 = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("255.255.255.255/32"),
	}
	wfpLAN6 = []netip.Prefix{netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8")}

	linkLocal6   = netip.MustParsePrefix("fe80::/10")
	dhcp6Servers = []netip.Prefix{netip.MustParsePrefix("ff02::1:2/128"), netip.MustParsePrefix("ff05::1:3/128")}
	allRouters6  = netip.MustParsePrefix("ff02::2/128")
	broadcast4   = netip.MustParsePrefix("255.255.255.255/32")
)

const (
	protoTCP    = 6
	protoUDP    = 17
	protoICMPv6 = 58
)

// WFPRules строит набор фильтров. Порядок не важен (решают веса), но стабилен: по нему же нумеруются ключи фильтров.
func WFPRules(c WFPConfig) ([]WFPRule, error) {
	if c.AppPath == "" {
		return nil, fmt.Errorf("не задан путь к исполняемому файлу движка")
	}
	var rs []WFPRule
	add := func(name string, permit bool, weight uint8, conds []WFPCond, layers ...WFPLayer) {
		for _, l := range layers {
			rs = append(rs, WFPRule{Name: name, Layer: l, Conds: conds, Permit: permit, Weight: weight})
		}
	}
	ale := []WFPLayer{LayerConnect4, LayerRecv4, LayerConnect6, LayerRecv6}

	// движок: соединения к узлам, WARP, проверки, обновления
	add("engine", true, weightApp, []WFPCond{{Kind: CondAppID, Path: c.AppPath}}, ale...)
	for i := range rs {
		rs[i].Hard = true
	}
	add("loopback", true, weightLoopback, []WFPCond{{Kind: CondLoopback}}, ale...)
	if c.TunLUID != 0 {
		add("tun", true, weightTun, []WFPCond{{Kind: CondLocalIface, Num: c.TunLUID}}, ale...)
		add("tun-forward", true, weightTun, []WFPCond{{Kind: CondForwardIface, Num: c.TunLUID}}, LayerForward4, LayerForward6)
	}

	// DNS (TCP и UDP, порты 53 и 853) наружу мимо движка и туннеля блокируется раньше разрешения «локальная сеть»: после падения
	// движка DNS-клиент системы идёт к роутеру с частным адресом, и без этого запрос ушёл бы провайдеру. Слои IPFORWARD полей порта и
	// протокола не имеют (фильтр с такими условиями WFP отвергнет), поэтому блок стоит на connect4/connect6; транзит по порту отсечь
	// нельзя, он остаётся разрешённым в локальную сеть (WSL, Hyper-V, общий доступ).
	add("dns-block", false, weightDNSBlock, []WFPCond{{Kind: CondProto, Num: protoTCP}, {Kind: CondProto, Num: protoUDP},
		{Kind: CondRemotePort, Num: 53}, {Kind: CondRemotePort, Num: 853}}, LayerConnect4, LayerConnect6)

	// DHCPv4: запрос на широковещательный адрес, ответ с порта 67
	add("dhcp4-request", true, weightPermit, []WFPCond{{Kind: CondProto, Num: protoUDP}, {Kind: CondLocalPort, Num: 68},
		{Kind: CondRemotePort, Num: 67}, {Kind: CondRemoteAddr, Prefix: broadcast4}}, LayerConnect4)
	add("dhcp4-response", true, weightPermit, []WFPCond{{Kind: CondProto, Num: protoUDP}, {Kind: CondLocalPort, Num: 68},
		{Kind: CondRemotePort, Num: 67}}, LayerRecv4)
	// DHCPv6: только с link-local адреса
	req6 := []WFPCond{{Kind: CondProto, Num: protoUDP}}
	for _, p := range dhcp6Servers {
		req6 = append(req6, WFPCond{Kind: CondRemoteAddr, Prefix: p})
	}
	req6 = append(req6, WFPCond{Kind: CondRemotePort, Num: 547}, WFPCond{Kind: CondLocalAddr, Prefix: linkLocal6}, WFPCond{Kind: CondLocalPort, Num: 546})
	add("dhcp6-request", true, weightPermit, req6, LayerConnect6)
	add("dhcp6-response", true, weightPermit, []WFPCond{{Kind: CondProto, Num: protoUDP}, {Kind: CondRemoteAddr, Prefix: linkLocal6},
		{Kind: CondRemotePort, Num: 547}, {Kind: CondLocalAddr, Prefix: linkLocal6}, {Kind: CondLocalPort, Num: 546}}, LayerRecv6)
	// NDP (тип ICMPv6 — в поле локального порта, код — в поле удалённого)
	ndp := func(typ uint64, extra ...WFPCond) []WFPCond {
		return append([]WFPCond{{Kind: CondProto, Num: protoICMPv6}, {Kind: CondLocalPort, Num: typ}, {Kind: CondRemotePort, Num: 0}}, extra...)
	}
	add("ndp-router-solicit", true, weightPermit, ndp(133, WFPCond{Kind: CondRemoteAddr, Prefix: allRouters6}), LayerConnect6)
	add("ndp-router-advert", true, weightPermit, ndp(134, WFPCond{Kind: CondRemoteAddr, Prefix: linkLocal6}), LayerRecv6)
	add("ndp-neighbor-solicit", true, weightPermit, ndp(135), LayerConnect6, LayerRecv6)
	add("ndp-neighbor-advert", true, weightPermit, ndp(136), LayerConnect6, LayerRecv6)
	add("ndp-redirect", true, weightPermit, ndp(137, WFPCond{Kind: CondRemoteAddr, Prefix: linkLocal6}), LayerRecv6)

	if c.AllowLAN {
		lan := func(kind WFPCondKind, ps []netip.Prefix) []WFPCond {
			cs := make([]WFPCond, len(ps))
			for i, p := range ps {
				cs[i] = WFPCond{Kind: kind, Prefix: p}
			}
			return cs
		}
		add("lan", true, weightPermit, lan(CondRemoteAddr, wfpLAN4), LayerConnect4, LayerRecv4)
		add("lan", true, weightPermit, lan(CondRemoteAddr, wfpLAN6), LayerConnect6, LayerRecv6)
		add("lan-forward", true, weightPermit, lan(CondDestAddr, wfpLAN4), LayerForward4)
		add("lan-forward", true, weightPermit, lan(CondDestAddr, wfpLAN6), LayerForward6)
	}

	add("block", false, weightBlock, nil, LayerConnect4, LayerRecv4, LayerConnect6, LayerRecv6, LayerForward4, LayerForward6)
	if len(rs) > wfpMaxFilters {
		return nil, fmt.Errorf("слишком много фильтров: %d (не больше %d)", len(rs), wfpMaxFilters)
	}
	return rs, nil
}

// wfpMaxFilters — сколько ключей фильтров зарезервировано (ключи детерминированные, чтобы снять свой набор и после падения демона).
const wfpMaxFilters = 64

// DescribeWFP — текст набора для сухого прогона.
func DescribeWFP(c WFPConfig) (string, error) {
	rs, err := WFPRules(c)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, r := range rs {
		fmt.Fprintf(&b, "%2d %s\n", i, r)
	}
	return b.String(), nil
}
