//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsUnix(t *testing.T) {
	if got := Default().DataDirectory; got != "~/.local/share/agentbus" {
		t.Fatalf("data directory = %q", got)
	}
	home, _ := os.UserHomeDir()
	p, err := DefaultPath()
	if err != nil || p != filepath.Join(home, ".config", "agentbus", "config.json") {
		t.Fatalf("DefaultPath = %q, %v", p, err)
	}
}
