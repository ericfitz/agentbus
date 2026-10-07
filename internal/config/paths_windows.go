//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultDataDirectory is %LocalAppData%\agentbus: Local, not Roaming,
// because the SQLite database must not roam with a domain profile. Without
// LOCALAPPDATA it falls back to the same place relative to the home
// directory, which resolve expands.
func defaultDataDirectory() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "agentbus")
	}
	return "~/AppData/Local/agentbus"
}

// defaultConfigPath is %AppData%\agentbus\config.json (os.UserConfigDir).
func defaultConfigPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve AppData: %w", err)
	}
	return filepath.Join(d, "agentbus", "config.json"), nil
}
