// Без тега android: чистая логика моста (идентификатор сети, DNS, платный трафик, уровень лога, очищенный журнал) не зависит от libbox
// и проверяется обычным go test на Linux (androidcore_test.go). Экспортируемые функции попадают в gomobile bind.
package androidcore

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"brod/core"
	"brod/notify"
	"brod/supervisor"
)

var netID atomic.Pointer[string]

// SetNetworkID сообщает ядру идентификатор текущей сети (например, «wifi:<ssid>» или «cell»): Go-рантайм на Android 11+ не видит
// интерфейсы (netlink закрыт), а результаты проверок узлов привязаны к сети. Приложение вызывает при каждой смене сети.
func SetNetworkID(id string) { netID.Store(&id) }

var metered atomic.Bool

// SetMetered сообщает ядру, что текущая сеть платная (мобильная или Wi-Fi с ограничением трафика): большие файлы (обновление приложения)
// без согласия пользователя не скачиваются. Приложение вызывает при смене сети.
func SetMetered(on bool) { metered.Store(on) }

var dnsAddr atomic.Pointer[string]

// SetDNS сообщает ядру адрес DNS-сервера текущей сети (IPv4): у Go и движка на Android нет /etc/resolv.conf. Приложение вызывает при смене сети.
func SetDNS(addr string) { dnsAddr.Store(&addr) }

func currentDNS() string {
	if p := dnsAddr.Load(); p != nil && *p != "" {
		return *p
	}
	return "77.88.8.8" // пока приложение не сообщило DNS сети
}

// logLevel — уровень лога движка: файл stateDir/loglevel (debug|info|warn|error) позволяет включить подробный лог на устройстве без пересборки.
func logLevel(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "loglevel")); err == nil {
		switch v := strings.TrimSpace(string(b)); v {
		case "trace", "debug", "info", "warn", "error":
			return v
		}
	}
	return "warn"
}

func currentNetID() string {
	if p := netID.Load(); p != nil && *p != "" {
		return *p
	}
	return "android"
}

// SanitizedLog возвращает хвост (до maxBytes) журнала stateDir/name с вычищенными адресами, именами хостов и паролями — для
// «Поделиться журналом». Работает и при остановленном ядре. Имя только из известных: произвольный путь не принимается.
func SanitizedLog(stateDir, name string, maxBytes int64) (string, error) {
	if name != "core.log" && name != "engine.log" {
		return "", fmt.Errorf("неизвестный журнал %q", name)
	}
	return core.SanitizedLogTail(filepath.Join(stateDir, name), maxBytes)
}

// sources — продуктивные источники подписок (76 шт.);
// на Linux их кладёт установщик в ~/.config/vpnclient/sources.txt, на Android файла нет — список вшит в библиотеку.
//
//go:embed sources.txt
var sources []byte

var (
	mu  sync.Mutex
	cur *instance
)

// StateListener — приложение узнаёт, включён ли VPN: служба переднего плана с уведомлением живёт, пока VPN включён.
type StateListener interface {
	OnVPN(on bool)
	// OnBrief вызывается, пока VPN включён, при смене режима, протокола или страны активного узла (для текста уведомления службы).
	OnBrief(mode, proto, country string)
}

var listener atomic.Pointer[StateListener]

// SetStateListener задаёт получателя изменений состояния VPN (сразу получает текущее).
func SetStateListener(l StateListener) {
	listener.Store(&l)
	mu.Lock()
	in := cur
	mu.Unlock()
	if in != nil {
		l.OnVPN(in.sup.On())
		if b, ok := briefOf(in.sup); ok {
			l.OnBrief(b[0], b[1], b[2])
		}
	}
}

// briefOf собирает режим, протокол и страну выхода; ok=false — VPN выключен или ядра нет.
func briefOf(sup *supervisor.Supervisor) (b [3]string, ok bool) {
	c := sup.Core()
	if !sup.On() || c == nil {
		return b, false
	}
	b[0] = sup.Mode()
	b[1], b[2] = c.ActiveBrief()
	return b, true
}

// Disconnect выключает VPN (кнопка «Отключить» в уведомлении, отзыв VPN в системе).
func Disconnect() {
	mu.Lock()
	in := cur
	mu.Unlock()
	if in == nil {
		return
	}
	off := false
	if _, err := in.sup.Set(&off, ""); err != nil {
		log.Printf("отключение: %v", err)
	}
}

// watchState сообщает приложению об изменениях: о смене включённости — сразу по событию супервизора (опрос задерживал бы закрытие TUN), о смене режима/протокола/страны активного узла — по тому же событию и медленным опросом,
// который идёт только пока VPN включён (эти поля меняет ядро, а не супервизор).
func watchState(ctx context.Context, sup *supervisor.Supervisor) {
	last := sup.On()
	var lastBrief [3]string
	check := func() {
		l := listener.Load()
		if on := sup.On(); on != last {
			last = on
			lastBrief = [3]string{}
			if l != nil {
				(*l).OnVPN(on)
			}
		}
		if b, ok := briefOf(sup); ok && b != lastBrief {
			lastBrief = b
			if l != nil {
				(*l).OnBrief(b[0], b[1], b[2])
			}
		}
	}
	t := time.NewTicker(briefPoll)
	defer t.Stop()
	for {
		changed := sup.Changed() // берём до проверки: смена между проверкой и ожиданием не потеряется
		check()
		var tick <-chan time.Time
		if last {
			tick = t.C
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-tick:
		}
	}
}

// briefPoll — период проверки режима/протокола/страны, пока VPN включён.
const briefPoll = 3 * time.Second

type instance struct {
	cancel context.CancelFunc
	sup    *supervisor.Supervisor
	sock   string
	done   chan struct{}
}

// Connect включает VPN в сохранённом (mode == "") или заданном режиме: «Постоянный VPN» после перезагрузки и плитка быстрых настроек.
func Connect(mode string) error {
	mu.Lock()
	in := cur
	mu.Unlock()
	if in == nil {
		return fmt.Errorf("ядро не запущено")
	}
	on := true
	_, err := in.sup.Set(&on, mode)
	return err
}

// Running сообщает, запущено ли ядро в этом процессе.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return cur != nil
}

// VPNOn сообщает, включён ли VPN.
func VPNOn() bool {
	mu.Lock()
	in := cur
	mu.Unlock()
	return in != nil && in.sup.On()
}

// Notifier показывает уведомления Android (Kotlin). actions — «ключ\x1fподпись» через \x1e.
type Notifier interface {
	Show(id int32, title, body, actions string, important bool)
	Cancel(id int32)
}

var (
	notifier     atomic.Pointer[Notifier]
	notifySender atomic.Pointer[sender]
)

// SetNotifier задаёт показ уведомлений приложением.
func SetNotifier(n Notifier) { notifier.Store(&n) }

// NotifyAction передаёт ядру нажатие кнопки уведомления (id, key из Show).
func NotifyAction(id int32, key string) {
	if s := notifySender.Load(); s != nil {
		s.mu.Lock()
		f := s.onAction
		s.mu.Unlock()
		if f != nil {
			f(uint32(id), key)
		}
	}
}

// sender — notify.Sender поверх Notifier.
type sender struct {
	next     atomic.Uint32
	mu       sync.Mutex
	onAction func(id uint32, key string)
}

func (s *sender) Send(n notify.Notification) (uint32, error) {
	id := n.ReplaceID
	if id == 0 {
		id = s.next.Add(1)
	}
	var acts []string
	for _, a := range n.Actions {
		acts = append(acts, a.Key+"\x1f"+a.Label)
	}
	if p := notifier.Load(); p != nil {
		(*p).Show(int32(id), n.Summary, n.Body, strings.Join(acts, "\x1e"), n.Urgency >= notify.Normal)
	}
	return id, nil
}

func (s *sender) Close(id uint32) {
	if p := notifier.Load(); p != nil {
		(*p).Cancel(int32(id))
	}
}

func (s *sender) OnAction(f func(id uint32, key string)) {
	s.mu.Lock()
	s.onAction = f
	s.mu.Unlock()
}

// Stop останавливает ядро (и VPN, если он был включён).
func Stop() {
	mu.Lock()
	in := cur
	cur = nil
	mu.Unlock()
	stopInstance(in, stopTimeout)
}

// stopTimeout — сколько Stop ждёт закрытия супервизора.
const stopTimeout = 10 * time.Second

// stopInstance останавливает экземпляр ядра: отмена контекста, закрытие супервизора (не дольше wait), ожидание API-сервера, удаление сокета.
// Вынесено из Stop без изменения поведения, чтобы проверяться обычным go test.
func stopInstance(in *instance, wait time.Duration) {
	if in == nil {
		return
	}
	in.cancel()
	closed := make(chan error, 1)
	go func() { closed <- in.sup.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			log.Printf("при остановке: %v", err)
		}
	case <-time.After(wait):
		log.Printf("остановка заняла больше 10 с")
	}
	<-in.done
	_ = os.Remove(in.sock)
}
