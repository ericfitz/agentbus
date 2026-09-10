package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/repoconfig"
)

func repoRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSubscribeRequiresRepoRoot(t *testing.T) {
	var out bytes.Buffer
	err := Subscribe(t.TempDir(), "reviews", &out)
	if err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("want repository root error, got %v", err)
	}
}

func TestSubscribeCreatesFileAndAddsChannel(t *testing.T) {
	root := repoRoot(t, "myrepo")
	var out bytes.Buffer
	if err := Subscribe(root, "reviews", &out); err != nil {
		t.Fatal(err)
	}
	want := "Agentbus: persistent channels for myrepo: general, memory, reviews\n"
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("%q", out.String())
	}
	if !strings.Contains(out.String(), "next register") {
		t.Fatal("must say when it takes effect")
	}
	f, err := repoconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := f.Channels()
	if len(ch) != 3 || ch[2] != "reviews" {
		t.Fatal(ch)
	}
}

func TestSubscribeRejectsBadName(t *testing.T) {
	root := repoRoot(t, "myrepo")
	if err := Subscribe(root, "bad/name", &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnsubscribe(t *testing.T) {
	root := repoRoot(t, "myrepo")
	var out bytes.Buffer
	if err := Unsubscribe(root, "memory", &out); err == nil {
		t.Fatal("unsubscribe on a missing file must error")
	}
	if err := Subscribe(root, "reviews", &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Unsubscribe(root, "memory", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Agentbus: persistent channels for myrepo: general, reviews\n") {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if err := Unsubscribe(root, "nope", &out); err != nil {
		t.Fatal("unsubscribing an unlisted channel is a no-op, got", err)
	}
	if !strings.HasPrefix(out.String(), "Agentbus: persistent channels for myrepo: general, reviews\n") {
		t.Fatalf("%q", out.String())
	}
}
