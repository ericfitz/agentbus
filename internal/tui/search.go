package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

const searchCount = 20

type searchState struct {
	input        textinput.Model
	mode         string
	query        string // the query the current hits answer; typing past it makes enter re-run
	hits         []bus.SearchHit
	cursor       int
	ran          bool
	textOnly     bool
	semanticDown bool
	err          error
}

func (m *Model) openSearch() tea.Cmd {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 512
	// Static cursor: see the compose textarea's Cursor.SetMode note in model.go.
	in.Cursor.SetMode(cursor.CursorStatic)
	mode := "text"
	if m.c.cfg.EmbeddingEndpoint != "" {
		mode = "both"
	}
	m.search = searchState{input: in, mode: mode, semanticDown: m.search.semanticDown}
	m.mode = modeSearch
	return m.search.input.Focus() // focus the copy the model keeps, not the local
}

func (m *Model) updateSearch(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc":
		m.mode = modeNormal
		return nil
	case "tab":
		switch m.search.mode {
		case "text":
			m.search.mode = "semantic"
		case "semantic":
			m.search.mode = "both"
		default:
			m.search.mode = "text"
		}
		return nil
	case "up":
		m.search.cursor = max(m.search.cursor-1, 0)
		return nil
	case "down":
		m.search.cursor = min(m.search.cursor+1, max(len(m.search.hits)-1, 0))
		return nil
	case "enter":
		if m.search.ran && len(m.search.hits) > 0 && strings.TrimSpace(m.search.input.Value()) == m.search.query {
			hit := m.search.hits[m.search.cursor]
			m.mode = modeNormal
			return m.jumpTo(hit.Message)
		}
		return m.runSearch()
	}
	var cmd tea.Cmd
	m.search.input, cmd = m.search.input.Update(msg)
	return cmd
}

func (m *Model) runSearch() tea.Cmd {
	q := strings.TrimSpace(m.search.input.Value())
	if q == "" {
		return nil
	}
	m.search.query = q
	c := m.c
	in := bus.SearchInput{Query: q, Mode: m.search.mode, Count: searchCount}
	return func() tea.Msg {
		res, err := c.b.Search(c.as, in)
		return searchMsg{res: res, err: err}
	}
}

// jumpTo selects the hit's channel, puts the normal-mode cursor on the hit,
// and loads the page of history before it (the historyMsg handler keeps the
// cursor on the hit when that page is prepended).
func (m *Model) jumpTo(hit bus.Message) tea.Cmd {
	for i, c := range m.channels {
		if c.Name == hit.Channel {
			m.selectChannel(i)
		}
	}
	m.loaded[hit.Channel] = true
	m.addMessages(hit.Channel, []bus.Message{hit})
	m.markSeen(hit.Channel)
	m.placeCursor(hit.Seq)
	before := hit.Seq
	c := m.c
	return func() tea.Msg {
		ms, err := c.b.History(c.as, hit.Channel, &before, nil, historyPage)
		return historyMsg{channel: hit.Channel, msgs: ms, prepend: true, err: err}
	}
}

func (m Model) viewSearch() string {
	th := m.theme
	dim := th.Style(th.Dim)
	var b strings.Builder
	b.WriteString(m.search.input.View() + "\n\n")
	for i, h := range m.search.hits {
		mark := "  "
		if h.MemoryID != nil {
			mark = th.Style(th.Mem).Render("◆ ")
		}
		rev := ""
		if h.Revision != nil {
			rev = " r" + itoa(*h.Revision)
		}
		first := strings.SplitN(h.Content, "\n", 2)[0]
		line := fmt.Sprintf("%s%s #%d%s %s %s  %s", mark, h.Channel, h.Seq, rev, th.Style(th.Agent).Render(h.Sender), dim.Render(clock(h.CreatedAt)), first)
		if i == m.search.cursor {
			line = th.Style(th.Agent).Render("› ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	if m.search.ran {
		summary := fmt.Sprintf("%d results", len(m.search.hits))
		if m.search.err != nil {
			summary = th.Style(th.Error).Render("✗ " + errText(m.search.err))
		}
		b.WriteString("\n" + dim.Render(summary))
	}
	if m.search.textOnly {
		b.WriteString("\n" + th.Style(th.Warn).Render("text only — embedding endpoint unreachable"))
	}
	title := "search  " + dim.Render("mode "+m.search.mode+" · tab to switch")
	return m.overlay(title, th.Agent, b.String(), "enter search / open  ↑↓ move  esc close")
}
