package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

func (f *fixture) selectTagPane(t *testing.T, name string) {
	t.Helper()
	f.key("esc")
	for i, c := range f.m.channels {
		if c.Name == name {
			f.run(f.m.selectChannel(i))
			return
		}
	}
	t.Fatalf("no tag pane %q in %v", name, f.m.channels)
}

func TestTagRailSectionListsSetsAndPaneShowsMatches(t *testing.T) {
	f := newFixture(t)
	if err := f.c.b.SubscribeTags(f.c.as, []string{"bug", "agentbus"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	rail := ansi.Strip(f.m.renderRails())
	if !strings.Contains(rail, "tags") || !strings.Contains(rail, "agentbus") {
		t.Fatalf("rail lacks the tags section:\n%s", rail)
	}
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dev", Content: "match", Tags: []string{"agentbus", "bug", "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "general", Content: "partial", Tags: []string{"bug"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dev-notes", Content: "memory", Tags: []string{"agentbus", "bug"}}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	f.selectTagPane(t, tagPanePrefix+"agentbus,bug")
	s := ansi.Strip(f.m.renderStream())
	if !strings.Contains(s, "match") || strings.Contains(s, "partial") || strings.Contains(s, "memory") {
		t.Fatalf("tag pane shows chat messages carrying every tag:\n%s", s)
	}
	if !strings.Contains(s, "Sam"+iconArrow+iconChat+"dev") {
		t.Fatalf("rows keep the #3 header:\n%s", s)
	}
	for _, k := range []string{"i", "enter", "r"} {
		f.key(k)
		if f.m.mode != modeNormal || f.m.toast != tagReadOnlyToast {
			t.Fatalf("key %q on a tag pane: mode=%v toast=%q", k, f.m.mode, f.m.toast)
		}
	}
}

func TestTagKeySubscribesAndSUnsubscribes(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("t")
	for _, r := range "Release, docs" {
		f.key(string(r))
	}
	f.key("enter")
	sets, err := f.c.b.TagSubscriptions(f.c.as)
	if err != nil || len(sets) != 1 || sets[0][0] != "docs" || sets[0][1] != "release" {
		t.Fatalf("t prompt subscribes a normalized set: %v %v", sets, err)
	}
	f.selectTagPane(t, tagPanePrefix+"docs,release")
	f.key("s")
	if sets, _ := f.c.b.TagSubscriptions(f.c.as); len(sets) != 0 {
		t.Fatalf("s on a tag row unsubscribes: %v", sets)
	}
	for _, c := range f.m.channels {
		if strings.HasPrefix(c.Name, tagPanePrefix) {
			t.Fatalf("rail still lists the set: %v", f.m.channels)
		}
	}
}

// followTags subscribes the TUI to each set and refreshes the rail.
func (f *fixture) followTags(t *testing.T, sets ...[]string) {
	t.Helper()
	for _, s := range sets {
		if err := f.c.b.SubscribeTags(f.c.as, s); err != nil {
			t.Fatal(err)
		}
	}
	f.run(f.m.statusCmd())
}

// TestTagKeyOpensThePromptOnlyFromTheRail (ADR 0011 decision 7): t is a
// rail key. In a message list it does nothing; in the compose box it types.
func TestTagKeyOpensThePromptOnlyFromTheRail(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "hello")
	f.receive(t)
	f.key("t") // compose has focus: text
	if f.m.prompt.active || f.m.compose.Value() != "t" {
		t.Fatalf("t in compose must type: prompt=%v value=%q", f.m.prompt.active, f.m.compose.Value())
	}
	f.key("ctrl+u")
	f.key("esc") // channel list
	f.key("tab") // stream
	if f.m.pane() != paneStream {
		t.Fatalf("pane = %v, want stream", f.m.pane())
	}
	f.key("t")
	if f.m.prompt.active {
		t.Fatal("t in a message list must not open the tag prompt")
	}
	f.key("home")
	f.key("t")
	if !f.m.prompt.active || !strings.HasPrefix(f.m.prompt.label, "tags") {
		t.Fatalf("t in the channel list opens the tag prompt: active=%v label=%q", f.m.prompt.active, f.m.prompt.label)
	}
	f.key("esc")
	f.toSessions()
	f.key("t")
	if !f.m.prompt.active {
		t.Fatal("t in the sessions pane opens the tag prompt")
	}
}

// TestRailBlankLineBeforeTagsIsNotSelectable: a blank row separates the
// last channel from the "tags" label, like the one before "sessions", and
// the down key steps over it (it is not a row of m.channels).
func TestRailBlankLineBeforeTagsIsNotSelectable(t *testing.T) {
	f := newFixture(t)
	f.followTags(t, []string{"docs"})
	lines := strings.Split(ansi.Strip(f.m.renderRails()), "\n")
	// JoinVertical pads every rail line to the column width, so match trimmed.
	i := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "tags" })
	if i < 2 || strings.TrimSpace(lines[i-1]) != "" || strings.TrimSpace(lines[i-2]) == "" {
		t.Fatalf("want exactly one blank line before tags:\n%s", strings.Join(lines, "\n"))
	}
	f.key("esc")
	downs := 0
	for !isTagPane(f.m.selName()) {
		if downs > len(f.m.channels) {
			t.Fatalf("down never reached the tag set, at %q", f.m.selName())
		}
		f.key("down")
		downs++
	}
	if downs != len(f.m.channels)-1 {
		t.Fatalf("the blank line must take no key: %d downs for %d rail entries", downs, len(f.m.channels))
	}
}

// TestDAndSOnATagSetUnfollowAndMoveTheSelection (ADR 0011 decision 7): no
// confirmation; the selection goes to the next tag set, else the previous
// one, else the last channel, and focus stays in the rail.
func TestDAndSOnATagSetUnfollowAndMoveTheSelection(t *testing.T) {
	f := newFixture(t)
	f.followTags(t, []string{"a"}, []string{"b"}, []string{"c"})
	var lastChannel string
	for _, c := range f.m.channels {
		if !isTagPane(c.Name) {
			lastChannel = c.Name
		}
	}
	f.selectTagPane(t, tagPanePrefix+"b")
	f.key("d")
	if f.m.mode != modeNormal || f.m.pane() != paneChannels || f.m.selName() != tagPanePrefix+"c" {
		t.Fatalf("d on b: mode=%v pane=%v sel=%q, want the next set c with no confirmation", f.m.mode, f.m.pane(), f.m.selName())
	}
	f.key("s")
	if f.m.selName() != tagPanePrefix+"a" {
		t.Fatalf("s on the last set: sel=%q, want the previous set a", f.m.selName())
	}
	f.key("d")
	if f.m.selName() != lastChannel {
		t.Fatalf("d on the only set: sel=%q, want the last channel %q", f.m.selName(), lastChannel)
	}
	if sets, _ := f.c.b.TagSubscriptions(f.c.as); len(sets) != 0 {
		t.Fatalf("every set must be unfollowed: %v", sets)
	}
	for _, c := range f.m.channels {
		if isTagPane(c.Name) {
			t.Fatalf("rail still lists a set: %v", f.m.channels)
		}
	}
}
