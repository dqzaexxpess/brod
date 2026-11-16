// Команда mobilestat: охват корпуса белыми списками мобильного интернета (без проверок по сети).
//
//	mobilestat -lists work/mobile work/discovered/*
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"brod/mobile"
	"brod/translator"
)

func main() {
	dir := flag.String("lists", "work/mobile", "каталог с whitelist.txt / cidrwhitelist.txt / ipwhitelist.txt")
	exportLinks := flag.String("export-links", "", "файл для экспорта ссылок лучших кандидатов (секреты, права 0600)")
	minScore := flag.Int("min-score", 3, "минимальный мобильный балл для экспорта")
	maxExport := flag.Int("max-export", 300, "сколько ссылок экспортировать")
	flag.Parse()
	l, err := mobile.Load(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "списки:", err)
		os.Exit(1)
	}
	d, i, c := l.Sizes()
	fmt.Printf("списки: доменов %d, IP %d, CIDR %d\n", d, i, c)

	seen := map[string]bool{}
	type agg struct{ n, ip, sni, both, hostIP int }
	var tot agg
	byProto := map[string]*agg{}
	bySrc := map[string]*agg{}
	add := func(a *agg, v mobile.Verdict) {
		a.n++
		if v.HostIsIP {
			a.hostIP++
		}
		if v.IPListed {
			a.ip++
		}
		if v.SNIListed {
			a.sni++
		}
		if v.IPListed && v.SNIListed {
			a.both++
		}
	}
	type cand struct {
		score int
		proto string
		link  string
	}
	var cands []cand
	for _, pat := range flag.Args() {
		files, _ := filepath.Glob(pat)
		for _, f := range files {
			fh, err := os.Open(f)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(fh)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				o, err := translator.Parse(strings.TrimSpace(sc.Text()))
				if err != nil || seen[o.Key] {
					continue
				}
				seen[o.Key] = true
				v := l.Evaluate(o.Opts)
				if v.Score() >= *minScore && *exportLinks != "" && !o.Flags.NoSecurity {
					cands = append(cands, cand{v.Score(), o.Proto, strings.TrimSpace(sc.Text())})
				}
				add(&tot, v)
				if byProto[o.Proto] == nil {
					byProto[o.Proto] = &agg{}
				}
				add(byProto[o.Proto], v)
				src := filepath.Base(f)
				if bySrc[src] == nil {
					bySrc[src] = &agg{}
				}
				add(bySrc[src], v)
			}
			fh.Close()
		}
	}
	if *exportLinks != "" {
		// сначала vless (самый частый и самый гибкий протокол для Reality с SNI), по убыванию балла
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].score != cands[j].score {
				return cands[i].score > cands[j].score
			}
			return (cands[i].proto == "vless") && (cands[j].proto != "vless")
		})
		var links []string
		for _, c := range cands {
			if len(links) >= *maxExport {
				break
			}
			links = append(links, c.link)
		}
		exportHook(*exportLinks, links)
		fmt.Printf("экспортировано кандидатов (балл ≥ %d): %d из %d → %s (0600, секреты)\n", *minScore, len(links), len(cands), *exportLinks)
	}
	pr := func(name string, a *agg) {
		fmt.Printf("  %-44s узлов %7d | хост=IP %7d | IP в списке %6d | SNI в списке %6d | IP+SNI %5d\n", name, a.n, a.hostIP, a.ip, a.sni, a.both)
	}
	fmt.Println("== итого (уникальные узлы)")
	pr("все", &tot)
	fmt.Println("== по протоколам")
	for p, a := range byProto {
		pr(p, a)
	}
	type kv struct {
		k string
		a *agg
	}
	var s []kv
	for k, a := range bySrc {
		s = append(s, kv{k, a})
	}
	sort.Slice(s, func(i, j int) bool { return s[i].a.both+s[i].a.ip > s[j].a.both+s[j].a.ip })
	fmt.Println("== источники с наибольшим числом узлов с адресом из белого списка")
	for i := 0; i < 15 && i < len(s); i++ {
		pr(s[i].k, s[i].a)
	}
}

func init() {
	exportHook = func(path string, cands []string) {
		_ = os.WriteFile(path, []byte(strings.Join(cands, "\n")+"\n"), 0o600)
	}
}

var exportHook func(string, []string)
