//go:build !darwin && !linux

package procs

import (
	"errors"
	"syscall"
)

type systemTable struct{}

var errUnsupported = errors.New("procs: process table not supported on this platform")

func (systemTable) Parent(int) (int, error)  { return 0, errUnsupported }
func (systemTable) Comm(int) (string, error) { return "", errUnsupported }

// StartTime returns 0 (unknown) for a live pid, so Alive degrades to a plain
// existence check.
func (systemTable) StartTime(pid int) (int64, error) {
	if !exists(pid) {
		return 0, ErrGone
	}
	return 0, nil
}

// exists reports whether pid is a live process.
func exists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
