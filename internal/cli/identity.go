// Package cli implements the status, reset, and identity commands.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ericfitz/agentbus/internal/bus"
)

// Identity prints the registration prompt for cwd: the identity in the
// nearest .local/agentbus.json walking up, else the basename of the nearest
// git repository root, else the basename of cwd. The walk stops at the
// nearest .git even if no identity file was found there, so a nested
// repository reports its own (inner) name rather than an enclosing repo's.
// A malformed or unreadable identity file, or one whose identity fails the
// name rule, is reported to stderr and treated as absent.
func Identity(cwd string, out io.Writer) error {
	return identity(cwd, out, os.Stderr)
}

func identity(cwd string, out, warn io.Writer) error {
	name := filepath.Base(cwd)
	dir := cwd
	for {
		if got, ok := readIdentityFile(dir, warn); ok {
			name = got
			break
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

// readIdentityFile reads dir/.local/agentbus.json, returning (identity,
// true) only when the file is present, parses, and names a valid identity.
// A missing file is silently absent; any other problem (unreadable,
// malformed JSON, or an identity failing validIdentityName) is reported to
// warn and treated as absent, so the walk continues upward.
func readIdentityFile(dir string, warn io.Writer) (string, bool) {
	path := filepath.Join(dir, ".local", "agentbus.json")
	body, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(warn, "agentbus: %s: %v\n", path, err)
		}
		return "", false
	}
	var f struct {
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		fmt.Fprintf(warn, "agentbus: %s: %v\n", path, err)
		return "", false
	}
	if f.Identity == "" {
		return "", false
	}
	if err := bus.NameRule(f.Identity); err != nil {
		fmt.Fprintf(warn, "agentbus: %s: identity %q: %v\n", path, f.Identity, err)
		return "", false
	}
	return f.Identity, true
}
