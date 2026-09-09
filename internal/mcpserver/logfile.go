package mcpserver

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/ericfitz/agentbus/internal/config"
)

// rotatingWriter appends to path and rotates to path.1 .. path.(keep-1) when
// the file exceeds maxBytes. Multiple agentbus processes append to the same
// log file, so every Write holds an exclusive cross-process flock
// (path+".lock") for its whole critical section: reopen path if another
// process rotated it since our last write, rotate if the fresh size calls
// for it, then append. darwin and linux only, matching this project's
// targets. One flock per log line is fine for a local log file.
type rotatingWriter struct {
	path     string
	maxBytes int64
	keep     int

	mu  sync.Mutex // process-local: makes the critical section visible to the race detector, which can't see flock's cross-process ordering
	f   *os.File
	dev uint64
	ino uint64
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	unlock, err := w.lockAcrossProcesses()
	if err != nil {
		return 0, err
	}
	defer unlock()

	if err := w.ensureCurrent(); err != nil {
		return 0, err
	}
	st, err := w.f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size()+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
		if err := w.ensureCurrent(); err != nil {
			return 0, err
		}
	}
	return w.f.Write(p)
}

// lockAcrossProcesses takes an exclusive flock on path+".lock" and returns a
// func that releases it. Closing the lock file also releases the flock (it
// is scoped to the open file description), so the returned func just closes it.
func (w *rotatingWriter) lockAcrossProcesses() (func(), error) {
	lf, err := os.OpenFile(w.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		_ = lf.Close()
		return nil, err
	}
	return func() { _ = lf.Close() }, nil
}

// ensureCurrent opens w.path if this is the first write, or reopens it if
// another process rotated the file since our last write (its (dev, ino) no
// longer match what we have open). Must be called while holding the
// cross-process lock.
func (w *rotatingWriter) ensureCurrent() error {
	if w.f != nil {
		if fi, err := os.Stat(w.path); err == nil {
			if dev, ino, ok := fileIdentity(fi); ok && dev == w.dev && ino == w.ino {
				return nil // still the file we have open
			}
		}
		_ = w.f.Close()
		w.f = nil
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f = f
	w.dev, w.ino, _ = fileIdentity(fi)
	return nil
}

// fileIdentity extracts (device, inode) from a stat result, when the
// platform's FileInfo.Sys() supports it (darwin and linux both do).
func fileIdentity(fi os.FileInfo) (dev, ino uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}

// rotate shifts path.1..path.(keep-1) up by one, archives the current file
// as path.1, and drops the file beyond keep. Must be called while holding
// the cross-process lock; leaves w.f closed and nil (ensureCurrent reopens
// it, including after a rotate that partially failed).
func (w *rotatingWriter) rotate() error {
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			return err
		}
		w.f = nil
	}
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
// files) and returns a text-handler logger writing to it. Never writes to
// stdout or stderr.
func OpenLog(cfg config.Config) (*slog.Logger, error) {
	if err := os.MkdirAll(cfg.DataDirectory, 0o700); err != nil {
		return nil, err
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	w := &rotatingWriter{path: filepath.Join(cfg.DataDirectory, "agentbus.log"), maxBytes: 16 << 20, keep: 4}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}
