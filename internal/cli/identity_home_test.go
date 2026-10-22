package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// fakeHome points the home-directory and user-name seams at a temp home.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oldHome, oldUser := userHomeDir, tuiUserName
	userHomeDir = func() (string, error) { return home, nil }
	tuiUserName = func() string { return "eric" }
	t.Cleanup(func() { userHomeDir, tuiUserName = oldHome, oldUser })
	return home
}

func TestIdentityNameHomeNoRepo(t *testing.T) {
	home := fakeHome(t)
	if got := IdentityName(home, io.Discard); got != "eric-home" {
		t.Fatalf("got %q, want eric-home", got)
	}
}

func TestIdentityNameHomeIsGitRoot(t *testing.T) {
	home := fakeHome(t)
	if err := os.Mkdir(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(home, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, sub} {
		if got := IdentityName(dir, io.Discard); got != "eric-home" {
			t.Fatalf("%s: got %q, want eric-home", dir, got)
		}
	}
}

func TestIdentityNameSymlinkedHome(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "homelink")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	fakeHome(t)
	userHomeDir = func() (string, error) { return link, nil }
	if got := IdentityName(real, io.Discard); got != "eric-home" {
		t.Fatalf("got %q, want eric-home", got)
	}
}

func TestIdentityNameHomeSubdirKeepsBasename(t *testing.T) {
	home := fakeHome(t)
	sub := filepath.Join(home, "notes")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := IdentityName(sub, io.Discard); got != "notes" {
		t.Fatalf("got %q, want notes", got)
	}
}

func TestIdentityNameExplicitFileInHomeNotRewritten(t *testing.T) {
	home := fakeHome(t)
	p := filepath.Join(home, ".local", "agentbus.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"identity":"mine"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := IdentityName(home, io.Discard); got != "mine" {
		t.Fatalf("got %q, want mine", got)
	}
}

func TestIdentityNameUnresolvableHomeNotRewritten(t *testing.T) {
	home := fakeHome(t)
	userHomeDir = func() (string, error) { return filepath.Join(home, "missing"), nil }
	if got := IdentityName(home, io.Discard); got != filepath.Base(home) {
		t.Fatalf("got %q, want %q", got, filepath.Base(home))
	}
	userHomeDir = func() (string, error) { return "", os.ErrNotExist }
	if got := IdentityName(home, io.Discard); got != filepath.Base(home) {
		t.Fatalf("got %q, want %q", got, filepath.Base(home))
	}
}
