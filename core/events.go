package core

import (
	"sync"

	"brod/store"
)

// Виды событий журнала.
const (
	EvSwitch        = "switch"         // активный узел сменился (кто — в Initiator)
	EvConnectFailed = "connect-failed" // выбранный узел не удалось поднять; прежний остаётся
	EvRestored      = "restored"       // после перезапуска восстановлен сохранённый выбор
	EvRestoreFailed = "restore-failed" // сохранённый узел не поднялся; active остаётся без цели
	EvRefresh       = "refresh"        // подписки обновлены
	EvDegraded      = "degraded"       // активный узел деградирует
	EvWarpDown      = "warp-down"      // WARP не отвечает: цепочка через него отключена, активный узел перерегистрирован напрямую
	EvWarpUp        = "warp-up"        // WARP снова отвечает: новые подключения опять идут цепочкой
	EvAutoSwap      = "auto-swap"      // активный узел заменён автоматически (настройка degrade.action=auto)
	EvRecovered     = "recovered"      // активный узел снова в порядке
	EvAlternatives  = "alternatives"   // найдены альтернативы деградировавшему узлу
	EvNetChanged    = "net-changed"    // сменилась сеть
	EvTargetSwitch  = "target-switch"  // узел цели сменился
	EvTargetDegrade = "target-degraded"
	EvTargetOK      = "target-recovered"
	EvNode          = "node"         // действия над узлом (избранное, закрепление)
	EvSource        = "source"       // изменения источников
	EvChainHop      = "chain-hop"    // звено активной цепочки не отвечает
	EvSubscription  = "subscription" // подписка: срок скоро кончится или кончился, трафик на исходе
	EvUpdate        = "update"       // обновление приложения: доступна новая версия / файл скачан и проверен
	EvSetting       = "setting"      // изменения настроек
	EvError         = "error"        // внутренняя ошибка фонового задания
	EvStarted       = "started"      // демон запущен
	EvStopped       = "stopped"      // демон остановлен
	EvTargetChange  = "target"       // цель зарегистрирована/изменена/удалена
	EvRouteVia      = "route-via"    // выход правила маршрутизации перепривязан (страна/узел/запасной вариант)
	EvProbe         = "probe-run"    // итог пакетной проверки
	EvNotifyTest    = "notify-test"  // проверка уведомлений (нажатие тестовой кнопки)
)

// Bus сохраняет события в журнал и рассылает подписчикам. Медленный подписчик не тормозит остальных: при переполнении
// его события пропускаются, а Sub.Lagged сообщает об этом (клиент догоняет через Core.EventsAfter по последнему ID).
type Bus struct {
	st   *store.Store
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

// Sub — подписка на события.
type Sub struct {
	C      chan store.Event
	b      *Bus
	mu     sync.Mutex
	lagged bool
}

func newBus(st *store.Store) *Bus { return &Bus{st: st, subs: map[*Sub]struct{}{}} }

// Publish пишет событие в журнал и рассылает подписчикам. Ошибка записи в журнал не теряет рассылку.
func (b *Bus) Publish(e store.Event) store.Event {
	saved, err := b.st.AddEvent(e)
	if err != nil {
		saved = e // в журнал не легло — но подписчики об этом узнать должны
	}
	b.mu.Lock()
	for s := range b.subs {
		select {
		case s.C <- saved:
		default:
			s.mu.Lock()
			s.lagged = true
			s.mu.Unlock()
		}
	}
	b.mu.Unlock()
	return saved
}

// Subscribe создаёт подписку с буфером buf событий.
func (b *Bus) Subscribe(buf int) *Sub {
	if buf <= 0 {
		buf = 64
	}
	s := &Sub{C: make(chan store.Event, buf), b: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Lagged сообщает, что подписчик пропустил события, и сбрасывает признак.
func (s *Sub) Lagged() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.lagged
	s.lagged = false
	return l
}

// Close отписывает.
func (s *Sub) Close() {
	s.b.mu.Lock()
	delete(s.b.subs, s)
	s.b.mu.Unlock()
}
