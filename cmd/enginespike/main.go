// Спайк Ф2: проверка, что sing-box-lx позволяет создать outbound на лету (OutboundManager.Create)
// в уже работающем экземпляре и ходить через него в процессе, без перезапуска и без TUN/входов.
//
//	enginespike <outbound.json> <url> <sha256-hex ожидаемого тела>
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: enginespike <outbound.json> <url> <sha256>")
		os.Exit(2)
	}
	ctx, cancel := context.WithCancel(include.Context(context.Background()))
	defer cancel()

	t0 := time.Now()
	instance, err := box.New(box.Options{Context: ctx, Options: option.Options{Log: &option.LogOptions{Level: "error"}}})
	must(err, "box.New")
	must(instance.Start(), "instance.Start")
	fmt.Printf("движок запущен за %v (без входов, только direct)\n", time.Since(t0).Round(time.Millisecond))
	defer instance.Close()

	raw, err := os.ReadFile(os.Args[1])
	must(err, "read outbound")
	var ob option.Outbound
	must(json.UnmarshalContext(ctx, raw, &ob), "decode outbound")

	t1 := time.Now()
	must(instance.Outbound().Create(ctx, instance.Router(), instance.LogFactory().NewLogger("probe/"+ob.Tag), ob.Tag, ob.Type, ob.Options), "Outbound.Create")
	out, ok := instance.Outbound().Outbound(ob.Tag)
	if !ok {
		must(fmt.Errorf("outbound %q не найден после Create", ob.Tag), "lookup")
	}
	fmt.Printf("outbound %q (%s) создан на лету за %v\n", ob.Tag, ob.Type, time.Since(t1).Round(time.Microsecond))

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return out.DialContext(ctx, network, M.ParseSocksaddr(addr))
			},
		},
	}
	t2 := time.Now()
	resp, err := client.Get(os.Args[2])
	must(err, "GET через динамический outbound")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	must(err, "read body")
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	fmt.Printf("HTTP %d, %d байт за %v, sha256 совпал: %v\n", resp.StatusCode, len(body), time.Since(t2).Round(time.Millisecond), got == os.Args[3])
	if got != os.Args[3] {
		os.Exit(1)
	}
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		os.Exit(1)
	}
}
