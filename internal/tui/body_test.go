package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

// sendSubject posts content with a subject from Sam.
func (f *fixture) sendSubject(t *testing.T, ch, subject, content string) bus.SendResult {
	t.Helper()
	r, err := f.ab.Send(f.sam, bus.SendInput{Channel: ch, Subject: subject, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// stream is the rendered stream without escapes.
func (f *fixture) stream() string { return ansi.Strip(f.m.renderStream()) }

// onChannel selects ch, loads it through history (no peeked replies), and
// puts the stream cursor on seq.
func (f *fixture) onChannel(t *testing.T, ch string, seq int64) {
	t.Helper()
	f.key("esc")
	for i, c := range f.m.channels {
		if c.Name == ch {
			f.run(f.m.selectChannel(i))
		}
	}
	f.run(f.m.loadHistory(ch, nil))
	f.m.placeCursor(seq)
	if r, ok := f.m.cursorRow(); !ok || r.msg.Seq != seq {
		t.Fatalf("cursor not on %d: %+v", seq, r)
	}
}

// A collapsed row shows the subject, or the first line when there's none,
// and hides the rest of the content behind ▶.
func TestCollapsedRowShowsSubjectElseFirstLine(t *testing.T) {
	f := newFixture(t)
	f.sendSubject(t, "dev", "Deploy plan", "step one\nstep two")
	f.agentSend(t, "dev", "first line\nsecond line")
	f.receive(t)
	s := f.stream()
	for _, want := range []string{"Deploy plan", "first line"} {
		if !strings.Contains(s, want) {
			t.Fatalf("row line %q missing:\n%s", want, s)
		}
	}
	for _, hidden := range []string{"step one", "second line"} {
		if strings.Contains(s, hidden) {
			t.Fatalf("body %q must stay hidden:\n%s", hidden, s)
		}
	}
	if strings.Count(s, markSel) != 2 {
		t.Fatalf("both rows have a body to open:\n%s", s)
	}
}

// → opens the body, then the direct replies; ← hides the replies, then the
// body; space toggles the replies only.
func TestRightOpensBodyThenRepliesLeftClosesRepliesThenBody(t *testing.T) {
	f := newFixture(t)
	a := f.sendSubject(t, "dev", "Deploy plan", "step one")
	f.agentReply(t, "dev", a.Seq, "A1")
	f.onChannel(t, "dev", a.Seq)
	if got := contents(f.m.rows("dev")); !eq(got, []string{"step one"}) || strings.Contains(f.stream(), "step one") {
		t.Fatalf("starts collapsed: rows %v\n%s", got, f.stream())
	}
	f.key("right")
	if s := f.stream(); !strings.Contains(s, "step one") || !eq(contents(f.m.rows("dev")), []string{"step one"}) || !strings.Contains(s, markSel) {
		t.Fatalf("first → opens the body only; replies still hidden behind ▶:\n%s", s)
	}
	f.key("right")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"step one", ">A1"}) || !strings.Contains(f.stream(), markOpen) {
		t.Fatalf("second → shows the replies: %v\n%s", got, f.stream())
	}
	f.key("left")
	if s := f.stream(); !eq(contents(f.m.rows("dev")), []string{"step one"}) || !strings.Contains(s, "step one") {
		t.Fatalf("first ← hides the replies and keeps the body open:\n%s", s)
	}
	f.key("left")
	if s := f.stream(); strings.Contains(s, "step one") || !strings.Contains(s, "Deploy plan") || !strings.Contains(s, markSel) {
		t.Fatalf("second ← closes the body:\n%s", s)
	}
	f.key(" ")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"step one"}) || strings.Contains(f.stream(), "step one") {
		t.Fatalf("space no longer shows or hides replies (ADR 0011 decision 4): %v\n%s", got, f.stream())
	}
}

// A one-line message with no subject that fits the width has no body: no
// marker, and → has nothing to open.
func TestOneLineMessageWithoutSubjectHasNoBody(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", "hello")
	f.onChannel(t, "dev", a.Seq)
	if s := f.stream(); strings.Contains(s, markSel) || strings.Contains(s, markOpen) {
		t.Fatalf("nothing to expand:\n%s", s)
	}
	f.key("right")
	if f.m.bodyOpen[a.Seq] {
		t.Fatal("→ must not open an empty body")
	}
}

// A first line wider than the pane is cut with … and counts as a body: →
// shows the full text (Review Focus 1).
func TestCutFirstLineOpensAsBody(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", strings.Repeat("word ", 30))
	f.m.width = 60
	f.m.layout()
	f.onChannel(t, "dev", a.Seq)
	if s := f.stream(); !strings.Contains(s, "…") || !strings.Contains(s, markSel) {
		t.Fatalf("a cut first line is openable:\n%s", s)
	}
	f.key("right")
	if s := f.stream(); strings.Count(s, "word") != 30 || !strings.Contains(s, markOpen) {
		t.Fatalf("the open body shows the whole text:\n%s", s)
	}
}

// Stepping memory versions shows each version's subject and, with the body
// open, its content; the body stays open across steps.
func TestBodyStaysOpenAcrossMemoryVersions(t *testing.T) {
	f := newFixture(t)
	first := f.sendSubject(t, "dev-notes", "Release v1", "notes one")
	v2 := "Release v2"
	if _, err := f.ab.EditMemory(f.sam, bus.EditInput{ID: *first.MemoryID, Subject: &v2, Content: "notes two"}); err != nil {
		t.Fatal(err)
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
	f.streamHas(t, "Release v2")
	if strings.Contains(f.stream(), "notes two") {
		t.Fatal("starts collapsed")
	}
	f.key("right")
	f.streamHas(t, "Release v2", "notes two")
	f.key(".")
	f.streamHas(t, "Release v1", "notes one", "r1 · 2 kept")
	if s := f.stream(); strings.Contains(s, "notes two") || strings.Contains(s, "Release v2") {
		t.Fatalf("the selected version only:\n%s", s)
	}
	f.key(",")
	f.streamHas(t, "Release v2", "notes two", "r2 · 2 kept")
}

// The merged DM pane and tag panes collapse rows too.
func TestDMAndTagPanesCollapseRows(t *testing.T) {
	f := newFixture(t)
	if err := f.c.b.SubscribeTags(f.c.as, []string{"bug"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dm/" + f.c.as, Subject: "Question", Content: "line one\nline two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dev", Subject: "Bug report", Content: "it broke\nbadly", Tags: []string{"bug"}}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	f.selectSessionNamed(t, "Sam")
	if s := f.stream(); !strings.Contains(s, "Question") || strings.Contains(s, "line two") {
		t.Fatalf("DM pane collapses:\n%s", s)
	}
	f.selectTagPane(t, tagPanePrefix+"bug")
	if s := f.stream(); !strings.Contains(s, "Bug report") || strings.Contains(s, "badly") {
		t.Fatalf("tag pane collapses:\n%s", s)
	}
}

// Search hit rows show the subject when there is one.
func TestSearchRowsShowTheSubject(t *testing.T) {
	f := newFixture(t)
	f.sendSubject(t, "dev", "Deploy plan", "the release script")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	v := ansi.Strip(f.m.View())
	if !strings.Contains(v, "Deploy plan") || strings.Contains(v, "the release script") {
		t.Fatalf("hit row shows the subject, not the snippet:\n%s", v)
	}
}
