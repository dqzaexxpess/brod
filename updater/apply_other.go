//go:build !linux

package updater

import "errors"

func pidAlive(int) bool     { return false }
func helperInstalled() bool { return false }
func startDetached(string) error {
	return errors.New("не поддерживается на этой платформе")
}
