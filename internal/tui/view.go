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
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	default:
		return fmt.Sprintf("%d KiB", n>>10)
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

func (m Model) showLeft() bool { return m.width >= 60 }

// railWidth is the left column: a fifth of the screen, floored so channel
// names stay readable at the 60-column minimum.
func (m Model) railWidth() int { return max(m.width/5, 16) }

// Rail icons. Each carries U+FE0F so the terminal draws the color emoji
// even when its monospace font has its own glyph at that codepoint.
const (
	iconChat = "\U0001F4AC\uFE0F " // speech balloon
	iconMem  = "\U0001F4BE\uFE0F " // floppy disk
	// ponytail: gear is Neutral width: Terminal.app draws it two cells wide but
	// advances one, while lipgloss counts two. CSI 1C moves the cursor one
	// cell (uncounted by lipgloss) so both agree; swap to a Wide emoji if a
	// terminal that advances two ever matters. Robot U+1F916 was rejected:
	// Source Code Pro ships its own glyph there and U+FE0F does not override it.
	// CSI 1C skips its cell without writing it, so when a redraw moves the
	// gear onto a line that held a wide emoji (a new session sorting above
	// the user row) the old glyph's half stays on screen. CSI 2X (erase two
	// cells, cursor stays, also uncounted) blanks both cells first.
	iconAgent = "\x1b[2X\u2699\uFE0F\x1b[1C " // gear
	iconUser  = "\U0001F9D1\uFE0F "           // adult
	iconIdle  = "\U0001F4A4\uFE0F "           // sleeping sign
	iconTasks = "\U0001F4CB\uFE0F "           // clipboard
)

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
	case modeHelp:
		return m.viewHelp()
	}
	dim := m.theme.Style(m.theme.Dim)
	header := m.renderHeader()
	center := lipgloss.JoinVertical(lipgloss.Left, header, m.stream.View())
	body := center
	if m.showLeft() {
		border := strings.TrimRight(strings.Repeat(dim.Render("│")+"\n", m.stream.Height+1), "\n")
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.renderRails(), border, center)
	}
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

// chanStyle is the color a channel's name is drawn in everywhere: memory
// channels in the memory color, a DM inbox in the user color for the TUI's
// own inbox and the agent color for anyone else's, chat channels in the
// agent color.
func (m Model) chanStyle(c bus.Channel) lipgloss.Style {
	switch {
	case c.Kind == "memory":
		return m.theme.Style(m.theme.Mem)
	case c.Name == bus.DMChannel(m.c.as):
		return m.theme.Style(m.theme.User)
	default:
		return m.theme.Style(m.theme.Agent)
	}
}

func (m Model) renderHeader() string {
	ch := m.selected()
	if ch == nil {
		return m.theme.Style(m.theme.Dim).Render("no channels yet · c to create one")
	}
	var s string
	if owner, ok := strings.CutPrefix(ch.Name, bus.DMPrefix); ok {
		s = fmt.Sprintf("%s · direct · %d unread · %d messages", m.chanStyle(*ch).Render("@"+owner), m.unread(ch.Name), ch.Messages)
	} else {
		s = fmt.Sprintf("%s · %d unread · %d messages", m.chanStyle(*ch).Render(ch.Name), m.unread(ch.Name), ch.Messages)
	}
	if m.status.Notice != "" {
		s += "   " + m.theme.Style(m.theme.Warn).Render("! capacity: "+m.status.Notice)
	}
	// MaxWidth truncates a long notice instead of Width's word-wrap, which
	// would add a row and push the status bar off the bottom of the screen.
	return lipgloss.NewStyle().MaxWidth(m.stream.Width).Render(s)
}

// renderRails draws the left column: the channel list with the session list
// under it, the two sharing the stream's height. Each row is truncated (never wrapped) to its rail's width via MaxWidth --
// Style.Width word-wraps overlong content instead of clipping it, which would
// silently grow the rail past the stream's height and push rows below it (the
// compose line, the status bar) off the bottom of the screen. MaxHeight on
// the container caps the row count the same way, for more rows than fit.
func (m Model) renderRails() string {
	th := m.theme
	dim := th.Style(th.Dim)
	rail := m.railWidth()
	trunc := lipgloss.NewStyle().MaxWidth(rail)
	var l strings.Builder
	l.WriteString(dim.Render("channels") + "\n")
	for i, c := range m.channels {
		mark := iconChat
		switch {
		case bus.IsTaskChannel(c.Name):
			mark = iconTasks
		case c.Kind == "memory":
			mark = iconMem
		}
		line := m.chanStyle(c).Render(mark + c.Name)
		if n := m.unread(c.Name); n > 0 {
			line += " " + th.Style(th.Agent).Render(strconv.Itoa(n))
		}
		if !m.c.isSubscribed(c.Name) {
			line += dim.Render(" (off)")
		}
		// While a session is selected, the channel list draws no highlighted
		// row; the sessions list below highlights instead.
		if m.sessSel < 0 && i == m.sel {
			line = th.Highlight(markSel+line, rail)
		} else {
			line = " " + line
		}
		l.WriteString(trunc.Render(line) + "\n")
	}

	var r strings.Builder
	r.WriteString(dim.Render("sessions") + "\n")
	live := map[string]bus.Session{}
	for _, s := range m.status.Sessions {
		live[s.Sender] = s
	}
	names := m.sessionNames()
	for i, n := range names {
		var row string
		if s, ok := live[n]; ok {
			ctx, name := s.Context, th.Style(th.Agent).Render(n)
			if n == m.c.as {
				ctx, name = "you", th.Style(th.User).Render(n)
			}
			icon := iconAgent
			if n == m.c.as {
				icon = iconUser
			}
			row = icon + name + " " + dim.Render(ctx)
		} else {
			age := time.Since(m.sessionsSeen[n]).Round(time.Minute)
			row = iconIdle + dim.Render(n+" "+shortDur(age))
		}
		if c := m.unread(bus.DMChannel(n)); c > 0 {
			row += " " + th.Style(th.Agent).Render(strconv.Itoa(c))
		}
		if i == m.sessSel {
			row = th.Highlight(markSel+row, rail)
		} else {
			row = " " + row
		}
		r.WriteString(trunc.Render(row) + "\n")
	}
	// Channels take what they need up to half the column; sessions get the
	// rest, and a blank row separates the two lists.
	total := m.stream.Height + 1
	chanRows := min(len(m.channels)+1, max(total/2, total-len(names)-2))
	channels := lipgloss.NewStyle().MaxHeight(chanRows).Render(l.String())
	sessions := lipgloss.NewStyle().MaxHeight(max(total-chanRows-1, 1)).Render(r.String())
	col := lipgloss.JoinVertical(lipgloss.Left, channels, "", sessions)
	return lipgloss.NewStyle().Width(rail).MaxHeight(total).Render(col)
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
	m.cursorLine = -1
	if ch == "" {
		return ""
	}
	if bus.IsTaskChannel(ch) {
		return m.renderTasks(ch)
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
	lineNum := 0
	// Threads reorder messages, so a seq range no longer maps to one place
	// in the stream: evicted gaps are listed at the top.
	for _, g := range gaps {
		b.WriteString(divider(itoa(g.To-g.From+1)+" evicted") + "\n")
		lineNum++
	}
	newShown := m.divider < 0
	for i, r := range m.rows(ch) {
		x := r.msg
		if !newShown && r.depth == 0 && r.newest > m.divider {
			b.WriteString(divider("new") + "\n")
			lineNum++
			newShown = true
		}
		name := th.Style(th.Agent).Render(x.Sender)
		if x.Sender == m.c.as {
			name = th.Style(th.User).Render(x.Sender)
		}
		// The tree prefix (indent plus expand/collapse marker) is applied
		// after wrapping so every wrapped line sits at the row's depth.
		prefix := strings.Repeat("  ", r.depth)
		switch {
		case r.hidden > 0:
			prefix += dim.Render(markSel + " ")
		case r.open:
			prefix += dim.Render(markOpen + " ")
		default:
			prefix += "  "
		}
		pw := lipgloss.Width(prefix)
		head := dim.Render(clock(x.CreatedAt)) + " " + name + " "
		indent := strings.Repeat(" ", lipgloss.Width(head))
		body := strings.ReplaceAll(x.Content, "\n", "\n"+indent)
		line := head + body
		if x.MemoryID != nil && x.Revision != nil && *x.Revision > 1 {
			line += " " + th.Style(th.Mem).Render("r"+itoa(*x.Revision))
		}
		if r.hidden > 0 {
			summary := strconv.Itoa(r.hidden) + " replies"
			if r.hidden == 1 {
				summary = "1 reply"
			}
			if r.depth == 0 {
				summary += " · " + clock(r.latest)
			}
			line += "\n" + indent + dim.Render(summary)
		}
		line = lipgloss.NewStyle().Width(w - pw).Render(line)
		line = prefix + strings.ReplaceAll(line, "\n", "\n"+strings.Repeat(" ", pw))
		if i == m.cursor && m.mode == modeNormal {
			line = th.Highlight(line, w)
		}
		if i == m.cursor {
			m.cursorLine = lineNum
		}
		b.WriteString(line + "\n")
		lineNum += strings.Count(line, "\n") + 1
	}
	return strings.TrimRight(b.String(), "\n")
}

// markSel marks the selected list item and a message whose replies can be
// expanded; markOpen marks a message whose replies are shown.
const (
	markSel  = "\u25b6" // ▶
	markOpen = "\u25bc" // ▼
)

func (m Model) renderReplyBanner() string {
	th := m.theme
	r := m.replyTo
	quote := strings.SplitN(r.Content, "\n", 2)[0]
	if rs := []rune(quote); len(rs) > 40 {
		quote = string(rs[:40]) + "…"
	}
	return th.Style(th.Dim).Render("replying to ") + th.Style(th.Agent).Render(r.Sender) + th.Style(th.Dim).Render(" #"+itoa(r.Seq)+" “"+quote+"”  esc cancel")
}

func (m Model) renderCompose() string {
	th := m.theme
	if m.mode == modeConfirmChannel {
		return m.confirmChannelLine()
	}
	if m.prompt.active {
		return th.Style(th.Agent).Render(m.prompt.label+" › ") + m.prompt.input.View()
	}
	ch := m.selected()
	label := ""
	hint := ""
	if ch != nil {
		mark, text := iconChat, ch.Name
		if ch.Kind == "memory" {
			mark = iconMem
			hint = th.Style(th.Dim).Render("  ⏎ new memory")
		}
		if owner, ok := strings.CutPrefix(ch.Name, bus.DMPrefix); ok {
			mark, text = iconAgent, "@"+owner
			if owner == m.c.as {
				mark = iconUser
			}
		}
		label = m.chanStyle(*ch).Render(mark + text)
	}
	prompt := label + th.Style(th.Dim).Render(" › ")
	if m.mode != modeInsert {
		prompt = label + th.Style(th.Dim).Render(" "+markSel+" ")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, prompt, m.compose.View(), hint)
}

func (m Model) renderStatusBar() string {
	th := m.theme
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
	help := m.hints("?", "help", "/", "search", "m", "memories", "h", "health", "q", "quit")
	if m.mode == modeInsert {
		help = m.hints("esc", "commands", "tab", "next pane", "alt+enter", "newline")
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

// hints renders key/description pairs as a help line: keys in the agent
// color, descriptions dim, so a key stands apart from the words around it.
func (m Model) hints(pairs ...string) string {
	key, dim := m.theme.Style(m.theme.Agent), m.theme.Style(m.theme.Dim)
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(key.Render(pairs[i]) + " " + dim.Render(pairs[i+1]))
	}
	return b.String()
}

// overlaySize returns the box width and height overlay renders at, so a
// view (e.g. viewSearch) can size and window its own content to match.
func (m Model) overlaySize() (w, h int) {
	return min(max(m.width-8, 40), 100), max(m.height-4, 10)
}

// overlay renders a titled, bordered box centered on the screen; the border
// color names the overlay (cyan search, magenta memories, green health).
func (m Model) overlay(title string, border lipgloss.TerminalColor, body, footer string) string {
	w, h := m.overlaySize()
	if m.toast != "" {
		footer = m.theme.Style(m.theme.Error).Render("✗ "+m.toast) + "\n" + footer
		h--
	}
	inner := lipgloss.JoinVertical(lipgloss.Left,
		m.theme.Style(border).Bold(true).Render(title),
		lipgloss.NewStyle().Width(w-4).Height(h-4).MaxHeight(h-4).Render(body),
		m.theme.Style(m.theme.Dim).Render(footer),
	)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(w - 2).Render(inner)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// window returns the [start, end) slice bounds that keep cursor visible
// among n items when only visible of them fit, centering cursor when the
// list is longer than that.
func window(cursor, n, visible int) (start, end int) {
	start = 0
	if n > visible {
		start = max(0, min(cursor-visible/2, n-visible))
	}
	return start, min(start+visible, n)
}
