// Команда checklink: прогоняет одну ссылку узла через все наши проверки (L2, L3, YouTube, Google API, Telegram, страна и чистота выхода).
// Нужна, чтобы сверить наши оценки с реальностью («с этого узла у меня YouTube работает» — а что скажут проверки?).
//
//	echo 'ss://…' | bin/checklink            # ссылка со стандартного входа (секрет не попадает в аргументы процесса)
//	bin/checklink -f work/link.txt -n 3      # из файла, три прогона YouTube
//
// В вывод адрес узла попадает только в маске.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"brod/engine"
	"brod/prober"
	"brod/translator"
)

func main() {
	file := flag.String("f", "", "файл со ссылкой (по умолчанию стандартный вход)")
	n := flag.Int("n", 2, "сколько раз повторить проверку YouTube")
	flag.Parse()

	in := os.Stdin
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}
	var link string
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); strings.Contains(l, "://") {
			link = l
			break
		}
	}
	if link == "" {
		fmt.Fprintln(os.Stderr, "нет ссылки на входе")
		os.Exit(1)
	}
	o, err := translator.Parse(link)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ссылка не разобрана:", err)
		os.Exit(1)
	}
	host := fmt.Sprint(o.Opts["server"])
	fmt.Printf("узел: %s, %s:%v, класс %s\n", o.Proto, mask(host), o.Opts["server_port"], translator.Class(o.Opts))

	eng, err := engine.NewFromJSON("error", []byte(`{}`))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer eng.Close()
	raw, _ := json.Marshal(o.Opts)
	p, err := eng.NewProbe(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "движок не принял конфиг:", err)
		os.Exit(1)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	show := func(name string, r prober.Result) {
		st := "ОК"
		if !r.OK {
			st = "ПРОВАЛ " + string(r.Class)
		}
		extra := ""
		if r.SpeedKBps > 0 {
			extra = fmt.Sprintf(", %.1f Мбит/с", r.SpeedKBps*8/1024)
		}
		if r.Detail != "" {
			extra += " (" + r.Detail + ")"
		}
		fmt.Printf("  %-22s %s, %d мс%s\n", name, st, r.LatencyMs, extra)
	}
	show("L2 рукопожатие+HTTPS", prober.L2(ctx, p))
	show("L3 загрузка 2 МБ", prober.L3(ctx, p, 2<<20))
	show("google-api", prober.L4GoogleAPI(ctx, p))
	show("telegram", prober.L4Telegram(ctx, p))
	for i := 1; i <= *n; i++ {
		show(fmt.Sprintf("YouTube (прогон %d)", i), prober.L4YouTube(ctx, p))
	}
	e := prober.Exit(ctx, p)
	fmt.Printf("  выход: страна %q, ЦОД Cloudflare %q, адрес %s\n", e.Country, e.Colo, mask(e.IP))
	tri := func(v *bool, yes, no string) string {
		switch {
		case v == nil:
			return "не проверялось"
		case *v:
			return yes
		}
		return no
	}
	fmt.Printf("  Google без капчи: %s; строгий сайт (chatgpt.com): %s\n", tri(e.GoogleOK, "да", "НЕТ, просит капчу"), tri(e.SiteOK, "открывается", "НЕ пускает"))
	if e.Detail != "" {
		fmt.Println("  замечание:", e.Detail)
	}
}

func mask(s string) string {
	parts := strings.Split(s, ".")
	if len(parts) == 4 {
		return parts[0] + "." + parts[1] + ".x.x"
	}
	if len(s) > 8 {
		return s[:4] + "…" + s[len(s)-4:]
	}
	return s
}
