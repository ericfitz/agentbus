package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/config"
)

func TestIdentityWalksUpAndDefaultsToRepoName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myrepo")
	sub := filepath.Join(root, "a", "b")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	var out bytes.Buffer
	if err := Identity(sub, &out); err != nil || out.String() != identityLine("myrepo") {
		t.Fatalf("%q %v", out.String(), err)
	}
	_ = os.MkdirAll(filepath.Join(root, ".local"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".local", "agentbus.json"), []byte(`{"identity": "Sam"}`), 0o600)
	out.Reset()
	_ = Identity(sub, &out)
	if out.String() != identityLine("Sam") {
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
	_ = os.MkdirAll(filepath.Join(outer, ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(inner, ".git"), 0o755)
	var out bytes.Buffer
	if err := Identity(inner, &out); err != nil || out.String() != identityLine("inner") {
		t.Fatalf("%q %v", out.String(), err)
	}

	// Outer identity file must not leak into the inner repo's default.
	_ = os.MkdirAll(filepath.Join(outer, ".local"), 0o755)
	_ = os.WriteFile(filepath.Join(outer, ".local", "agentbus.json"), []byte(`{"identity": "OuterName"}`), 0o600)
	out.Reset()
	if err := Identity(inner, &out); err != nil || out.String() != identityLine("inner") {
		t.Fatalf("%q %v", out.String(), err)
	}

	// An identity file at the inner (nearest) level still applies.
	_ = os.MkdirAll(filepath.Join(inner, ".local"), 0o755)
	_ = os.WriteFile(filepath.Join(inner, ".local", "agentbus.json"), []byte(`{"identity": "InnerName"}`), 0o600)
	out.Reset()
	if err := Identity(inner, &out); err != nil || out.String() != identityLine("InnerName") {
		t.Fatalf("%q %v", out.String(), err)
	}
}

// TestIdentityWarnsOnMalformedOrInvalidIdentity covers the minor fold-in:
// a malformed/unreadable identity file, or an identity failing the name
// rule, must warn (not silently ignore) and fall back to the next level.
func TestIdentityWarnsOnMalformedOrInvalidIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myrepo")
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".local"), 0o755)
	identityPath := filepath.Join(root, ".local", "agentbus.json")

	// Malformed JSON: warn and fall back to the repo basename.
	_ = os.WriteFile(identityPath, []byte(`{not json`), 0o600)
	var out, warn bytes.Buffer
	if err := identity(root, &out, &warn); err != nil || out.String() != identityLine("myrepo") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if warn.Len() == 0 {
		t.Fatal("malformed identity file must warn")
	}

	// Identity value fails the name rule (contains '/'): warn and fall back.
	_ = os.WriteFile(identityPath, []byte(`{"identity": "a/b"}`), 0o600)
	out.Reset()
	warn.Reset()
	if err := identity(root, &out, &warn); err != nil || out.String() != identityLine("myrepo") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if warn.Len() == 0 {
		t.Fatal("invalid identity name must warn")
	}

	// A valid identity produces no warning.
	_ = os.WriteFile(identityPath, []byte(`{"identity": "Sam"}`), 0o600)
	out.Reset()
	warn.Reset()
	if err := identity(root, &out, &warn); err != nil || out.String() != identityLine("Sam") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if warn.Len() != 0 {
		t.Fatalf("valid identity must not warn, got %q", warn.String())
	}
}

func TestIdentityLineIsPrescriptive(t *testing.T) {
	got := identityLine("Sam")
	for _, want := range []string{
		`Agentbus: call the register tool now with the name parameter set to "Sam"`,
		"Then follow this protocol:",
		"- Call receive right after registering",
		"- Post to your subscribed chat channel",
		"- Search all your subscribed memory channels",
		"- Post to a memory channel whenever you discover a non-obvious fact",
		"- Call discover before assuming you are the only agent working.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("must end with newline")
	}
	if !strings.Contains(InitPrompt, Protocol) {
		t.Fatal("InitPrompt must embed Protocol")
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
