package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericfitz/agentbus/internal/filelock"
	"github.com/ericfitz/agentbus/internal/procs"
)

// waitPidfile is where the wait for identity as records itself:
// <dataDir>/wait/<as>.pid. as is sanitized to a safe file name.
func waitPidfile(dataDir, as string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, as)
	return filepath.Join(dataDir, "wait", name+".pid")
}

// flockFile takes an exclusive flock on path (created if missing) and returns
// the release func. It guards only the read-signal-write sequence in
// acquireWaitLock and the compare-remove in release, never a whole wait, so a
// crashed wait cannot hold it.
func flockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil // closing also releases the flock
}

func readPidfile(path string) (procs.Ref, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return procs.Ref{}, false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return procs.Ref{}, false
	}
	pid, err1 := strconv.Atoi(f[0])
	start, err2 := strconv.ParseInt(f[1], 10, 64)
	if err1 != nil || err2 != nil {
		return procs.Ref{}, false
	}
	return procs.Ref{Pid: pid, Start: start}, true
}

// acquireWaitLock makes self the one wait for identity as: it records self in
// the pidfile and signals SIGTERM to the wait it replaces, if that process is
// still alive. The recorded start time guards against a reused pid, so a
// stale pidfile (process gone, or the pid now someone else's) is simply
// overwritten and never signals anything. The returned release removes the
// pidfile only if it still names self. Callers install their signal handler
// before calling, so a replacement signal cannot arrive unhandled.
func acquireWaitLock(dataDir, as string, self procs.Ref) (release func(), err error) {
	path := waitPidfile(dataDir, as)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	unlock, err := flockFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	if old, ok := readPidfile(path); ok && old != self && procs.Alive(procs.System, old) {
		_ = syscall.Kill(old.Pid, syscall.SIGTERM) // already gone is fine
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, self.Pid)
	if err := os.WriteFile(tmp, fmt.Appendf(nil, "%d %d\n", self.Pid, self.Start), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return func() {
		unlock, err := flockFile(path + ".lock")
		if err != nil {
			return
		}
		defer unlock()
		if cur, ok := readPidfile(path); ok && cur == self {
			_ = os.Remove(path)
		}
	}, nil
}
