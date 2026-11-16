package killswitch

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// T-8: синтаксис набора правил проверяет сам nft (`nft -c`, ничего не ставит), а не сравнение строк. `nft -c` всё равно обращается к ядру,
// поэтому без root нужен либо CAP_NET_ADMIN, либо пользовательское пространство имён (`unshare -rn`). Нет ни того ни другого:
// t.Skip с причиной; при BROD_REQUIRE_NFT=1 это ошибка (отдельный шаг ci.yml запускает их под sudo с установленным nftables).

// nftCheck возвращает (текст ошибки nft, "") если набор отвергнут, ("", "") если принят, ("", причина) если проверить нечем.
func nftCheck(text string) (rejected, unavailable string) {
	bin, err := exec.LookPath("nft")
	if err != nil {
		for _, p := range []string{"/usr/sbin/nft", "/sbin/nft"} {
			if _, e := os.Stat(p); e == nil {
				bin, err = p, nil
				break
			}
		}
	}
	if err != nil {
		return "", "команда nft не найдена (пакет nftables)"
	}
	attempts := [][]string{{bin, "-c", "-f", "-"}}
	if un, e := exec.LookPath("unshare"); e == nil {
		attempts = append(attempts, []string{un, "-rn", bin, "-c", "-f", "-"})
	}
	var last string
	for _, a := range attempts {
		cmd := exec.Command(a[0], a[1:]...)
		cmd.Stdin = strings.NewReader(text)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err == nil {
			return "", ""
		}
		msg := strings.TrimSpace(out.String())
		if strings.Contains(msg, "Operation not permitted") || strings.Contains(msg, "Permission denied") ||
			strings.Contains(msg, "Operation not supported") || strings.Contains(msg, "unshare failed") {
			last = msg
			continue // нет прав на проверку этим способом: пробуем следующий
		}
		return msg, ""
	}
	return "", "nft -c недоступен без root и пространства имён: " + last
}

func skipNoNft(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("BROD_REQUIRE_NFT") == "1" {
		t.Fatalf("при BROD_REQUIRE_NFT=1 проверка nft -c обязательна: %s", why)
	}
	t.Skip(why)
}

func TestRulesetPassesNftCheck(t *testing.T) {
	for name, c := range map[string]Config{
		"lan":        {Interface: "vpnclient0", AllowLAN: true},
		"без lan":    {Interface: "tun0", AllowLAN: false},
		"своя метка": {Interface: "brod-tun.1", Mark: 0x1234, AllowLAN: true},
	} {
		t.Run(name, func(t *testing.T) {
			text, err := Ruleset(c)
			if err != nil {
				t.Fatal(err)
			}
			rej, why := nftCheck(text)
			if why != "" {
				skipNoNft(t, why)
			}
			if rej != "" {
				t.Fatalf("nft -c отверг набор правил:\n%s\n--- набор ---\n%s", rej, text)
			}
		})
	}
}

// Контроль самой проверки: заведомо битый набор обязан быть отвергнут, иначе тест выше ничего не доказывает.
func TestNftCheckRejectsBrokenRuleset(t *testing.T) {
	text, err := Ruleset(Config{Interface: "tun0", AllowLAN: true})
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(text, "counter drop", "counter dropp", 1)
	if bad == text {
		t.Fatal("в наборе нет «counter drop»: контроль устарел")
	}
	rej, why := nftCheck(bad)
	if why != "" {
		skipNoNft(t, why)
	}
	if rej == "" {
		t.Fatal("nft -c принял заведомо битый набор: проверка ничего не доказывает")
	}
}
