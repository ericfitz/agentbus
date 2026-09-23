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
	want := "Agentbus: persistent channels for myrepo: general, memory, tasks, reviews\n"
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
	if len(ch) != 4 || ch[3] != "reviews" {
		t.Fatal(ch)
	}
}

func TestSubscribeRejectsBadName(t *testing.T) {
	root := repoRoot(t, "myrepo")
	if err := Subscribe(root, "bad/name", &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSubscribeAcceptsTaskList(t *testing.T) {
	root := repoRoot(t, "myrepo")
	var out bytes.Buffer
	if err := Subscribe(root, "tasks/work", &out); err != nil {
		t.Fatal(err)
	}
	f, err := repoconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := f.Channels()
	if len(ch) != 4 || ch[3] != "tasks/work" {
		t.Fatal(ch)
	}
}

func TestSubscribeRejectsBadTaskListName(t *testing.T) {
	root := repoRoot(t, "myrepo")
	for _, bad := range []string{"tasks/", "tasks/a/b"} {
		if err := Subscribe(root, bad, &bytes.Buffer{}); err == nil {
			t.Fatalf("Subscribe(%q): expected error", bad)
		}
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
	if !strings.HasPrefix(out.String(), "Agentbus: persistent channels for myrepo: general, tasks, reviews\n") {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if err := Unsubscribe(root, "nope", &out); err != nil {
		t.Fatal("unsubscribing an unlisted channel is a no-op, got", err)
	}
	if !strings.HasPrefix(out.String(), "Agentbus: persistent channels for myrepo: general, tasks, reviews\n") {
		t.Fatalf("%q", out.String())
	}
}

func TestSubscribeTagsWritesTagSets(t *testing.T) {
	root := repoRoot(t, "myrepo")
	var out bytes.Buffer
	if err := SubscribeTags(root, []string{"Bug", "agentbus"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Agentbus: persistent tag subscriptions for myrepo: agentbus,bug\n") {
		t.Fatalf("%q", out.String())
	}
	f, err := repoconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if sets, _ := f.TagSubscriptions(); len(sets) != 1 {
		t.Fatalf("%v", sets)
	}
	out.Reset()
	if err := UnsubscribeTags(root, []string{"agentbus", "bug"}, &out); err != nil || !strings.Contains(out.String(), "(none)") {
		t.Fatalf("%v %q", err, out.String())
	}
}
