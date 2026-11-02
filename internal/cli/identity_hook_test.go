package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/procs"
)

// ADR 0025 decision 10: the SessionStart hook names the live session the
// harness holds, not the cwd/config name.
func TestIdentityHookNamesHeldSession(t *testing.T) {
	cfg, cwd, _, second, _ := twoSessions(t)
	hook := func(c *config.Config) string {
		var out bytes.Buffer
		if err := identity(c, cwd, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	cwdName := IdentityName(cwd, io.Discard)
	// Held name differs from the cwd name.
	setHarness(t, procs.Ref{Pid: 200, Start: 2}, nil)
	if got := hook(&cfg); got != identityLine(second) || second == cwdName {
		t.Fatalf("held session: %q", got)
	}
	// Harness holds nothing and the cwd name is live under another harness.
	setHarness(t, procs.Ref{Pid: 300, Start: 3}, nil)
	if got := hook(&cfg); got != identityLine(cwdName) {
		t.Fatalf("name held by another harness: %q", got)
	}
	// findHarness fails.
	setHarness(t, procs.Ref{}, procs.ErrGone)
	if got := hook(&cfg); got != identityLine(cwdName) {
		t.Fatalf("no harness: %q", got)
	}
	// Bus cannot open: the data directory is a file.
	bad := config.Default()
	bad.DataDirectory = filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad.DataDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	setHarness(t, procs.Ref{Pid: 200, Start: 2}, nil)
	var badOut, badWarn bytes.Buffer
	if err := identity(&bad, cwd, &badOut, &badWarn); err != nil || badOut.String() != identityLine(cwdName) {
		t.Fatalf("bus cannot open: %q %v", badOut.String(), err)
	}
	if !strings.HasPrefix(badWarn.String(), "agentbus: ") {
		t.Fatalf("bus Open failure must warn, got %q", badWarn.String())
	}
}

// With an empty bus and no held session, the cwd name is printed.
func TestIdentityHookHarnessHoldsNothing(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cwd := t.TempDir()
	setHarness(t, procs.Ref{Pid: 400, Start: 4}, nil)
	var out bytes.Buffer
	if err := identity(&cfg, cwd, &out, io.Discard); err != nil || out.String() != identityLine(filepath.Base(cwd)) {
		t.Fatalf("%q %v", out.String(), err)
	}
}

// A malformed identity file is warned about once, not once per lookup.
func TestIdentityHookWarnsOnce(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	root := filepath.Join(t.TempDir(), "myrepo")
	mkdirAll(t, filepath.Join(root, ".git"))
	mkdirAll(t, filepath.Join(root, ".local"))
	writeIdentityFile(t, filepath.Join(root, ".local", "agentbus.json"), `{not json`)
	setHarness(t, procs.Ref{Pid: 400, Start: 4}, nil)
	var out, warn bytes.Buffer
	if err := identity(&cfg, root, &out, &warn); err != nil || out.String() != identityLine("myrepo") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if n := strings.Count(warn.String(), "\n"); n != 1 {
		t.Fatalf("want 1 warning line, got %d: %q", n, warn.String())
	}
}
