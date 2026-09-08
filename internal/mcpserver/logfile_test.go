package mcpserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingWriterRotates(t *testing.T) {
	dir := t.TempDir()
	w := &rotatingWriter{path: filepath.Join(dir, "agentbus.log"), maxBytes: 100, keep: 4}
	for i := 0; i < 50; i++ {
		w.Write([]byte("0123456789\n"))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) < 2 || len(entries) > 4 {
		t.Fatalf("expected 2-4 files, got %d", len(entries))
	}
}

// TestRotatingWriterRecoversAfterFailedRotate covers fix round 1 finding 3:
// after w.f.Close() succeeds but a rename during rotation fails, the next
// Write must reopen path rather than write through the closed handle.
func TestRotatingWriterRecoversAfterFailedRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbus.log")
	// Seed rotation state so the very first rename rotate attempts
	// (path.3 -> path.4) fails: path.3 is a regular file and path.4 is a
	// non-empty directory, so the OS refuses to rename the file onto it.
	if err := os.WriteFile(path+".3", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".4", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+".4", "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// maxBytes = 12: the first write (10 bytes) stays under it, the second
	// (3 more bytes) pushes it over and triggers the seeded rotation
	// failure, and the third (1 more byte, appended after a reopen) stays
	// under it again so it does not retrigger the same permanent collision
	// — isolating "did the writer recover" from "did the collision clear".
	w := &rotatingWriter{path: path, maxBytes: 12, keep: 4}
	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("xxx")); err == nil {
		t.Fatal("expected the seeded rotation collision to fail")
	}
	if _, err := w.Write([]byte("y")); err != nil {
		t.Fatalf("writer did not recover after a failed rotation (stale closed handle?): %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "0123456789y" {
		t.Fatalf("recovery write did not append correctly: got %q", got)
	}
}
