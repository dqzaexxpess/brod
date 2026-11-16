// Package netwatch сообщает о смене сети (интерфейсы, адреса, маршруты) без опроса. На Linux — подписка netlink (RTMGRP_*);
// на остальных системах и при ошибке подписки канал не выдаёт событий, а вызывающий продолжает опрашивать сам (запасной путь).
package netwatch

import (
	"context"
	"time"
)

// Debounce сворачивает всплески событий (при смене сети их приходят десятки за доли секунды) в одно событие через d после последнего.
func Debounce(ctx context.Context, in <-chan struct{}, d time.Duration) <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		var timer *time.Timer
		var fire <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case _, ok := <-in:
				if !ok {
					return
				}
				if timer == nil {
					timer = time.NewTimer(d)
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(d)
				}
				fire = timer.C
			case <-fire:
				fire = nil
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
