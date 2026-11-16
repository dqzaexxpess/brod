//go:build !linux && !windows

package core

func (c *Core) spawnInstaller(InstallSpec) error { return errInstallUnsupported }
