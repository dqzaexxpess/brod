package notify

import (
	"fmt"
	"os/exec"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	dest  = "org.freedesktop.Notifications"
	path  = "/org/freedesktop/Notifications"
	iface = "org.freedesktop.Notifications"
)

// DBus — отправка через сессионную шину: кнопки действий поддерживаются.
type DBus struct {
	conn *dbus.Conn
	obj  dbus.BusObject
	mu   sync.Mutex
	cb   func(uint32, string)
}

// OpenDBus подключается к сессионной шине. Ошибка, если шины нет (сервер без графики): тогда вызывающий берёт Exec или Noop.
func OpenDBus() (*DBus, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	d := &DBus{conn: conn, obj: conn.Object(dest, path)}
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(iface), dbus.WithMatchMember("ActionInvoked")); err != nil {
		conn.Close()
		return nil, err
	}
	ch := make(chan *dbus.Signal, 16)
	conn.Signal(ch)
	go func() {
		for sig := range ch {
			if sig.Name != iface+".ActionInvoked" || len(sig.Body) < 2 {
				continue
			}
			id, ok1 := sig.Body[0].(uint32)
			key, ok2 := sig.Body[1].(string)
			d.mu.Lock()
			cb := d.cb
			d.mu.Unlock()
			if ok1 && ok2 && cb != nil {
				cb(id, key)
			}
		}
	}()
	return d, nil
}

func (d *DBus) OnAction(f func(uint32, string)) { d.mu.Lock(); d.cb = f; d.mu.Unlock() }

func (d *DBus) Send(n Notification) (uint32, error) {
	var actions []string
	for _, a := range n.Actions {
		actions = append(actions, a.Key, a.Label)
	}
	hints := map[string]dbus.Variant{"urgency": dbus.MakeVariant(byte(n.Urgency)), "category": dbus.MakeVariant("network")}
	timeout := int32(-1)
	if n.Timeout > 0 {
		timeout = int32(n.Timeout.Milliseconds())
	} else if n.Timeout < 0 {
		timeout = 0 // 0 — не скрывать
	}
	var id uint32
	err := d.obj.Call(iface+".Notify", 0, "Brod", n.ReplaceID, "network-vpn", n.Summary, n.Body, actions, hints, timeout).Store(&id)
	return id, err
}

func (d *DBus) Close(id uint32) { _ = d.obj.Call(iface+".CloseNotification", 0, id).Err }

// Shutdown закрывает соединение с шиной.
func (d *DBus) Shutdown() { _ = d.conn.Close() }

// Exec — запасной вариант без кнопок: notify-send.
type Exec struct{ bin string }

// OpenExec ищет notify-send.
func OpenExec() (*Exec, error) {
	p, err := exec.LookPath("notify-send")
	if err != nil {
		return nil, err
	}
	return &Exec{bin: p}, nil
}

func (e *Exec) OnAction(func(uint32, string)) {}
func (e *Exec) Close(uint32)                  {}
func (e *Exec) Send(n Notification) (uint32, error) {
	urg := map[Urgency]string{Low: "low", Normal: "normal", Critical: "critical"}[n.Urgency]
	return 0, exec.Command(e.bin, "-a", "Brod", "-u", urg, "-i", "network-vpn", n.Summary, n.Body).Run()
}

// Noop — заглушка (уведомления отключены или недоступны): события остаются в журнале и API.
type Noop struct{}

func (Noop) OnAction(func(uint32, string))     {}
func (Noop) Close(uint32)                      {}
func (Noop) Send(Notification) (uint32, error) { return 0, nil }

// Open выбирает лучший доступный способ: D-Bus → notify-send → заглушка. Второй результат описывает выбор для лога.
func Open() (Sender, string) {
	if d, err := OpenDBus(); err == nil {
		return d, "D-Bus (с кнопками)"
	}
	if e, err := OpenExec(); err == nil {
		return e, "notify-send (без кнопок)"
	}
	return Noop{}, fmt.Sprint("уведомлений нет (нет сессионной шины и notify-send)")
}
