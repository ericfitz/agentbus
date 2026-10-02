package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/muesli/termenv"
)

// The sender icon is drawn in the sender's color (user for the TUI's own
// identity, agent for everyone else) in every icon mode, and styling it
// never changes a width: the gear's CSI escapes stay invisible to lipgloss.
func TestSenderIconColoredInEveryIconMode(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev); setIcons(emojiIcons) })
	for _, mode := range []string{"emoji", "nerdfont", "custom"} {
		cfg := config.Default()
		cfg.Icons = mode
		if mode == "custom" {
			cfg.IconMap = map[string]string{"agent": "A", "user": "U"}
		}
		LoadIcons(cfg, io.Discard)
		f := newFixture(t)
		th := f.m.theme
		agent := th.Style(th.Agent).Render(iconAgent)
		user := th.Style(th.User).Render(iconUser)
		if agent == iconAgent || user == iconUser {
			t.Fatalf("%s: theme renders no color", mode)
		}
		if got := f.m.agentLabel("Sam"); !strings.HasPrefix(got, agent) {
			t.Errorf("%s: agent header icon uncolored: %q", mode, got)
		}
		if got := f.m.agentLabel(f.c.as); !strings.HasPrefix(got, user) {
			t.Errorf("%s: own header icon uncolored: %q", mode, got)
		}
		if w, want := lipgloss.Width(agent), lipgloss.Width(ansi.Strip(iconAgent)); w != want {
			t.Errorf("%s: styled agent icon width %d, want %d", mode, w, want)
		}
		f.run(f.m.statusCmd())
		rail := f.m.renderRails()
		if !strings.Contains(rail, agent) || !strings.Contains(rail, user) {
			t.Errorf("%s: sessions rail icons uncolored: %q", mode, rail)
		}
		msg := bus.Message{Seq: 1, Channel: "dev", Sender: "Sam", CreatedAt: time.Now().UnixMilli()}
		colored := lipgloss.Width(f.m.header(msg, "dev", "", lipgloss.NewStyle(), 200))
		lipgloss.SetColorProfile(termenv.Ascii)
		plain := lipgloss.Width(f.m.header(msg, "dev", "", lipgloss.NewStyle(), 200))
		lipgloss.SetColorProfile(termenv.ANSI)
		if colored != plain {
			t.Errorf("%s: header width %d colored vs %d plain", mode, colored, plain)
		}
	}
}
