//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsWindows(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\pat\AppData\Local`)
	t.Setenv("APPDATA", `C:\Users\pat\AppData\Roaming`)
	if got := Default().DataDirectory; got != `C:\Users\pat\AppData\Local\agentbus` {
		t.Fatalf("data directory = %q", got)
	}
	p, err := DefaultPath()
	if err != nil || p != `C:\Users\pat\AppData\Roaming\agentbus\config.json` {
		t.Fatalf("DefaultPath = %q, %v", p, err)
	}
	// Without LOCALAPPDATA the default stays a usable ~-relative path that
	// validate accepts and resolve expands.
	t.Setenv("LOCALAPPDATA", "")
	if got := Default().DataDirectory; got != "~/AppData/Local/agentbus" {
		t.Fatalf("data directory without LOCALAPPDATA = %q", got)
	}
	cfg, _, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if cfg.DataDirectory != filepath.Join(home, "AppData", "Local", "agentbus") {
		t.Fatalf("resolved data directory = %q", cfg.DataDirectory)
	}
}
