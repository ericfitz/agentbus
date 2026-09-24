package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/mcpserver"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// senderName renders an envelope's sender for display: "bus" for the empty
// sender a tick reclaim writes (design: "the TUI shows it as bus"), else the
// sender as-is.
func senderName(s string) string {
	if s == "" {
		return "bus"
	}
	return s
}

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

// stamp renders a message time as "YYYY-MM-DD HH:MM:SS"; today's date is
// replaced by "(today)" padded to the same ten columns so times line up.
func stamp(ms int64) string {
	t := time.UnixMilli(ms).Local()
	date := t.Format("2006-01-02")
	if now := time.Now(); t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		date = "(today)   "
	}
	return date + " " + t.Format("15:04:05")
}

// agentLabel is an identity with its rail icon and color: the TUI's own
// name in the user color, everyone else (and the empty tick sender, shown
// as bus) in the agent color.
func (m Model) agentLabel(name string) string {
	if name == m.c.as {
		return iconUser + m.theme.Style(m.theme.User).Render(name)
	}
	return iconAgent + m.theme.Style(m.theme.Agent).Render(senderName(name))
}

// channelLabel is a message's recipient: the agent for a dm/ inbox, else
// the full channel name with its kind icon in chanStyle's color. A channel
// not (yet) in the rail is drawn as chat.
func (m Model) channelLabel(name string) string {
	if owner, ok := strings.CutPrefix(name, bus.DMPrefix); ok {
		return m.agentLabel(owner)
	}
	ch := bus.Channel{Name: name, Kind: "ordinary"}
	for _, c := range m.channels {
		if c.Name == name {
			ch = c
		}
	}
	icon := iconChat
	switch {
	case bus.IsTaskChannel(name):
		icon = iconTasks
	case ch.Kind == "memory":
		icon = iconMem
	}
	return m.chanStyle(ch).Render(icon + name)
}

// tagChips renders tags as " tag " chips on the tag color, one space apart.
// When the background alone emits no escape (tag is "default", or lipgloss
// is on its no-color profile) a chip would be invisible, so they fall back
// to dim "#tag" words.
func (m Model) tagChips(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	chip := lipgloss.NewStyle().Background(m.theme.Tag).Foreground(lipgloss.Color("15"))
	parts := make([]string, len(tags))
	if lipgloss.NewStyle().Background(m.theme.Tag).Render("x") == "x" {
		for i, t := range tags {
			parts[i] = "#" + t
		}
		return m.theme.Style(m.theme.Dim).Render(strings.Join(parts, " "))
	}
	for i, t := range tags {
		parts[i] = chip.Render(" " + t + " ")
	}
	return strings.Join(parts, " ")
}

// header is a message's first line: timestamp, sender --> recipient, tag
// chips. It never wraps: past avail columns the chips are cut first (dropped
// under four columns), then the whole line is cut with an ellipsis.
func (m Model) header(x bus.Message, stampStyle lipgloss.Style, avail int) string {
	head := stampStyle.Render(stamp(x.CreatedAt)) + "  " + m.agentLabel(x.Sender) + " --> " + m.channelLabel(x.Channel)
	if chips := m.tagChips(x.Tags); chips != "" {
		if room := avail - lipgloss.Width(head) - 2; room >= lipgloss.Width(chips) {
			head += "  " + chips
		} else if room >= 4 {
			head += "  " + ansi.Truncate(chips, room, "…")
		}
	}
	if lipgloss.Width(head) > avail {
		head = ansi.Truncate(head, avail, "…")
	}
	return head
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
	iconTasks = "\U0001F4CB\uFE0F "           // clipboard; Wide like chat and memory, so no CSI trick
)

// Task status marks. The stopwatch U+23F1 is text-presentation by default
// (like the gear) and carries U+FE0F for the color glyph; if a terminal
// font draws its own half-width stopwatch, give it the gear's CSI 2X/1C
// treatment.
const (
	taskPending    = "\u274E "       // negative squared cross mark
	taskInProgress = "\u23F1\uFE0F " // stopwatch
	taskCompleted  = "\u2705 "       // white heavy check mark
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

// chanStyle is the color a channel's name is drawn in everywhere: task
// lists in the tasks color, other memory channels in the memory color, a DM inbox in the user color for the TUI's
// own inbox and the agent color for anyone else's, chat channels in the
// agent color.
func (m Model) chanStyle(c bus.Channel) lipgloss.Style {
	switch {
	case bus.IsTaskChannel(c.Name):
		return m.theme.Style(m.theme.Tasks)
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
	switch {
	case isTagPane(ch.Name):
		s = m.tagChips(tagPaneSet(ch.Name)) + fmt.Sprintf(" · %d messages", len(m.rows(ch.Name)))
	case strings.HasPrefix(ch.Name, bus.DMPrefix):
		owner, _ := strings.CutPrefix(ch.Name, bus.DMPrefix)
		s = fmt.Sprintf("%s · direct · %d unread · %d messages", m.chanStyle(*ch).Render("@"+owner), m.unread(ch.Name), ch.Messages)
	default:
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
	// tagsShown tracks whether the "tags" section header has been written
	// yet: tag panes sort after every real channel (their names start
	// "tags:"), so the header goes up once, right before the first one.
	tagsShown := false
	for i, c := range m.channels {
		var line string
		if isTagPane(c.Name) {
			if !tagsShown {
				l.WriteString(dim.Render("tags") + "\n")
				tagsShown = true
			}
			line = m.tagChips(tagPaneSet(c.Name))
		} else {
			mark := iconChat
			switch {
			case bus.IsTaskChannel(c.Name):
				mark = iconTasks
			case c.Kind == "memory":
				mark = iconMem
			}
			line = m.chanStyle(c).Render(mark + c.Name)
			if n := m.unread(c.Name); n > 0 {
				line += " " + th.Style(th.Agent).Render(strconv.Itoa(n))
			}
			if !isTagPane(c.Name) && !m.c.isSubscribed(c.Name) {
				line += dim.Render(" (off)")
			}
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
	extra := 0
	if tagsShown {
		extra = 1
	}
	total := m.stream.Height + 1
	chanRows := min(len(m.channels)+1+extra, max(total/2, total-len(names)-2))
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
// bus reported gaps, and the normal-mode cursor row highlighted. Each message
// is a header line (timestamp, sender --> recipient, tag chips) followed by
// its body at the row's depth.
func (m *Model) renderStream() string {
	th := m.theme
	dim := th.Style(th.Dim)
	ch := m.selName()
	m.cursorLine = -1
	if ch == "" {
		return ""
	}
	if bus.IsTaskChannel(ch) {
		return m.renderTasks(ch)
	}
	// A dm/X pane also shows X's outgoing messages (#4), so emptiness is
	// judged on the merged rows, not the inbox alone.
	if len(m.paneMsgs(ch)) == 0 {
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
		selected := i == m.cursor && m.mode == modeNormal
		// On the selected row every dim segment (timestamp, marker, summary)
		// takes the text color so it stays readable on the selection
		// background; chips keep their own background.
		rowDim, stampStyle := dim, th.Style(th.Stamp)
		if selected {
			rowDim, stampStyle = th.Style(th.Text), th.Style(th.Text)
		}
		// The tree prefix (indent plus expand/collapse marker) is applied
		// after wrapping so every wrapped line sits at the row's depth.
		prefix := strings.Repeat("  ", r.depth)
		switch {
		case r.hidden > 0:
			prefix += rowDim.Render(markSel + " ")
		case r.open:
			prefix += rowDim.Render(markOpen + " ")
		default:
			prefix += "  "
		}
		pw := lipgloss.Width(prefix)
		line := m.header(x, stampStyle, w-pw) + "\n" + x.Content
		if x.MemoryID != nil && x.Revision != nil && *x.Revision > 1 {
			line += " " + th.Style(th.Mem).Render("r"+itoa(*x.Revision))
		}
		if r.hidden > 0 {
			summary := strconv.Itoa(r.hidden) + " replies"
			if r.hidden == 1 {
				summary = "1 reply"
			}
			if r.depth == 0 {
				summary += " \u00b7 " + clock(r.latest)
			}
			line += "\n" + rowDim.Render(summary)
		}
		line = lipgloss.NewStyle().Width(w - pw).Render(line)
		line = prefix + strings.ReplaceAll(line, "\n", "\n"+strings.Repeat(" ", pw))
		if selected {
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
	switch {
	case ch == nil:
	case isTagPane(ch.Name):
		label = m.tagChips(tagPaneSet(ch.Name))
	default:
		mark, text := iconChat, ch.Name
		switch {
		case bus.IsTaskChannel(ch.Name):
			mark = iconTasks
		case ch.Kind == "memory":
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
	left := th.Style(th.Dim).Render("agentbus v"+mcpserver.Version) + "  " + fmt.Sprintf("db %s / %s  embed %s  backlog %d  as %s", fmtBytes(m.status.UsageBytes), fmtBytes(m.status.BudgetBytes), embed, m.status.EmbeddingBacklog, th.Style(th.User).Render(m.c.as))
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
