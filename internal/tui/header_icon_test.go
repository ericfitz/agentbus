package tui

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/muesli/termenv"
)

// #30: the messages pane title shows the channel's type icon before its
// name, in the channel type's color (a color emoji ignores it), in every
// icon mode; a DM pane shows the agent or user icon, like the DM compose
// label. Cutting the title never leaves an icon's cursor escape behind.
func TestPaneTitleShowsChannelTypeIcon(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev); setIcons(emojiIcons) })
	for _, mode := range []string{"emoji", "nerdfont"} {
		cfg := config.Default()
		cfg.Icons = mode
		LoadIcons(cfg, io.Discard)
		f := newFixture(t)
		for name, kind := range map[string]string{"notes": "memory", "tasks/work": "memory"} {
			if _, err := f.ab.CreateChannel(f.sam, name, kind); err != nil {
				t.Fatal(err)
			}
		}
		f.run(f.m.statusCmd())
		f.key("esc")
		seen := map[string]bool{}
		for i := 0; i <= len(f.m.channels)+len(f.m.dms); i++ {
			ch := f.m.selected()
			if ch == nil {
				break
			}
			head := f.m.renderHeader()
			var want string
			switch owner, dm := strings.CutPrefix(ch.Name, bus.DMPrefix); {
			case dm:
				want = f.m.senderIcon(owner) + f.m.chanStyle(*ch).Render("@"+owner)
			case bus.IsTaskChannel(ch.Name):
				want = f.m.chanStyle(*ch).Render(iconTasks + ch.Name)
				seen["tasks"] = true
			case ch.Kind == "memory":
				want = f.m.chanStyle(*ch).Render(iconMem + ch.Name)
			default:
				want = f.m.chanStyle(*ch).Render(iconChat + ch.Name)
			}
			if !strings.HasPrefix(head, want) {
				t.Errorf("%s: %s title = %q, want prefix %q", mode, ch.Name, head, want)
			}
			seen[ch.Kind] = true
			f.key("down")
		}
		if !seen["memory"] || !seen["ordinary"] || !seen["tasks"] {
			t.Fatalf("%s: covered kinds %v", mode, seen)
		}
	}
}
