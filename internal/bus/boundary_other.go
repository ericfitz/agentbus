//go:build !linux && !windows

package bus

// platformBoundary: no Windows-WSL boundary exists on this platform.
func platformBoundary(string) (fs, suggestion string, refused bool) { return "", "", false }
