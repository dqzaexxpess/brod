package core

import "sync"

// Activity — что демон делает прямо сейчас (для индикации в приложении): обновление подписок, круг проверки узлов, круг проверки
// прокси Telegram. Прогресс — «сделано из отобрано» и сколько из сделанных живых.
type Activity struct {
	Refreshing  bool `json:"refreshing"`
	Checking    bool `json:"checking"`
	CheckPicked int  `json:"check_picked"`
	CheckDone   int  `json:"check_done"`
	CheckOK     int  `json:"check_ok"`
	TGChecking  bool `json:"tg_checking"`
	TGPicked    int  `json:"tg_picked"`
	TGDone      int  `json:"tg_done"`
	TGOK        int  `json:"tg_ok"`
}

type activityState struct {
	mu sync.Mutex
	a  Activity
}

func (s *activityState) get() Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.a
}

func (s *activityState) update(f func(a *Activity)) {
	s.mu.Lock()
	f(&s.a)
	s.mu.Unlock()
}

// Activity возвращает текущую активность демона.
func (c *Core) Activity() Activity { return c.act.get() }
