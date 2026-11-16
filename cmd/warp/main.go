// Команда warp: регистрация устройства в Cloudflare WARP и проверка самого WARP теми же уровнями L2–L4.
//
//	warp register [-out work/warp/account.json]        # внешнее действие: запросы к api.cloudflareclient.com
//	warp test [-account f] [-mask none|quic|dns|stun|sip] [-endpoint host] [-port N] [-mtu N] [-jc N -jmin N -jmax N]
//
// Секреты пишутся только в файл -out (права 0600); в stdout их нет.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/warp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: warp register|test [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "register":
		register(os.Args[2:])
	case "test":
		test(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "неизвестная команда", os.Args[1])
		os.Exit(2)
	}
}

func register(args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	out := fs.String("out", "work/warp/account.json", "куда сохранить аккаунт (секреты, права 0600)")
	force := fs.Bool("force", false, "перезаписать существующий аккаунт")
	fs.Parse(args)
	if _, err := os.Stat(*out); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "%s уже существует (аккаунт не перерегистрируется; -force чтобы заменить)\n", *out)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	acc, err := warp.Register(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
	_ = os.MkdirAll(filepath.Dir(*out), 0o700)
	b, _ := json.MarshalIndent(acc, "", " ")
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("зарегистрировано; аккаунт сохранён в %s (0600). Внутренний адрес %s, пир %s…, reserved=%v\n", *out, acc.AddrV4, acc.PeerPublicKey[:6], acc.Reserved())
}

func test(args []string) {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	accFile := fs.String("account", "work/warp/account.json", "файл аккаунта")
	mask := fs.String("mask", "none", "none (обычный WG) | quic | dns | stun | sip (AWG)")
	endpoint := fs.String("endpoint", "", "host пира (по умолчанию engage.cloudflareclient.com)")
	port := fs.Uint("port", 0, "порт пира (по умолчанию 2408)")
	mtu := fs.Uint("mtu", 0, "MTU (по умолчанию 1280)")
	jc := fs.Uint("jc", 0, "AWG Jc")
	jmin := fs.Uint("jmin", 0, "AWG Jmin")
	jmax := fs.Uint("jmax", 0, "AWG Jmax")
	wait := fs.Duration("timeout", 25*time.Second, "общий таймаут проверки")
	l2only := fs.Bool("l2-only", false, "только L2; код возврата 0 = WARP работает, 1 = нет (для скриптов сканирования)")
	fs.Parse(args)

	b, err := os.ReadFile(*accFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "аккаунт:", err)
		os.Exit(1)
	}
	var acc warp.Account
	if err := json.Unmarshal(b, &acc); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m := *mask
	if m == "none" {
		m = ""
	}
	ep := acc.Endpoint(warp.Options{Mask: m, Endpoint: *endpoint, Port: uint16(*port), MTU: uint32(*mtu), Jc: uint32(*jc), Jmin: uint32(*jmin), Jmax: uint32(*jmax)})
	cfg, _ := json.Marshal(map[string]any{"endpoints": []any{ep}})

	eng, err := engine.NewFromJSON("error", cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "движок не принял конфиг WARP:", err)
		os.Exit(1)
	}
	defer eng.Close()
	p, err := eng.Existing("warp")
	if err != nil {
		fmt.Fprintln(os.Stderr, "outbound через WARP:", err)
		os.Exit(1)
	}
	defer p.Close()

	// первое рукопожатие WG/AWG к WARP то мгновенное, то с повтором через 5 с (таймер WireGuard) — даём L2 почти весь общий таймаут
	prober.L2Timeout = *wait - 2*time.Second
	if prober.L2Timeout < 10*time.Second {
		prober.L2Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), *wait)
	defer cancel()
	fmt.Printf("WARP (mask=%s endpoint=%s:%v mtu=%v):\n", *mask, orDef(*endpoint, "engage.cloudflareclient.com"), orDefU(*port, 2408), orDefU(*mtu, 1280))
	if *l2only {
		r := prober.L2(ctx, p)
		fmt.Printf("L2 %v %d мс %s\n", r.OK, r.LatencyMs, r.Class)
		if !r.OK {
			os.Exit(1)
		}
		return
	}
	var wg sync.WaitGroup
	res := make([]prober.Result, 5)
	run := func(i int, f func() prober.Result) { wg.Add(1); go func() { defer wg.Done(); res[i] = f() }() }
	run(0, func() prober.Result { return prober.L2(ctx, p) })
	run(1, func() prober.Result { return prober.L3(ctx, p, 256*1024) })
	run(2, func() prober.Result { return prober.L4YouTube(ctx, p) })
	run(3, func() prober.Result { return prober.L4Telegram(ctx, p) })
	run(4, func() prober.Result { return prober.L4GoogleAPI(ctx, p) })
	wg.Wait()
	for _, r := range res {
		st := "ОК "
		if !r.OK {
			st = "нет"
		}
		fmt.Printf("  L%d %-18s %s  %5d мс  %s %s\n", r.Level, r.Target, st, r.LatencyMs, r.Class, r.Detail)
	}
}

func orDef(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
func orDefU(v, d uint) uint {
	if v == 0 {
		return d
	}
	return v
}

var _ = filepath.Join
