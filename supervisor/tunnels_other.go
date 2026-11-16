//go:build !linux && !windows

package supervisor

// На macOS и прочих системах обнаружение чужих туннелей пока не реализовано.
func foreignTunnels(string) []string { return nil }
