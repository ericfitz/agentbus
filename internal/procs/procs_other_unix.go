//go:build unix && !darwin && !linux

package procs

import (
	"errors"
	"syscall"
)

// exists reports whether pid is a live process (kill(pid, 0); EPERM means it
// exists but belongs to someone else).
func exists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
