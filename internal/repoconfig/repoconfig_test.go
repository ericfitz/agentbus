package repoconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".local", "agentbus.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindWalksUpAndStopsAtGit(t *testing.T) {
	outer := filepath.Join(t.TempDir(), "outer")
	inner := filepath.Join(outer, "inner")
	sub := filepath.Join(inner, "a", "b")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.MkdirAll(filepath.Join(outer, ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(inner, ".git"), 0o755)
	writeFile(t, outer, `{"identity":"Outer"}`)

	f, err := Find(sub)
	if err != nil || f != nil {
		t.Fatalf("outer file must not leak past inner .git: %v %v", f, err)
	}
	p := writeFile(t, inner, `{"identity":"Inner"}`)
	f, err = Find(sub)
	if err != nil || f == nil || f.Identity != "Inner" || f.Path != p {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestFindReportsMalformedAndInvalidIdentity(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{not json`)
	if _, err := Find(dir); err == nil {
		t.Fatal("malformed file must error")
	}
	writeFile(t, dir, `{"identity":"has/slash"}`)
	if _, err := Find(dir); err == nil {
		t.Fatal("invalid identity must error")
	}
}

func TestLoadNotExist(t *testing.T) {
	_, err := Load(t.TempDir())
	if !os.IsNotExist(err) {
		t.Fatalf("want IsNotExist, got %v", err)
	}
}

func TestChannelsDefaultsWhenKeyAbsent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam"}`)
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, bad := f.Channels()
	if !reflect.DeepEqual(got, []string{"general", "memory", "tasks"}) || len(bad) != 0 {
		t.Fatalf("%v %v", got, bad)
	}
}

func TestChannelsEmptyListMeansNone(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","channels":[]}`)
	f, _ := Load(dir)
	got, _ := f.Channels()
	if len(got) != 0 {
		t.Fatal(got)
	}
}

func TestChannelsDedupesAndReportsBad(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","channels":["reviews","general","reviews","bad/name",7]}`)
	f, _ := Load(dir)
	got, bad := f.Channels()
	if !reflect.DeepEqual(got, []string{"reviews", "general"}) {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(bad, []string{"bad/name", "7"}) {
		t.Fatal(bad)
	}
}

func TestChannelsAcceptsTaskListRejectsBadTaskName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","channels":["tasks/work","tasks/","tasks/a/b"]}`)
	f, _ := Load(dir)
	got, bad := f.Channels()
	if !reflect.DeepEqual(got, []string{"tasks/work"}) {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(bad, []string{"tasks/", "tasks/a/b"}) {
		t.Fatal(bad)
	}
}

func TestAddChannelAcceptsTaskListRejectsBadTaskName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam"}`)
	f, _ := Load(dir)
	got, err := f.AddChannel("tasks/work")
	if err != nil || !reflect.DeepEqual(got, []string{"general", "memory", "tasks", "tasks/work"}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"tasks/", "tasks/a/b"} {
		if _, err := f.AddChannel(bad); err == nil {
			t.Fatalf("AddChannel(%q): expected error", bad)
		}
	}
}

func TestAddChannelMaterializesDefaultsAndPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","future":{"x":1}}`)
	f, _ := Load(dir)
	got, err := f.AddChannel("reviews")
	if err != nil || !reflect.DeepEqual(got, []string{"general", "memory", "tasks", "reviews"}) {
		t.Fatalf("%v %v", got, err)
	}
	f2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f2.Raw["future"]; !ok {
		t.Fatal("unknown key dropped")
	}
	again, _ := f2.AddChannel("reviews")
	if !reflect.DeepEqual(again, []string{"general", "memory", "tasks", "reviews"}) {
		t.Fatal("duplicate added:", again)
	}
	body, _ := os.ReadFile(f.Path)
	if body[len(body)-1] != '\n' {
		t.Fatal("file must end with newline")
	}
}

func TestAddChannelRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam"}`)
	f, _ := Load(dir)
	if _, err := f.AddChannel("bad/name"); err == nil {
		t.Fatal("expected error")
	}
}

// TestAddChannelRejectsDMChannels reproduces F5: a persisted "dm" or
// "dm/x" channel resubscribes at every register and fails every time (DM
// inboxes are managed by register, not the persistent list). "dm/x" already
// fails bus.NameRule's '/' check; the bare name "dm" did not.
func TestAddChannelRejectsDMChannels(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam"}`)
	f, _ := Load(dir)
	for _, bad := range []string{"dm", "dm/Sam", "dm/Sam/impl"} {
		if _, err := f.AddChannel(bad); err == nil {
			t.Fatalf("AddChannel(%q): expected error", bad)
		}
	}
}

func TestRemoveChannel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","future":{"x":1}}`)
	f, _ := Load(dir)
	got, err := f.RemoveChannel("memory")
	if err != nil || !reflect.DeepEqual(got, []string{"general", "tasks"}) {
		t.Fatalf("%v %v", got, err)
	}
	got, err = f.RemoveChannel("nope")
	if err != nil || !reflect.DeepEqual(got, []string{"general", "tasks"}) {
		t.Fatalf("%v %v", got, err)
	}
	f2, _ := Load(dir)
	ch, _ := f2.Channels()
	if !reflect.DeepEqual(ch, []string{"general", "tasks"}) {
		t.Fatal(ch)
	}
	if _, ok := f2.Raw["future"]; !ok {
		t.Fatal("unknown key dropped by RemoveChannel")
	}
}

func TestCreate(t *testing.T) {
	dir := t.TempDir()
	f, err := Create(dir, "Sam")
	if err != nil || f.Identity != "Sam" {
		t.Fatalf("%v %v", f, err)
	}
	if _, err := Create(dir, "Sam"); err == nil {
		t.Fatal("second create must fail")
	}
	if _, err := Create(t.TempDir(), "bad/name"); err == nil {
		t.Fatal("invalid identity must fail")
	}
	ch, _ := f.Channels()
	if !reflect.DeepEqual(ch, []string{"general", "memory", "tasks"}) {
		t.Fatal(ch)
	}
}

func TestWriteUsesInitFileMode(t *testing.T) {
	f, err := Create(t.TempDir(), "Sam")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(f.Path)
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v err %v", st.Mode(), err)
	}
}

func TestWriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	f, err := Create(dir, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AddChannel("reviews"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".local"))
	if len(entries) != 1 || entries[0].Name() != "agentbus.json" {
		t.Fatalf("unexpected entries: %v", entries)
	}
}
