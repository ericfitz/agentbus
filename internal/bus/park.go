package bus

import (
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Park state (ADR 0025 decision 3) is the file <data dir>/park/<encoded
// identity>, so the schema does not change. Existence is the whole state. Only
// top-level identities can be parked.

func (b *Bus) parkDir() string { return filepath.Join(b.cfg.DataDirectory, "park") }

// parkEnc is lowercase unpadded base32 (a-z, 2-7): it is safe on every file
// system, including case-insensitive ones and Windows, and a 128-byte name
// encodes to 205 characters, under the 255 limit.
var parkEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// parkTempPrefix starts every temp file; '.' and '-' never occur in an
// encoded name, so a temp file cannot collide with one.
const parkTempPrefix = ".tmp-"

// parkFileName is the file name for identity as: the only place identities
// are encoded. It is injective, so no two identities share a file.
func parkFileName(as string) string {
	return strings.ToLower(parkEnc.EncodeToString([]byte(as)))
}

// parkPath is the park file for as, or an error when as cannot be one: it
// must pass NameRule (so no '/'). The identity is encoded (parkFileName), so
// the path is always a plain file name directly inside park/.
func (b *Bus) parkPath(as string) (string, error) {
	if err := NameRule(as); err != nil {
		return "", errf("validation", false, "name %v", err)
	}
	return filepath.Join(b.parkDir(), parkFileName(as)), nil
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
	b.sweepParkTemps()
	f, err := os.CreateTemp(b.parkDir(), parkTempPrefix)
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

// parkTempMaxAge is how old a temp file must be before Park sweeps it: a
// park killed between CreateTemp and Rename leaves one behind, and a live
// Park finishes within milliseconds.
const parkTempMaxAge = time.Minute

// sweepParkTemps removes temp files older than parkTempMaxAge. Failures are
// ignored: a leftover temp file is harmless.
func (b *Bus) sweepParkTemps() {
	ents, err := os.ReadDir(b.parkDir())
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), parkTempPrefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > parkTempMaxAge {
			_ = os.Remove(filepath.Join(b.parkDir(), e.Name()))
		}
	}
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
