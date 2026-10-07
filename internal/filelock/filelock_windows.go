//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// Lock takes an exclusive LockFileEx lock on the first byte of f (a byte
// range may extend past EOF, so an empty lock file works), blocking until
// it is free. Closing f releases it.
func Lock(f *os.File) error {
	h := windows.Handle(f.Fd())
	if h == windows.InvalidHandle {
		return os.ErrClosed
	}
	return windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}
