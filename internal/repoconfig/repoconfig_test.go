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
