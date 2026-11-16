package killswitch

import (
	"strings"
	"testing"
)

func TestRulesetStructure(t *testing.T) {
	txt, err := Ruleset(Config{Interface: "vpnclient0", AllowLAN: true})
	if err != nil {
		t.Fatal(err)
	}
	// атомарная замена: add → delete → create, в таком порядке
	i1, i2, i3 := strings.Index(txt, "add table inet brod_ks"), strings.Index(txt, "delete table inet brod_ks"), strings.Index(txt, "table inet brod_ks {")
	if !(i1 == 0 && i1 < i2 && i2 < i3) {
		t.Fatalf("порядок add/delete/create нарушен:\n%s", txt)
	}
	for _, want := range []string{
		`oifname "lo" accept`,
		`meta mark 0x62726f64 counter accept`,
		`oifname "vpnclient0" accept`,
		`ip daddr { 10.0.0.0/8`,
		`ip6 daddr { fc00::/7`,
		`udp sport 68 udp dport 67 accept`,
		`counter drop comment "killswitch"`,
		`hook output`, `hook forward`,
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("нет %q:\n%s", want, txt)
		}
	}
	// drop — последнее правило каждой цепочки output и forward: после него ничего не должно идти
	out := txt[strings.Index(txt, "chain output"):strings.Index(txt, "chain forward")]
	if !strings.Contains(out[strings.LastIndex(out, "\n\t\t"):], "drop") {
		t.Errorf("drop не последний в output:\n%s", out)
	}
	// «разрешить всё своё» идёт раньше drop
	if strings.Index(txt, `oifname "lo" accept`) > strings.Index(txt, "counter drop") {
		t.Error("lo-accept после drop")
	}
	// ни одной команды, которая сбрасывает весь набор правил системы
	if strings.Contains(txt, "flush ruleset") {
		t.Error("flush ruleset недопустим")
	}
}

func TestRulesetNoLAN(t *testing.T) {
	txt, err := Ruleset(Config{Interface: "tun0", Mark: 0x1234, AllowLAN: false})
	if err != nil {
		t.Fatal(err)
	}
	out := txt[strings.Index(txt, "chain output"):strings.Index(txt, "chain forward")]
	if strings.Contains(out, "daddr") {
		t.Errorf("при AllowLAN=false в output нет правил по адресам:\n%s", out)
	}
	if !strings.Contains(txt, "meta mark 0x1234 ") {
		t.Errorf("своя метка не подставилась:\n%s", txt)
	}
	if !strings.Contains(txt[strings.Index(txt, "chain forward"):], "daddr") {
		t.Error("транзит между локальными сетями остаётся разрешённым")
	}
}

func TestRulesetRejectsBadInterface(t *testing.T) {
	for _, name := range []string{"", "a b", `x"; flush ruleset; "`, "очень-длинное-имя-интерфейса", "abcdefghijklmnop", "a\nb", "a;b"} {
		if _, err := Ruleset(Config{Interface: name}); err == nil {
			t.Errorf("имя %q принято", name)
		}
	}
	for _, name := range []string{"vpnclient0", "tun0", "wg-0", "a.b_c"} {
		if _, err := Ruleset(Config{Interface: name}); err != nil {
			t.Errorf("имя %q отклонено: %v", name, err)
		}
	}
}

func TestMarkerLifecycle(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if Active() {
		t.Fatal("признак есть до установки")
	}
	writeMarker(Config{Interface: "tun0"})
	if !Active() {
		t.Fatal("признак не записан")
	}
	removeMarker()
	if Active() {
		t.Fatal("признак не снят")
	}
}

func TestNoTableIsNotAnError(t *testing.T) {
	for _, s := range []string{"nft delete table inet brod_ks: exit status 1: Error: No such file or directory; did you mean table", "Error: table does not exist"} {
		if !isNoTable(errString(s)) {
			t.Errorf("не распознано как «таблицы нет»: %s", s)
		}
	}
	if isNoTable(errString("Operation not permitted")) {
		t.Error("отказ в правах не должен считаться «таблицы нет»")
	}
}

// DNS наружу (53, 853) блокируется ДО исключения «локальная сеть», но после lo, метки движка и TUN.
func TestRulesetBlocksDNSBeforeLAN(t *testing.T) {
	txt, err := Ruleset(Config{Interface: "vpnclient0", AllowLAN: true})
	if err != nil {
		t.Fatal(err)
	}
	const dns = `meta l4proto { tcp, udp } th dport { 53, 853 }`
	chains := map[string]string{
		"output":  txt[strings.Index(txt, "chain output"):strings.Index(txt, "chain forward")],
		"forward": txt[strings.Index(txt, "chain forward"):],
	}
	for name, ch := range chains {
		iDNS := strings.Index(ch, dns)
		if iDNS < 0 {
			t.Fatalf("%s: нет правила блокировки DNS:\n%s", name, ch)
		}
		line := ch[iDNS:]
		line = line[:strings.Index(line, "\n")]
		if !strings.Contains(line, " drop") {
			t.Errorf("%s: правило DNS не drop: %s", name, line)
		}
		if strings.Contains(line, "oifname") || strings.Contains(line, "mark") {
			t.Errorf("%s: блок DNS не должен зависеть от интерфейса и метки: %s", name, line)
		}
		for _, lan := range []string{"ip daddr {", "ip6 daddr {"} {
			if i := strings.Index(ch, lan); i < 0 || iDNS > i {
				t.Errorf("%s: блок DNS должен стоять раньше исключения %q:\n%s", name, lan, ch)
			}
		}
		// DNS через TUN (и через lo и метку движка в output) остаётся: эти accept стоят раньше блока
		early := []string{`oifname "vpnclient0" accept`}
		if name == "output" {
			early = append(early, `oifname "lo" accept`, `meta mark 0x62726f64 counter accept`)
		}
		for _, e := range early {
			if i := strings.Index(ch, e); i < 0 || i > iDNS {
				t.Errorf("%s: %q должен стоять раньше блока DNS", name, e)
			}
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
