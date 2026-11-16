package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestConnectionsAndTraffic(t *testing.T) {
	c := startCore(t, t.TempDir())
	node := addNode(t, c, "tr")
	if err := c.Connect(node, "user"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 200000)))
	}))
	defer srv.Close()
	addr, user, pass := c.AppProxy()
	pu, _ := url.Parse("http://" + user + ":" + pass + "@" + addr)
	cl := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 10 * time.Second}
	resp, err := cl.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	cl.CloseIdleConnections()
	if n != 200000 {
		t.Fatalf("получено %d", n)
	}
	var found *ConnView
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && found == nil {
		for _, v := range c.Connections(true, 100) {
			if strings.Contains(v.Dest, srv.Listener.Addr().String()[strings.LastIndex(srv.Listener.Addr().String(), ":")+1:]) && v.Down >= 200000 {
				v := v
				found = &v
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("соединение с загрузкой не найдено: %+v", c.Connections(true, 100))
	}
	if found.Route != RouteProxy || found.Inbound != InboundApp || found.Network != "tcp" || found.Node == "" {
		t.Fatalf("маршрут: %+v", found)
	}
	st := c.Traffic(60)
	if st.DownTotal < 200000 {
		t.Fatalf("суммарный счётчик: %+v", st)
	}
	// кольцо отсчётов заполняется раз в секунду
	waitForD(t, 4*time.Second, "отсчёты скорости", func() bool { return len(c.Traffic(60).Samples) >= 2 })
}

func TestDailyTraffic(t *testing.T) {
	c := startCore(t, t.TempDir())
	node := addNode(t, c, "daily")
	if err := c.Connect(node, "user"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("y", 300000)))
	}))
	defer srv.Close()
	addr, user, pass := c.AppProxy()
	pu, _ := url.Parse("http://" + user + ":" + pass + "@" + addr)
	cl := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 10 * time.Second}
	time.Sleep(1500 * time.Millisecond) // первый отсчёт «заряжает» счётчики, приращения считаются со второго
	for i := 0; i < 2; i++ {
		resp, err := cl.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	cl.CloseIdleConnections()
	waitForD(t, 6*time.Second, "суточный счётчик", func() bool {
		days, err := c.DailyTraffic(7)
		return err == nil && len(days) == 1 && days[0].Down >= 600000 && days[0].VPNDown >= 600000
	})
	days, _ := c.DailyTraffic(7)
	if days[0].Day != time.Now().Format("2006-01-02") {
		t.Fatalf("день: %+v", days[0])
	}
}
