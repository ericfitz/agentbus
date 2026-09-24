package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
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
	case "up":
		m.health.scroll = max(m.health.scroll-1, 0)
	case "down":
		maxScroll := max(len(m.healthLines())-1, 0)
		m.health.scroll = min(m.health.scroll+1, maxScroll)
	case "o":
		// A GUI $VISUAL runs off the terminal: the command waits for it in
		// the background, the TUI stays live, and the config check toasts
		// when the editor exits (with code --wait, when the tab closes).
		if cmd, ok := backgroundEditor(m.c.cfg.Path); ok {
			return func() tea.Msg { return configEditedMsg{err: cmd.Run()} }
		}
		return tea.ExecProcess(editorCommand(m.c.cfg.Path), func(err error) tea.Msg { return configEditedMsg{err: err} })
	}
	return nil
}

// onConfigEdited re-reads the config file after the editor closes. The
// running bus keeps its old settings; the toast says so.
func (m *Model) onConfigEdited(msg configEditedMsg) tea.Cmd {
	if msg.err != nil {
		return m.showToast(editorErrText(msg.err))
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
	p("%s %s\n", dim.Render("version"), "agentbus v"+mcpserver.Version)
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
	p("%s %s · %s %s · %d gaps\n", dim.Render("receive"), recv, dim.Render("last batch"), lastB, m.gapCount)
	p("%s %s\n\n", dim.Render("log ·"), mcpserver.LogPath(cfg))
	p("%s %s\n", dim.Render("theme ·"), th.Name)
	for _, kv := range config.DefaultTheme().Colors() {
		p("  %s %s\n", kv[0], th.Sources[kv[0]])
	}
	p("%s %s\n", dim.Render("icons ·"), th.IconSet)
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

// editorCommand runs $VISUAL, else $EDITOR, else vi, on path. A value that
// is itself an existing file is run as-is, so a bare path with spaces
// ("/Applications/Visual Studio Code.app/Contents/MacOS/Code") works;
// splitting it on whitespace used to break it at "/Applications/Visual".
// Anything else goes through the shell the way git runs GIT_EDITOR, so
// arguments and quoting work ("code --wait"). A blank or whitespace-only
// value falls back the same as unset.
func editorCommand(path string) *exec.Cmd {
	ed, _ := editorSetting()
	if st, err := os.Stat(ed); err == nil && !st.IsDir() {
		return exec.Command(ed, path)
	}
	return exec.Command("/bin/sh", "-c", ed+` "$1"`, "sh", path)
}

// terminalEditors need the terminal, so a $VISUAL naming one still blocks.
// ponytail: fixed list; a terminal editor missing from it would run in the
// background without a terminal, so add names as they come up.
var terminalEditors = map[string]bool{
	"vi": true, "vim": true, "view": true, "nvim": true, "nano": true, "pico": true,
	"emacs": true, "micro": true, "hx": true, "helix": true, "kak": true,
	"joe": true, "ne": true, "mg": true, "ed": true,
}

// backgroundEditor is $VISUAL on path, to run without the terminal while
// the TUI stays live; ok is false when $VISUAL is unset or names a
// terminal editor, and the caller then blocks as before.
func backgroundEditor(path string) (cmd *exec.Cmd, ok bool) {
	ed := strings.TrimSpace(os.Getenv("VISUAL"))
	if ed == "" || terminalEditors[filepath.Base(editorProgram(ed))] {
		return nil, false
	}
	return editorCommand(path), true
}

// editorProgram is the program an editor setting runs: the whole value when
// it is an existing file (a path with spaces), else its first shell word
// with surrounding quotes removed.
func editorProgram(ed string) string {
	if st, err := os.Stat(ed); err == nil && !st.IsDir() {
		return ed
	}
	if q := ed[0]; q == '\'' || q == '"' {
		if end := strings.IndexByte(ed[1:], q); end >= 0 {
			return ed[1 : end+1]
		}
	}
	prog, _, _ := strings.Cut(ed, " ")
	return prog
}

// editorSetting is the editor editorCommand runs and where it came from
// ("$VISUAL", "$EDITOR", or "default").
func editorSetting() (ed, source string) {
	if ed = strings.TrimSpace(os.Getenv("VISUAL")); ed != "" {
		return ed, "$VISUAL"
	}
	if ed = strings.TrimSpace(os.Getenv("EDITOR")); ed != "" {
		return ed, "$EDITOR"
	}
	return "vi", "default"
}

// editorErrText explains an editor failure for a toast. The shell exits 127
// when the editor command does not exist and 126 when it cannot be run;
// both usually mean a stale $VISUAL or $EDITOR, so name the setting.
func editorErrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		ed, source := editorSetting()
		switch ee.ExitCode() {
		case 127:
			return fmt.Sprintf("editor: command not found: %s (from %s)", ed, source)
		case 126:
			return fmt.Sprintf("editor: not executable: %s (from %s)", ed, source)
		}
	}
	return "editor: " + err.Error()
}
