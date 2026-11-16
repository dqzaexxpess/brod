// Package notify превращает события ядра в уведомления рабочего стола (P1: «узел деградирует — вот три проверенные альтернативы»).
//
// Показ — через интерфейс Sender: D-Bus org.freedesktop.Notifications с кнопками действий либо запасной notify-send без них.
// Узел сам не меняется никогда: кнопка «Переключить на …» — явное действие пользователя (инициатор user).
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"brod/core"
	"brod/store"
)

// Urgency — срочность по спецификации Desktop Notifications.
type Urgency byte

const (
	Low      Urgency = 0
	Normal   Urgency = 1
	Critical Urgency = 2
)

// Action — кнопка уведомления.
type Action struct{ Key, Label string }

// Notification — уведомление.
type Notification struct {
	Summary, Body string
	Actions       []Action
	Urgency       Urgency
	ReplaceID     uint32        // заменить показанное ранее
	Timeout       time.Duration // 0 — по умолчанию сервера; <0 — не скрывать
}

// Sender показывает уведомления.
type Sender interface {
	Send(n Notification) (uint32, error)
	Close(id uint32)
	// OnAction регистрирует обработчик нажатия на кнопку.
	OnAction(func(id uint32, key string))
}

// Presenter подписывается на шину событий ядра и показывает уведомления.
type Presenter struct {
	c *core.Core
	s Sender
	// VPNOn сообщает, включён ли VPN (по умолчанию — режим ядра); тесты подменяют.
	VPNOn func() bool

	mu       sync.Mutex
	degraded uint32                       // показанное уведомление о деградации активного узла
	targets  map[string]uint32            // цель → уведомление
	pending  map[uint32]map[string]func() // уведомление → действия по ключам кнопок
	last     map[string]time.Time         // ограничение частоты
	now      func() time.Time
}

// New создаёт презентер.
func New(c *core.Core, s Sender) *Presenter {
	p := &Presenter{c: c, s: s, VPNOn: c.VPNEnabled, targets: map[string]uint32{}, pending: map[uint32]map[string]func(){}, last: map[string]time.Time{}, now: time.Now}
	s.OnAction(p.HandleAction)
	return p
}

// Run читает события до отмены контекста.
func (p *Presenter) Run(ctx context.Context) {
	sub := p.c.Bus().Subscribe(128)
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C:
			p.Handle(e)
		}
	}
}

func (p *Presenter) throttled(key string, every time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.last[key]; ok && p.now().Sub(t) < every {
		return true
	}
	p.last[key] = p.now()
	return false
}

func nodeLabel(n core.NodeInfo) string {
	if n.Server == "" {
		return core.ShortID(n.ID)
	}
	return fmt.Sprintf("%s %s:%d", n.Proto, n.Server, n.Port)
}

func (p *Presenter) label(nodeID string) string {
	switch nodeID {
	case "":
		return "—"
	case store.SelDirect:
		return "напрямую"
	case store.SelWarp:
		return "WARP"
	}
	if v, err := p.c.NodeByID(nodeID); err == nil {
		return nodeLabel(v.NodeInfo)
	}
	return core.ShortID(nodeID)
}

type detailJSON struct {
	Reason       string   `json:"reason"`
	Alternatives []string `json:"alternatives"`
	Policy       string   `json:"policy"`
}

func parseDetail(d string) detailJSON {
	var v detailJSON
	_ = json.Unmarshal([]byte(d), &v)
	return v
}

// quiet — уведомление об активном узле не нужно: уровень «off», либо VPN выключен (через активный узел тогда идёт только вход для приложений,
// а о целях приложений сообщают отдельные уведомления).
func (p *Presenter) quiet(e store.Event) bool {
	lvl := p.c.NotifyLevel()
	if lvl == "off" {
		return true
	}
	if e.Target != "" {
		return false // события целей — про приложения, не про VPN
	}
	return lvl == "important" && !p.VPNOn()
}

// loud — уровень «all»: «липкие» критичные уведомления. Иначе уведомления короткие и обычной срочности.
func (p *Presenter) loud() bool { return p.c.NotifyLevel() == "all" }

func (p *Presenter) urgency() Urgency {
	if p.loud() {
		return Critical
	}
	return Normal
}

func (p *Presenter) timeout(sticky time.Duration) time.Duration {
	if p.loud() {
		return -1
	}
	return sticky
}

// Handle показывает уведомление по событию (если оно того заслуживает).
func (p *Presenter) Handle(e store.Event) {
	switch e.Kind {
	case core.EvDegraded, core.EvAlternatives, core.EvAutoSwap, core.EvRecovered, core.EvTargetDegrade, core.EvTargetSwitch, core.EvRestoreFailed:
		if p.quiet(e) {
			return
		}
	}
	switch e.Kind {
	case core.EvAutoSwap:
		d := struct {
			From string `json:"from"`
		}{}
		_ = json.Unmarshal([]byte(e.Detail), &d)
		p.s.Send(Notification{Summary: "Узел заменён автоматически", Urgency: Normal, Timeout: 8 * time.Second,
			Body: fmt.Sprintf("%s не отвечал → теперь %s", p.label(d.From), p.label(e.Node))})
	case core.EvDegraded:
		if p.c.DegradeAction() == "auto" && !p.loud() {
			return // в режиме автопереключения сообщим о результате, а не о каждой стадии
		}
		if !p.loud() && p.throttled("deg:"+e.Node, 5*time.Minute) {
			return // один и тот же узел не мигает уведомлениями чаще раза в 5 минут
		}
		d := parseDetail(e.Detail)
		id, _ := p.s.Send(Notification{Summary: "Узел деградирует", Urgency: p.urgency(), Timeout: p.timeout(8 * time.Second),
			Body: fmt.Sprintf("%s не отвечает (%s). Подбираю проверенные альтернативы…", p.label(e.Node), d.Reason)})
		p.mu.Lock()
		p.degraded = id
		p.mu.Unlock()
	case core.EvAlternatives:
		if e.Target != "" {
			p.targetAlternatives(e)
			return
		}
		p.activeAlternatives(e)
	case core.EvRecovered:
		p.mu.Lock()
		id := p.degraded
		p.degraded = 0
		p.mu.Unlock()
		p.s.Send(Notification{Summary: "Узел снова отвечает", Body: p.label(e.Node), Urgency: Low, ReplaceID: id, Timeout: 5 * time.Second})
	case core.EvTargetDegrade:
		d := parseDetail(e.Detail)
		if d.Policy == store.PolicyAutoFailover && !p.loud() {
			return // цель сама переключится: сообщим о результате, а не о каждой стадии (как с активным узлом в режиме «авто»)
		}
		if p.throttled("tdeg:"+e.Target+d.Reason, time.Minute) {
			return
		}
		id, _ := p.s.Send(Notification{Summary: "Цель «" + e.Target + "» деградирует", Urgency: p.urgency(), Timeout: p.timeout(15 * time.Second),
			Body: fmt.Sprintf("%s: %s", p.label(e.Node), d.Reason)})
		p.mu.Lock()
		p.targets[e.Target] = id
		p.mu.Unlock()
	case core.EvTargetSwitch:
		if e.Initiator == "auto" { // пользовательские переключения он и так видит; автоматические обязан увидеть
			if !p.loud() && p.throttled("tsw:"+e.Target, 10*time.Minute) {
				return // нестабильный путь не мигает уведомлениями: одно на цель в 10 минут (все переключения — в журнале событий)
			}
			p.s.Send(Notification{Summary: "Цель «" + e.Target + "» переключена автоматически", Body: "Теперь: " + p.label(e.Node), Urgency: Normal, Timeout: 10 * time.Second})
		}
	case core.EvRestoreFailed:
		p.s.Send(Notification{Summary: "Сохранённый узел не поднялся", Urgency: Critical, Timeout: -1,
			Body: "Автоматически другой узел не выбирается. Выберите узел: brodctl list, brodctl connect"})
	}
}

func (p *Presenter) activeAlternatives(e store.Event) {
	d := parseDetail(e.Detail)
	if p.c.DegradeAction() == "auto" && !p.loud() && len(d.Alternatives) > 0 {
		return // автопереключение уже работает: уведомление придёт о его результате
	}
	p.mu.Lock()
	replace := p.degraded
	p.mu.Unlock()
	if len(d.Alternatives) == 0 {
		if !p.loud() {
			// первое уведомление обещало «подбираю альтернативы»: говорим, что их нет, иначе пользователь так и ждёт. Один раз за 5 минут:
			// поиск повторяется каждые ~45 с, и повторять одно и то же уведомление не нужно
			if p.throttled("noalt:"+e.Node, 5*time.Minute) {
				return
			}
			id, _ := p.s.Send(Notification{Summary: "Узел деградирует", Urgency: p.urgency(), Timeout: p.timeout(10 * time.Second), ReplaceID: replace,
				Body: p.label(e.Node) + " не отвечает, проверенных альтернатив пока нет. Продолжаю искать."})
			p.mu.Lock()
			p.degraded = id
			p.mu.Unlock()
			return
		}
		id, _ := p.s.Send(Notification{Summary: "Узел деградирует", Urgency: Critical, Timeout: -1, ReplaceID: replace,
			Body: p.label(e.Node) + " не отвечает, проверенных альтернатив сейчас нет. Новые попытки при следующем круге проверок."})
		p.mu.Lock()
		p.degraded = id
		p.mu.Unlock()
		return
	}
	n := Notification{Summary: "Узел деградирует — есть проверенные альтернативы", Urgency: p.urgency(), Timeout: p.timeout(20 * time.Second), ReplaceID: replace}
	acts := map[string]func(){}
	var lines []string
	for i, alt := range d.Alternatives {
		key := fmt.Sprintf("connect:%d", i)
		n.Actions = append(n.Actions, Action{Key: key, Label: "Переключить на " + p.label(alt)})
		lines = append(lines, "• "+p.label(alt))
		alt := alt
		acts[key] = func() { _ = p.c.Connect(alt, "user") }
	}
	n.Body = p.label(e.Node) + " не отвечает. Проверены сейчас:\n" + strings.Join(lines, "\n")
	id, _ := p.s.Send(n)
	p.mu.Lock()
	p.degraded = id
	p.pending[id] = acts
	p.mu.Unlock()
}

func (p *Presenter) targetAlternatives(e store.Event) {
	d := parseDetail(e.Detail)
	p.mu.Lock()
	replace := p.targets[e.Target]
	p.mu.Unlock()
	if len(d.Alternatives) == 0 {
		return
	}
	n := Notification{Summary: "Цель «" + e.Target + "»: есть проверенные узлы", Urgency: p.urgency(), Timeout: p.timeout(20 * time.Second), ReplaceID: replace}
	acts := map[string]func(){}
	var lines []string
	for i, alt := range d.Alternatives {
		key := fmt.Sprintf("switch:%d", i)
		n.Actions = append(n.Actions, Action{Key: key, Label: "Переключить на " + p.label(alt)})
		lines = append(lines, "• "+p.label(alt))
		alt, target := alt, e.Target
		acts[key] = func() { _, _ = p.c.SwitchTarget(context.Background(), target, alt, "user") }
	}
	n.Body = p.label(e.Node) + " не проходит проверку цели. Проверены сейчас:\n" + strings.Join(lines, "\n")
	id, _ := p.s.Send(n)
	p.mu.Lock()
	p.targets[e.Target] = id
	p.pending[id] = acts
	p.mu.Unlock()
}

// HandleAction выполняет действие нажатой кнопки.
func (p *Presenter) HandleAction(id uint32, key string) {
	p.mu.Lock()
	act := p.pending[id][key]
	delete(p.pending, id)
	p.mu.Unlock()
	if act != nil {
		act()
		p.s.Close(id)
	}
}

// SelfTest показывает проверочное уведомление с двумя кнопками. Нажатие только пишет событие в журнал и ничего не переключает:
// так можно убедиться, что уведомления и кнопки доходят до рабочего стола, не трогая узлы.
func (p *Presenter) SelfTest() error {
	n := Notification{Summary: "Brod: проверка уведомлений", Urgency: Normal, Timeout: 60 * time.Second,
		Body:    "Нажмите любую кнопку: узлы это не затронет, в журнале появится событие notify-test.",
		Actions: []Action{{Key: "t1", Label: "Кнопка 1"}, {Key: "t2", Label: "Кнопка 2"}}}
	id, err := p.s.Send(n)
	if err != nil {
		return err
	}
	acts := map[string]func(){}
	for i, key := range []string{"t1", "t2"} {
		i := i + 1
		acts[key] = func() {
			p.c.Bus().Publish(store.Event{Kind: core.EvNotifyTest, Initiator: "user", Detail: fmt.Sprintf("нажата кнопка %d", i)})
		}
	}
	p.mu.Lock()
	p.pending[id] = acts
	p.mu.Unlock()
	return nil
}
