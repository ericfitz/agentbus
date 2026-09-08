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
