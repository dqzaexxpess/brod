// Команда translate: прогоняет файлы со ссылками через translator и печатает статистику (Ф1).
//
//	go run ./cmd/translate -out work/outbounds.jsonl work/corpus/*
//
// В stdout только агрегаты (без хостов/UUID). Файл -out содержит секреты, держать в work/.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"brod/translator"
)

func main() {
	out := flag.String("out", "", "куда писать нормализованные outbound'ы (jsonl, содержит секреты)")
	flag.Parse()

	seenLine := map[string]bool{}
	var links []string
	for _, p := range flag.Args() {
		f, err := os.Open(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if strings.Contains(l, "://") && !strings.HasPrefix(l, "#") && !seenLine[l] {
				seenLine[l] = true
				links = append(links, l)
			}
		}
		f.Close()
	}

	reasons := map[string]int{}
	details := map[string]map[string]int{}
	notes := map[string]int{}
	protos := map[string]int{}
	var ok, noSec, insecure, split int
	dedup := map[string]bool{}
	var w *bufio.Writer
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		w = bufio.NewWriter(f)
		defer w.Flush()
	}
	for _, l := range links {
		o, err := translator.Parse(l)
		if err != nil {
			var te *translator.Error
			r, d := "internal", err.Error()
			if errors.As(err, &te) {
				r, d = string(te.Reason), te.Detail
			}
			reasons[r]++
			if details[r] == nil {
				details[r] = map[string]int{}
			}
			details[r][d]++
			continue
		}
		ok++
		protos[o.Proto]++
		if o.Flags.NoSecurity {
			noSec++
		}
		if o.Flags.Insecure {
			insecure++
		}
		if o.Flags.DownloadSplit {
			split++
		}
		for _, n := range o.Notes {
			notes[n]++
		}
		dedup[o.Key] = true
		if w != nil {
			b, _ := json.Marshal(map[string]any{"key": o.Key, "proto": o.Proto, "opts": o.Opts, "flags": o.Flags})
			w.Write(b)
			w.WriteByte('\n')
		}
	}
	total := len(links)
	fmt.Printf("ссылок (уникальных строк): %d\nразобрано: %d (%.2f%%)\nпосле дедупликации по ключу: %d\n", total, ok, 100*float64(ok)/float64(total), len(dedup))
	fmt.Println("по протоколам:", protos)
	fmt.Printf("флаги: security=none %d, insecure %d, downloadSettings %d\n", noSec, insecure, split)
	fmt.Println("== причины отказов (по частоте)")
	type kv struct {
		k string
		v int
	}
	var rs []kv
	for k, v := range reasons {
		rs = append(rs, kv{k, v})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].v > rs[j].v })
	for _, r := range rs {
		fmt.Printf("  %6d  %s\n", r.v, r.k)
		var ds []kv
		for d, c := range details[r.k] {
			ds = append(ds, kv{d, c})
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i].v > ds[j].v })
		for i, d := range ds {
			if i >= 4 {
				break
			}
			fmt.Printf("          %5d  %s\n", d.v, d.k)
		}
	}
	fmt.Println("== заметки (частые)")
	var ns []kv
	for k, v := range notes {
		ns = append(ns, kv{k, v})
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].v > ns[j].v })
	for i, n := range ns {
		if i >= 12 {
			break
		}
		fmt.Printf("  %6d  %s\n", n.v, n.k)
	}
}
