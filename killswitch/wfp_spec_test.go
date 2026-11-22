package killswitch

import (
	"strings"
	"testing"
)

func wfpRulesT(t *testing.T, c WFPConfig) []WFPRule {
	t.Helper()
	rs, err := WFPRules(c)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestWFPRulesNeedEnginePath(t *testing.T) {
	if _, err := WFPRules(WFPConfig{TunLUID: 1}); err == nil {
		t.Fatal("без пути к движку набор строиться не должен: иначе движок сам себя заблокирует")
	}
}

// Каждое условие — на слое, где WFP это поле знает, и с адресом своего семейства; иначе FwpmFilterAdd0 откажет на живой машине.
func TestWFPRulesConditionsFitLayers(t *testing.T) {
	for _, lan := range []bool{true, false} {
		for _, r := range wfpRulesT(t, WFPConfig{AppPath: `C:\Program Files\Brod\brodd.exe`, TunLUID: 0x123, AllowLAN: lan}) {
			for _, c := range r.Conds {
				fwdOnly := c.Kind == CondForwardIface || c.Kind == CondDestAddr
				if fwdOnly != r.Layer.Forward() {
					t.Errorf("%s: условие %s не для слоя %s", r.Name, c, r.Layer)
				}
				if c.Kind == CondRemoteAddr || c.Kind == CondLocalAddr || c.Kind == CondDestAddr {
					if c.Prefix.Addr().Is6() != r.Layer.IPv6() {
						t.Errorf("%s: адрес %s на слое %s", r.Name, c.Prefix, r.Layer)
					}
				}
			}
		}
	}
}

func TestWFPRulesShape(t *testing.T) {
	rs := wfpRulesT(t, WFPConfig{AppPath: "brodd.exe", TunLUID: 0x123, AllowLAN: true})
	if len(rs) > wfpMaxFilters {
		t.Fatalf("фильтров %d, ключей %d", len(rs), wfpMaxFilters)
	}
	byName := map[string][]WFPRule{}
	for _, r := range rs {
		byName[r.Name] = append(byName[r.Name], r)
	}
	// движок: жёсткое разрешение с наибольшим весом на всех четырёх слоях ALE
	if e := byName["engine"]; len(e) != 4 {
		t.Fatalf("engine: %v", e)
	}
	for _, r := range byName["engine"] {
		if !r.Permit || !r.Hard || r.Weight != weightApp || r.Conds[0].Path != "brodd.exe" {
			t.Errorf("engine: %s", r)
		}
	}
	// блок: на всех шести слоях, с наименьшим весом, без условий; других блоков нет
	blocks := 0
	for _, r := range rs {
		if r.Permit {
			if r.Weight <= weightBlock {
				t.Errorf("разрешение с весом блока: %s", r)
			}
			continue
		}
		if r.Name == "dns-block" { // проверяется в TestWFPRulesBlockDNSAboveLAN
			continue
		}
		blocks++
		if r.Name != "block" || r.Weight != weightBlock || len(r.Conds) != 0 {
			t.Errorf("блок: %s", r)
		}
	}
	if blocks != 6 {
		t.Fatalf("блоков %d, нужно 6 (connect/recv v4/v6 и транзит v4/v6)", blocks)
	}
	if len(byName["tun"]) != 4 || len(byName["tun-forward"]) != 2 || byName["tun"][0].Conds[0].Num != 0x123 {
		t.Fatalf("tun: %v %v", byName["tun"], byName["tun-forward"])
	}
	if len(byName["lan"]) != 4 || len(byName["lan-forward"]) != 2 || len(byName["loopback"]) != 4 {
		t.Fatalf("lan/loopback: %v", rs)
	}
	// транзит пропускается только в TUN и в локальную сеть
	for _, r := range rs {
		if r.Layer.Forward() && r.Permit && r.Name != "tun-forward" && r.Name != "lan-forward" {
			t.Errorf("лишнее разрешение транзита: %s", r)
		}
	}
}

// Пока адаптера TUN нет (набор ставится до запуска ядра), разрешения TUN нет — всё мимо движка блокируется.
func TestWFPRulesWithoutTun(t *testing.T) {
	for _, r := range wfpRulesT(t, WFPConfig{AppPath: "brodd.exe", AllowLAN: true}) {
		if strings.HasPrefix(r.Name, "tun") {
			t.Fatalf("при LUID 0 разрешения TUN быть не должно: %s", r)
		}
	}
}

func TestWFPRulesWithoutLAN(t *testing.T) {
	rs := wfpRulesT(t, WFPConfig{AppPath: "brodd.exe", TunLUID: 1})
	for _, r := range rs {
		if strings.HasPrefix(r.Name, "lan") {
			t.Fatalf("при AllowLAN=false: %s", r)
		}
	}
	// DHCP и NDP остаются: без них адрес и соседи в сети не поддерживаются
	var dhcp, ndp int
	for _, r := range rs {
		if strings.HasPrefix(r.Name, "dhcp") {
			dhcp++
		}
		if strings.HasPrefix(r.Name, "ndp") {
			ndp++
		}
	}
	if dhcp != 4 || ndp != 7 {
		t.Fatalf("dhcp %d, ndp %d", dhcp, ndp)
	}
}

// фильтры блокировки TCP/UDP на порты 53 и 853 весят больше разрешения локальной сети и DHCP, но меньше движка, loopback и TUN.
// Транзитные слои IPFORWARD полей порта и протокола не имеют (FwpmFilterAdd0 такой фильтр отвергнет и сорвёт всю транзакцию), поэтому
// блок по порту стоит только на connect4/connect6; транзит в локальную сеть остаётся (см. комментарий у dns-block в wfp_spec.go).
func TestWFPRulesBlockDNSAboveLAN(t *testing.T) {
	rs := wfpRulesT(t, WFPConfig{AppPath: "brodd.exe", TunLUID: 0x123, AllowLAN: true})
	var dns []WFPRule
	for _, r := range rs {
		if r.Name == "dns-block" {
			dns = append(dns, r)
		}
	}
	if len(dns) != 2 {
		t.Fatalf("dns-block: %v", dns)
	}
	layers := map[WFPLayer]bool{}
	for _, r := range dns {
		layers[r.Layer] = true
		if r.Permit || r.Hard || r.Layer.Forward() {
			t.Errorf("dns-block: %s", r)
		}
		var tcp, udp bool
		ports := map[uint64]bool{}
		for _, c := range r.Conds {
			switch {
			case c.Kind == CondProto && c.Num == protoTCP:
				tcp = true
			case c.Kind == CondProto && c.Num == protoUDP:
				udp = true
			case c.Kind == CondRemotePort:
				ports[c.Num] = true
			default:
				t.Errorf("dns-block: лишнее условие %s", c)
			}
		}
		if !tcp || !udp || len(ports) != 2 || !ports[53] || !ports[853] {
			t.Errorf("dns-block: %s", r)
		}
	}
	if !layers[LayerConnect4] || !layers[LayerConnect6] {
		t.Fatalf("dns-block: слои %v", layers)
	}
	for _, d := range dns {
		for _, r := range rs {
			if r.Layer != d.Layer || r.Name == "dns-block" {
				continue
			}
			switch {
			case r.Name == "engine" || r.Name == "loopback" || r.Name == "tun":
				if r.Weight <= d.Weight {
					t.Errorf("%s (w=%d) должен весить больше dns-block (w=%d) на %s", r.Name, r.Weight, d.Weight, d.Layer)
				}
			case r.Permit:
				if r.Weight >= d.Weight {
					t.Errorf("%s (w=%d) должен весить меньше dns-block (w=%d) на %s", r.Name, r.Weight, d.Weight, d.Layer)
				}
			}
		}
	}
}

func TestDescribeWFP(t *testing.T) {
	out, err := DescribeWFP(WFPConfig{AppPath: "brodd.exe", TunLUID: 1, AllowLAN: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"permit!", "app=brodd.exe", "raddr=192.168.0.0/16", "fwd-iface=0x1", "block"} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q в\n%s", want, out)
		}
	}
}

// Код «объекта нет» узнаётся и со знаковым расширением старших бит (по-видимому, так он приходил на Windows 11 25H2; причина выведена из текста ошибки, на устройстве не проверена): раньше сравнивалось целое значение,
// и «фильтра нет» превращалось в ошибку снятия правил.
func TestWFPNotFoundIgnoresHighBits(t *testing.T) {
	// 64-битные значения собираются через переменные: на 32-битной сборке константа не влезла бы в uintptr
	hi := func(v uint64) uintptr { return uintptr(v) }
	for _, r := range []uintptr{0x80320003, hi(0xFFFFFFFF80320003), 0x80320005, hi(0xFFFFFFFF80320007)} {
		if !wfpNotFound(r) {
			t.Errorf("%#x: «не найдено» не распознано", r)
		}
	}
	for _, r := range []uintptr{0, 0x5, hi(0xFFFFFFFF80320004), 0x80320001} {
		if wfpNotFound(r) {
			t.Errorf("%#x принят за «не найдено»", r)
		}
	}
	if wfpFailed(0) || wfpFailed(hi(0xDEADBEEF00000000)) || !wfpFailed(0x5) || !wfpFailed(hi(0xFFFFFFFF80320003)) {
		t.Error("wfpFailed: успех и ошибка различаются по младшим 32 битам")
	}
}
