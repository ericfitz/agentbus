package filelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A second handle blocks in Lock until the first handle is closed; closing
// is the only release.
func TestLockExcludesSecondHandleUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a := open(t, path)
	if err := Lock(a); err != nil {
		t.Fatal(err)
	}
	b := open(t, path)
	defer func() { _ = b.Close() }()
	got := make(chan error, 1)
	go func() { got <- Lock(b) }()
	select {
	case err := <-got:
		t.Fatalf("second lock acquired while the first is held (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("second lock after close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second lock never acquired after the first handle closed")
	}
}

// Locking an empty file works (the lock covers a byte range past EOF on
// Windows; flock does not care).
func TestLockEmptyFile(t *testing.T) {
	f := open(t, filepath.Join(t.TempDir(), "empty.lock"))
	defer func() { _ = f.Close() }()
	if err := Lock(f); err != nil {
		t.Fatal(err)
	}
}

// A closed file cannot be locked: the error must surface, not panic.
func TestLockClosedFileErrors(t *testing.T) {
	f := open(t, filepath.Join(t.TempDir(), "closed.lock"))
	_ = f.Close()
	if err := Lock(f); err == nil {
		t.Fatal("Lock on a closed file must fail")
	}
}
