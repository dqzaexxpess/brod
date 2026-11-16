package core

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Android: «Поделиться журналом» отдаёт хвост файла, прошедший ту же очистку, что и GET /v1/logs: без имён сайтов, адресов
// и паролей, без оборванной первой строки, не больше maxBytes.
func TestSanitizedLogTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engine.log")
	var b strings.Builder
	b.WriteString("OLD-" + strings.Repeat("x", 5000) + " secret-old.example.org\n")
	b.WriteString("ERROR dns: lookup failed for secret-site.example.org: timeout\n")
	b.WriteString("ERROR open connection to node.vpn.example:8443 using outbound/vless[a]\n")
	b.WriteString("INFO dial 203.0.113.9:443 password=hunter2\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SanitizedLogTail(path, 400)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret-site", "node.vpn", "8443", "203.0.113", "hunter2", "OLD-", "xxxx"} {
		if strings.Contains(got, leak) {
			t.Errorf("утечка или лишнее %q в %q", leak, got)
		}
	}
	if !strings.Contains(got, "lookup <dom>") || len(got) > 400 {
		t.Errorf("ожидался очищенный хвост до 400 байт: %q", got)
	}
	if _, err := SanitizedLogTail(filepath.Join(dir, "нет.log"), 400); !os.IsNotExist(err) {
		t.Errorf("отсутствующий файл должен давать os.IsNotExist, а не %v", err)
	}
}

// Logs читает только хвост файла: результат совпадает с наивным проходом по всему файлу при любых размерах, границах блоков,
// длинных строках, сериях шума и с завершающим переводом строки или без него.
func TestLogsTailMatchesFullScan(t *testing.T) {
	for seed := int64(1); seed <= 30; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var all []string
		total := 0
		target := []int{0, 500, 100_000, 300_000, 1_500_000}[rng.Intn(5)]
		for total < target {
			var line string
			switch rng.Intn(6) {
			case 0:
				line = "ERROR probe: " + logNoise + " by protocol"
			case 1:
				line = strings.Repeat("длинная строка ", 1+rng.Intn(4000)/15)
			case 2:
				line = ""
			default:
				line = fmt.Sprintf("INFO строка-%d", rng.Intn(1_000_000))
			}
			all = append(all, line)
			total += len(line) + 1
		}
		text := strings.Join(all, "\n")
		if len(all) > 0 && rng.Intn(4) != 0 {
			text += "\n"
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "engine.log"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		c := &Core{}
		c.cfg.StateDir = dir
		n := []int{1, 5, 50, 200, 2000}[rng.Intn(5)]
		got, err := c.Logs(n)
		if err != nil {
			t.Fatal(err)
		}
		// эталон: весь файл, шум отдельно, последние n непустых по смыслу строк
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		if text == "" {
			lines = nil
		}
		var kept []string
		for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
			if strings.Contains(lines[i], logNoise) {
				continue
			}
			kept = append([]string{lines[i]}, kept...)
		}
		if len(got) < len(kept) || strings.Join(got[:len(kept)], "\n") != strings.Join(kept, "\n") {
			t.Fatalf("seed %d, n=%d, файл %d байт: хвост не совпал\nполучено %d строк, нужно %d\n", seed, n, len(text), len(got), len(kept))
		}
		if extra := got[len(kept):]; len(extra) > 1 || (len(extra) == 1 && !strings.Contains(extra[0], "скрыто однотипных строк шума")) {
			t.Fatalf("seed %d: лишние строки в конце: %q", seed, extra)
		}
	}
}

// Время Logs не зависит от размера журнала: 40 МБ шума с несколькими нужными строками в конце.
func TestLogsReadsOnlyTailOfHugeFile(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "engine.log"))
	if err != nil {
		t.Fatal(err)
	}
	row := []byte("INFO заполнитель журнала движка заполнитель журнала движка заполнитель журнала движка\n")
	for i := 0; i < 40<<20/len(row); i++ {
		f.Write(row)
	}
	fmt.Fprintln(f, "INFO последняя-1")
	fmt.Fprintln(f, "INFO последняя-2")
	f.Close()
	c := &Core{}
	c.cfg.StateDir = dir
	got, err := c.Logs(2)
	if err != nil || len(got) != 2 || got[0] != "INFO последняя-1" || got[1] != "INFO последняя-2" {
		t.Fatalf("%q, %v", got, err)
	}
}
