package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// P1: узлы добавляются и убираются в работающем движке без перезапуска; зависимые и «по умолчанию» защищены от удаления.
func TestAddRemoveOutbounds(t *testing.T) {
	e, err := New("error")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if _, err := e.add([]byte(`{"type":"direct","tag":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if !e.Has("a") {
		t.Fatal("a не зарегистрирован")
	}
	if _, err := e.add([]byte(`{"type":"selector","tag":"sel","outbounds":["a"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("a"); err == nil {
		t.Fatal("удаление используемого outbound'а должно быть отклонено")
	}
	if !e.Has("a") {
		t.Fatal("a исчез после отклонённого удаления")
	}
	if err := e.Remove("sel"); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("a"); err != nil {
		t.Fatalf("после удаления группы a должен удаляться: %v", err)
	}
	if e.Has("a") || e.Has("sel") {
		t.Fatal("outbound'ы остались после удаления")
	}
	if err := e.Remove("a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("повторное удаление: %v", err)
	}
	if err := e.Remove(e.Direct().Tag()); err == nil {
		t.Fatal("outbound по умолчанию удалять нельзя")
	}
	if _, err := e.add([]byte(`{"type":"direct"}`)); err == nil {
		t.Fatal("без tag добавление должно отклоняться")
	}
}

// Параллельные Add/Remove не должны ломать итерацию по Outbounds() (Remove создаёт новый срез, а не правит общий).
func TestAddRemoveConcurrent(t *testing.T) {
	e, err := New("error")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				tag := "n" + string(rune('a'+w)) + string(rune('A'+i%26)) + string(rune('0'+i/26))
				if _, err := e.add([]byte(`{"type":"direct","tag":"` + tag + `"}`)); err != nil {
					t.Error(err)
					return
				}
				for range e.box.Outbound().Outbounds() { // итерация, пока соседи добавляют/удаляют
				}
				if err := e.Remove(tag); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}

// Узел с протоколом, которому нужен Start (anytls), не должен ронять процесс: ошибка соединения — да, паника — нет.
func TestAnyTLSProbeDoesNotPanic(t *testing.T) {
	e, err := New("error")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p, err := e.NewProbe([]byte(`{"type":"anytls","server":"127.0.0.1","server_port":1,"password":"x","tls":{"enabled":true,"server_name":"example.com","insecure":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.DialContext(ctx, "tcp", "example.com:443"); err == nil {
		t.Fatal("ожидалась ошибка соединения с закрытым портом")
	}
}

// Постоянный outbound проходит стадии запуска: протокол, которому нужен Start, не падает на nil при первом дозвоне.
// Проверка достигает TLS-конфига только при живом сервере, поэтому цели — слушающие TCP/UDP-сокеты, которые ничего не отвечают
// (дозвон обязан закончиться ошибкой рукопожатия/таймаутом, но не паникой драйвера).
func TestAddRunsStartStages(t *testing.T) {
	e, err := New("error")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpLn.Close()
	go func() {
		for {
			c, err := tcpLn.Accept()
			if err != nil {
				return
			}
			go func() { time.Sleep(200 * time.Millisecond); c.Close() }()
		}
	}()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	tcpPort := tcpLn.Addr().(*net.TCPAddr).Port
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	tls := `"tls":{"enabled":true,"server_name":"example.com","insecure":true}`
	nodes := map[string]string{
		"anytls": fmt.Sprintf(`{"type":"anytls","tag":"a","server":"127.0.0.1","server_port":%d,"password":"x",`+tls+`}`, tcpPort),
		"hy2":    fmt.Sprintf(`{"type":"hysteria2","tag":"h","server":"127.0.0.1","server_port":%d,"password":"x",`+tls+`}`, udpPort),
		"tuic":   fmt.Sprintf(`{"type":"tuic","tag":"t","server":"127.0.0.1","server_port":%d,"uuid":"5b0e5c2e-0000-4000-8000-000000000001","password":"x",`+tls+`}`, udpPort),
		"trojan": fmt.Sprintf(`{"type":"trojan","tag":"r","server":"127.0.0.1","server_port":%d,"password":"x",`+tls+`}`, tcpPort),
	}
	for name, raw := range nodes {
		tag, err := e.Add([]byte(raw))
		if err != nil {
			t.Fatalf("%s: Add: %v", name, err)
		}
		p, err := e.Existing(tag)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err = p.DialContext(ctx, "tcp", "example.com:443")
		cancel()
		if err == nil {
			t.Fatalf("%s: к молчащему серверу дозвон не может удаться", name)
		}
		t.Logf("%s: %v", name, err)
		if strings.Contains(err.Error(), "panic") { // Probe превращает панику драйвера в ошибку; без стадий запуска она и случалась
			t.Fatalf("%s: драйвер упал: %v", name, err)
		}
		if err := e.Remove(tag); err != nil {
			t.Fatalf("%s: Remove: %v", name, err)
		}
	}
}

func TestDefaultCertificateStore(t *testing.T) {
	if c := defaultCertificate("windows"); c == nil || c.Store != "mozilla" {
		t.Fatalf("windows: %+v", c)
	}
	for _, goos := range []string{"linux", "android", "darwin"} {
		if c := defaultCertificate(goos); c != nil {
			t.Fatalf("%s: хранилище системы должно остаться: %+v", goos, c)
		}
	}
}

// Хранилище Mozilla движок принимает (на Windows оно подставляется по умолчанию).
func TestMozillaCertificateStoreAccepted(t *testing.T) {
	if err := ValidateConfig([]byte(`{"certificate":{"store":"mozilla"},"outbounds":[{"type":"direct","tag":"d"}]}`)); err != nil {
		t.Fatal(err)
	}
}
