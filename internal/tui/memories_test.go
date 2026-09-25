package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

// onMemory opens dev-notes with a three-revision memory plus an ordinary
// memory, and puts the stream cursor on the three-revision one.
func onMemory(t *testing.T) (f *fixture, id int64) {
	t.Helper()
	f = newFixture(t)
	first := f.agentSend(t, "dev-notes", "Release v1")
	f.agentSend(t, "dev-notes", "Reviewer checklist")
	for _, c := range []string{"Release v2", "Release v3"} {
		if _, err := f.ab.EditMemory(f.sam, bus.EditInput{ID: *first.MemoryID, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	f.key("esc")
	for i, c := range f.m.channels {
		if c.Name == "dev-notes" {
			f.run(f.m.selectChannel(i))
		}
	}
	f.run(f.m.loadHistory("dev-notes", nil))
	for _, r := range f.m.rows("dev-notes") {
		if r.msg.MemoryID != nil && *r.msg.MemoryID == *first.MemoryID {
			f.m.placeCursor(r.msg.Seq)
		}
	}
	if r, ok := f.m.cursorRow(); !ok || r.msg.Content != "Release v3" {
		t.Fatalf("cursor not on the edited memory: %+v", r)
	}
	return f, *first.MemoryID
}

func (f *fixture) streamHas(t *testing.T, want ...string) {
	t.Helper()
	v := ansi.Strip(f.m.renderStream())
	for _, w := range want {
		if !strings.Contains(v, w) {
			t.Fatalf("stream lacks %q:\n%s", w, v)
		}
	}
}

// . and > step to older versions, , and < to newer, stopping at both ends;
// the row starts on the latest version.
func TestMemoryVersionsStepOlderAndNewer(t *testing.T) {
	f, _ := onMemory(t)
	f.key(",")
	f.streamHas(t, "Release v3 r3")
	f.key(".")
	f.streamHas(t, "Release v2 r2 · 3 kept")
	f.key(">")
	f.streamHas(t, "Release v1 r1 · 3 kept")
	f.key(".")
	f.streamHas(t, "Release v1 r1 · 3 kept")
	f.key("<")
	f.streamHas(t, "Release v2 r2 · 3 kept")
	f.key(",")
	f.key(",")
	f.streamHas(t, "Release v3 r3 · 3 kept")
}

// Any other key (here, moving the cursor away and back) returns the row to
// its latest version.
func TestMemoryVersionResetsOnOtherKeys(t *testing.T) {
	f, _ := onMemory(t)
	f.key(".")
	f.streamHas(t, "Release v2 r2 · 3 kept")
	f.key("down")
	f.key("up")
	f.key("down")
	if r, _ := f.m.cursorRow(); r.msg.MemoryID == nil {
		t.Fatal("cursor left the memory channel")
	}
	f.key("up")
	f.streamHas(t, "Release v3 r3")
	if strings.Contains(ansi.Strip(f.m.renderStream()), "kept") {
		t.Fatal("version view must reset to latest")
	}
}

// The version keys do nothing on an ordinary message, and m no longer opens
// a memory browser.
func TestVersionKeysIgnoreOrdinaryRowsAndMIsGone(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", "hello")
	f.key("esc")
	f.run(f.m.loadHistory("dev", nil))
	for i, c := range f.m.channels {
		if c.Name == "dev" {
			f.run(f.m.selectChannel(i))
		}
	}
	f.m.placeCursor(a.Seq)
	f.key(".")
	if f.m.mem.seq != 0 {
		t.Fatal(". on an ordinary message must not start a version view")
	}
	f.key("m")
	if f.m.mode != modeNormal {
		t.Fatalf("m must not open an overlay, mode=%v", f.m.mode)
	}
}

// TestMemoryVersionLabelUsesTheRealRevision (ADR 0011 decision 5): once the
// tombstone purge has removed revision 1, the memory's one surviving row is
// revision 2 and stepping reads "r2 · 1 kept", not "r1 of 1".
func TestMemoryVersionLabelUsesTheRealRevision(t *testing.T) {
	cfg := testConfig(t)
	cfg.TombstoneMinHours = -1 // every tombstone is old enough on the next tick (config is not validated here)
	f := newFixtureWith(t, cfg)
	first := f.agentSend(t, "dev-notes", "Release v1")
	if _, err := f.ab.EditMemory(f.sam, bus.EditInput{ID: *first.MemoryID, Content: "Release v2"}); err != nil {
		t.Fatal(err)
	}
	// Whichever of the two buses holds the maintenance lease runs the purge.
	f.ab.Tick(context.Background())
	f.c.b.Tick(context.Background())
	revs, err := f.c.b.MemoryRevisions(f.c.as, *first.MemoryID)
	if err != nil || len(revs) != 1 {
		t.Fatalf("fixture: want one surviving revision, got %d (%v)", len(revs), err)
	}
	f.key("esc")
	for i, c := range f.m.channels {
		if c.Name == "dev-notes" {
			f.run(f.m.selectChannel(i))
		}
	}
	f.run(f.m.loadHistory("dev-notes", nil))
	f.m.placeCursor(revs[0].Seq)
	f.key(".")
	f.streamHas(t, "Release v2 r2 · 1 kept")
	if s := ansi.Strip(f.m.renderStream()); strings.Contains(s, "r1") {
		t.Fatalf("the label must not count positions:\n%s", s)
	}
}
