package switchout_test

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"brod/ext/exttest"
	switchout "brod/ext/switch"
)

type rig struct {
	*exttest.Rig
}

func newRig(t *testing.T) *rig { return &rig{exttest.New(t, switchout.RegisterOutbound)} }

func (r *rig) addFake(t *testing.T, tag string) *exttest.Fake { return r.Fake(t, tag) }

func (r *rig) addSwitch(t *testing.T, tag string, o switchout.Options) *switchout.Switch {
	t.Helper()
	if err := r.Create(tag, switchout.Type, &o); err != nil {
		t.Fatal(err)
	}
	s, _ := r.Mgr.Outbound(tag)
	return s.(*switchout.Switch)
}

func dialName(t *testing.T, o adapter.Outbound) (string, net.Conn) {
	return exttest.DialName(t, o, "example.invalid:80")
}

func TestInitialTargetAndSwitch(t *testing.T) {
	r := newRig(t)
	r.addFake(t, "a")
	r.addFake(t, "b")
	sw := r.addSwitch(t, "active", switchout.Options{Target: "a"})

	if got := sw.Current(); got != "a" {
		t.Fatalf("начальная цель %q", got)
	}
	name, c := dialName(t, sw)
	c.Close()
	if name != "a" {
		t.Fatalf("дозвон ушёл на %q", name)
	}
	if err := sw.SetTarget("b"); err != nil {
		t.Fatal(err)
	}
	name, c = dialName(t, sw)
	c.Close()
	if name != "b" || sw.Current() != "b" {
		t.Fatalf("после переключения: дозвон %q, текущая %q", name, sw.Current())
	}
	if deps := sw.Dependencies(); len(deps) != 1 || deps[0] != "b" {
		t.Fatalf("зависимости %v", deps)
	}
}

// Менеджер не даёт удалить outbound, на который смотрит switch; после переключения старый удаляется.
func TestRemoveProtection(t *testing.T) {
	r := newRig(t)
	r.addFake(t, "a")
	r.addFake(t, "b")
	sw := r.addSwitch(t, "active", switchout.Options{Target: "a"})

	if err := r.Mgr.Remove("a"); err == nil || !strings.Contains(err.Error(), "used by active") {
		t.Fatalf("Remove выбранной цели должен отказывать, получили %v", err)
	}
	if err := r.Mgr.Remove("b"); err != nil { // невыбранный удаляется свободно
		t.Fatal(err)
	}
	r.addFake(t, "c")
	if err := sw.SetTarget("c"); err != nil {
		t.Fatal(err)
	}
	if err := r.Mgr.Remove("a"); err != nil {
		t.Fatalf("после переключения старый узел должен удаляться: %v", err)
	}
	if err := r.Mgr.Remove("c"); err == nil {
		t.Fatal("новая цель должна быть защищена")
	}
}

func TestSetTargetErrorsKeepOldTarget(t *testing.T) {
	r := newRig(t)
	r.addFake(t, "a")
	sw := r.addSwitch(t, "active", switchout.Options{Target: "a"})
	for _, tag := range []string{"", "нет-такого"} {
		if err := sw.SetTarget(tag); err == nil {
			t.Fatalf("SetTarget(%q) должен вернуть ошибку", tag)
		}
		if sw.Current() != "a" {
			t.Fatalf("после неудачного SetTarget(%q) цель изменилась на %q", tag, sw.Current())
		}
	}
}

func TestNoTarget(t *testing.T) {
	r := newRig(t)
	sw := r.addSwitch(t, "active", switchout.Options{})
	if _, err := sw.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.invalid:80")); err == nil {
		t.Fatal("без цели дозвон обязан отказывать")
	}
	if sw.Dependencies() != nil || sw.Current() != "" {
		t.Fatal("без цели зависимостей быть не должно")
	}
}

func TestLoopsRejected(t *testing.T) {
	r := newRig(t)
	r.addFake(t, "a")
	s1 := r.addSwitch(t, "s1", switchout.Options{Target: "a"})
	s2 := r.addSwitch(t, "s2", switchout.Options{Target: "s1"})
	if err := s1.SetTarget("s1"); err == nil {
		t.Fatal("switch на самого себя")
	}
	if err := s1.SetTarget("s2"); err == nil {
		t.Fatal("цикл s1 → s2 → s1 должен отклоняться")
	}
	if s1.Current() != "a" {
		t.Fatalf("цель после отказа: %q", s1.Current())
	}
	// цепочка без цикла допустима
	r.addFake(t, "b")
	if err := s2.SetTarget("b"); err != nil {
		t.Fatal(err)
	}
	if err := s1.SetTarget("s2"); err != nil {
		t.Fatalf("после разрыва связи цепочка s1 → s2 → b допустима: %v", err)
	}
	if name, c := dialName(t, s1); name != "b" {
		t.Fatalf("цепочка отдала %q", name)
	} else {
		c.Close()
	}
	if _, err := r.addSwitchErr("self", switchout.Options{Target: "self"}); err == nil {
		t.Fatal("создание switch с целью на самого себя должно отказывать")
	}
}

func (r *rig) addSwitchErr(tag string, o switchout.Options) (adapter.Outbound, error) {
	return nil, r.Create(tag, switchout.Type, &o)
}

func TestClear(t *testing.T) {
	r := newRig(t)
	a := r.addFake(t, "a")
	sw := r.addSwitch(t, "active", switchout.Options{Target: "a", InterruptExisting: true})
	_, c := dialName(t, sw)
	sw.Clear()
	if sw.Current() != "" || len(sw.Dependencies()) != 0 {
		t.Fatalf("после Clear цель %q, зависимости %v", sw.Current(), sw.Dependencies())
	}
	deadline := time.Now().Add(time.Second)
	for a.Closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Closed.Load() == 0 {
		t.Fatal("Clear при interrupt_existing должен рвать открытые соединения")
	}
	c.Close()
	if _, err := sw.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.invalid:80")); err == nil {
		t.Fatal("без цели дозвон должен отклоняться")
	}
	if err := r.Mgr.Remove("a"); err != nil {
		t.Fatalf("после Clear прежнюю цель можно удалить: %v", err)
	}
	r.addFake(t, "b")
	if err := sw.SetTarget("b"); err != nil || sw.Current() != "b" {
		t.Fatalf("после Clear цель назначается снова: %v, %q", err, sw.Current())
	}
}

func TestInterruptExisting(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		r := newRig(t)
		a := r.addFake(t, "a")
		r.addFake(t, "b")
		sw := r.addSwitch(t, "active", switchout.Options{Target: "a", InterruptExisting: interrupt})
		_, c := dialName(t, sw)
		if err := sw.SetTarget("b"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for interrupt && a.Closed.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		switch {
		case interrupt && a.Closed.Load() == 0:
			t.Fatal("interrupt_existing: старое соединение не закрыто")
		case !interrupt && a.Closed.Load() != 0:
			t.Fatal("без interrupt_existing старое соединение должно жить")
		}
		c.Close()
	}
}

// Статический конфиг: switch объявлен раньше своей цели, цель разрешается на стадии запуска.
func TestStartResolvesLateTarget(t *testing.T) {
	r := newRig(t)
	sw := r.addSwitch(t, "active", switchout.Options{Target: "late"})
	if sw.Current() != "" {
		t.Fatal("цели ещё нет в менеджере")
	}
	if err := sw.Start(adapter.StartStateStart, nil); err == nil {
		t.Fatal("цели нет — старт должен падать")
	}
	r.addFake(t, "late")
	if err := sw.Start(adapter.StartStateStart, nil); err != nil {
		t.Fatal(err)
	}
	if sw.Current() != "late" {
		t.Fatalf("цель после старта: %q", sw.Current())
	}
}

// Параллельные дозвоны, переключения и удаления. Как в core, SetTarget и Remove/Create сериализованы владельцем:
// тогда дозвон не может увидеть ни пустую, ни удалённую цель, а Remove выбранной цели всегда отказывает.
func TestConcurrentSwitchAndRemove(t *testing.T) { concurrent(t, true) }

// Без сериализации (страховочный путь отката в SetTarget): допустимы ошибки переключения и редкие дозвоны без цели,
// недопустимы паника и гонка данных (проверяется под -race).
func TestConcurrentUnserialized(t *testing.T) { concurrent(t, false) }

func concurrent(t *testing.T, serialized bool) {
	var owner sync.Mutex // владелец менеджера: в core одна блокировка на SetTarget и Remove/Create
	lock := func() {
		if serialized {
			owner.Lock()
		}
	}
	unlock := func() {
		if serialized {
			owner.Unlock()
		}
	}
	r := newRig(t)
	const n = 6
	for i := 0; i < n; i++ {
		r.addFake(t, "n"+string(rune('0'+i)))
	}
	sw := r.addSwitch(t, "active", switchout.Options{Target: "n0"})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var dialErrs atomic.Int64
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c, err := sw.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.invalid:80"))
				if err != nil {
					dialErrs.Add(1)
					continue
				}
				c.Close()
			}
		}()
	}
	wg.Add(1)
	go func() { // переключатель
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			lock()
			_ = sw.SetTarget("n" + string(rune('0'+i%n)))
			unlock()
		}
	}()
	wg.Add(1)
	go func() { // удалитель и пересоздатель: Remove иногда проходит (цель в этот момент другая), иногда отказывает
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			tag := "n" + string(rune('0'+i%n))
			lock()
			if err := r.Mgr.Remove(tag); err == nil {
				_ = r.Create(tag, "fake", &exttest.FakeOptions{Name: tag})
			}
			unlock()
		}
	}()
	time.Sleep(700 * time.Millisecond)
	close(stop)
	wg.Wait()
	if d := dialErrs.Load(); serialized && d != 0 {
		t.Fatalf("%d дозвонов закончились ошибкой: цель всегда существует, ошибок быть не должно", d)
	}
	if cur := sw.Current(); serialized || cur != "" {
		if o, ok := r.Mgr.Outbound(cur); !ok || o == nil {
			t.Fatalf("итоговая цель %q отсутствует в менеджере", cur)
		}
	}
}

// Смена цели во время дозвона: соединение, установленное через прежнюю цель, при interrupt_existing не должно пережить смену
// (оно не попало в группу вовремя и не было бы оборвано Interrupt'ом); без interrupt_existing — остаётся.
func TestSwitchDuringDial(t *testing.T) {
	for _, interrupt := range []bool{true, false} {
		r := newRig(t)
		a := r.addFake(t, "a")
		r.addFake(t, "b")
		sw := r.addSwitch(t, "active", switchout.Options{Target: "a", InterruptExisting: interrupt})
		gate, started := make(chan struct{}), make(chan struct{}, 1)
		a.Gate.Store(&gate)
		a.Started.Store(&started)
		type res struct {
			c   net.Conn
			err error
		}
		done := make(chan res, 1)
		go func() {
			c, err := sw.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.invalid:80"))
			done <- res{c, err}
		}()
		<-started // дозвон через a начался
		if err := sw.SetTarget("b"); err != nil {
			t.Fatal(err)
		}
		close(gate) // дозвон через a заканчивается уже после смены
		got := <-done
		if interrupt {
			if got.err == nil {
				got.c.Close()
				t.Fatal("interrupt_existing: соединение через прежнюю цель пережило смену")
			}
			if a.Closed.Load() == 0 {
				// сервер a должен увидеть закрытие: иначе утечка соединения
				time.Sleep(100 * time.Millisecond)
				if a.Closed.Load() == 0 {
					t.Fatal("брошенное соединение не закрыто")
				}
			}
		} else if got.err != nil {
			t.Fatalf("без interrupt_existing дозвон должен завершиться: %v", got.err)
		} else {
			got.c.Close()
		}
	}
}
