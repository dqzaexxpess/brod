//go:build windows

package api

func setUmask(m int) int { return 0 }
