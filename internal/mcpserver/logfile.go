package mcpserver

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/filelock"
)

// rotatingWriter appends to path and rotates to path.1 .. path.(keep-1) when
// the file exceeds maxBytes. Multiple agentbus processes append to the same
// log file, so every Write holds an exclusive cross-process flock
// (path+".lock") for its whole critical section: open path, rotate if its
// size calls for it, append, close. The lock is flock on Unix and LockFileEx
// on Windows (internal/filelock). No handle stays open outside the lock:
// Windows refuses to rename a file another process holds open (Go opens
// files without FILE_SHARE_DELETE), so a long-lived handle would block every
// other process's rotation. One open and one lock per log line is fine for
// a local log file.
type rotatingWriter struct {
	path     string
	maxBytes int64
	keep     int

	mu sync.Mutex // process-local: makes the critical section visible to the race detector, which can't see flock's cross-process ordering
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	unlock, err := w.lockAcrossProcesses()
	if err != nil {
		return 0, err
	}
	defer unlock()

	f, err := w.open()
	if err != nil {
		return 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, err
	}
	if st.Size()+int64(len(p)) > w.maxBytes {
		if err := f.Close(); err != nil {
			return 0, err
		}
		if err := w.rotate(); err != nil {
			return 0, err
		}
		if f, err = w.open(); err != nil {
			return 0, err
		}
	}
	n, err := f.Write(p)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// lockAcrossProcesses takes the exclusive cross-process lock and returns a
// func that releases it. Closing the lock file also releases the flock (it
// is scoped to the open file description), so the returned func just closes it.
func (w *rotatingWriter) lockAcrossProcesses() (func(), error) {
	lf, err := os.OpenFile(w.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(lf); err != nil {
		_ = lf.Close()
		return nil, err
	}
	return func() { _ = lf.Close() }, nil
}

// open opens path for appending, creating it if a rotation (ours or another
// process's) moved it away. Must be called while holding the cross-process
// lock.
func (w *rotatingWriter) open() (*os.File, error) {
	return os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

// rotate shifts path.1..path.(keep-1) up by one, archives the current file
// as path.1, and drops the file beyond keep. Must be called while holding
// the cross-process lock, with path closed; the next Write reopens path,
// including after a rotate that partially failed.
func (w *rotatingWriter) rotate() error {
	for i := w.keep - 1; i >= 1; i-- {
		if err := os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(fmt.Sprintf("%s.%d", w.path, w.keep)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// OpenLog opens the rotating log file in cfg.DataDirectory (four 16 MiB
// files) and returns a JSON-lines logger writing to it, one record per
// line with a UTC RFC3339 millisecond timestamp and this process's pid.
// Never writes to stdout or stderr.
// LogPath is the log file every agentbus process on this data directory
// writes to.
func LogPath(cfg config.Config) string { return filepath.Join(cfg.DataDirectory, "agentbus.log") }

func OpenLog(cfg config.Config) (*slog.Logger, error) {
	if err := os.MkdirAll(cfg.DataDirectory, 0o700); err != nil {
		return nil, err
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	w := &rotatingWriter{path: LogPath(cfg), maxBytes: 16 << 20, keep: 4}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl, ReplaceAttr: utcMillis})
	return slog.New(h).With("pid", os.Getpid()), nil
}

// utcMillis rewrites the top-level time attribute as UTC RFC3339 with
// millisecond precision, e.g. 2026-09-09T17:04:05.123Z.
func utcMillis(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		a.Value = slog.StringValue(a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	}
	return a
}
