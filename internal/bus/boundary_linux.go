//go:build linux

package bus

import (
	"os"
	"path/filepath"
	"strings"
)

// isWSL detects WSL from /proc: the WSLInterop binfmt entry, or "microsoft"
// in the kernel release. readFile is injected for tests.
func isWSL(readFile func(string) ([]byte, error)) bool {
	if _, err := readFile("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
		return true
	}
	b, err := readFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// platformBoundary refuses, under WSL, a directory whose mount is a Windows
// drive (9p or drvfs). Anything that cannot be determined is allowed.
func platformBoundary(dir string) (fs, suggestion string, refused bool) {
	if !isWSL(os.ReadFile) {
		return "", "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	mi, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", "", false
	}
	t, ok := mountFSType(string(mi), abs)
	if ok && (t == "9p" || t == "drvfs") {
		return t + " (a Windows drive mounted in WSL)", "$HOME/.local/share/agentbus", true
	}
	return "", "", false
}
