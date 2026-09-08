package mcpserver

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/ericfitz/agentbus-local/internal/config"
)

// rotatingWriter appends to path and rotates to path.1 .. path.(keep-1) when
// the file exceeds maxBytes. Small and stdlib-only on purpose.
type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return 0, err
		}
		w.f, w.size = f, st.Size()
	}
	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.f.Close(); err != nil {
			return 0, err
		}
		for i := w.keep - 1; i >= 1; i-- {
			if err := os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1)); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
		}
		if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		if err := os.Remove(fmt.Sprintf("%s.%d", w.path, w.keep)); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		w.f, w.size = f, 0
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
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
