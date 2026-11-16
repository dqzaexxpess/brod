//go:build windows

package main

import (
	"time"

	"golang.org/x/sys/windows/svc"
)

// serviceName — имя службы Windows (создаёт deploy/windows/install.ps1).
const serviceName = "Brod"

// serviceStop закрывается, когда диспетчер служб просит остановиться (см. baseContext).
var serviceStop = make(chan struct{})

// runAsService запускает body под управлением диспетчера служб, если процесс запущен как служба; иначе возвращает false.
func runAsService(body func()) bool {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		return false
	}
	_ = svc.Run(serviceName, &service{body: body})
	return true
}

type service struct{ body func() }

func (s *service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	done := make(chan struct{})
	go func() {
		s.body()
		close(done)
	}()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				close(serviceStop)
				select {
				case <-done:
				case <-time.After(25 * time.Second):
				}
				return false, 0
			}
		case <-done:
			if quitForUpdate.Load() {
				return false, 0
			}
			// демон завершился сам (ошибка старта и т. п.): код 1, чтобы диспетчер применил действия восстановления
			return false, 1
		}
	}
}
