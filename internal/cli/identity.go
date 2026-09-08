// Package cli implements the status, reset, and identity commands.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Identity prints the registration prompt for cwd: the identity in the
// nearest .local/agentbus.json walking up, else the basename of the nearest
// git repository root, else the basename of cwd. The walk stops at the
// nearest .git even if no identity file was found there, so a nested
// repository reports its own (inner) name rather than an enclosing repo's.
func Identity(cwd string, out io.Writer) error {
	name := filepath.Base(cwd)
	dir := cwd
	for {
		if body, err := os.ReadFile(filepath.Join(dir, ".local", "agentbus.json")); err == nil {
			var f struct {
				Identity string `json:"identity"`
			}
			if json.Unmarshal(body, &f) == nil && f.Identity != "" {
				name = f.Identity
				break
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			name = filepath.Base(dir)
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	_, err := fmt.Fprintf(out, "Agentbus: call register with name %s\n", name)
	return err
}
