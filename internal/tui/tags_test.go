package tui

import (
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
	if !strings.Contains(s, "Sam --> "+iconChat+"dev") {
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
