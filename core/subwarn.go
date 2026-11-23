package core

import (
	"fmt"
	"strconv"
	"time"

	"brod/store"
)

// Предупреждения о подписке: срок скоро кончится (или кончился), трафик на исходе. Сведения берутся из заголовка источника (subscription-userinfo),
// поэтому у источника без заголовка предупреждений нет. Каждая стадия сообщается один раз; когда подписку продлили, память о ней сбрасывается.

const (
	subExpireSoon = 3 * 24 * time.Hour // предупреждать за три дня до окончания
	subTrafficLow = 0.10               // и когда осталось меньше десятой части трафика
)

// subStages — стадии состояния подписки: "" (всё хорошо), "soon"/"expired" для срока, "low"/"out" для трафика.
func subStages(now time.Time, used, total int64, expire time.Time) (exp, traffic string) {
	if !expire.IsZero() {
		switch left := expire.Sub(now); {
		case left <= 0:
			exp = "expired"
		case left <= subExpireSoon:
			exp = "soon"
		}
	}
	if total > 0 {
		switch left := total - used; {
		case left <= 0:
			traffic = "out"
		case float64(left)/float64(total) < subTrafficLow:
			traffic = "low"
		}
	}
	return exp, traffic
}

// noteSubscription публикует событие EvSubscription, если у источника наступила новая стадия срока или трафика.
func (c *Core) noteSubscription(name, title string, used, total int64, expire time.Time) {
	now := c.cfg.Now()
	exp, traffic := subStages(now, used, total, expire)
	label := title
	if label == "" {
		label = name
	}
	check := func(kind, stage, reset string, extra map[string]any) {
		key := "subwarn." + name + "." + kind
		prev, _, _ := c.st.Setting(key)
		cur := ""
		if stage != "" {
			cur = stage + "|" + reset // reset: срок или объём, с которым связана стадия; новая подписка — новое предупреждение
		}
		if cur == prev {
			return
		}
		_ = c.st.SetSetting(key, cur)
		if cur == "" {
			return
		}
		d := map[string]any{"source": label, "kind": kind, "stage": stage}
		for k, v := range extra {
			d[k] = v
		}
		c.bus.Publish(store.Event{Kind: EvSubscription, Initiator: "auto", Detail: detail(d)})
	}
	check("expire", exp, strconv.FormatInt(expire.Unix(), 10), map[string]any{"days": int(expire.Sub(now).Hours() / 24)})
	check("traffic", traffic, strconv.FormatInt(total, 10), map[string]any{"left_pct": leftPct(used, total)})
}

func leftPct(used, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(max(total-used, 0) * 100 / total)
}

// SubscriptionText — текст уведомления по Detail события EvSubscription (общий для всех платформ).
func SubscriptionText(kind, stage, source string, days, leftPct int) (summary, body string) {
	switch {
	case kind == "expire" && stage == "expired":
		return "Подписка закончилась", fmt.Sprintf("Срок подписки «%s» истёк: новые узлы из неё не придут.", source)
	case kind == "expire":
		when := "скоро"
		switch {
		case days <= 0:
			when = "сегодня"
		case days == 1:
			when = "завтра"
		default:
			when = fmt.Sprintf("через %d %s", days, daysWord(days))
		}
		return "Подписка скоро закончится", fmt.Sprintf("Подписка «%s» истекает %s.", source, when)
	case stage == "out":
		return "Трафик подписки закончился", fmt.Sprintf("В подписке «%s» не осталось трафика.", source)
	default:
		return "Трафик подписки на исходе", fmt.Sprintf("В подписке «%s» осталось около %d%% трафика.", source, leftPct)
	}
}

func daysWord(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 14:
		return "дней"
	case n%10 == 1:
		return "день"
	case n%10 >= 2 && n%10 <= 4:
		return "дня"
	}
	return "дней"
}
