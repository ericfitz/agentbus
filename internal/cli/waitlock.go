package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// lockFile takes an exclusive filelock on path (created if missing) and returns
// the release func. It guards only the read-terminate-write sequence in
// acquireWaitLock and the compare-remove in release, never a whole wait, so a
// crashed wait cannot hold it.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil // closing also releases the lock
}

// Where an identity came from, recorded in the pidfile so an unexplained wait
// can be diagnosed from it.
const (
	SourceAs      = "as"      // given with -as
	SourceSession = "session" // resolved through the harness session lookup
	SourceCwd     = "cwd"     // the working-directory fallback
)

func validSource(s string) bool {
	return s == SourceAs || s == SourceSession || s == SourceCwd
}

// waitRecord is the content of a pidfile: the wait itself, the harness that
// started it (zero for a subagent identity, whose wait is replaced only by
// identity, and for the old two-field form), and the identity source (empty
// in the old form).
type waitRecord struct {
	Self    procs.Ref
	Harness procs.Ref
	Source  string
}

// readPidfile parses `pid start` (old form) or `pid start harness_pid
// harness_start source`. Anything else is treated as absent. The source is
// diagnostic only, so any value is accepted on read; the writer emits only
// the known ones.
func readPidfile(path string) (waitRecord, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return waitRecord{}, false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 && len(f) != 5 {
		return waitRecord{}, false
	}
	pid, err1 := strconv.Atoi(f[0])
	start, err2 := strconv.ParseInt(f[1], 10, 64)
	if err1 != nil || err2 != nil {
		return waitRecord{}, false
	}
	rec := waitRecord{Self: procs.Ref{Pid: pid, Start: start}}
	if len(f) == 5 {
		hpid, err1 := strconv.Atoi(f[2])
		hstart, err2 := strconv.ParseInt(f[3], 10, 64)
		if err1 != nil || err2 != nil {
			return waitRecord{}, false
		}
		rec.Harness = procs.Ref{Pid: hpid, Start: hstart}
		rec.Source = f[4]
	}
	return rec, true
}

// lockWaitDir takes the directory-wide lock, then the per-identity lock for
// path, in that order, and returns the release func for both. The per-identity
// lock stays for exclusion against older binaries, which take only it.
func lockWaitDir(path string) (func(), error) {
	unlockDir, err := lockFile(filepath.Join(filepath.Dir(path), ".lock"))
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		unlockDir()
		return nil, err
	}
	return func() { unlock(); unlockDir() }, nil
}

// acquireWaitLock makes self the one wait for identity as: it records self in
// the pidfile and stops the wait it replaces (terminateWait: SIGTERM on Unix,
// TerminateProcess with exit code 3 on Windows), if that process is still
// alive. The recorded start time guards against a reused pid, so a
// stale pidfile (process gone, or the pid now someone else's) is simply
// overwritten and never terminates anything. A top-level identity (no '/')
// with a known harness also stops every other live wait recorded with the
// same harness (pid and start), whatever identity it resolved: a session
// whose identity changed under a live harness must not leave its old wait
// behind (ADR 0014 amendment). Waits for subagent identities record no
// harness and neither scan nor are found by the scan. source says how as was
// chosen (SourceAs, SourceSession or SourceCwd) and is recorded for
// diagnosis; any other value is an error. The whole sequence runs under the
// wait directory's lock and then the identity's lock, so waits for different
// identities from one harness cannot interleave. The returned release removes
// the pidfile only if it still names self. Callers install their signal
// handler before calling, so a replacement signal cannot arrive unhandled.
func acquireWaitLock(dataDir, as string, self, harness procs.Ref, source string) (release func(), err error) {
	if !validSource(source) {
		return nil, fmt.Errorf("wait: unknown identity source %q", source)
	}
	path := waitPidfile(dataDir, as)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	unlock, err := lockWaitDir(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if old, ok := readPidfile(path); ok && old.Self != self && procs.Alive(procs.System, old.Self) {
		_ = terminateWait(old.Self.Pid) // already gone is fine
	}
	if strings.Contains(as, "/") {
		harness = procs.Ref{}
	} else if harness.Pid > 0 {
		others, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.pid"))
		for _, p := range others {
			if p == path {
				continue
			}
			if rec, ok := readPidfile(p); ok && rec.Harness == harness && rec.Self != self && procs.Alive(procs.System, rec.Self) {
				_ = terminateWait(rec.Self.Pid)
			}
		}
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, self.Pid)
	line := fmt.Appendf(nil, "%d %d %d %d %s\n", self.Pid, self.Start, harness.Pid, harness.Start, source)
	if err := os.WriteFile(tmp, line, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return func() {
		unlock, err := lockWaitDir(path)
		if err != nil {
			return
		}
		defer unlock()
		if cur, ok := readPidfile(path); ok && cur.Self == self {
			_ = os.Remove(path)
		}
	}, nil
}
