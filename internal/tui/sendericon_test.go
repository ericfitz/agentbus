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

// On the selected stream row, the colored sender icon ends with a reset;
// Highlight must re-apply the selection background right after it (and
// after every other inner reset), or the rest of the line loses it.
func TestSelectedRowReappliesBackgroundAfterSenderIcon(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.key("shift+tab") // compose -> stream directly: cursor lands on the last message
	th := f.m.theme
	pre, _, _ := strings.Cut(lipgloss.NewStyle().Background(th.Sel).Render("\x00"), "\x00")
	if pre == "" {
		t.Fatal("selection background renders no escape")
	}
	icon := th.Style(th.Agent).Render(iconAgent)
	first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]
	if !strings.Contains(first, icon+pre) {
		t.Fatalf("selection background not re-applied after the sender icon: %q", first)
	}
	body := strings.TrimSuffix(first, "\x1b[0m")
	if n, m := strings.Count(body, "\x1b[0m"), strings.Count(body, "\x1b[0m"+pre); n != m {
		t.Fatalf("%d of %d inner resets drop the selection background: %q", n-m, n, first)
	}
}
