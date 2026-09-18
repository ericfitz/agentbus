package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/config"
)

// mkdirAll and writeFile are t.MkdirAll/os.WriteFile for test setup with the
// error actually checked, instead of the `_ = ...` this file used to drop.
func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeIdentityFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityWalksUpAndDefaultsToRepoName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myrepo")
	sub := filepath.Join(root, "a", "b")
	mkdirAll(t, sub)
	mkdirAll(t, filepath.Join(root, ".git"))
	var out bytes.Buffer
	if err := Identity(sub, &out); err != nil || out.String() != identityLine("myrepo") {
		t.Fatalf("%q %v", out.String(), err)
	}
	mkdirAll(t, filepath.Join(root, ".local"))
	writeIdentityFile(t, filepath.Join(root, ".local", "agentbus.json"), `{"identity": "Sam"}`)
	out.Reset()
	if err := Identity(sub, &out); err != nil {
		t.Fatal(err)
	}
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
	mkdirAll(t, filepath.Join(outer, ".git"))
	mkdirAll(t, filepath.Join(inner, ".git"))
	var out bytes.Buffer
	if err := Identity(inner, &out); err != nil || out.String() != identityLine("inner") {
		t.Fatalf("%q %v", out.String(), err)
	}

	// Outer identity file must not leak into the inner repo's default.
	mkdirAll(t, filepath.Join(outer, ".local"))
	writeIdentityFile(t, filepath.Join(outer, ".local", "agentbus.json"), `{"identity": "OuterName"}`)
	out.Reset()
	if err := Identity(inner, &out); err != nil || out.String() != identityLine("inner") {
		t.Fatalf("%q %v", out.String(), err)
	}

	// An identity file at the inner (nearest) level still applies.
	mkdirAll(t, filepath.Join(inner, ".local"))
	writeIdentityFile(t, filepath.Join(inner, ".local", "agentbus.json"), `{"identity": "InnerName"}`)
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
	mkdirAll(t, filepath.Join(root, ".git"))
	mkdirAll(t, filepath.Join(root, ".local"))
	identityPath := filepath.Join(root, ".local", "agentbus.json")

	// Malformed JSON: warn and fall back to the repo basename.
	writeIdentityFile(t, identityPath, `{not json`)
	var out, warn bytes.Buffer
	if err := identity(root, &out, &warn); err != nil || out.String() != identityLine("myrepo") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if warn.Len() == 0 {
		t.Fatal("malformed identity file must warn")
	}

	// Identity value fails the name rule (contains '/'): warn and fall back.
	writeIdentityFile(t, identityPath, `{"identity": "a/b"}`)
	out.Reset()
	warn.Reset()
	if err := identity(root, &out, &warn); err != nil || out.String() != identityLine("myrepo") {
		t.Fatalf("%q %v", out.String(), err)
	}
	if warn.Len() == 0 {
		t.Fatal("invalid identity name must warn")
	}

	// A valid identity produces no warning.
	writeIdentityFile(t, identityPath, `{"identity": "Sam"}`)
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
		"- Post when you start, finish, or get blocked",
		"Use general only when you have no\n  project channel",
		"- Search all your subscribed memory channels",
		"- Post to a memory channel whenever you discover a non-obvious fact",
		"- For work shared between agents, use a task list",
		"- Register returns the other live agents in \"others\"; call discover only to",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("must end with newline")
	}
	if !strings.Contains(InitPrompt, protocol) {
		t.Fatal("InitPrompt must embed protocol")
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
