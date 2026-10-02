// Package procs reads just enough of the process table to tell whether a
// process is still the one we remember: its parent, command name, and start
// time. agentbus wait uses it to replace an earlier wait for the same identity
// without ever signaling a process that merely reuses the old pid.
package procs

import "errors"

// ErrGone means the process does not exist (any more).
var ErrGone = errors.New("process gone")

// Table reads the process table. System reads the real one; tests inject a
// fake. StartTime is opaque: only equality between two reads of the same pid
// is meaningful, and 0 means the platform cannot tell.
type Table interface {
	Parent(pid int) (int, error)
	Comm(pid int) (string, error)
	StartTime(pid int) (int64, error)
}

// System is the real process table of this machine.
var System Table = systemTable{}

// Ref identifies one process incarnation: a pid alone can be reused.
type Ref struct {
	Pid   int
	Start int64
}

// Self returns the Ref of the calling process.
func Self(t Table, pid int) Ref {
	start, _ := t.StartTime(pid) // 0 when unknown: matches any incarnation
	return Ref{Pid: pid, Start: start}
}

// Alive reports whether r's process exists and is the same incarnation: the
// start time still equals r.Start. A zero r.Start (unknown) matches any
// process with that pid.
func Alive(t Table, r Ref) bool {
	if r.Pid <= 0 {
		return false
	}
	start, err := t.StartTime(r.Pid)
	if err != nil {
		return false
	}
	return r.Start == 0 || start == 0 || start == r.Start
}
