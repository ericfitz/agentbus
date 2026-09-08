package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus-local/internal/config"
)

func TestIdentityWalksUpAndDefaultsToRepoName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myrepo")
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	var out bytes.Buffer
	if err := Identity(sub, &out); err != nil || out.String() != "Agentbus: call register with name myrepo\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	os.MkdirAll(filepath.Join(root, ".local"), 0o755)
	os.WriteFile(filepath.Join(root, ".local", "agentbus.json"), []byte(`{"identity": "Sam"}`), 0o600)
	out.Reset()
	Identity(sub, &out)
	if out.String() != "Agentbus: call register with name Sam\n" {
		t.Fatal(out.String())
	}
}

// TestIdentityStopsAtNearestGitRoot covers ruling P5: a nested repository
// reports the INNER (nearest) repo's basename, not the outer one, and an
// identity file present only at the outer level does not apply to the inner
// repo since the walk stops at the nearest .git.
func TestIdentityStopsAtNearestGitRoot(t *testing.T) {
	outer := filepath.Join(t.TempDir(), "outer")
	inner := filepath.Join(outer, "inner")
	os.MkdirAll(filepath.Join(outer, ".git"), 0o755)
	os.MkdirAll(filepath.Join(inner, ".git"), 0o755)
	var out bytes.Buffer
	if err := Identity(inner, &out); err != nil || out.String() != "Agentbus: call register with name inner\n" {
		t.Fatalf("%q %v", out.String(), err)
	}

	// Outer identity file must not leak into the inner repo's default.
	os.MkdirAll(filepath.Join(outer, ".local"), 0o755)
	os.WriteFile(filepath.Join(outer, ".local", "agentbus.json"), []byte(`{"identity": "OuterName"}`), 0o600)
	out.Reset()
	if err := Identity(inner, &out); err != nil || out.String() != "Agentbus: call register with name inner\n" {
		t.Fatalf("%q %v", out.String(), err)
	}

	// An identity file at the inner (nearest) level still applies.
	os.MkdirAll(filepath.Join(inner, ".local"), 0o755)
	os.WriteFile(filepath.Join(inner, ".local", "agentbus.json"), []byte(`{"identity": "InnerName"}`), 0o600)
	out.Reset()
	if err := Identity(inner, &out); err != nil || out.String() != "Agentbus: call register with name InnerName\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
}

// TestIdentityWarnsOnMalformedOrInvalidIdentity covers the minor fold-in:
// a malformed/unreadable identity file, or an identity failing the name
// rule, must warn (not silently ignore) and fall back to the next level.
func TestIdentityWarnsOnMalformedOrInvalidIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myrepo")
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, ".local"), 0o755)
	identityPath := filepath.Join(root, ".local", "agentbus.json")

	// Malformed JSON: warn and fall back to the repo basename.
	os.WriteFile(identityPath, []byte(`{not json`), 0o600)
	var out, warn bytes.Buffer
	if err := identity(root, &out, &warn); err != nil || out.String() != "Agentbus: call register with name myrepo\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if warn.Len() == 0 {
		t.Fatal("malformed identity file must warn")
	}

	// Identity value fails the name rule (contains '/'): warn and fall back.
	os.WriteFile(identityPath, []byte(`{"identity": "a/b"}`), 0o600)
	out.Reset()
	warn.Reset()
	if err := identity(root, &out, &warn); err != nil || out.String() != "Agentbus: call register with name myrepo\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if warn.Len() == 0 {
		t.Fatal("invalid identity name must warn")
	}

	// A valid identity produces no warning.
	os.WriteFile(identityPath, []byte(`{"identity": "Sam"}`), 0o600)
	out.Reset()
	warn.Reset()
	if err := identity(root, &out, &warn); err != nil || out.String() != "Agentbus: call register with name Sam\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if warn.Len() != 0 {
		t.Fatalf("valid identity must not warn, got %q", warn.String())
	}
}

func TestResetRequiresYes(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	var out bytes.Buffer
	if err := Reset(cfg, strings.NewReader("no\n"), &out); err == nil {
		t.Fatal("must refuse without yes")
	}
	if err := Reset(cfg, strings.NewReader("yes\n"), &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Status(cfg, &out); err != nil || !strings.Contains(out.String(), "usage") {
		t.Fatalf("%q %v", out.String(), err)
	}
}
