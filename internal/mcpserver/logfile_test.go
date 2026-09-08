package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotatingWriterRotates(t *testing.T) {
	dir := t.TempDir()
	w := &rotatingWriter{path: filepath.Join(dir, "agentbus.log"), maxBytes: 100, keep: 4}
	for i := 0; i < 50; i++ {
		if _, err := w.Write([]byte("0123456789\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lock") { // M12.1's cross-process lock file, not a log
			n++
		}
	}
	if n < 2 || n > 4 {
		t.Fatalf("expected 2-4 log files, got %d (dir has %v)", n, entries)
	}
}

// TestRotatingWriterCoordinatesAcrossProcesses covers fix round 1 finding
// M12.1: independent agentbus processes append to the same log file, so
// rotation and size tracking cannot be process-local. Two rotatingWriter
// instances on one path stand in for two processes; every line either
// writer produces must survive in exactly one retained file, with no file
// exceeding the size limit by more than one write and no more than 4 files.
func TestRotatingWriterCoordinatesAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbus.log")
	w1 := &rotatingWriter{path: path, maxBytes: 200, keep: 4}
	w2 := &rotatingWriter{path: path, maxBytes: 200, keep: 4}

	const total = 60 // small enough that, spread across keep=4 * ~200B files, nothing gets rotated away
	lines := make([]string, total)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%04d\n", i)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	write := func(w *rotatingWriter, start int) {
		defer wg.Done()
		for i := start; i < total; i += 2 {
			if _, err := w.Write([]byte(lines[i])); err != nil {
				t.Errorf("write %d: %v", i, err)
			}
		}
	}
	go write(w1, 0)
	go write(w2, 1)
	wg.Wait()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var logFiles []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "agentbus.log") && !strings.HasSuffix(e.Name(), ".lock") {
			logFiles = append(logFiles, e.Name())
		}
	}
	if len(logFiles) > 4 {
		t.Fatalf("expected at most 4 retained files, got %d: %v", len(logFiles), logFiles)
	}

	const oneWriteSlack = int64(len("line-0000\n"))
	seen := map[string]int{}
	for _, name := range logFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(data)) > w1.maxBytes+oneWriteSlack {
			t.Fatalf("%s is %d bytes, more than one write over the %d-byte limit", name, len(data), w1.maxBytes)
		}
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line != "" {
				seen[line]++
			}
		}
	}
	for _, line := range lines {
		l := strings.TrimRight(line, "\n")
		if seen[l] != 1 {
			t.Fatalf("line %q appeared %d times across retained files, want exactly 1", l, seen[l])
		}
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
