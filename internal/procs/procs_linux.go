//go:build linux

package procs

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type systemTable struct{}

// statFields holds what we use from /proc/<pid>/stat.
type statFields struct {
	comm  string
	ppid  int
	start int64
}

// parseStat parses a /proc/<pid>/stat line. comm is parenthesized and may
// itself contain spaces and parentheses, so it runs from the first '(' to the
// LAST ')'; the fields after it are space separated, starting at field 3
// (state).
func parseStat(line string) (statFields, error) {
	open, end := strings.IndexByte(line, '('), strings.LastIndexByte(line, ')')
	if open < 0 || end < open {
		return statFields{}, fmt.Errorf("procs: malformed stat line")
	}
	rest := strings.Fields(line[end+1:])
	// rest[0] is field 3 (state), so field N is rest[N-3].
	if len(rest) < 20 {
		return statFields{}, fmt.Errorf("procs: short stat line")
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return statFields{}, fmt.Errorf("procs: ppid: %w", err)
	}
	start, err := strconv.ParseInt(rest[19], 10, 64)
	if err != nil {
		return statFields{}, fmt.Errorf("procs: starttime: %w", err)
	}
	return statFields{comm: line[open+1 : end], ppid: ppid, start: start}, nil
}

func readStat(pid int) (statFields, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return statFields{}, ErrGone
		}
		return statFields{}, err
	}
	return parseStat(string(b))
}

func (systemTable) Parent(pid int) (int, error) {
	s, err := readStat(pid)
	return s.ppid, err
}

func (systemTable) Comm(pid int) (string, error) {
	s, err := readStat(pid)
	return s.comm, err
}

func (systemTable) StartTime(pid int) (int64, error) {
	s, err := readStat(pid)
	return s.start, err
}
