package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/bus"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	default:
		return fmt.Sprintf("%d MiB", n>>20)
	}
}

// clock renders a message time as HH:MM for today, else "Sep 7".
func clock(ms int64) string {
	t := time.UnixMilli(ms).Local()
	if y, m, d := t.Date(); y == time.Now().Year() && m == time.Now().Month() && d == time.Now().Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2")
}

func (m Model) showLeft() bool  { return m.width >= 60 }
func (m Model) showRight() bool { return m.width >= 90 }

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	switch m.mode {
	case modeSearch:
		return m.viewSearch()
	case modeMemories, modeConfirmDelete:
		return m.viewMemories()
	case modeHealth:
		return m.viewHealth()
	}
	dim := m.theme.Style(m.theme.Dim)
	left, right := m.renderRails()
	header := m.renderHeader()
	centre := lipgloss.JoinVertical(lipgloss.Left, header, m.stream.View())
	cols := []string{}
	if m.showLeft() {
		cols = append(cols, left, dim.Render("│"))
	}
	cols = append(cols, centre)
	if m.showRight() {
		cols = append(cols, dim.Render("│"), right)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	parts := []string{body, dim.Render(strings.Repeat("─", m.width))}
	if m.replyTo != nil {
		parts = append(parts, m.renderReplyBanner())
	}
	parts = append(parts, m.renderCompose())
	if m.toast != "" {
		parts = append(parts, m.theme.Style(m.theme.Error).Render("✗ "+m.toast))
	}
	parts = append(parts, m.renderStatusBar())
	out := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.NewStyle().Background(m.theme.BG).Foreground(m.theme.Text).Width(m.width).MaxHeight(m.height).Render(out)
}

func (m Model) renderHeader() string {
	ch := m.selected()
	if ch == nil {
		return m.theme.Style(m.theme.Dim).Render("no channels yet · c to create one")
	}
	s := fmt.Sprintf("%s · %d unread · %d messages", ch.Name, m.unread(ch.Name), ch.Messages)
	if m.status.Notice != "" {
		s += "   " + m.theme.Style(m.theme.Warn).Render("! capacity: "+m.status.Notice)
	}
	// MaxWidth truncates a long notice instead of Width's word-wrap, which
	// would add a row and push the status bar off the bottom of the screen.
	return lipgloss.NewStyle().MaxWidth(m.stream.Width).Render(s)
}

// renderRails draws the channel list (left) and the session list (right).
// Each row is truncated (never wrapped) to its rail's width via MaxWidth --
// Style.Width word-wraps overlong content instead of clipping it, which would
// silently grow the rail past the stream's height and push rows below it (the
// compose line, the status bar) off the bottom of the screen. MaxHeight on
// the container caps the row count the same way, for more rows than fit.
func (m Model) renderRails() (string, string) {
	th := m.theme
	dim, memc, sel := th.Style(th.Dim), th.Style(th.Mem), lipgloss.NewStyle().Background(th.Sel)
	leftTrunc := lipgloss.NewStyle().MaxWidth(leftRail)
	var l strings.Builder
	l.WriteString(dim.Render("channels") + "\n")
	for i, c := range m.channels {
		mark := "  "
		if c.Kind == "memory" {
			mark = memc.Render("◆ ")
		}
		line := mark + c.Name
		if n := m.unread(c.Name); n > 0 {
			line += " " + th.Style(th.Agent).Render(strconv.Itoa(n))
		}
		if !m.c.isSubscribed(c.Name) {
			line += dim.Render(" (off)")
		}
		if i == m.sel {
			line = th.Style(th.Agent).Render("›") + line
			if pad := leftRail - 1 - lipgloss.Width(line); pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			line = sel.Render(line)
		} else {
			line = " " + line
		}
		l.WriteString(leftTrunc.Render(line) + "\n")
	}
	left := lipgloss.NewStyle().Width(leftRail).MaxHeight(m.stream.Height + 1).Render(l.String())

	rightTrunc := lipgloss.NewStyle().MaxWidth(rightRail)
	var r strings.Builder
	r.WriteString(dim.Render("sessions") + "\n")
	live := map[string]bus.Session{}
	for _, s := range m.status.Sessions {
		live[s.Sender] = s
	}
	names := make([]string, 0, len(m.sessionsSeen))
	for n := range m.sessionsSeen {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		var row string
		if s, ok := live[n]; ok {
			ctx, name := s.Context, th.Style(th.Agent).Render(n)
			if n == m.c.as {
				ctx, name = "you", th.Style(th.User).Render(n)
			}
			row = th.Style(th.Health).Render("● ") + name + " " + dim.Render(ctx)
		} else {
			age := time.Since(m.sessionsSeen[n]).Round(time.Minute)
			row = th.Style(th.Warn).Render("○ ") + dim.Render(n+" "+shortDur(age))
		}
		r.WriteString(rightTrunc.Render(row) + "\n")
	}
	right := lipgloss.NewStyle().Width(rightRail).MaxHeight(m.stream.Height + 1).Render(r.String())
	return left, right
}

func shortDur(d time.Duration) string {
	if d < time.Hour {
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	return strconv.Itoa(int(d.Hours())) + "h"
}

// renderStream draws the selected channel's messages, oldest first, with the
// "new" divider after the last seen message, "n evicted" dividers where the
// bus reported gaps, and the normal-mode cursor row highlighted.
func (m *Model) renderStream() string {
	th := m.theme
	dim := th.Style(th.Dim)
	ch := m.selName()
	ms := m.msgs[ch]
	if ch == "" {
		return ""
	}
	if len(ms) == 0 {
		return dim.Render("no messages yet in " + ch + " · type below to send the first")
	}
	w := max(m.stream.Width, 20)
	divider := func(label string) string {
		side := strings.Repeat("─", max((w-len(label)-2)/2, 1))
		return dim.Render(side + " " + label + " " + side)
	}
	gaps := append([]bus.Gap{}, m.gaps[ch]...)
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].From < gaps[j].From })
	var b strings.Builder
	gi := 0
	for i, x := range ms {
		for gi < len(gaps) && gaps[gi].To < x.Seq {
			b.WriteString(divider(itoa(gaps[gi].To-gaps[gi].From+1)+" evicted") + "\n")
			gi++
		}
		if m.divider >= 0 && x.Seq > m.divider && (i == 0 || ms[i-1].Seq <= m.divider) {
			b.WriteString(divider("new") + "\n")
		}
		name := th.Style(th.Agent).Render(x.Sender)
		if x.Sender == m.c.as {
			name = th.Style(th.User).Render(x.Sender)
		}
		head := dim.Render(clock(x.CreatedAt)) + " " + name + " "
		indent := strings.Repeat(" ", lipgloss.Width(head))
		body := strings.ReplaceAll(x.Content, "\n", "\n"+indent)
		line := head + body
		if x.ReplyTo != nil {
			line += "\n" + indent + dim.Render("↳ re #"+itoa(*x.ReplyTo))
		}
		if x.MemoryID != nil && x.Revision != nil && *x.Revision > 1 {
			line += " " + th.Style(th.Mem).Render("r"+itoa(*x.Revision))
		}
		line = lipgloss.NewStyle().Width(w).Render(line)
		if i == m.cursor && m.mode == modeNormal {
			line = lipgloss.NewStyle().Background(th.Sel).Width(w).Render(line)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderReplyBanner() string {
	th := m.theme
	r := m.replyTo
	quote := strings.SplitN(r.Content, "\n", 2)[0]
	if rs := []rune(quote); len(rs) > 40 {
		quote = string(rs[:40]) + "…"
	}
	return th.Style(th.Dim).Render("↳ replying to ") + th.Style(th.Agent).Render(r.Sender) + th.Style(th.Dim).Render(" #"+itoa(r.Seq)+" “"+quote+"”  esc cancel")
}

func (m Model) renderCompose() string {
	th := m.theme
	if m.prompt.active {
		return th.Style(th.Agent).Render(m.prompt.label+" › ") + m.prompt.input.View()
	}
	ch := m.selected()
	label := ""
	hint := ""
	if ch != nil {
		if ch.Kind == "memory" {
			label = th.Style(th.Mem).Render("◆ " + ch.Name)
			hint = th.Style(th.Dim).Render("  ⏎ new memory")
		} else {
			label = th.Style(th.Agent).Render(ch.Name)
		}
	}
	prompt := label + th.Style(th.Dim).Render(" › ")
	if m.mode != modeInsert {
		prompt = label + th.Style(th.Dim).Render(" ▸ ")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, prompt, m.compose.View(), hint)
}

func (m Model) renderStatusBar() string {
	th := m.theme
	dim := th.Style(th.Dim)
	embed := "unset"
	if m.c.cfg.EmbeddingEndpoint != "" {
		embed = th.Style(th.Health).Render("ok")
		if m.search.semanticDown {
			embed = th.Style(th.Warn).Render("unreachable")
		}
	}
	left := fmt.Sprintf("db %s / %s  embed %s  backlog %d  as %s", fmtBytes(m.status.UsageBytes), fmtBytes(m.status.BudgetBytes), embed, m.status.EmbeddingBacklog, th.Style(th.User).Render(m.c.as))
	if m.statusErr != nil {
		left += "  " + th.Style(th.Error).Render("status: "+errText(m.statusErr))
	}
	help := dim.Render("? help  / search  m memories  h health  q quit")
	if m.mode == modeInsert {
		help = dim.Render("esc commands  tab next unread  alt+enter newline")
	}
	// The status bar must stay exactly one row: a narrow terminal or a long
	// left side can make help too wide to fit; MaxWidth truncates it instead
	// of letting Width's word-wrap add a row that MaxHeight would then have
	// to clip from somewhere else on screen.
	avail := m.width - lipgloss.Width(left) - 1
	if avail <= 0 {
		return left
	}
	help = lipgloss.NewStyle().MaxWidth(avail).Render(help)
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(help), 1)
	return left + strings.Repeat(" ", gap) + help
}

// overlaySize returns the box width and height overlay renders at, so a
// view (e.g. viewSearch) can size and window its own content to match.
func (m Model) overlaySize() (w, h int) {
	return min(max(m.width-8, 40), 100), max(m.height-4, 10)
}

// overlay renders a titled, bordered box centred on the screen; the border
// colour names the overlay (cyan search, magenta memories, green health).
func (m Model) overlay(title string, border lipgloss.TerminalColor, body, footer string) string {
	w, h := m.overlaySize()
	inner := lipgloss.JoinVertical(lipgloss.Left,
		m.theme.Style(border).Bold(true).Render(title),
		lipgloss.NewStyle().Width(w-4).Height(h-4).MaxHeight(h-4).Render(body),
		m.theme.Style(m.theme.Dim).Render(footer),
	)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(w - 2).Render(inner)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// Stubs replaced by Tasks 9-10.
func (m Model) viewMemories() string { return "" }
func (m Model) viewHealth() string   { return "" }
