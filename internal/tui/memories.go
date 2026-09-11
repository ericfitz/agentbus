package tui

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/bus"
)

type memState struct {
	channel string
	list    []bus.Message
	cursor  int
	revs    []bus.Message
	rev     int
	err     error
}

func (s memState) current() *bus.Message {
	if s.cursor < 0 || s.cursor >= len(s.list) {
		return nil
	}
	return &s.list[s.cursor]
}

func (s memState) currentID() int64 {
	if c := s.current(); c != nil && c.MemoryID != nil {
		return *c.MemoryID
	}
	return 0
}

// openMemories browses the selected channel if it is a memory channel, else
// the first memory channel in the rail.
func (m *Model) openMemories() tea.Cmd {
	ch := ""
	if c := m.selected(); c != nil && c.Kind == "memory" {
		ch = c.Name
	}
	if ch == "" {
		for _, c := range m.channels {
			if c.Kind == "memory" {
				ch = c.Name
				break
			}
		}
	}
	if ch == "" {
		return m.showToast("no memory channels · c then \"name memory\" creates one")
	}
	m.mem = memState{channel: ch}
	m.mode = modeMemories
	return m.loadMemoryList()
}

// loadMemoryList fetches the newest historyPage live memories of the
// channel. Every live row in a memory channel is a memory's current
// revision, keyed by memory_id (unchanged by edits); "newest first" orders
// by that id descending, not by the row's own seq, since editing a memory
// bumps its live seq without making it a newer memory.
func (m *Model) loadMemoryList() tea.Cmd {
	c := m.c
	ch := m.mem.channel
	return func() tea.Msg {
		top := int64(math.MaxInt64)
		ms, err := c.b.History(c.as, ch, &top, nil, historyPage)
		sort.SliceStable(ms, func(i, j int) bool { return memoryID(ms[i]) > memoryID(ms[j]) })
		return memListMsg{channel: ch, msgs: ms, err: err}
	}
}

func memoryID(x bus.Message) int64 {
	if x.MemoryID != nil {
		return *x.MemoryID
	}
	return 0
}

// loadRevisions clears the (now stale) revisions of whatever memory was
// previously current before returning the command to fetch the new
// cursor's, so the detail section never shows one memory's id label over
// another's content while the fetch is in flight.
func (m *Model) loadRevisions() tea.Cmd {
	m.mem.revs, m.mem.rev = nil, 0
	id := m.mem.currentID()
	if id == 0 {
		return nil
	}
	c := m.c
	return func() tea.Msg {
		revs, err := c.b.MemoryRevisions(c.as, id)
		return revisionsMsg{id: id, revs: revs, err: err}
	}
}

func (m *Model) updateMemories(msg tea.Msg) tea.Cmd {
	k := keyString(msg)
	if m.mode == modeConfirmDelete {
		switch k {
		case "y":
			m.mode = modeMemories
			id := m.mem.currentID()
			c := m.c
			return func() tea.Msg { return memChangedMsg{id: id, err: c.b.DeleteMemory(c.as, id, "")} }
		case "n", "esc":
			m.mode = modeMemories
		}
		return nil
	}
	switch k {
	case "esc":
		m.mode = modeNormal
	case "up":
		m.mem.cursor = max(m.mem.cursor-1, 0)
		return m.loadRevisions()
	case "down":
		m.mem.cursor = min(m.mem.cursor+1, max(len(m.mem.list)-1, 0))
		return m.loadRevisions()
	case "left":
		m.mem.rev = max(m.mem.rev-1, 0)
	case "right":
		m.mem.rev = min(m.mem.rev+1, max(len(m.mem.revs)-1, 0))
	case "e":
		return m.editMemoryInEditor()
	case "d":
		if m.mem.current() != nil {
			m.mode = modeConfirmDelete
		}
	}
	return nil
}

// editorCommand runs $VISUAL, else $EDITOR, else vi, on path. A value that
// is itself an existing file is run as-is, so a bare path with spaces
// ("/Applications/Visual Studio Code.app/Contents/MacOS/Electron") works;
// splitting it on whitespace used to break it at "/Applications/Visual".
// Anything else goes through the shell the way git runs GIT_EDITOR, so
// arguments and quoting work ("code --wait"). A blank or whitespace-only
// value falls back the same as unset.
func editorCommand(path string) *exec.Cmd {
	ed := strings.TrimSpace(os.Getenv("VISUAL"))
	if ed == "" {
		ed = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if ed == "" {
		ed = "vi"
	}
	if st, err := os.Stat(ed); err == nil && !st.IsDir() {
		return exec.Command(ed, path)
	}
	return exec.Command("/bin/sh", "-c", ed+` "$1"`, "sh", path)
}

// editMemoryInEditor writes the current revision to a fresh, unique temp
// file (os.CreateTemp: unpredictable name, O_EXCL, 0600 — a shared,
// predictable path would let another process on the host race a symlink
// into place) and hands the terminal to the editor; memEditedMsg arrives
// when it exits.
func (m *Model) editMemoryInEditor() tea.Cmd {
	cur := m.mem.current()
	id := m.mem.currentID()
	if cur == nil || id == 0 {
		return nil
	}
	f, err := os.CreateTemp("", "agentbus-memory-*.md")
	if err != nil {
		return m.showToast("edit: " + err.Error())
	}
	original := cur.Content
	if _, err := f.WriteString(original + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return m.showToast("edit: " + err.Error())
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return m.showToast("edit: " + err.Error())
	}
	path := f.Name()
	return tea.ExecProcess(editorCommand(path), func(err error) tea.Msg {
		return memEditedMsg{id: id, path: path, original: original, err: err}
	})
}

// applyMemoryEdit reads the editor's file back, removes it, and edits the
// memory when the content changed. It compares against the content that was
// written to the file (msg.original) rather than re-reading the current
// memory, which may have moved on (list reload, cursor moved) since the
// editor was launched.
func (m *Model) applyMemoryEdit(msg memEditedMsg) tea.Cmd {
	defer func() { _ = os.Remove(msg.path) }()
	if msg.err != nil {
		return m.showToast("editor: " + msg.err.Error())
	}
	body, err := os.ReadFile(msg.path)
	if err != nil {
		return m.showToast("edit: " + err.Error())
	}
	content := strings.TrimRight(string(body), "\n")
	if content == "" {
		return m.showToast("empty edit ignored")
	}
	if content == msg.original {
		return m.showToast("memory unchanged")
	}
	c := m.c
	id := msg.id
	return func() tea.Msg {
		_, err := c.b.EditMemory(c.as, bus.EditInput{ID: id, Content: content})
		return memChangedMsg{id: id, err: err}
	}
}

// memoriesTail renders the detail section (current revision, type, refs)
// and, while confirming a delete, the confirmation prompt, as one string
// sized to fit within budget lines total. type, refs, and the confirm
// prompt are fixed-size overhead; the revision content is truncated harder
// as that overhead grows, so a long body plus type, refs, and a delete
// confirmation together can never push the y/n prompt past budget.
func (m Model) memoriesTail(budget int) string {
	th := m.theme
	dim := th.Style(th.Dim)
	confirming := m.mode == modeConfirmDelete && m.mem.current() != nil
	confirmLine := func() string {
		cur := m.mem.current()
		title := strings.SplitN(cur.Content, "\n", 2)[0]
		return "\n" + th.Style(th.Error).Render(fmt.Sprintf("delete memory #%d “%s”? tombstones all revisions · y yes  n no", m.mem.currentID(), title))
	}
	if len(m.mem.revs) == 0 {
		if confirming {
			return confirmLine()
		}
		return ""
	}
	r := m.mem.revs[min(m.mem.rev, len(m.mem.revs)-1)]
	overhead := 2 // blank line + revision header
	if r.Type != "" {
		overhead++
	}
	if len(r.Refs) > 0 {
		overhead++
	}
	if confirming {
		overhead += 2
	}
	contentBudget := max(budget-overhead, 1)
	var b strings.Builder
	b.WriteString("\n" + dim.Render(fmt.Sprintf("#%d · revision %d of %d · %s · %s", m.mem.currentID(), m.mem.rev+1, len(m.mem.revs), r.Sender, clock(r.CreatedAt))) + "\n")
	lines := strings.Split(r.Content, "\n")
	if len(lines) > contentBudget {
		show := max(contentBudget-1, 1)
		more := len(lines) - show
		b.WriteString(strings.Join(lines[:show], "\n") + "\n")
		b.WriteString(dim.Render(fmt.Sprintf("… (%d more lines)", more)) + "\n")
	} else {
		b.WriteString(r.Content + "\n")
	}
	if r.Type != "" {
		b.WriteString(dim.Render("type ") + r.Type + "\n")
	}
	if len(r.Refs) > 0 {
		refs := make([]string, len(r.Refs))
		for i, ref := range r.Refs {
			refs[i] = ref.Value
		}
		b.WriteString(dim.Render("refs ") + strings.Join(refs, " · ") + "\n")
	}
	if confirming {
		b.WriteString(confirmLine())
	}
	return b.String()
}

// tailLineCount returns the number of display lines s renders as, counting a
// final line with no trailing newline.
func tailLineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// viewMemories draws the memory list windowed to what fits above the detail
// section (revision content, type, refs) and, while confirming a delete, the
// confirmation prompt: the list can hold up to historyPage rows, so every row
// is width-truncated and only a window around the cursor is shown, sized so
// the tail below it is never clipped.
func (m Model) viewMemories() string {
	th := m.theme
	dim := th.Style(th.Dim)
	w, h := m.overlaySize()
	trunc := lipgloss.NewStyle().MaxWidth(w - 4)

	// Reserve at least one list row (h-4-1) as the tail's own truncation
	// budget, so the tail can never grow past what's left for it even before
	// its actual rendered length is known; the list then gets whatever room
	// the tail didn't need.
	tail := m.memoriesTail(max(h-5, 1))
	visible := max(h-4-tailLineCount(tail), 1)
	start, end := window(m.mem.cursor, len(m.mem.list), visible)

	var b strings.Builder
	for i := start; i < end; i++ {
		x := m.mem.list[i]
		id, rev := int64(0), int64(0)
		if x.MemoryID != nil {
			id = *x.MemoryID
		}
		if x.Revision != nil {
			rev = *x.Revision
		}
		title := strings.SplitN(x.Content, "\n", 2)[0]
		line := fmt.Sprintf("#%d r%d %s %s", id, rev, title, dim.Render(clock(x.CreatedAt)))
		if i == m.mem.cursor {
			line = th.Style(th.Agent).Render("› ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(trunc.Render(line) + "\n")
	}
	if m.mem.err != nil {
		b.WriteString(th.Style(th.Error).Render("✗ "+errText(m.mem.err)) + "\n")
	}
	if len(m.mem.list) == 0 {
		b.WriteString(dim.Render("no memories in "+m.mem.channel) + "\n")
	}
	b.WriteString(tail)
	count := fmt.Sprintf(" · %d memories", len(m.mem.list))
	if end-start < len(m.mem.list) {
		count = fmt.Sprintf(" · showing %d-%d of %d memories", start+1, end, len(m.mem.list))
	}
	title := th.Style(th.Mem).Render("◆ "+m.mem.channel) + dim.Render(count)
	return m.overlay(title, th.Mem, b.String(), m.hints("e", "edit", "d", "delete", "← →", "revision", "↑↓", "move", "esc", "close"))
}
