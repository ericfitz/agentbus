package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/config"
)

type healthState struct {
	lastQueryAt time.Time
	lastQueryOK bool
	scroll      int
}

func (m *Model) recordQuery(ok bool) {
	m.health.lastQueryAt = time.Now()
	m.health.lastQueryOK = ok
}

func (m *Model) openHealth() tea.Cmd {
	m.mode = modeHealth
	m.health.scroll = 0
	return m.statusCmd()
}

func (m *Model) updateHealth(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc", "h":
		m.mode = modeNormal
	case "?":
		return m.openHelp()
	case "up", "k":
		m.health.scroll = max(m.health.scroll-1, 0)
	case "down", "j":
		maxScroll := max(len(m.healthLines())-1, 0)
		m.health.scroll = min(m.health.scroll+1, maxScroll)
	case "o":
		return tea.ExecProcess(editorCommand(m.c.cfg.Path), func(err error) tea.Msg { return configEditedMsg{err: err} })
	}
	return nil
}

// onConfigEdited re-reads the config file after the editor closes. The
// running bus keeps its old settings; the toast says so.
func (m *Model) onConfigEdited(msg configEditedMsg) tea.Cmd {
	if msg.err != nil {
		return m.showToast("editor: " + msg.err.Error())
	}
	if _, _, err := config.Load(m.c.cfg.Path); err != nil {
		return m.showToast("config: " + err.Error())
	}
	return m.showToast("config saved; restart agentbus tui to apply")
}

// healthLines renders the health overlay's body as lines, shared by
// viewHealth and updateHealth (so scrolling down cannot go past the last
// line).
func (m Model) healthLines() []string {
	th := m.theme
	dim, ok, warn := th.Style(th.Dim), th.Style(th.Health), th.Style(th.Warn)
	st := m.status
	var b strings.Builder
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(&b, format, a...) }
	pct := 0
	if st.BudgetBytes > 0 {
		pct = int(st.UsageBytes * 100 / st.BudgetBytes)
	}
	filled := min(pct/5, 20)
	bar := strings.Repeat("█", filled) + dim.Render(strings.Repeat("█", 20-filled))
	p("%s %s of %s %s %d%%\n", dim.Render("storage"), fmtBytes(st.UsageBytes), fmtBytes(st.BudgetBytes), bar, pct)
	notice := "none"
	if st.Notice != "" {
		notice = warn.Render(st.Notice)
	}
	p("%s %s\n", dim.Render("notice"), notice)
	memCh := 0
	for _, c := range st.Channels {
		if c.Kind == "memory" {
			memCh++
		}
	}
	p("%s %d live · %s %d (%d memory)\n", dim.Render("sessions"), len(st.Sessions), dim.Render("channels"), len(st.Channels), memCh)
	cfg := m.c.cfg
	if cfg.EmbeddingEndpoint == "" {
		p("%s unset\n", dim.Render("embeddings"))
	} else {
		state := ok.Render("ok")
		if m.search.semanticDown {
			state = warn.Render("unreachable")
		}
		p("%s %s · %s · %s\n", dim.Render("embeddings"), state, cfg.EmbeddingModel, cfg.EmbeddingEndpoint)
	}
	lastQ := "none yet"
	if !m.health.lastQueryAt.IsZero() {
		lastQ = m.health.lastQueryAt.Format("15:04:05") + " "
		if m.health.lastQueryOK {
			lastQ += ok.Render("ok")
		} else {
			lastQ += warn.Render("text only")
		}
	}
	p("%s %d · %s %v s · %s %s\n", dim.Render("backlog"), st.EmbeddingBacklog, dim.Render("query timeout"), cfg.EmbeddingQueryTimeoutSeconds, dim.Render("last query"), lastQ)
	recv := ok.Render("long-poll connected")
	if m.receiveErr != nil {
		recv = th.Style(th.Error).Render(errText(m.receiveErr))
	}
	lastB := "none yet"
	if !m.lastBatchAt.IsZero() {
		lastB = m.lastBatchAt.Format("15:04:05")
	}
	p("%s %s · %s %s · %d gaps\n\n", dim.Render("receive"), recv, dim.Render("last batch"), lastB, m.gapCount)
	b.WriteString(dim.Render("theme") + dim.Render(" · config value, or the environment variable that overrides it") + "\n")
	for _, v := range themeVars {
		p("  %s %s", v.key, th.Sources[v.key])
		if env := th.Overrides[v.key]; env != "" {
			p(" %s", dim.Render("← "+env))
		}
		b.WriteString("\n")
	}
	p("\n%s %s\n", dim.Render("config ·"), cfg.Path)
	js, err := json.MarshalIndent(cfg, "  ", "  ")
	if err != nil {
		b.WriteString(err.Error())
	} else {
		b.WriteString("  " + string(js) + "\n")
	}
	return strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
}

func (m Model) viewHealth() string {
	lines := m.healthLines()
	if m.health.scroll < len(lines) {
		lines = lines[m.health.scroll:]
	}
	return m.overlay("health", m.theme.Health, strings.Join(lines, "\n"), m.hints("o", "open config in $EDITOR", "↑↓", "scroll", "?", "help", "esc", "close"))
}
