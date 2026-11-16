//go:build !windows

package killswitch

import "errors"

// WFP — киллсвитч Windows (wfp_windows.go); на других системах не работает, тип нужен, чтобы общий код собирался.
type WFP struct {
	AppPath string
	Dynamic bool
}

var errNoWFP = errors.New("WFP есть только на Windows")

// InterfaceLUID — на других системах LUID нет.
func InterfaceLUID(string) (uint64, error) { return 0, errNoWFP }

func (w *WFP) Install(Config) error    { return errNoWFP }
func (w *WFP) Remove() error           { return nil }
func (w *WFP) Release() error          { return errNoWFP }
func (w *WFP) Close() error            { return nil }
func (w *WFP) Status() (string, error) { return "", errNoWFP }
func (w *WFP) Probe() (string, error)  { return "", errNoWFP }
func (w *WFP) BindsInterface() bool    { return true }
