//go:build unix

package filelock

import (
	"os"
	"syscall"
)

// Lock takes an exclusive flock on f, blocking until it is free. Closing f
// releases it (the lock belongs to the open file description).
func Lock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}
