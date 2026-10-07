//go:build !windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultDataDirectory is the data directory before resolve expands "~/".
func defaultDataDirectory() string { return "~/.local/share/agentbus" }

// defaultConfigPath is ~/.config/agentbus/config.json (not XDG_CONFIG_HOME,
// unchanged from before Windows support).
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "agentbus", "config.json"), nil
}
