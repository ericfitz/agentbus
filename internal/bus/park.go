package bus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Park state (ADR 0025 decision 3) is the file <data dir>/park/<identity>,
// so the schema does not change. Existence is the whole state. Only
// top-level identities can be parked.

func (b *Bus) parkDir() string { return filepath.Join(b.cfg.DataDirectory, "park") }

// parkPath is the park file for as, or an error when as cannot be one:
// it must pass NameRule (so no '/'), and "." and ".." are not file names.
func (b *Bus) parkPath(as string) (string, error) {
	if err := NameRule(as); err != nil {
		return "", errf("validation", false, "name %v", err)
	}
	if as == "." || as == ".." {
		return "", errf("validation", false, "name %q cannot be parked", as)
	}
	return filepath.Join(b.parkDir(), as), nil
}

// Park parks as: only its direct messages wake it. It writes the file
// atomically (temp file, then rename), so overlapping parks leave one
// complete file; parking a parked identity succeeds and changes nothing.
func (b *Bus) Park(as string) error {
	path, err := b.parkPath(as)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b.parkDir(), 0o700); err != nil {
		return internal(err)
	}
	f, err := os.CreateTemp(b.parkDir(), ".tmp-")
	if err != nil {
		return internal(err)
	}
	tmp := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return internal(fmt.Errorf("park %q: %w", as, err))
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return internal(fmt.Errorf("park %q: %w", as, err))
	}
	return nil
}

// Unpark ends as's park. It succeeds when as is not parked.
func (b *Bus) Unpark(as string) error {
	path, err := b.parkPath(as)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return internal(fmt.Errorf("unpark %q: %w", as, err))
	}
	return nil
}

// Parked reports whether as is parked. A name that cannot be parked, or a
// park file that cannot be read, is not parked.
func (b *Bus) Parked(as string) bool {
	path, err := b.parkPath(as)
	if err != nil {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// clearParks removes every park file (Reset).
func (b *Bus) clearParks() error {
	if err := os.RemoveAll(b.parkDir()); err != nil {
		return internal(err)
	}
	return nil
}
