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
	if !reflect.DeepEqual(got, []string{"general", "memory"}) || len(bad) != 0 {
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

func TestAddChannelMaterializesDefaultsAndPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","future":{"x":1}}`)
	f, _ := Load(dir)
	got, err := f.AddChannel("reviews")
	if err != nil || !reflect.DeepEqual(got, []string{"general", "memory", "reviews"}) {
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
	if !reflect.DeepEqual(again, []string{"general", "memory", "reviews"}) {
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

func TestRemoveChannel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","future":{"x":1}}`)
	f, _ := Load(dir)
	got, err := f.RemoveChannel("memory")
	if err != nil || !reflect.DeepEqual(got, []string{"general"}) {
		t.Fatalf("%v %v", got, err)
	}
	got, err = f.RemoveChannel("nope")
	if err != nil || !reflect.DeepEqual(got, []string{"general"}) {
		t.Fatalf("%v %v", got, err)
	}
	f2, _ := Load(dir)
	ch, _ := f2.Channels()
	if !reflect.DeepEqual(ch, []string{"general"}) {
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
	if !reflect.DeepEqual(ch, []string{"general", "memory"}) {
		t.Fatal(ch)
	}
}
