package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

func TestLoadIconsEmojiIsDefaultAndByteIdentical(t *testing.T) {
	var warn bytes.Buffer
	if got := LoadIcons(config.Default(), &warn); got != "emoji" {
		t.Fatalf("name = %q, want emoji", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning: %q", warn.String())
	}
	for k, p := range iconVars {
		if *p != emojiIcons[k] {
			t.Fatalf("%s = %q, want emoji value %q", k, *p, emojiIcons[k])
		}
	}
}

// TestLoadIconsNerdfontWidthParity checks that every nerdfont glyph is
// padded to the same rail/task-row width as its emoji counterpart: the wide
// icons to 3 columns, the thread marks to 1.
func TestLoadIconsNerdfontWidthParity(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	cfg := config.Default()
	cfg.Icons = "nerdfont"
	var warn bytes.Buffer
	if got := LoadIcons(cfg, &warn); got != "nerdfont" || warn.Len() != 0 {
		t.Fatalf("name = %q, warn = %q", got, warn.String())
	}
	for name, p := range iconVars {
		switch name {
		case "arrow", "error", "collapsed", "expanded":
			continue
		}
		if w := lipgloss.Width(*p); w != 3 {
			t.Errorf("%s width = %d, want 3 (%q)", name, w, *p)
		}
	}
	if w := lipgloss.Width(markSel); w != 1 {
		t.Errorf("collapsed width = %d, want 1", w)
	}
	if w := lipgloss.Width(markOpen); w != 1 {
		t.Errorf("expanded width = %d, want 1", w)
	}

	// A task row and the rail must occupy the same width per line whichever
	// set is active.
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	pending, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a"})
	if err != nil {
		t.Fatal(err)
	}
	owner := f.sam
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: pending.ID, Owner: &owner}); err != nil {
		t.Fatal(err)
	}
	working, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskClaim(f.sam, working.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")

	setIcons(emojiIcons)
	emojiStream := widths(f.m.renderStream())
	emojiRail := widths(f.m.renderRails())

	setIcons(func() map[string]string {
		s := map[string]string{}
		for k, g := range nerdIcons {
			s[k] = padIcon(k, g)
		}
		return s
	}())
	nerdStream := widths(f.m.renderStream())
	nerdRail := widths(f.m.renderRails())

	if len(emojiStream) != len(nerdStream) {
		t.Fatalf("task stream line count differs: emoji %d, nerdfont %d", len(emojiStream), len(nerdStream))
	}
	for i := range emojiStream {
		if emojiStream[i] != nerdStream[i] {
			t.Errorf("task stream line %d width: emoji %d, nerdfont %d", i, emojiStream[i], nerdStream[i])
		}
	}
	if len(emojiRail) != len(nerdRail) {
		t.Fatalf("rail line count differs: emoji %d, nerdfont %d", len(emojiRail), len(nerdRail))
	}
	for i := range emojiRail {
		if emojiRail[i] != nerdRail[i] {
			t.Errorf("rail line %d width: emoji %d, nerdfont %d", i, emojiRail[i], nerdRail[i])
		}
	}
}

func widths(s string) []int {
	lines := strings.Split(s, "\n")
	out := make([]int, len(lines))
	for i, l := range lines {
		out[i] = lipgloss.Width(l)
	}
	return out
}

func TestLoadIconsCustomOverridesSubset(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	cfg := config.Default()
	cfg.Icons = "custom"
	cfg.IconMap = map[string]string{"chat": "X", "bogus": "Y", "user": ""}
	var warn bytes.Buffer
	if got := LoadIcons(cfg, &warn); got != "custom" {
		t.Fatalf("name = %q, want custom", got)
	}
	if iconChat != "X  " {
		t.Fatalf("iconChat = %q, want padded %q", iconChat, "X  ")
	}
	if iconUser != emojiIcons["user"] {
		t.Fatalf("iconUser = %q, want emoji fallback %q", iconUser, emojiIcons["user"])
	}
	if n := strings.Count(warn.String(), "\n"); n != 2 {
		t.Fatalf("want exactly two warning lines, got %d: %q", n, warn.String())
	}
}

func TestLoadIconsUnknownSetWarnsAndFallsBackToEmoji(t *testing.T) {
	cfg := config.Default()
	cfg.Icons = "fancy"
	var warn bytes.Buffer
	if got := LoadIcons(cfg, &warn); got != "emoji" {
		t.Fatalf("name = %q, want emoji", got)
	}
	if n := strings.Count(warn.String(), "\n"); n != 1 {
		t.Fatalf("want exactly one warning line, got %d: %q", n, warn.String())
	}
	if !strings.Contains(warn.String(), `"fancy"`) {
		t.Fatalf("warning must name the bad value: %q", warn.String())
	}
	for k, p := range iconVars {
		if *p != emojiIcons[k] {
			t.Fatalf("%s = %q, want emoji value %q", k, *p, emojiIcons[k])
		}
	}
}
