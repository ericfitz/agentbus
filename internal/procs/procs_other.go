//go:build !darwin && !linux && !windows

package procs

import "errors"

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
