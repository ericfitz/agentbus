# agentbus tui Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `agentbus tui` — a live chat-client dashboard for the bus where a human can watch channels and sessions, send messages, create/edit/delete memories, search, and inspect health, all in one binary with no server.

**Architecture:** A new `internal/tui` package opens the SQLite bus through `internal/bus` exactly as `status` and `reset` do, registers as the human (`tui_name`), subscribes to every channel, and runs one goroutine that long-polls `Receive` and feeds batches into a Bubble Tea program as messages. The model is a single struct with a `mode` enum (insert, normal, search, memories, health, confirm-delete); the view is pure string rendering with Lip Gloss styles built once from environment-variable theme colours. Heartbeat and maintenance ticks reuse the loop the MCP server already runs.

**Tech Stack:** Go 1.27, `github.com/charmbracelet/bubbletea` v1.3.10, `github.com/charmbracelet/lipgloss` v1.1.0, `github.com/charmbracelet/bubbles` v1.0.0 (textarea, textinput, viewport). v1 of the Charm libraries, not v2: v2 moved to the `charm.land` import path and changed the `View` contract; v1 is stable, its API is the one every existing example uses, and the ANSI-only theme needs nothing from v2.

**Spec:** `docs/superpowers/specs/2026-09-08-agentbus-tui-design.md` (authoritative), plus the canvas "Design notes" artboard `docs/design/tui/Notes.dc.html` for the keymap and data flow. Where they disagree the spec wins.

## Global Constraints

- Data path is in-process: the TUI opens the bus via `bus.Open(cfg, log)`; never the MCP client.
- Identity: `tui_name` config key, default the OS user name; `--as` overrides. The bus's `Register` applies the name rule (1-128 bytes, no `/`, no control characters); the TUI does not re-implement it.
- Theme: ten `AGENTBUS_TUI_*COLOR` variables, read once at startup. Accepted values: the sixteen ANSI names, integers `0`-`15`, `default`; case-insensitive. Anything else prints one stderr line naming the variable and the accepted forms, then uses the role default and continues. `#RGB`/`#RRGGBB` are rejected.
- Search never errors on embeddings: `SemanticUnavailable` renders as a `text only` badge in the warn colour.
- Errors from the bus render as a one-line toast above the status bar for 5 s or until any key.
- Quit closes the bus only after the heartbeat and tick goroutines have exited (same ordering as `mcpserver.Run`).
- Message content is never coloured; only names, timestamps, dividers, badges, borders.
- Out of scope for v1: mouse, multiple humans, in-place config editing, reset, truecolour, message detail view.
- Every task ends with `gofmt -l .` clean, `go vet ./...`, `golangci-lint run ./...` reporting 0 issues, and `go test ./...` green. The repo is lint-clean at the start; keep it that way (write `_ = x.Close()` for discarded error returns).
- Commit after each task with a conventional-commit subject. Do not push.

## Interpretations recorded by this plan (not in the spec; flag to the user at the end)

1. **Two input modes.** The canvas keymap assigns letters (`j`, `k`, `m`, `h`, `q`, `r`, ...) to commands and also has a compose line. The plan resolves this with an insert/normal split: the compose line starts focused (insert); `esc` leaves it (normal mode, where the letter keymap applies); `i` or `enter` returns to insert. `tab` (next unread channel), `pgup`/`pgdn`, and `ctrl+c` work in both modes.
2. **Stream cursor.** In normal mode `↑`/`↓` move a highlighted cursor over the stream (so `r` has a message to reply to); `j`/`k` select channels. The canvas listed `j k or ↑ ↓` for channels only.
3. **Newline in compose is `alt+enter`**, not `shift+enter`: Bubble Tea v1 cannot distinguish shift+enter from enter in most terminals.
4. **Idle sessions.** The bus only lists sessions with a heartbeat in the last 30 s, so an "idle" session is one the TUI listed earlier and no longer sees. It renders dimmed with `○` and the age since last seen, and is dropped after one hour.
5. **Memory revisions** need one small read-only bus addition, `MemoryRevisions`, because old revisions are tombstoned and no existing read returns them. The spec's "bus changes needed: none required" predates this; the addition is one query.
6. **Health `o`** opens the config in `$EDITOR`; on return the TUI re-reads and validates the file and shows a toast (either the validation error or "saved; restart agentbus tui to apply"). Live reload is not attempted (spec: in-place config editing is out of scope).

---

## File structure

| File | Responsibility |
|------|----------------|
| `internal/config/config.go` (modify) | `tui_name` key, default from `os/user`, non-empty validation |
| `internal/bus/memories.go` (modify) | `MemoryRevisions(as, id)` read |
| `internal/mcpserver/server.go` (modify) | export `StartBackgroundLoops` |
| `internal/tui/theme.go` (new) | env-var colour parsing → `Theme` of `lipgloss.Style`s |
| `internal/tui/client.go` (new) | bus session: open, register, subscribe, receive loop, close |
| `internal/tui/msgs.go` (new) | every `tea.Msg` type the model consumes |
| `internal/tui/model.go` (new) | `Model` struct, `New`, `Init`, `Update` dispatch, channel/stream state |
| `internal/tui/compose.go` (new) | compose line: send, reply, recall, memory creation |
| `internal/tui/view.go` (new) | main-screen rendering |
| `internal/tui/search.go` (new) | search overlay state, update, view |
| `internal/tui/memories.go` (new) | memories overlay: list, revisions, edit, delete |
| `internal/tui/health.go` (new) | health overlay and config editor |
| `internal/tui/tui.go` (new) | `Run(cfg, as, stderr)` entry point |
| `main.go` (modify) | `tui` subcommand with `--config` and `--as` |
| `docs/install.md`, `README.md` (modify) | document `agentbus tui`, `tui_name`, theme variables |

Test files sit beside each source file (`*_test.go`) in package `tui`; they open a real bus in `t.TempDir()`.

---

### Task 1: `tui_name` config key

**Files:**
- Modify: `internal/config/config.go` (struct after `LogLevel`, `Default()`, `validate()`)
- Modify: `docs/install.md` (Configuration section, after the `embedding_query_timeout_seconds` paragraph, line ~83)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.TUIName string` (`json:"tui_name"`), default `defaultTUIName()` = OS user name or `"human"`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestTUINameDefaultsToOSUser(t *testing.T) {
	c := Default()
	if c.TUIName == "" {
		t.Fatal("tui_name default must not be empty")
	}
	if strings.ContainsRune(c.TUIName, '/') {
		t.Fatalf("tui_name default %q must not contain '/'", c.TUIName)
	}
}

func TestTUINameFromFile(t *testing.T) {
	p := write(t, t.TempDir(), `{"tui_name": "eric"}`)
	c, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.TUIName != "eric" {
		t.Fatalf("want eric, got %q", c.TUIName)
	}
}

func TestRejectsEmptyTUIName(t *testing.T) {
	p := write(t, t.TempDir(), `{"tui_name": ""}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "tui_name") {
		t.Fatalf("want tui_name error, got %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run 'TUIName' -v`
Expected: compile error `c.TUIName undefined`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`:

Add to the imports: `"os/user"`.

Add the field after `LogLevel`:

```go
	LogLevel                     string   `json:"log_level"`
	TUIName                      string   `json:"tui_name"`
```

Add to `Default()` after `LogLevel: "info",`:

```go
		TUIName:                      defaultTUIName(),
```

Add after `Default()`:

```go
// defaultTUIName is the OS user name, the identity `agentbus tui` registers
// under unless tui_name or --as says otherwise. On Windows user.Current
// returns DOMAIN\name; keep the part after the last backslash. A '/' would
// fail the bus's name rule, so it is replaced.
func defaultTUIName() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "human"
	}
	name := u.Username
	if i := strings.LastIndex(name, `\`); i >= 0 {
		name = name[i+1:]
	}
	return strings.ReplaceAll(name, "/", "-")
}
```

In `validate()`, before the final `return nil`:

```go
	if c.TUIName == "" {
		return errors.New("tui_name must not be empty")
	}
```

- [ ] **Step 4: Document**

In `docs/install.md`, after the `embedding_query_timeout_seconds` paragraph (ends "...and set `semantic_unavailable`."), add:

```markdown
`tui_name` (default: your OS user name) is the identity `agentbus tui`
registers under; `agentbus tui --as <name>` overrides it for one run. It
follows the same rule as agent names: 1-128 bytes, no `/`, no control
characters.
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/config/ && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go docs/install.md
git commit -m "feat(config): tui_name key for the agentbus tui identity"
```

---

### Task 2: Theme parsing

**Files:**
- Create: `internal/tui/theme.go`
- Test: `internal/tui/theme_test.go`

**Interfaces:**
- Produces:
  - `type Theme struct { BG, Text, Dim, Agent, User, Mem, Health, Warn, Error, Sel lipgloss.TerminalColor; Sources map[string]string }`
  - `func LoadTheme(getenv func(string) string, warn io.Writer) Theme`
  - `func ParseColor(s string) (lipgloss.TerminalColor, error)`
  - `func (t Theme) Style(c lipgloss.TerminalColor) lipgloss.Style` — foreground style; `lipgloss.NewStyle().Foreground(c)`
  - Package-level `themeVars []themeVar` in role order (name, env var, default index or -1 for terminal default).
  - `Theme.Sources` maps env var name → the resolved value string (`"cyan"`, `"default"`, ...) for the Health overlay's "theme" heading.

- [ ] **Step 1: Add the dependencies**

```bash
go get github.com/charmbracelet/bubbletea@v1.3.10 github.com/charmbracelet/lipgloss@v1.1.0 github.com/charmbracelet/bubbles@v1.0.0
```

(`go mod tidy` runs at the end of the task, after the code imports them.)

- [ ] **Step 2: Write the failing tests**

`internal/tui/theme_test.go`:

```go
package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestParseColorAcceptsNamesIndicesAndDefault(t *testing.T) {
	cases := map[string]lipgloss.TerminalColor{
		"cyan":        lipgloss.Color("6"),
		"CYAN":        lipgloss.Color("6"),
		"brightblack": lipgloss.Color("8"),
		"BrightWhite": lipgloss.Color("15"),
		"0":           lipgloss.Color("0"),
		"15":          lipgloss.Color("15"),
		"default":     lipgloss.NoColor{},
		" yellow ":    lipgloss.Color("3"),
	}
	for in, want := range cases {
		got, err := ParseColor(in)
		if err != nil || got != want {
			t.Errorf("ParseColor(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestParseColorRejectsHexAndJunk(t *testing.T) {
	for _, in := range []string{"#f00", "#ff0000", "16", "-1", "orange", ""} {
		if _, err := ParseColor(in); err == nil {
			t.Errorf("ParseColor(%q) accepted", in)
		}
	}
}

func TestLoadThemeUsesDefaultsAndReportsBadValues(t *testing.T) {
	env := map[string]string{
		"AGENTBUS_TUI_AGENTCOLOR": "green",
		"AGENTBUS_TUI_MEMCOLOR":   "#ff00ff",
	}
	var warn bytes.Buffer
	th := LoadTheme(func(k string) string { return env[k] }, &warn)
	if th.Agent != lipgloss.Color("2") {
		t.Fatalf("agent colour not applied: %v", th.Agent)
	}
	if th.Mem != lipgloss.Color("5") {
		t.Fatalf("bad value must fall back to default magenta, got %v", th.Mem)
	}
	if th.User != lipgloss.Color("3") || th.Dim != lipgloss.Color("8") || th.BG != (lipgloss.NoColor{}) {
		t.Fatalf("defaults wrong: %+v", th)
	}
	out := warn.String()
	if !strings.Contains(out, "AGENTBUS_TUI_MEMCOLOR") || !strings.Contains(out, "brightblack") || !strings.Contains(out, "default") {
		t.Fatalf("warning must name the variable and the accepted forms, got %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("exactly one line per bad variable, got %q", out)
	}
	if th.Sources["AGENTBUS_TUI_AGENTCOLOR"] != "green" || th.Sources["AGENTBUS_TUI_MEMCOLOR"] != "magenta" {
		t.Fatalf("sources wrong: %v", th.Sources)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'ParseColor|LoadTheme' -v`
Expected: FAIL, `undefined: ParseColor`.

- [ ] **Step 4: Implement**

`internal/tui/theme.go`:

```go
// Package tui is the agentbus terminal dashboard: a chat-client view of the
// bus that a human reads and posts to, running in-process on the bus
// package like the status and reset commands do.
package tui

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds one terminal colour per role. Only ANSI indices 0-15 and the
// terminal default are representable in v1, on purpose: the parser rejects
// #RGB so a later version can add it without silently changing behaviour.
type Theme struct {
	BG, Text, Dim, Agent, User, Mem, Health, Warn, Error, Sel lipgloss.TerminalColor
	// Sources records the resolved value per environment variable for the
	// Health overlay ("cyan", "default", ...).
	Sources map[string]string
}

type themeVar struct {
	env string
	def string // parseable default
	set func(*Theme, lipgloss.TerminalColor)
}

var themeVars = []themeVar{
	{"AGENTBUS_TUI_BGCOLOR", "default", func(t *Theme, c lipgloss.TerminalColor) { t.BG = c }},
	{"AGENTBUS_TUI_TEXTCOLOR", "default", func(t *Theme, c lipgloss.TerminalColor) { t.Text = c }},
	{"AGENTBUS_TUI_DIMCOLOR", "brightblack", func(t *Theme, c lipgloss.TerminalColor) { t.Dim = c }},
	{"AGENTBUS_TUI_AGENTCOLOR", "cyan", func(t *Theme, c lipgloss.TerminalColor) { t.Agent = c }},
	{"AGENTBUS_TUI_USERCOLOR", "yellow", func(t *Theme, c lipgloss.TerminalColor) { t.User = c }},
	{"AGENTBUS_TUI_MEMCOLOR", "magenta", func(t *Theme, c lipgloss.TerminalColor) { t.Mem = c }},
	{"AGENTBUS_TUI_HEALTHCOLOR", "green", func(t *Theme, c lipgloss.TerminalColor) { t.Health = c }},
	{"AGENTBUS_TUI_WARNCOLOR", "yellow", func(t *Theme, c lipgloss.TerminalColor) { t.Warn = c }},
	{"AGENTBUS_TUI_ERRORCOLOR", "red", func(t *Theme, c lipgloss.TerminalColor) { t.Error = c }},
	{"AGENTBUS_TUI_SELCOLOR", "brightblack", func(t *Theme, c lipgloss.TerminalColor) { t.Sel = c }},
}

var ansiNames = []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}

// ParseColor accepts the sixteen ANSI names (bright forms prefixed with
// "bright"), the integers 0-15, and "default"; case-insensitive, surrounding
// whitespace ignored.
func ParseColor(s string) (lipgloss.TerminalColor, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "default" {
		return lipgloss.NoColor{}, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 || n > 15 {
			return nil, errors.New("index out of range")
		}
		return lipgloss.Color(strconv.Itoa(n)), nil
	}
	base, bright := strings.CutPrefix(v, "bright")
	for i, name := range ansiNames {
		if base == name {
			if bright {
				i += 8
			}
			return lipgloss.Color(strconv.Itoa(i)), nil
		}
	}
	return nil, errors.New("unknown colour")
}

// LoadTheme reads every theme variable through getenv. A bad value prints one
// line to warn naming the variable and the accepted forms, then keeps the
// role's default; the TUI still starts.
func LoadTheme(getenv func(string) string, warn io.Writer) Theme {
	t := Theme{Sources: map[string]string{}}
	for _, v := range themeVars {
		value := v.def
		if raw := getenv(v.env); raw != "" {
			if _, err := ParseColor(raw); err != nil {
				_, _ = fmt.Fprintf(warn, "agentbus tui: %s=%q is not a colour (%v); accepted: black, red, green, yellow, blue, magenta, cyan, white, their bright forms such as brightblack, 0-15, or default; using %s\n", v.env, raw, err, v.def)
			} else {
				value = strings.ToLower(strings.TrimSpace(raw))
			}
		}
		c, _ := ParseColor(value) // v.def and the validated raw both parse
		v.set(&t, c)
		t.Sources[v.env] = value
	}
	return t
}

// Style is a foreground-only style in colour c.
func (t Theme) Style(c lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c)
}
```

- [ ] **Step 5: Tidy and run the tests**

Run: `go mod tidy && go test ./internal/tui/ -v && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues. `go.mod` now lists the three Charm modules as direct requirements.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/tui/theme.go internal/tui/theme_test.go
git commit -m "feat(tui): theme parsing from AGENTBUS_TUI_*COLOR"
```

---

### Task 3: Bus and server additions the TUI needs

**Files:**
- Modify: `internal/bus/memories.go` (append)
- Modify: `internal/mcpserver/server.go:277` (rename `startBackgroundLoops` → `StartBackgroundLoops`, update its two call sites: `Run` and any test)
- Test: `internal/bus/memories_test.go` (append)

**Interfaces:**
- Produces:
  - `func (b *Bus) MemoryRevisions(as string, id int64) ([]Message, error)` — every revision row of memory `id`, oldest first, tombstoned ones included; `not_found` when no row exists.
  - `func mcpserver.StartBackgroundLoops(ctx context.Context, b *bus.Bus, cfg config.Config, log *slog.Logger, heartbeatEvery time.Duration) *sync.WaitGroup` — unchanged body, exported.

- [ ] **Step 1: Write the failing test**

Append to `internal/bus/memories_test.go` (`newTestBus` lives in `bus_test.go`, `reg` in `messages_test.go`; `strings` is already imported):

```go
func TestMemoryRevisionsListsAllRevisionsOldestFirst(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	c, err := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	revs, err := b.MemoryRevisions(sam, *c.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 || revs[0].Content != "v1" || revs[1].Content != "v2" || *revs[0].Revision != 1 || *revs[1].Revision != 2 {
		t.Fatalf("want [v1 r1, v2 r2], got %+v", revs)
	}
	if _, err := b.MemoryRevisions(sam, 999999); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("want not_found, got %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/bus/ -run MemoryRevisions -v`
Expected: FAIL, `b.MemoryRevisions undefined`.

- [ ] **Step 3: Implement**

Append to `internal/bus/memories.go`:

```go
// MemoryRevisions returns every revision of memory id, oldest first,
// including tombstoned ones, so a reader can browse a memory's history.
// Old revisions are only kept for tombstone_min_hours; after that the list
// shrinks to whatever the tick has not purged yet.
func (b *Bus) MemoryRevisions(as string, id int64) ([]Message, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	rows, err := b.db.Query("SELECT "+messageColumns+" FROM messages WHERE memory_id=? ORDER BY revision ASC", id)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, internal(err)
	}
	if len(msgs) == 0 {
		return nil, errf("not_found", false, "memory %d does not exist", id)
	}
	return msgs, nil
}
```

Then in `internal/mcpserver/server.go` rename `startBackgroundLoops` to `StartBackgroundLoops` (definition at line ~277, call in `Run`, and any reference in `internal/mcpserver/*_test.go`; `rg -n startBackgroundLoops internal/` must return nothing afterwards). Update the doc comment's first word to match.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/bus/ ./internal/mcpserver/ && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/memories.go internal/bus/memories_test.go internal/mcpserver/
git commit -m "feat(bus): MemoryRevisions; export StartBackgroundLoops for the tui"
```

---

### Task 4: Bus client for the TUI

**Files:**
- Create: `internal/tui/client.go`
- Create: `internal/tui/msgs.go`
- Test: `internal/tui/client_test.go`

**Interfaces:**
- Consumes: `bus.Open`, `Register`, `ListChannels`, `Subscribe`, `Receive`, `Close`, `mcpserver.StartBackgroundLoops`, `mcpserver.OpenLog`.
- Produces:

```go
type client struct {
	b      *bus.Bus
	cfg    config.Config
	as     string      // display name Register returned
	ctx    context.Context
	cancel context.CancelFunc
	wg     *sync.WaitGroup
	subscribed map[string]bool
}
func newClient(cfg config.Config, name string, log *slog.Logger) (*client, error)
func (c *client) subscribe(ch bus.Channel, from string) error   // idempotent per channel
func (c *client) receiveLoop(send func(tea.Msg))                 // blocks until ctx cancelled or bus closed
func (c *client) close() error                                    // cancel, wait, Close
```

and in `msgs.go` the message types every later task uses:

```go
type batchMsg struct{ res bus.ReceiveResult }
type receiveErrMsg struct{ err error }
type statusMsg struct { st bus.Status; err error }
type statusTickMsg struct{}
type historyMsg struct { channel string; msgs []bus.Message; prepend bool; err error }
type sentMsg struct { channel string; err error }
type searchMsg struct { res bus.SearchResult; err error }
type memListMsg struct { channel string; msgs []bus.Message; err error }
type revisionsMsg struct { id int64; revs []bus.Message; err error }
type memEditedMsg struct { id int64; path string; err error }
type memChangedMsg struct { id int64; err error }   // an edit or delete finished; reload the list
type configEditedMsg struct{ err error }
type toastClearMsg struct{ seq int }
type subscribedMsg struct { channel string; err error }
```

- [ ] **Step 1: Write the failing test**

`internal/tui/client_test.go`:

```go
package tui

import (
	"io"
	"log/slog"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = cfg.DataDirectory + "/config.json"
	cfg.ReceiveMaxWaitSeconds = 1
	return cfg
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// agent registers a second identity on the same database, as an agent
// process would, so tests can post messages the TUI must see.
func agent(t *testing.T, cfg config.Config, name string) (*bus.Bus, string) {
	t.Helper()
	b, err := bus.Open(cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	r, err := b.Register(name, "", "test", false)
	if err != nil {
		t.Fatal(err)
	}
	return b, r.Sender
}

func TestNewClientRegistersAndSubscribesToEveryChannel(t *testing.T) {
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	for _, ch := range []string{"dev", "notes"} {
		kind := "ordinary"
		if ch == "notes" {
			kind = "memory"
		}
		if _, err := ab.CreateChannel(sam, ch, kind); err != nil {
			t.Fatal(err)
		}
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.close() }()
	if c.as != "eric" {
		t.Fatalf("as = %q", c.as)
	}
	if !c.subscribed["dev"] || !c.subscribed["notes"] {
		t.Fatalf("subscribed = %v", c.subscribed)
	}
	live, err := c.b.LiveSessionCount()
	if err != nil || live != 2 {
		t.Fatalf("live sessions = %d, %v", live, err)
	}
}

func TestReceiveLoopDeliversBatchesAndStopsOnClose(t *testing.T) {
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	if _, err := ab.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan tea.Msg, 16)
	done := make(chan struct{})
	go func() { c.receiveLoop(func(m tea.Msg) { got <- m }); close(done) }()
	if _, err := ab.Send(sam, bus.SendInput{Channel: "dev", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		b, ok := m.(batchMsg)
		if !ok || len(b.res.Messages) != 1 || b.res.Messages[0].Content != "hello" {
			t.Fatalf("got %#v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no batch delivered")
	}
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("receive loop did not stop after close")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui/ -run 'NewClient|ReceiveLoop' -v`
Expected: FAIL, `undefined: newClient`.

- [ ] **Step 3: Implement**

`internal/tui/msgs.go`:

```go
package tui

import "github.com/ericfitz/agentbus/internal/bus"

// Messages delivered to Model.Update. Every bus call the model makes runs in
// a tea.Cmd and comes back as one of these; the receive goroutine sends
// batchMsg and receiveErrMsg directly.
type (
	batchMsg        struct{ res bus.ReceiveResult }
	receiveErrMsg   struct{ err error }
	statusTickMsg   struct{}
	statusMsg       struct {
		st  bus.Status
		err error
	}
	historyMsg struct {
		channel string
		msgs    []bus.Message
		prepend bool
		err     error
	}
	sentMsg struct {
		channel string
		err     error
	}
	searchMsg struct {
		res bus.SearchResult
		err error
	}
	memListMsg struct {
		channel string
		msgs    []bus.Message
		err     error
	}
	revisionsMsg struct {
		id   int64
		revs []bus.Message
		err  error
	}
	memEditedMsg struct {
		id   int64
		path string
		err  error
	}
	memChangedMsg struct { // an edit or delete finished; reload the list
		id  int64
		err error
	}
	configEditedMsg struct{ err error }
	toastClearMsg   struct{ seq int }
	subscribedMsg   struct {
		channel string
		err     error
	}
)
```

`internal/tui/client.go`:

```go
package tui

import (
	"context"
	"log/slog"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
)

// client is the TUI's bus session: one registration, a subscription to every
// channel, and the heartbeat/tick loops the mcp server also runs.
type client struct {
	b          *bus.Bus
	cfg        config.Config
	as         string
	ctx        context.Context
	cancel     context.CancelFunc
	wg         *sync.WaitGroup
	subscribed map[string]bool
}

// newClient opens the bus, registers name (resuming an earlier session of
// the same name so its cursors survive a restart), and subscribes to every
// existing channel from "now".
func newClient(cfg config.Config, name string, log *slog.Logger) (*client, error) {
	b, err := bus.Open(cfg, log)
	if err != nil {
		return nil, err
	}
	reg, err := b.Register(name, "", "tui", true)
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{b: b, cfg: cfg, as: reg.Sender, ctx: ctx, cancel: cancel, subscribed: map[string]bool{}}
	c.wg = mcpserver.StartBackgroundLoops(ctx, b, cfg, log, 10*time.Second)
	chans, err := b.ListChannels(c.as)
	if err != nil {
		_ = c.close()
		return nil, err
	}
	for _, ch := range chans {
		if err := c.subscribe(ch, "now"); err != nil {
			_ = c.close()
			return nil, err
		}
	}
	return c, nil
}

// subscribe is idempotent per channel name.
func (c *client) subscribe(ch bus.Channel, from string) error {
	if c.subscribed[ch.Name] {
		return nil
	}
	if err := c.b.Subscribe(c.as, ch.Name, from); err != nil {
		return err
	}
	c.subscribed[ch.Name] = true
	return nil
}

// receiveLoop long-polls Receive, acking the previous batch each time, and
// hands every non-empty batch to send. It returns once the context is
// cancelled; a Receive in flight at that moment ends on its own when close()
// shuts the database (the bus returns an error, which is ignored after
// cancellation). Own messages are included so the human's sends arrive
// through the same path as everyone else's.
func (c *client) receiveLoop(send func(tea.Msg)) {
	ack := ""
	for c.ctx.Err() == nil {
		res, err := c.b.Receive(c.as, bus.ReceiveInput{
			Ack:         ack,
			Count:       c.cfg.ReceiveMaxCount,
			WaitSeconds: c.cfg.ReceiveMaxWaitSeconds,
			IncludeOwn:  true,
		})
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			send(receiveErrMsg{err})
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		ack = res.Batch
		if len(res.Messages) > 0 || len(res.Gaps) > 0 || len(res.Expired) > 0 || res.Notice != "" {
			send(batchMsg{res})
		}
	}
}

// close stops the background loops, waits for them, then closes the bus:
// the same order as mcpserver.Run, so no loop runs against a closed bus.
func (c *client) close() error {
	c.cancel()
	c.wg.Wait()
	return c.b.Close()
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tui/ -v && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues. If `TestReceiveLoopDeliversBatchesAndStopsOnClose` hangs at "did not stop after close", the bus's `Receive` is sleeping between polls with `ReceiveMaxWaitSeconds` left; the test sets it to 1 s so the loop must return within ~1.3 s.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/client.go internal/tui/msgs.go internal/tui/client_test.go
git commit -m "feat(tui): bus client with receive loop"
```

---

### Task 5: Model core — channels, stream, unread, gaps, navigation

**Files:**
- Create: `internal/tui/model.go`
- Test: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: `client`, `Theme`, message types from Task 4.
- Produces (used by every later task):

```go
type mode int
const (
	modeInsert mode = iota // compose focused
	modeNormal             // letter keymap active
	modeSearch
	modeMemories
	modeHealth
	modeConfirmDelete
)

const historyPage = 200

type Model struct {
	c      *client
	theme  Theme
	width, height int
	mode   mode

	channels []bus.Channel           // sorted by name
	sel      int                     // index into channels; -1 when none
	msgs     map[string][]bus.Message // per channel, ascending seq, unique
	gaps     map[string][]bus.Gap
	loaded   map[string]bool         // history fetched at least once
	seen     map[string]int64        // last-seen seq per channel
	divider  int64                   // "new" divider: messages after this seq are new (-1 = none)
	cursor   int                     // normal-mode stream cursor index; -1 = none/follow
	stream   viewport.Model
	follow   bool                    // stick to bottom on new messages

	compose textarea.Model
	replyTo *bus.Message
	lastSent string

	status       bus.Status
	statusErr    error
	sessionsSeen map[string]time.Time // sender -> last time listed live
	lastBatchAt  time.Time
	gapCount     int
	receiveErr   error

	toast    string
	toastSeq int

	search searchState   // Task 8
	mem    memState      // Task 9
	health healthState   // Task 10
}

func New(c *client, th Theme) Model
func (m Model) Init() tea.Cmd
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)
func (m Model) View() string                       // Task 7 replaces the stub
func (m *Model) selected() *bus.Channel            // nil when no channels
func (m *Model) unread(ch string) int
func (m *Model) selectChannel(i int) tea.Cmd       // sets divider/seen, loads history if needed
func (m *Model) nextUnread() tea.Cmd
func (m *Model) addMessages(ch string, in []bus.Message)   // merge, keep ascending unique
func (m *Model) showToast(s string) tea.Cmd
func (m *Model) loadHistory(ch string, before *int64) tea.Cmd
func (m *Model) statusCmd() tea.Cmd
func (m *Model) refreshStream()                    // Task 7 fills in; stub sets content ""
func (m *Model) placeCursor(seq int64)             // normal-mode cursor onto the message with seq, scrolled into view
func keyString(msg tea.Msg) string                 // tea.KeyMsg -> msg.String(), "" otherwise
var tick = tea.Tick                                // tests replace it so timers never sleep
```

For Task 5, `searchState`, `memState`, `healthState` are declared as empty structs in `model.go` (`type searchState struct{}` etc.); Tasks 8-10 replace those declarations with real ones in their own files (delete the empty declaration from `model.go` when doing so).

- [ ] **Step 1: Write the failing tests**

`internal/tui/model_test.go`:

```go
package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

// fixture opens a bus with channels dev (ordinary) and notes (memory), an
// agent "Sam", and a TUI model registered as "eric" whose Init has run.
type fixture struct {
	m   Model
	c   *client
	ab  *bus.Bus
	sam string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	if _, err := ab.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := ab.CreateChannel(sam, "notes", "memory"); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	f := &fixture{c: c, ab: ab, sam: sam}
	f.m = New(c, LoadTheme(func(string) string { return "" }, nil))
	f.m.width, f.m.height = 100, 32
	f.run(f.m.Init())
	return f
}

// Timers (status tick, toast clear) would sleep for real inside run; tests
// drive those messages by hand instead.
func init() { tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil } }

// run executes cmd synchronously and feeds every resulting message back
// through Update, recursively, so tests see the same state the program would.
func (f *fixture) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			f.run(c)
		}
		return
	}
	if _, ok := msg.(statusTickMsg); ok {
		return // the periodic timer; tests trigger status explicitly
	}
	f.send(msg)
}

func (f *fixture) send(msg tea.Msg) {
	next, cmd := f.m.Update(msg)
	f.m = next.(Model)
	f.run(cmd)
}

func (f *fixture) key(k string) {
	switch k {
	case "enter":
		f.send(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		f.send(tea.KeyMsg{Type: tea.KeyEsc})
	case "tab":
		f.send(tea.KeyMsg{Type: tea.KeyTab})
	case "up":
		f.send(tea.KeyMsg{Type: tea.KeyUp})
	case "down":
		f.send(tea.KeyMsg{Type: tea.KeyDown})
	case "pgup":
		f.send(tea.KeyMsg{Type: tea.KeyPgUp})
	case "pgdown":
		f.send(tea.KeyMsg{Type: tea.KeyPgDown})
	case "left":
		f.send(tea.KeyMsg{Type: tea.KeyLeft})
	case "right":
		f.send(tea.KeyMsg{Type: tea.KeyRight})
	case "ctrl+u":
		f.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	case "alt+enter":
		f.send(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	default:
		f.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
}

func (f *fixture) agentSend(t *testing.T, ch, content string) bus.SendResult {
	t.Helper()
	r, err := f.ab.Send(f.sam, bus.SendInput{Channel: ch, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// receive pulls whatever is pending for the TUI, as the receive goroutine
// would, and feeds it to the model.
func (f *fixture) receive(t *testing.T) {
	t.Helper()
	res, err := f.c.b.Receive(f.c.as, bus.ReceiveInput{IncludeOwn: true, WaitSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	f.send(batchMsg{res})
}

func TestInitSelectsFirstChannelAndLoadsHistory(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "old one")
	f.m = New(f.c, f.m.theme) // re-init after the message exists
	f.run(f.m.Init())
	if got := f.m.selected(); got == nil || got.Name != "dev" {
		t.Fatalf("selected = %v", got)
	}
	if len(f.m.msgs["dev"]) != 1 || f.m.msgs["dev"][0].Content != "old one" {
		t.Fatalf("history not loaded: %+v", f.m.msgs["dev"])
	}
	if f.m.unread("dev") != 0 {
		t.Fatalf("history is not unread, got %d", f.m.unread("dev"))
	}
	if f.m.mode != modeInsert {
		t.Fatalf("start in insert mode, got %v", f.m.mode)
	}
}

func TestBatchOnOtherChannelCountsUnreadAndSelectingClearsIt(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "notes", "remember this")
	f.receive(t)
	if f.m.unread("notes") != 1 || f.m.unread("dev") != 0 {
		t.Fatalf("unread notes=%d dev=%d", f.m.unread("notes"), f.m.unread("dev"))
	}
	f.key("esc") // normal mode
	f.key("j")   // select notes
	if f.m.selected().Name != "notes" {
		t.Fatalf("j did not select notes: %v", f.m.selected())
	}
	if f.m.unread("notes") != 0 {
		t.Fatalf("selecting must mark seen, unread=%d", f.m.unread("notes"))
	}
	if f.m.divider < 0 {
		t.Fatal("divider must mark where new messages start")
	}
	f.key("k")
	if f.m.selected().Name != "dev" {
		t.Fatal("k did not go back to dev")
	}
}

func TestTabJumpsToNextUnreadInBothModes(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "notes", "x")
	f.receive(t)
	f.key("tab") // insert mode
	if f.m.selected().Name != "notes" {
		t.Fatal("tab in insert mode must jump to the unread channel")
	}
	f.key("k") // typed into compose in insert mode: must not change selection
	if f.m.selected().Name != "notes" || f.m.compose.Value() != "k" {
		t.Fatalf("insert mode must type, sel=%v compose=%q", f.m.selected(), f.m.compose.Value())
	}
}

func TestGapsAreRecordedPerChannel(t *testing.T) {
	f := newFixture(t)
	f.send(batchMsg{res: bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "dev", From: 3, To: 7}}}})
	if len(f.m.gaps["dev"]) != 1 || f.m.gapCount != 1 {
		t.Fatalf("gaps=%v count=%d", f.m.gaps, f.m.gapCount)
	}
}

func TestAddMessagesMergesAndDedupes(t *testing.T) {
	f := newFixture(t)
	f.m.addMessages("dev", []bus.Message{{Seq: 5}, {Seq: 2}})
	f.m.addMessages("dev", []bus.Message{{Seq: 3}, {Seq: 5}})
	got := f.m.msgs["dev"]
	if len(got) != 3 || got[0].Seq != 2 || got[1].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestPgUpAtTopLoadsOlderHistory(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		f.agentSend(t, "dev", "m")
	}
	f.m = New(f.c, f.m.theme)
	f.run(f.m.Init())
	// Pretend only the newest message was loaded, then page up.
	f.m.msgs["dev"] = f.m.msgs["dev"][2:]
	f.key("pgup")
	if len(f.m.msgs["dev"]) != 3 {
		t.Fatalf("pgup at top must prepend older history, have %d", len(f.m.msgs["dev"]))
	}
}

func TestStatusMsgTracksSessionsAndNewChannels(t *testing.T) {
	f := newFixture(t)
	if _, err := f.ab.CreateChannel(f.sam, "late", "ordinary"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	if len(f.m.channels) != 3 || !f.c.subscribed["late"] {
		t.Fatalf("new channel not picked up: %v %v", f.m.channels, f.c.subscribed)
	}
	if _, ok := f.m.sessionsSeen["Sam"]; !ok {
		t.Fatal("Sam must be listed as seen")
	}
}

func TestToastClearsOnKey(t *testing.T) {
	f := newFixture(t)
	f.send(receiveErrMsg{err: &bus.Error{Code: "internal", Message: "boom"}})
	if f.m.toast == "" {
		t.Fatal("receive error must toast")
	}
	f.key("x")
	if f.m.toast != "" {
		t.Fatal("any key clears the toast")
	}
}

func TestQuitKeysInNormalModeOnly(t *testing.T) {
	f := newFixture(t)
	f.key("q")
	if f.m.compose.Value() != "q" {
		t.Fatal("q in insert mode types")
	}
	f.key("esc")
	next, cmd := f.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	f.m = next.(Model)
	if cmd == nil {
		t.Fatal("q in normal mode must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q must return tea.Quit")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'Init|Batch|Tab|Gaps|AddMessages|PgUp|StatusMsg|Toast|Quit' -v`
Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Implement**

`internal/tui/model.go`:

```go
package tui

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

type mode int

const (
	modeInsert mode = iota // compose line focused; letters type
	modeNormal             // letter keymap active
	modeSearch
	modeMemories
	modeHealth
	modeConfirmDelete
)

const (
	historyPage    = 200
	statusEvery    = 5 * time.Second
	toastFor       = 5 * time.Second
	idleSessionTTL = time.Hour
)

// tick is tea.Tick, swapped out by tests so no command ever sleeps.
var tick = tea.Tick

func statusTick() tea.Cmd {
	return tick(statusEvery, func(time.Time) tea.Msg { return statusTickMsg{} })
}

// Model is the whole TUI state. Bubble Tea copies it by value on every
// Update, so maps and slices are shared and the pointer-receiver helpers
// mutate the copy Update is working on.
type Model struct {
	c      *client
	theme  Theme
	width  int
	height int
	mode   mode

	channels []bus.Channel
	sel      int
	msgs     map[string][]bus.Message
	gaps     map[string][]bus.Gap
	loaded   map[string]bool
	seen     map[string]int64
	divider  int64
	cursor   int
	stream   viewport.Model
	follow   bool

	compose  textarea.Model
	replyTo  *bus.Message
	lastSent string

	status       bus.Status
	statusErr    error
	sessionsSeen map[string]time.Time
	lastBatchAt  time.Time
	gapCount     int
	receiveErr   error

	toast    string
	toastSeq int

	search searchState
	mem    memState
	health healthState
}

// Placeholders until Tasks 8-10 define the real overlay state.
type (
	searchState struct{}
	memState    struct{}
	healthState struct{}
)

func New(c *client, th Theme) Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetEnabled(false)
	ta.Focus()
	return Model{
		c:            c,
		theme:        th,
		mode:         modeInsert,
		sel:          -1,
		cursor:       -1,
		divider:      -1,
		follow:       true,
		msgs:         map[string][]bus.Message{},
		gaps:         map[string][]bus.Gap{},
		loaded:       map[string]bool{},
		seen:         map[string]int64{},
		sessionsSeen: map[string]time.Time{},
		stream:       viewport.New(80, 20),
		compose:      ta,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.statusCmd(), statusTick(), textarea.Blink)
}

// keyString is the key as Bubble Tea names it ("q", "ctrl+c", "alt+enter"),
// or "" for any other message.
func keyString(msg tea.Msg) string {
	if k, ok := msg.(tea.KeyMsg); ok {
		return k.String()
	}
	return ""
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if k := keyString(msg); k != "" {
		if m.toast != "" {
			m.toast = ""
			m.layout()
		}
		if k == "ctrl+c" {
			return m, tea.Quit
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case batchMsg:
		cmds = append(cmds, m.onBatch(msg.res))
	case receiveErrMsg:
		m.receiveErr = msg.err
		cmds = append(cmds, m.showToast("receive: "+errText(msg.err)))
	case statusTickMsg:
		// The one periodic chain: Init starts it, and only this case
		// re-arms it, so opening Health (which also calls statusCmd) never
		// doubles the tick rate.
		cmds = append(cmds, m.statusCmd(), statusTick())
	case statusMsg:
		cmds = append(cmds, m.onStatus(msg))
	case subscribedMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("subscribe "+msg.channel+": "+errText(msg.err)))
		} else {
			cmds = append(cmds, m.statusCmd()) // a channel created from the prompt shows up in the rail
		}
	case historyMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("history: "+errText(msg.err)))
			break
		}
		m.loaded[msg.channel] = true
		// A prepend shifts indices; keep the cursor on the same message.
		var cursorSeq int64
		if ms := m.msgs[msg.channel]; msg.channel == m.selName() && m.cursor >= 0 && m.cursor < len(ms) {
			cursorSeq = ms[m.cursor].Seq
		}
		m.addMessages(msg.channel, msg.msgs)
		if !msg.prepend {
			m.markSeen(msg.channel)
		}
		m.refreshStream()
		if cursorSeq > 0 {
			m.placeCursor(cursorSeq)
		}
	case toastClearMsg:
		if msg.seq == m.toastSeq {
			m.toast = ""
			m.layout()
		}
	}
	switch m.mode {
	case modeInsert:
		cmds = append(cmds, m.updateInsert(msg))
	case modeNormal:
		cmds = append(cmds, m.updateNormal(msg))
	case modeSearch:
		cmds = append(cmds, m.updateSearch(msg))
	case modeMemories, modeConfirmDelete:
		cmds = append(cmds, m.updateMemories(msg))
	case modeHealth:
		cmds = append(cmds, m.updateHealth(msg))
	}
	return m, tea.Batch(cmds...)
}

// updateInsert: compose focused. Only the keys the compose line owns plus
// tab (next unread), pgup/pgdn (stream), esc (to normal mode) are handled
// here; everything else types.
func (m *Model) updateInsert(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc":
		if m.replyTo != nil {
			m.replyTo = nil
			m.layout()
			return nil
		}
		m.mode = modeNormal
		m.compose.Blur()
		return nil
	case "tab":
		return m.nextUnread()
	case "pgup", "pgdown":
		return m.scrollStream(msg)
	case "enter":
		return m.submitCompose()
	case "alt+enter":
		m.compose.InsertString("\n")
		m.fitCompose()
		return nil
	case "ctrl+u":
		m.compose.Reset()
		m.fitCompose()
		return nil
	case "up":
		if m.compose.Value() == "" && m.lastSent != "" {
			m.compose.SetValue(m.lastSent)
			m.fitCompose()
			return nil
		}
	}
	var cmd tea.Cmd
	m.compose, cmd = m.compose.Update(msg)
	m.fitCompose()
	return cmd
}

// updateNormal is the letter keymap from the design notes.
func (m *Model) updateNormal(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "q":
		return tea.Quit
	case "i", "enter":
		m.mode = modeInsert
		return m.compose.Focus()
	case "esc":
		m.replyTo = nil
		m.cursor = -1
		m.layout()
	case "j", "down":
		if m.cursor >= 0 {
			return m.moveCursor(1)
		}
		return m.selectChannel(m.sel + 1)
	case "k", "up":
		if m.cursor >= 0 {
			return m.moveCursor(-1)
		}
		return m.selectChannel(m.sel - 1)
	case "tab":
		return m.nextUnread()
	case "pgup", "pgdown":
		return m.scrollStream(msg)
	case "g":
		m.follow = false
		m.stream.GotoTop()
		return m.loadOlder()
	case "G":
		m.follow = true
		m.stream.GotoBottom()
	case "left", "right":
		// ← → enter/leave the stream cursor: → puts the cursor on the newest
		// message, ← clears it.
		if keyString(msg) == "right" {
			if n := len(m.msgs[m.selName()]); n > 0 {
				m.cursor = n - 1
			}
		} else {
			m.cursor = -1
		}
		m.refreshStream()
	case "r":
		if ms := m.msgs[m.selName()]; m.cursor >= 0 && m.cursor < len(ms) {
			target := ms[m.cursor]
			m.replyTo = &target
		} else if n := len(ms); n > 0 {
			target := ms[n-1]
			m.replyTo = &target
		}
		m.layout()
		m.mode = modeInsert
		return m.compose.Focus()
	case "c":
		return m.createChannelPrompt()
	case "s":
		return m.toggleSubscribe()
	case "/":
		return m.openSearch()
	case "m":
		return m.openMemories()
	case "h", "?":
		return m.openHealth()
	}
	return nil
}

// moveCursor moves the normal-mode stream cursor and scrolls to keep it
// visible (refreshStream renders the cursor row highlighted).
func (m *Model) moveCursor(d int) tea.Cmd {
	n := len(m.msgs[m.selName()])
	if n == 0 {
		m.cursor = -1
		return nil
	}
	m.cursor = min(max(m.cursor+d, 0), n-1)
	m.follow = false
	m.refreshStream()
	return nil
}

// placeCursor puts the normal-mode cursor on the message with seq (no-op if
// it is not loaded) and scrolls so it sits mid-viewport.
func (m *Model) placeCursor(seq int64) {
	for i, x := range m.msgs[m.selName()] {
		if x.Seq == seq {
			m.cursor = i
		}
	}
	m.follow = false
	m.refreshStream()
	m.stream.SetYOffset(max(m.cursor-m.stream.Height/2, 0))
}

func (m *Model) scrollStream(msg tea.Msg) tea.Cmd {
	if keyString(msg) == "pgup" && m.stream.AtTop() {
		return m.loadOlder()
	}
	m.follow = false
	var cmd tea.Cmd
	m.stream, cmd = m.stream.Update(msg)
	if m.stream.AtBottom() {
		m.follow = true
	}
	return cmd
}

// loadOlder pages history backwards from the oldest loaded message.
func (m *Model) loadOlder() tea.Cmd {
	ch := m.selName()
	ms := m.msgs[ch]
	if ch == "" || len(ms) == 0 {
		return nil
	}
	before := ms[0].Seq
	return m.loadHistory(ch, &before)
}

func (m *Model) selName() string {
	if c := m.selected(); c != nil {
		return c.Name
	}
	return ""
}

func (m *Model) selected() *bus.Channel {
	if m.sel < 0 || m.sel >= len(m.channels) {
		return nil
	}
	return &m.channels[m.sel]
}

func (m *Model) unread(ch string) int {
	n := 0
	for _, x := range m.msgs[ch] {
		if x.Seq > m.seen[ch] {
			n++
		}
	}
	return n
}

// markSeen records the newest loaded seq of ch as seen.
func (m *Model) markSeen(ch string) {
	if ms := m.msgs[ch]; len(ms) > 0 {
		m.seen[ch] = max(m.seen[ch], ms[len(ms)-1].Seq)
	}
}

// selectChannel moves the selection (clamped), places the "new" divider at
// the previous last-seen seq, marks everything seen, and loads history on
// first visit.
func (m *Model) selectChannel(i int) tea.Cmd {
	if len(m.channels) == 0 {
		m.sel = -1
		return nil
	}
	i = min(max(i, 0), len(m.channels)-1)
	m.sel = i
	m.cursor = -1
	m.follow = true
	m.replyTo = nil
	ch := m.channels[i].Name
	m.divider = -1
	if m.unread(ch) > 0 {
		m.divider = m.seen[ch] // 0 when nothing was ever seen: every message is new
	}
	m.markSeen(ch)
	m.refreshStream()
	m.stream.GotoBottom()
	if !m.loaded[ch] {
		return m.loadHistory(ch, nil)
	}
	return nil
}

func (m *Model) nextUnread() tea.Cmd {
	n := len(m.channels)
	for k := 1; k <= n; k++ {
		i := (m.sel + k) % n
		if m.unread(m.channels[i].Name) > 0 {
			return m.selectChannel(i)
		}
	}
	return nil
}

// addMessages merges in into the channel buffer, ascending by seq, dropping
// duplicates (a message can arrive via history and receive both).
func (m *Model) addMessages(ch string, in []bus.Message) {
	all := append(append([]bus.Message{}, m.msgs[ch]...), in...)
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	out := all[:0]
	for i, x := range all {
		if i == 0 || x.Seq != all[i-1].Seq {
			out = append(out, x)
		}
	}
	m.msgs[ch] = out
}

func (m *Model) onBatch(res bus.ReceiveResult) tea.Cmd {
	m.lastBatchAt = time.Now()
	m.receiveErr = nil
	byCh := map[string][]bus.Message{}
	for _, x := range res.Messages {
		byCh[x.Channel] = append(byCh[x.Channel], x)
	}
	for ch, ms := range byCh {
		m.addMessages(ch, ms)
	}
	for _, g := range res.Gaps {
		m.gaps[g.Channel] = append(m.gaps[g.Channel], g)
		m.gapCount++
	}
	if cur := m.selName(); cur != "" {
		if _, ok := byCh[cur]; ok {
			m.markSeen(cur)
		}
	}
	m.refreshStream()
	if m.follow {
		m.stream.GotoBottom()
	}
	var cmds []tea.Cmd
	for _, ch := range res.Expired {
		cmds = append(cmds, m.showToast("subscription to "+ch+" expired; resubscribing"), m.subscribeCmd(bus.Channel{Name: ch}, "now"))
	}
	if res.Notice != "" {
		cmds = append(cmds, m.showToast(res.Notice))
	}
	return tea.Batch(cmds...)
}

// onStatus refreshes the status bar data, learns channels created since
// startup (subscribing from oldest so nothing is missed), and tracks when
// each session was last listed live.
func (m *Model) onStatus(msg statusMsg) tea.Cmd {
	m.statusErr = msg.err
	if msg.err != nil {
		return nil
	}
	m.status = msg.st
	now := time.Now()
	for _, s := range msg.st.Sessions {
		m.sessionsSeen[s.Sender] = now
	}
	for name, at := range m.sessionsSeen {
		if now.Sub(at) > idleSessionTTL {
			delete(m.sessionsSeen, name)
		}
	}
	return m.setChannels(msg.st.Channels, "oldest")
}

// setChannels replaces the channel list (sorted by name), keeps the current
// selection by name, and subscribes to any channel not yet subscribed.
func (m *Model) setChannels(chans []bus.Channel, from string) tea.Cmd {
	cur := m.selName()
	sort.Slice(chans, func(i, j int) bool { return chans[i].Name < chans[j].Name })
	m.channels = chans
	m.sel = -1
	for i, c := range chans {
		if c.Name == cur {
			m.sel = i
		}
	}
	var cmds []tea.Cmd
	for _, c := range chans {
		if !m.c.subscribed[c.Name] {
			cmds = append(cmds, m.subscribeCmd(c, from))
		}
	}
	if m.sel < 0 && len(chans) > 0 {
		cmds = append(cmds, m.selectChannel(0))
	}
	return tea.Batch(cmds...)
}

func (m *Model) subscribeCmd(ch bus.Channel, from string) tea.Cmd {
	c := m.c
	return func() tea.Msg { return subscribedMsg{channel: ch.Name, err: c.subscribe(ch, from)} }
}

func (m *Model) statusCmd() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		st, err := c.b.StatusReport()
		return statusMsg{st: st, err: err}
	}
}

// loadHistory fetches the newest historyPage messages of ch (before == nil)
// or the page before seq *before.
func (m *Model) loadHistory(ch string, before *int64) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		b := before
		if b == nil {
			top := int64(math.MaxInt64)
			b = &top
		}
		ms, err := c.b.History(c.as, ch, b, nil, historyPage)
		return historyMsg{channel: ch, msgs: ms, prepend: before != nil, err: err}
	}
}

func (m *Model) showToast(s string) tea.Cmd {
	m.toast = s
	m.toastSeq++
	m.layout()
	seq := m.toastSeq
	return tick(toastFor, func(time.Time) tea.Msg { return toastClearMsg{seq: seq} })
}

// errText renders a bus error as "code: message" and anything else as-is.
func errText(err error) string {
	var be *bus.Error
	if errors.As(err, &be) {
		if be.Retryable {
			return fmt.Sprintf("%s: %s · retryable", be.Code, be.Message)
		}
		return be.Code + ": " + be.Message
	}
	return err.Error()
}

// layout, fitCompose, refreshStream are completed in Task 7; these minimal
// versions keep the stream viewport sized and the compose line 1-3 rows.
func (m *Model) layout() {
	m.stream.Width = max(m.width-leftRail-rightRail-2, 20)
	m.stream.Height = max(m.height-composeHeight(m.compose)-4, 3)
	m.compose.SetWidth(max(m.stream.Width-len(m.selName())-3, 10))
	m.refreshStream()
}

func composeHeight(ta textarea.Model) int { return min(max(ta.LineCount(), 1), 3) }

func (m *Model) fitCompose() {
	if h := composeHeight(m.compose); h != m.compose.Height() {
		m.compose.SetHeight(h)
		m.layout()
	}
}

func (m *Model) refreshStream() { m.stream.SetContent(m.renderStream()) }

// Stubs replaced by later tasks. Each returns nil so Task 5 compiles alone.
func (m *Model) renderStream() string            { return "" }
func (m *Model) submitCompose() tea.Cmd           { return nil }
func (m *Model) createChannelPrompt() tea.Cmd     { return nil }
func (m *Model) toggleSubscribe() tea.Cmd         { return nil }
func (m *Model) openSearch() tea.Cmd              { return nil }
func (m *Model) openMemories() tea.Cmd            { return nil }
func (m *Model) openHealth() tea.Cmd              { return nil }
func (m *Model) updateSearch(tea.Msg) tea.Cmd     { return nil }
func (m *Model) updateMemories(tea.Msg) tea.Cmd   { return nil }
func (m *Model) updateHealth(tea.Msg) tea.Cmd     { return nil }
func (m Model) View() string                      { return "" }

const (
	leftRail  = 18
	rightRail = 22
)
```

Note for the implementer: `Update` has a value receiver (Bubble Tea requires it) and calls pointer-receiver helpers on its local copy; that is fine because `m` inside `Update` is addressable. The helpers must never be called on a `Model` that is not the one being returned.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tui/ -v && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues. If `TestInitSelectsFirstChannelAndLoadsHistory` fails on `unread("dev") != 0`, `historyMsg` with `prepend == false` is not calling `markSeen` — the newest page is "already read".

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "feat(tui): model core with channels, stream buffers, unread, gaps"
```

---

### Task 6: Compose — send, reply, memory creation, recall, channel commands

**Files:**
- Create: `internal/tui/compose.go`
- Modify: `internal/tui/model.go` (delete the `submitCompose`, `createChannelPrompt`, `toggleSubscribe` stubs)
- Test: `internal/tui/compose_test.go`

**Interfaces:**
- Produces: `func (m *Model) submitCompose() tea.Cmd`, `func (m *Model) createChannelPrompt() tea.Cmd`, `func (m *Model) toggleSubscribe() tea.Cmd`, and two model fields added to `Model` in `model.go`: `prompt promptState` where

```go
// promptState is the one-line inline prompt used by "c" (create channel):
// while active it borrows the compose line.
type promptState struct {
	active bool
	label  string
	input  textinput.Model
	accept func(m *Model, value string) tea.Cmd
}
```

- [ ] **Step 1: Write the failing tests**

`internal/tui/compose_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
)

func TestEnterSendsToSelectedChannelAndRecordsLastSent(t *testing.T) {
	f := newFixture(t)
	f.key("ship it")
	f.key("enter")
	f.receive(t)
	ms := f.m.msgs["dev"]
	if len(ms) != 1 || ms[0].Content != "ship it" || ms[0].Sender != "eric" {
		t.Fatalf("got %+v", ms)
	}
	if f.m.compose.Value() != "" || f.m.lastSent != "ship it" {
		t.Fatalf("compose=%q lastSent=%q", f.m.compose.Value(), f.m.lastSent)
	}
	f.key("up")
	if f.m.compose.Value() != "ship it" {
		t.Fatal("up on empty compose recalls last sent")
	}
	f.key("ctrl+u")
	if f.m.compose.Value() != "" {
		t.Fatal("ctrl+u clears")
	}
}

func TestEmptyComposeDoesNotSend(t *testing.T) {
	f := newFixture(t)
	f.key("enter")
	f.receive(t)
	if len(f.m.msgs["dev"]) != 0 {
		t.Fatal("empty send")
	}
}

func TestAltEnterInsertsNewline(t *testing.T) {
	f := newFixture(t)
	f.key("a")
	f.key("alt+enter")
	f.key("b")
	if f.m.compose.Value() != "a\nb" {
		t.Fatalf("got %q", f.m.compose.Value())
	}
	if f.m.compose.Height() != 2 {
		t.Fatalf("compose grows to 2 rows, got %d", f.m.compose.Height())
	}
}

func TestReplySetsReplyToAndEscCancels(t *testing.T) {
	f := newFixture(t)
	r := f.agentSend(t, "dev", "question?")
	f.receive(t)
	f.key("esc")
	f.key("r")
	if f.m.replyTo == nil || f.m.replyTo.Seq != r.Seq || f.m.mode != modeInsert {
		t.Fatalf("replyTo=%v mode=%v", f.m.replyTo, f.m.mode)
	}
	f.key("yes")
	f.key("enter")
	f.receive(t)
	ms := f.m.msgs["dev"]
	if len(ms) != 2 || ms[1].ReplyTo == nil || *ms[1].ReplyTo != r.Seq {
		t.Fatalf("reply not linked: %+v", ms)
	}
	if f.m.replyTo != nil {
		t.Fatal("reply mode ends after send")
	}
	f.key("esc")
	f.key("r")
	f.key("esc")
	if f.m.replyTo != nil || f.m.mode != modeInsert {
		t.Fatal("esc in reply mode cancels the reply and stays in insert mode")
	}
}

func TestEnterOnMemoryChannelCreatesMemory(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("j") // notes
	f.key("i")
	f.key("Reviewer checklist")
	f.key("enter")
	f.receive(t)
	ms := f.m.msgs["notes"]
	if len(ms) != 1 || ms[0].MemoryID == nil {
		t.Fatalf("memory not created: %+v", ms)
	}
}

func TestSendErrorToasts(t *testing.T) {
	f := newFixture(t)
	f.send(sentMsg{channel: "dev", err: &bus.Error{Code: "rate_limited", Message: "slow down", Retryable: true}})
	if !strings.Contains(f.m.toast, "rate_limited") || !strings.Contains(f.m.toast, "retryable") {
		t.Fatalf("toast=%q", f.m.toast)
	}
}

func TestCreateChannelPrompt(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("c")
	if !f.m.prompt.active {
		t.Fatal("c opens the create-channel prompt")
	}
	f.key("ops")
	f.key("enter")
	found := false
	for _, c := range f.m.channels {
		found = found || c.Name == "ops"
	}
	if !found || f.m.prompt.active {
		t.Fatalf("channel not created: %v active=%v", f.m.channels, f.m.prompt.active)
	}
}

func TestToggleSubscribe(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("s")
	if f.c.subscribed["dev"] {
		t.Fatal("s unsubscribes the selected channel")
	}
	f.key("s")
	if !f.c.subscribed["dev"] {
		t.Fatal("s again resubscribes")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'Enter|Empty|AltEnter|Reply|Memory|SendError|CreateChannel|Toggle' -v`
Expected: FAIL (`f.m.prompt` undefined; sends are no-ops).

- [ ] **Step 3: Implement**

Add to the `Model` struct in `model.go` after `lastSent string`:

```go
	prompt   promptState
```

Delete these three stubs from `model.go`: `submitCompose`, `createChannelPrompt`, `toggleSubscribe`.

In `model.go`'s `updateInsert`, add as the very first statement (before the `switch`):

```go
	if m.prompt.active {
		return m.updatePrompt(msg)
	}
```

and in `updateNormal` likewise as the first statement (the prompt takes every key until enter/esc).

Add a `sentMsg` case to the first `switch` in `Update`:

```go
	case sentMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("send: "+errText(msg.err)))
		}
```

`internal/tui/compose.go`:

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

// promptState is the one-line inline prompt used by "c" (create channel).
// While active it replaces the compose line and takes every key.
type promptState struct {
	active bool
	label  string
	input  textinput.Model
	accept func(m *Model, value string) tea.Cmd
}

// submitCompose sends the compose text to the selected channel. On a memory
// channel the bus turns the send into a memory. The text is kept for ↑
// recall, and a failed send leaves it there via the toast's hint: the user
// presses ↑ then enter.
func (m *Model) submitCompose() tea.Cmd {
	text := strings.TrimRight(m.compose.Value(), "\n ")
	ch := m.selName()
	if strings.TrimSpace(text) == "" || ch == "" {
		return nil
	}
	in := bus.SendInput{Channel: ch, Content: text}
	if m.replyTo != nil {
		seq := m.replyTo.Seq
		in.ReplyTo = &seq
	}
	m.lastSent = text
	m.replyTo = nil
	m.compose.Reset()
	m.fitCompose()
	c := m.c
	return func() tea.Msg {
		_, err := c.b.Send(c.as, in)
		return sentMsg{channel: ch, err: err}
	}
}

func (m *Model) openPrompt(label string, accept func(*Model, string) tea.Cmd) tea.Cmd {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 128
	m.prompt = promptState{active: true, label: label, input: in, accept: accept}
	return m.prompt.input.Focus() // focus the copy the model keeps, not the local
}

func (m *Model) updatePrompt(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc":
		m.prompt = promptState{}
		return nil
	case "enter":
		v := strings.TrimSpace(m.prompt.input.Value())
		accept := m.prompt.accept
		m.prompt = promptState{}
		if v == "" {
			return nil
		}
		return accept(m, v)
	}
	var cmd tea.Cmd
	m.prompt.input, cmd = m.prompt.input.Update(msg)
	return cmd
}

// createChannelPrompt asks "channel name [kind]" where kind is "memory" or
// omitted for ordinary; e.g. "notes memory".
func (m *Model) createChannelPrompt() tea.Cmd {
	return m.openPrompt("new channel (name, or name memory)", func(m *Model, v string) tea.Cmd {
		name, kind, _ := strings.Cut(v, " ")
		kind = strings.TrimSpace(kind)
		if kind == "" {
			kind = "ordinary"
		}
		c := m.c
		// subscribedMsg's success path refreshes the status (and so the rail).
		return func() tea.Msg {
			ch, err := c.b.CreateChannel(c.as, name, kind)
			if err != nil {
				return sentMsg{channel: name, err: err}
			}
			return subscribedMsg{channel: name, err: c.subscribe(ch, "oldest")}
		}
	})
}

// toggleSubscribe unsubscribes the selected channel (its messages stop
// arriving; the rail still lists it) or subscribes it again from now.
func (m *Model) toggleSubscribe() tea.Cmd {
	ch := m.selected()
	if ch == nil {
		return nil
	}
	c := m.c
	name := ch.Name
	if c.subscribed[name] {
		if err := c.b.Unsubscribe(c.as, name); err != nil {
			return m.showToast("unsubscribe: " + errText(err))
		}
		delete(c.subscribed, name)
		return m.showToast("unsubscribed from " + name + " (s to resubscribe)")
	}
	if err := c.subscribe(*ch, "now"); err != nil {
		return m.showToast("subscribe: " + errText(err))
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tui/ -v && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues. If `TestReplySetsReplyToAndEscCancels` fails on the first `r`, check that `updateNormal`'s `r` case falls back to the newest message when `cursor < 0` (it does in Task 5's code).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/compose.go internal/tui/compose_test.go internal/tui/model.go
git commit -m "feat(tui): compose line with send, reply, memory creation, channel commands"
```

---

### Task 7: Main-screen rendering

**Files:**
- Create: `internal/tui/view.go`
- Modify: `internal/tui/model.go` (delete the `renderStream` and `View` stubs; keep `layout`, `fitCompose`, `refreshStream`)
- Test: `internal/tui/view_test.go`

**Interfaces:**
- Produces: `func (m Model) View() string`, `func (m *Model) renderStream() string`, `func (m Model) renderStatusBar() string`, `func (m Model) renderRails() (left, right string)`, `func fmtBytes(n int64) string` ("412 MiB", "2.0 GiB"), `func clock(ms int64) string` (HH:MM today, else "Sep 7"), `func (m Model) overlay(title string, border lipgloss.TerminalColor, body string, footer string) string` — centred box used by Tasks 8-10.

Layout (width W, height H):

```
┌ channels (18) ┬ header: "<channel> · N unread · M messages"           ┬ sessions (22) ┐
│ › general 3   │ stream viewport                                         │ ● xfa agentbus│
│   ◆ notes 12  │   HH:MM sender  content                                 │ ○ codex 14m   │
│               │   ↳ re #412 (dim, for replies)                          │               │
│               │   ──── new ──── / ──── 5 evicted ────                   │               │
├───────────────┴─────────────────────────────────────────────────────────┴───────────────┤
│ [↳ replying to xfa #412 "…"  esc cancel]                (only in reply mode)             │
│ <channel> › compose (1-3 rows)            or   ◆ notes › … ⏎ new memory                 │
│ ✗ toast                                                  (only while a toast is up)      │
│ db 412 MiB / 2 GiB  embed ok  backlog 0  as eric          ? help / search m memories … │
```

Rails hide when W < 90 (right) and W < 60 (both). Colours per the spec table: agent names `Agent`, own name `User`, memory channel marker and `◆` `Mem`, dividers/timestamps/help `Dim`, selected row background `Sel`, heartbeat dot `Health`, idle dot and badges `Warn`, toast `Error`.

- [ ] **Step 1: Write the failing tests**

`internal/tui/view_test.go`:

```go
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
)

func TestViewShowsRailsStreamComposeAndStatus(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.run(f.m.statusCmd())
	v := f.m.View()
	for _, want := range []string{"channels", "dev", "◆ notes", "hello from sam", "Sam", "sessions", "eric", "as eric", "? help", "/ search", "m memories", "h health", "q quit", "dev ›"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if lines := strings.Count(v, "\n") + 1; lines > f.m.height {
		t.Fatalf("view is %d lines for height %d", lines, f.m.height)
	}
}

func TestStreamShowsNewDividerAndGap(t *testing.T) {
	f := newFixture(t)
	one := f.agentSend(t, "notes", "one")
	f.receive(t)
	f.key("esc")
	f.key("j") // visit notes: "one" is now seen
	f.key("k") // back to dev
	f.agentSend(t, "notes", "two")
	f.send(batchMsg{res: bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "notes", From: one.Seq, To: one.Seq}}}})
	f.receive(t)
	f.key("j") // notes again: divider sits after "one"
	s := f.m.renderStream()
	if !strings.Contains(s, "new") || !strings.Contains(s, "1 evicted") {
		t.Fatalf("stream:\n%s", s)
	}
	if strings.Index(s, "one") > strings.Index(s, "new") {
		t.Fatalf("divider must sit after the last seen message:\n%s", s)
	}
}

func TestReplyBannerAndMemoryHint(t *testing.T) {
	f := newFixture(t)
	r := f.agentSend(t, "dev", "q")
	f.receive(t)
	f.key("esc")
	f.key("r")
	if v := f.m.View(); !strings.Contains(v, "replying to Sam") || !strings.Contains(v, "#"+itoa(r.Seq)) {
		t.Fatalf("view:\n%s", v)
	}
	f.key("esc")
	f.key("esc")
	f.key("j")
	if v := f.m.View(); !strings.Contains(v, "new memory") {
		t.Fatalf("memory channel hint missing:\n%s", v)
	}
}

func TestIdleSessionShowsAge(t *testing.T) {
	f := newFixture(t)
	f.m.sessionsSeen["codex"] = time.Now().Add(-14 * time.Minute)
	_, right := f.m.renderRails()
	if !strings.Contains(right, "○ codex") || !strings.Contains(right, "14m") {
		t.Fatalf("rail:\n%s", right)
	}
}

func TestEmptyChannelAndCapacityNotice(t *testing.T) {
	f := newFixture(t)
	f.m.status.Notice = "1.9 GiB of 2 GiB used"
	v := f.m.View()
	if !strings.Contains(v, "no messages yet in dev") || !strings.Contains(v, "capacity") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestFmtBytes(t *testing.T) {
	if got := fmtBytes(412 << 20); got != "412 MiB" {
		t.Fatal(got)
	}
	if got := fmtBytes(2 << 30); got != "2.0 GiB" {
		t.Fatal(got)
	}
}
```

Add to `internal/tui/view.go` (it is a production helper that the test also uses): `func itoa(n int64) string { return strconv.FormatInt(n, 10) }`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'View|Stream|Reply|Idle|Empty|FmtBytes' -v`
Expected: FAIL, `undefined: itoa`, `renderRails`, `fmtBytes`.

- [ ] **Step 3: Implement**

Delete the `renderStream` and `View` stubs from `model.go`.

`internal/tui/view.go`:

```go
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
	return lipgloss.NewStyle().Width(m.stream.Width).Render(s)
}

// renderRails draws the channel list (left) and the session list (right).
func (m Model) renderRails() (string, string) {
	th := m.theme
	dim, memc, sel := th.Style(th.Dim), th.Style(th.Mem), lipgloss.NewStyle().Background(th.Sel)
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
		if !m.c.subscribed[c.Name] {
			line += dim.Render(" (off)")
		}
		if i == m.sel {
			line = sel.Width(leftRail - 1).Render(th.Style(th.Agent).Render("›") + line)
		} else {
			line = " " + line
		}
		l.WriteString(line + "\n")
	}
	left := lipgloss.NewStyle().Width(leftRail).Height(m.stream.Height + 1).Render(l.String())

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
		if s, ok := live[n]; ok {
			ctx := s.Context
			if n == m.c.as {
				ctx = "you"
			}
			name := th.Style(th.Agent).Render(n)
			if n == m.c.as {
				name = th.Style(th.User).Render(n)
			}
			r.WriteString(th.Style(th.Health).Render("● ") + name + " " + dim.Render(ctx) + "\n")
		} else {
			age := time.Since(m.sessionsSeen[n]).Round(time.Minute)
			r.WriteString(th.Style(th.Warn).Render("○ ") + dim.Render(n+" "+shortDur(age)) + "\n")
		}
	}
	right := lipgloss.NewStyle().Width(rightRail).Height(m.stream.Height + 1).Render(r.String())
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
	if len(quote) > 40 {
		quote = quote[:40] + "…"
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
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(help), 1)
	return left + strings.Repeat(" ", gap) + help
}

// overlay renders a titled, bordered box centred on the screen; the border
// colour names the overlay (cyan search, magenta memories, green health).
func (m Model) overlay(title string, border lipgloss.TerminalColor, body, footer string) string {
	w := min(max(m.width-8, 40), 100)
	h := max(m.height-4, 10)
	inner := lipgloss.JoinVertical(lipgloss.Left,
		m.theme.Style(border).Bold(true).Render(title),
		lipgloss.NewStyle().Width(w-4).Height(h-4).MaxHeight(h-4).Render(body),
		m.theme.Style(m.theme.Dim).Render(footer),
	)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(w - 2).Render(inner)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
```

`m.search.semanticDown` and `viewSearch`/`viewMemories`/`viewHealth` do not exist yet. For this task add these stubs to `view.go` (Tasks 8-10 delete them):

```go
func (m Model) viewSearch() string   { return "" }
func (m Model) viewMemories() string { return "" }
func (m Model) viewHealth() string   { return "" }
```

and in `model.go` change the placeholder to `type searchState struct{ semanticDown bool }`.

Finally replace `layout` in `model.go` with the rail-aware version:

```go
func (m *Model) layout() {
	w := m.width
	if m.showLeft() {
		w -= leftRail + 1
	}
	if m.showRight() {
		w -= rightRail + 1
	}
	m.stream.Width = max(w, 20)
	extra := 3 // divider, compose label row, status bar
	if m.replyTo != nil {
		extra++
	}
	if m.toast != "" {
		extra++
	}
	m.stream.Height = max(m.height-1-composeHeight(m.compose)-extra, 3)
	m.compose.SetWidth(max(m.stream.Width-len(m.selName())-16, 10))
	m.refreshStream()
}
```

(`showToast`, the `toastClearMsg` case, and the reply-mode transitions already call `m.layout()`, so the stream height follows the extra rows; add the same call where `updateInsert` clears `replyTo` on `esc`.)

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tui/ -v && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues. Colours do not appear in test output (no TTY, so Lip Gloss renders plain text); assertions are on text only.

- [ ] **Step 5: Look at it**

Run: `go build -o /tmp/agentbus . && AGENTBUS_DATA_DIR=$(mktemp -d) /tmp/agentbus tui` is not possible before Task 11 wires the command; skip until then.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/view.go internal/tui/view_test.go internal/tui/model.go
git commit -m "feat(tui): main screen rendering"
```

---

### Task 8: Search overlay

**Files:**
- Create: `internal/tui/search.go`
- Modify: `internal/tui/model.go` (delete `openSearch`/`updateSearch` stubs and the `searchState` placeholder), `internal/tui/view.go` (delete `viewSearch` stub)
- Test: `internal/tui/search_test.go`

**Interfaces:**
- Produces:

```go
type searchState struct {
	input        textinput.Model
	mode         string        // "text" | "semantic" | "both"; starts "both" when an endpoint is configured, else "text"
	hits         []bus.SearchHit
	cursor       int
	ran          bool
	textOnly     bool          // last result had SemanticUnavailable
	semanticDown bool          // sticky for the status bar until a semantic search succeeds
	err          error
}
func (m *Model) openSearch() tea.Cmd
func (m *Model) updateSearch(msg tea.Msg) tea.Cmd
func (m Model) viewSearch() string
func (m *Model) jumpTo(hit bus.Message) tea.Cmd   // select its channel, load history around it, put the cursor on it
```

Keys: type to edit query, `tab` cycles mode text → semantic → both, `enter` runs the search (or, when the results list has focus after a search, opens the selected hit), `↑`/`↓` move over hits, `esc` closes.

- [ ] **Step 1: Write the failing tests**

`internal/tui/search_test.go`:

```go
package tui

import (
	"strings"
	"testing"
)

func TestSearchFindsMessagesAndJumps(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "the release script")
	r := f.agentSend(t, "notes", "release procedure memory")
	f.receive(t)
	f.key("esc")
	f.key("/")
	if f.m.mode != modeSearch || f.m.search.mode != "text" {
		t.Fatalf("mode=%v search mode=%q", f.m.mode, f.m.search.mode)
	}
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 2 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	v := f.m.View()
	if !strings.Contains(v, "2 results") || !strings.Contains(v, "release procedure") {
		t.Fatalf("view:\n%s", v)
	}
	// Move to the notes hit and open it.
	for i, h := range f.m.search.hits {
		if h.Seq == r.Seq {
			f.m.search.cursor = i
		}
	}
	f.key("enter")
	if f.m.mode != modeNormal || f.m.selected().Name != "notes" {
		t.Fatalf("enter must jump to the hit's channel: mode=%v sel=%v", f.m.mode, f.m.selected())
	}
	if f.m.cursor < 0 || f.m.msgs["notes"][f.m.cursor].Seq != r.Seq {
		t.Fatalf("cursor must sit on the hit, cursor=%d", f.m.cursor)
	}
}

func TestSearchTabCyclesModeAndSemanticUnavailableShowsBadge(t *testing.T) {
	f := newFixture(t)
	f.m.c.cfg.EmbeddingEndpoint = "http://127.0.0.1:9/v1/embeddings"
	f.key("esc")
	f.key("/")
	if f.m.search.mode != "both" {
		t.Fatalf("with an endpoint the default mode is both, got %q", f.m.search.mode)
	}
	f.key("tab")
	if f.m.search.mode != "text" {
		t.Fatalf("tab: both -> text, got %q", f.m.search.mode)
	}
	f.key("tab")
	f.key("tab")
	if f.m.search.mode != "both" {
		t.Fatalf("tab cycles text -> semantic -> both, got %q", f.m.search.mode)
	}
	f.m.search.textOnly = true
	if v := f.m.View(); !strings.Contains(v, "text only") {
		t.Fatalf("badge missing:\n%s", v)
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes the overlay")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run Search -v`
Expected: FAIL (`f.m.search.mode` undefined).

- [ ] **Step 3: Implement**

Delete `openSearch`, `updateSearch` and the `searchState` placeholder from `model.go`, and `viewSearch` from `view.go`. Add a `searchMsg` case to `Update`'s first switch:

```go
	case searchMsg:
		m.search.err = msg.err
		m.search.ran = true
		if msg.err == nil {
			m.search.hits = msg.res.Hits
			m.search.cursor = 0
			m.search.textOnly = msg.res.SemanticUnavailable
			if m.search.mode != "text" {
				m.search.semanticDown = msg.res.SemanticUnavailable
			}
		}
```

`internal/tui/search.go`:

```go
package tui

import (
	"fmt"
	"strings"

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
		} else if m.search.textOnly {
			summary += " · " + th.Style(th.Warn).Render("text only — embedding endpoint unreachable")
		}
		b.WriteString("\n" + dim.Render(summary))
	}
	title := "search  " + dim.Render("mode "+m.search.mode+" · tab to switch")
	return m.overlay(title, th.Agent, b.String(), "enter search / open  ↑↓ move  esc close")
}
```


- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tui/ -v && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues. `TestSearchFindsMessagesAndJumps`'s second `enter` opens the hit because the query text is unchanged since the search ran.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/search.go internal/tui/search_test.go internal/tui/model.go internal/tui/view.go
git commit -m "feat(tui): search overlay with text-only fallback badge"
```

---

### Task 9: Memories overlay — list, revisions, edit in $EDITOR, delete

**Files:**
- Create: `internal/tui/memories.go`
- Modify: `internal/tui/model.go` (delete `openMemories`/`updateMemories` stubs and the `memState` placeholder), `internal/tui/view.go` (delete `viewMemories` stub)
- Test: `internal/tui/memories_test.go`

**Interfaces:**
- Consumes: `bus.MemoryRevisions`, `bus.EditMemory`, `bus.DeleteMemory`, `bus.History`.
- Produces:

```go
type memState struct {
	channel string          // memory channel being browsed
	list    []bus.Message   // live memories, newest first
	cursor  int
	revs    []bus.Message   // revisions of list[cursor], oldest first
	rev     int             // index into revs being shown; len(revs)-1 = current
	err     error
}
func (m *Model) openMemories() tea.Cmd
func (m *Model) updateMemories(msg tea.Msg) tea.Cmd     // also handles modeConfirmDelete
func (m Model) viewMemories() string
func editorCommand(path string) *exec.Cmd               // $VISUAL, else $EDITOR, else vi
```

Keys: `↑`/`↓` (or `j`/`k`) move over the list and load that memory's revisions; `←`/`→` step revisions; `e` writes the current revision's content to a temp file, opens the editor via `tea.ExecProcess`, and on return calls `EditMemory` if the content changed; `d` asks `y`/`n`; `esc` closes.

The memory channel browsed is the selected channel if it is a memory channel, otherwise the first memory channel in the rail; with none, `m` toasts "no memory channels".

- [ ] **Step 1: Write the failing tests**

`internal/tui/memories_test.go`:

```go
package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
)

func seedMemories(t *testing.T, f *fixture) (first, second bus.SendResult) {
	t.Helper()
	first = f.agentSend(t, "notes", "Release procedure\nbump, tag, push")
	second = f.agentSend(t, "notes", "Reviewer checklist")
	if _, err := f.ab.EditMemory(f.sam, bus.EditInput{ID: *first.MemoryID, Content: "Release procedure\nbump, tag, push, run release.sh"}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	return first, second
}

func TestMemoriesOverlayListsNewestFirstWithRevisions(t *testing.T) {
	f := newFixture(t)
	first, _ := seedMemories(t, f)
	f.key("esc")
	f.key("m")
	if f.m.mode != modeMemories || f.m.mem.channel != "notes" {
		t.Fatalf("mode=%v channel=%q err=%v", f.m.mode, f.m.mem.channel, f.m.mem.err)
	}
	if len(f.m.mem.list) != 2 || f.m.mem.list[0].Content != "Reviewer checklist" {
		t.Fatalf("list=%+v", f.m.mem.list)
	}
	f.key("down")
	if got := f.m.mem.list[f.m.mem.cursor].MemoryID; got == nil || *got != *first.MemoryID {
		t.Fatalf("cursor not on first memory")
	}
	if len(f.m.mem.revs) != 2 || f.m.mem.rev != 1 {
		t.Fatalf("revs=%d rev=%d err=%v", len(f.m.mem.revs), f.m.mem.rev, f.m.mem.err)
	}
	v := f.m.View()
	if !strings.Contains(v, "2 memories") || !strings.Contains(v, "revision 2 of 2") || !strings.Contains(v, "run release.sh") {
		t.Fatalf("view:\n%s", v)
	}
	f.key("left")
	if v := f.m.View(); !strings.Contains(v, "revision 1 of 2") || strings.Contains(v, "run release.sh") {
		t.Fatalf("left must show the older revision:\n%s", v)
	}
	f.key("right")
	if f.m.mem.rev != 1 {
		t.Fatal("right returns to the current revision")
	}
}

func TestMemoryEditViaEditorFile(t *testing.T) {
	f := newFixture(t)
	_, second := seedMemories(t, f)
	f.key("esc")
	f.key("m")
	// Simulate the editor having returned: write the temp file ourselves.
	path := f.m.memEditPath()
	if err := os.WriteFile(path, []byte("Reviewer checklist\nrun -race\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.send(memEditedMsg{id: *second.MemoryID, path: path})
	f.receive(t)
	cur, err := f.c.b.GetMemory(f.c.as, *second.MemoryID)
	if err != nil || cur.Content != "Reviewer checklist\nrun -race" || *cur.Revision != 2 {
		t.Fatalf("edit not applied: %+v %v", cur, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("temp file must be removed after the edit")
	}
	if f.m.mem.list[0].Content != "Reviewer checklist\nrun -race" {
		t.Fatalf("list must refresh after edit: %+v", f.m.mem.list[0])
	}
}

func TestMemoryDeleteAsksThenTombstones(t *testing.T) {
	f := newFixture(t)
	_, second := seedMemories(t, f)
	f.key("esc")
	f.key("m")
	f.key("d")
	if f.m.mode != modeConfirmDelete {
		t.Fatal("d asks for confirmation")
	}
	if v := f.m.View(); !strings.Contains(v, "delete memory #"+itoa(*second.MemoryID)) || !strings.Contains(v, "Reviewer checklist") {
		t.Fatalf("view:\n%s", v)
	}
	f.key("n")
	if f.m.mode != modeMemories {
		t.Fatal("n cancels")
	}
	f.key("d")
	f.key("y")
	if _, err := f.c.b.GetMemory(f.c.as, *second.MemoryID); err == nil {
		t.Fatal("memory must be deleted")
	}
	if len(f.m.mem.list) != 1 || f.m.mode != modeMemories {
		t.Fatalf("list=%d mode=%v", len(f.m.mem.list), f.m.mode)
	}
}

func TestMemoriesWithoutMemoryChannelToasts(t *testing.T) {
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	if _, err := ab.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	f := &fixture{c: c, ab: ab, sam: sam, m: New(c, LoadTheme(func(string) string { return "" }, nil))}
	f.m.width, f.m.height = 100, 32
	f.run(f.m.Init())
	f.key("esc")
	f.key("m")
	if f.m.mode != modeNormal || !strings.Contains(f.m.toast, "no memory channels") {
		t.Fatalf("mode=%v toast=%q", f.m.mode, f.m.toast)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'Memor' -v`
Expected: FAIL (`f.m.mem.channel` undefined).

- [ ] **Step 3: Implement**

Delete `openMemories`, `updateMemories`, the `memState` placeholder from `model.go`, and `viewMemories` from `view.go`. Add cases to `Update`'s first switch:

```go
	case memListMsg:
		m.mem.err = msg.err
		if msg.err == nil {
			m.mem.list = msg.msgs
			m.mem.cursor = min(m.mem.cursor, max(len(msg.msgs)-1, 0))
			cmds = append(cmds, m.loadRevisions())
		}
	case revisionsMsg:
		if msg.err == nil && m.mem.currentID() == msg.id {
			m.mem.revs = msg.revs
			m.mem.rev = len(msg.revs) - 1
		}
	case memEditedMsg:
		cmds = append(cmds, m.applyMemoryEdit(msg))
	case memChangedMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("memory: "+errText(msg.err)))
		} else {
			cmds = append(cmds, m.loadMemoryList())
		}
```

`internal/tui/memories.go`:

```go
package tui

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
// revision, so History is the list; it comes back ascending and is reversed.
func (m *Model) loadMemoryList() tea.Cmd {
	c := m.c
	ch := m.mem.channel
	return func() tea.Msg {
		top := int64(math.MaxInt64)
		ms, err := c.b.History(c.as, ch, &top, nil, historyPage)
		for i, j := 0, len(ms)-1; i < j; i, j = i+1, j-1 {
			ms[i], ms[j] = ms[j], ms[i]
		}
		return memListMsg{channel: ch, msgs: ms, err: err}
	}
}

func (m *Model) loadRevisions() tea.Cmd {
	id := m.mem.currentID()
	if id == 0 {
		m.mem.revs, m.mem.rev = nil, 0
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
	case "up", "k":
		m.mem.cursor = max(m.mem.cursor-1, 0)
		return m.loadRevisions()
	case "down", "j":
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

// memEditPath is the temp file the editor opens for the current memory.
func (m *Model) memEditPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("agentbus-memory-%d.md", m.mem.currentID()))
}

func editorCommand(path string) *exec.Cmd {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	parts := strings.Fields(ed)
	return exec.Command(parts[0], append(parts[1:], path)...) //nolint:gosec // the user's own $EDITOR
}

// editMemoryInEditor writes the current revision to a temp file and hands
// the terminal to the editor; memEditedMsg arrives when it exits.
func (m *Model) editMemoryInEditor() tea.Cmd {
	cur := m.mem.current()
	if cur == nil {
		return nil
	}
	path := m.memEditPath()
	if err := os.WriteFile(path, []byte(cur.Content+"\n"), 0o600); err != nil {
		return m.showToast("edit: " + err.Error())
	}
	id := *cur.MemoryID
	return tea.ExecProcess(editorCommand(path), func(err error) tea.Msg { return memEditedMsg{id: id, path: path, err: err} })
}

// applyMemoryEdit reads the editor's file back, removes it, and edits the
// memory when the content changed.
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
	cur := m.mem.current()
	if cur == nil || content == "" || content == cur.Content {
		return m.showToast("memory unchanged")
	}
	c := m.c
	id := msg.id
	return func() tea.Msg {
		_, err := c.b.EditMemory(c.as, bus.EditInput{ID: id, Content: content})
		return memChangedMsg{id: id, err: err}
	}
}

func (m Model) viewMemories() string {
	th := m.theme
	dim := th.Style(th.Dim)
	var b strings.Builder
	for i, x := range m.mem.list {
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
		b.WriteString(line + "\n")
	}
	if m.mem.err != nil {
		b.WriteString(th.Style(th.Error).Render("✗ "+errText(m.mem.err)) + "\n")
	}
	if len(m.mem.list) == 0 {
		b.WriteString(dim.Render("no memories in "+m.mem.channel) + "\n")
	}
	if len(m.mem.revs) > 0 {
		r := m.mem.revs[min(m.mem.rev, len(m.mem.revs)-1)]
		b.WriteString("\n" + dim.Render(fmt.Sprintf("#%d · revision %d of %d · %s · %s", m.mem.currentID(), m.mem.rev+1, len(m.mem.revs), r.Sender, clock(r.CreatedAt))) + "\n")
		b.WriteString(r.Content + "\n")
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
	}
	if m.mode == modeConfirmDelete {
		cur := m.mem.current()
		title := strings.SplitN(cur.Content, "\n", 2)[0]
		b.WriteString("\n" + th.Style(th.Error).Render(fmt.Sprintf("delete memory #%d “%s”? tombstones all revisions · y yes  n no", m.mem.currentID(), title)))
	}
	title := th.Style(th.Mem).Render("◆ "+m.mem.channel) + dim.Render(fmt.Sprintf(" · %d memories", len(m.mem.list)))
	return m.overlay(title, th.Mem, b.String(), "e edit  d delete  ← → revision  ↑↓ move  esc close")
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tui/ -v && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues. The `//nolint:gosec` is only needed if `gosec` is enabled; the repo runs default linters, so drop the directive if `golangci-lint` reports it as unused (`nolintlint`).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/memories.go internal/tui/memories_test.go internal/tui/model.go internal/tui/view.go
git commit -m "feat(tui): memories overlay with revisions, editor edit, delete"
```

---

### Task 10: Health overlay and config editor

**Files:**
- Create: `internal/tui/health.go`
- Modify: `internal/tui/model.go` (delete `openHealth`/`updateHealth` stubs and the `healthState` placeholder), `internal/tui/view.go` (delete `viewHealth` stub)
- Test: `internal/tui/health_test.go`

**Interfaces:**
- Produces:

```go
type healthState struct {
	lastQueryAt  time.Time  // last search run
	lastQueryOK  bool
	scroll       int
}
func (m *Model) openHealth() tea.Cmd
func (m *Model) updateHealth(msg tea.Msg) tea.Cmd
func (m Model) viewHealth() string
func (m *Model) recordQuery(ok bool)   // called from the searchMsg case
```

Keys: `o` opens `cfg.Path` in the editor; `↑`/`↓` scroll; `esc` closes.

- [ ] **Step 1: Write the failing tests**

`internal/tui/health_test.go`:

```go
package tui

import (
	"os"
	"strings"
	"testing"
)

func TestHealthOverlayShowsStorageSessionsThemeAndConfig(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.key("esc")
	f.key("h")
	if f.m.mode != modeHealth {
		t.Fatal("h opens health")
	}
	v := f.m.View()
	for _, want := range []string{"storage", "2.0 GiB", "sessions 2 live", "channels 2 (1 memory)", "embeddings unset", "receive", "theme", "AGENTBUS_TUI_AGENTCOLOR", "cyan", "config ·", f.c.cfg.Path, "\"tui_name\"", "\"sqlite_budget_mib\": 2048"} {
		if !strings.Contains(v, want) {
			t.Errorf("health lacks %q:\n%s", want, v)
		}
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes")
	}
}

func TestConfigEditedReloadsAndReportsErrors(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.c.cfg.Path, []byte(`{"tui_name": ""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.send(configEditedMsg{})
	if !strings.Contains(f.m.toast, "tui_name") {
		t.Fatalf("invalid config must toast the validation error, got %q", f.m.toast)
	}
	if err := os.WriteFile(f.c.cfg.Path, []byte(`{"tui_name": "eric"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.send(configEditedMsg{})
	if !strings.Contains(f.m.toast, "restart") {
		t.Fatalf("valid config must say a restart applies it, got %q", f.m.toast)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'Health|ConfigEdited' -v`
Expected: FAIL (`h` does nothing; `configEditedMsg` unhandled).

- [ ] **Step 3: Implement**

Delete `openHealth`, `updateHealth`, the `healthState` placeholder from `model.go`, and `viewHealth` from `view.go`. In `Update`'s first switch add:

```go
	case configEditedMsg:
		cmds = append(cmds, m.onConfigEdited(msg))
```

and in the existing `searchMsg` case add `m.recordQuery(msg.err == nil && !msg.res.SemanticUnavailable)` as the first line.

`internal/tui/health.go`:

```go
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
	case "esc", "h", "?":
		m.mode = modeNormal
	case "up", "k":
		m.health.scroll = max(m.health.scroll-1, 0)
	case "down", "j":
		m.health.scroll++
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

func (m Model) viewHealth() string {
	th := m.theme
	dim, ok, warn := th.Style(th.Dim), th.Style(th.Health), th.Style(th.Warn)
	st := m.status
	var b strings.Builder
	p := func(format string, a ...any) { b.WriteString(fmt.Sprintf(format, a...)) }
	pct := 0
	if st.BudgetBytes > 0 {
		pct = int(st.UsageBytes * 100 / st.BudgetBytes)
	}
	bar := strings.Repeat("█", pct/5) + dim.Render(strings.Repeat("█", 20-pct/5))
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
	b.WriteString(dim.Render("theme") + "\n")
	for _, v := range themeVars {
		p("  %s %s\n", v.env, th.Sources[v.env])
	}
	p("\n%s %s\n", dim.Render("config ·"), cfg.Path)
	js, err := json.MarshalIndent(cfg, "  ", "  ")
	if err != nil {
		b.WriteString(err.Error())
	} else {
		b.WriteString("  " + string(js) + "\n")
	}
	lines := strings.Split(b.String(), "\n")
	if m.health.scroll < len(lines) {
		lines = lines[m.health.scroll:]
	}
	return m.overlay("health", th.Health, strings.Join(lines, "\n"), "o open config in $EDITOR  ↑↓ scroll  esc close")
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tui/ -v && go vet ./... && golangci-lint run ./...`
Expected: PASS, 0 issues. If the `"\"sqlite_budget_mib\": 2048"` assertion fails, check the indent arguments to `MarshalIndent` (prefix `"  "`, indent `"  "` gives `"sqlite_budget_mib": 2048` with a single space after the colon).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/health.go internal/tui/health_test.go internal/tui/model.go internal/tui/view.go
git commit -m "feat(tui): health overlay and config editor"
```

---

### Task 11: `agentbus tui` command, entry point, docs

**Files:**
- Create: `internal/tui/tui.go`
- Modify: `main.go` (usage line, new `case "tui"`)
- Modify: `docs/install.md` (Operating section, new Theme subsection), `README.md` (Usage)
- Modify: `PROGRESS.md` (append an entry when pushed; write it now, it is part of the task)
- Test: `internal/tui/tui_test.go`

**Interfaces:**
- Produces: `func Run(cfg config.Config, as string, stderr io.Writer) error`.

- [ ] **Step 1: Write the failing test**

`internal/tui/tui_test.go`:

```go
package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsBadNameBeforeStartingTheProgram(t *testing.T) {
	cfg := testConfig(t)
	var stderr bytes.Buffer
	err := Run(cfg, "bad/name", &stderr)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("want a name validation error, got %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui/ -run RunRejects -v`
Expected: FAIL, `undefined: Run`.

- [ ] **Step 3: Implement**

`internal/tui/tui.go`:

```go
package tui

import (
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
)

// Run starts the dashboard as identity `as` (empty means cfg.TUIName) and
// returns when the user quits. Theme warnings go to stderr before the
// alternate screen opens; bus logs go to the shared log file.
func Run(cfg config.Config, as string, stderr io.Writer) error {
	if as == "" {
		as = cfg.TUIName
	}
	log, err := mcpserver.OpenLog(cfg)
	if err != nil {
		return err
	}
	theme := LoadTheme(os.Getenv, stderr)
	c, err := newClient(cfg, as, log)
	if err != nil {
		return err
	}
	p := tea.NewProgram(New(c, theme), tea.WithAltScreen())
	go c.receiveLoop(p.Send)
	_, runErr := p.Run()
	closeErr := c.close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}
```

`main.go`: change the usage line to

```go
		fmt.Fprintln(os.Stderr, "usage: agentbus <init|mcp|tui|status|reset|identity|version> [flags]")
```

and add before `case "version":`:

```go
	case "tui":
		fs := flag.NewFlagSet("agentbus tui", flag.ContinueOnError)
		path := fs.String("config", "", "configuration file")
		as := fs.String("as", "", "identity to register as (default: tui_name from the config)")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		cfg, _, err := config.Load(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		if err := tui.Run(cfg, *as, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
```

with `"github.com/ericfitz/agentbus/internal/tui"` added to the imports.

- [ ] **Step 4: Document**

`docs/install.md`, Operating section: add after the `agentbus status` bullet:

```markdown
- `agentbus tui` opens a live dashboard: channels with unread counts on the
  left, the selected channel's stream in the centre, live sessions on the
  right, a compose line, and a status bar. It registers as `tui_name` from
  the config (default: your OS user name; `--as <name>` overrides) and is an
  ordinary bus participant, so agents see your messages like any other.
  Press `esc` for the command keys (`?` lists them), `/` to search, `m` for
  the memory browser, `h` for health, `q` to quit. Colours come from
  `AGENTBUS_TUI_*COLOR` environment variables; see below.
```

Add a new section before `## Limits worth knowing`:

```markdown
## TUI theme

Each variable takes one of the sixteen ANSI colour names (`black`, `red`,
`green`, `yellow`, `blue`, `magenta`, `cyan`, `white`, or a `bright` form
such as `brightblack`), an index `0`-`15`, or `default` for the terminal's
own colour. Matching is case-insensitive. Other values, including `#RRGGBB`,
are rejected with one line on stderr and the default is used.

| Variable | Used for | Default |
|----------|----------|---------|
| `AGENTBUS_TUI_BGCOLOR` | screen background | `default` |
| `AGENTBUS_TUI_TEXTCOLOR` | message content | `default` |
| `AGENTBUS_TUI_DIMCOLOR` | timestamps, dividers, help | `brightblack` |
| `AGENTBUS_TUI_AGENTCOLOR` | agent names, selected channel, key hints | `cyan` |
| `AGENTBUS_TUI_USERCOLOR` | your own name and messages | `yellow` |
| `AGENTBUS_TUI_MEMCOLOR` | memory channels and the memory browser | `magenta` |
| `AGENTBUS_TUI_HEALTHCOLOR` | live heartbeat dot, ok states | `green` |
| `AGENTBUS_TUI_WARNCOLOR` | warnings such as the text-only search badge | `yellow` |
| `AGENTBUS_TUI_ERRORCOLOR` | errors and the delete confirmation | `red` |
| `AGENTBUS_TUI_SELCOLOR` | selected row background | `brightblack` |

The health overlay (`h`) lists the resolved theme.
```

`README.md`, in Usage after the first paragraph, add:

```markdown
`agentbus tui` opens a live dashboard where you can read every channel and
post alongside the agents.
```

`PROGRESS.md`: append

```markdown
## <today's date>: agentbus tui

Pushed to `main`:

- `agentbus tui`: Bubble Tea dashboard per
  `docs/superpowers/specs/2026-09-08-agentbus-tui-design.md` — channel and
  session rails, stream with new/evicted dividers, compose with reply and
  memory creation, search / memories / health overlays, env-var ANSI theme.
- New config key `tui_name`; new bus read `MemoryRevisions`;
  `mcpserver.StartBackgroundLoops` exported for reuse.
```

- [ ] **Step 5: Run everything and try it**

Run: `gofmt -l . ; go vet ./... && golangci-lint run ./... && go test ./...`
Expected: nothing from gofmt, 0 issues, all packages PASS.

Then, in a real terminal (not the agent sandbox), the executor or the user runs:

```bash
go build -o /tmp/agentbus . && AGENTBUS_DATA_DIR=$(mktemp -d) /tmp/agentbus tui
```

Expected: the alternate screen opens with "no channels yet · c to create one"; `esc`, `c`, type `dev`, `enter` creates a channel; `i`, type a message, `enter` shows it in the stream within a second; `h` shows the health overlay; `q` exits cleanly with no stderr output. If a subagent cannot open a TTY, record that this step was skipped and leave it for the user.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/tui.go internal/tui/tui_test.go main.go docs/install.md README.md PROGRESS.md
git commit -m "feat: agentbus tui command"
```

---

## Self-review

**Spec coverage.**
- Purpose observe and participate: Tasks 5-7 (observe), 6 (participate). ✓
- In-process bus, own token, heartbeat, tick: Task 4 (`StartBackgroundLoops`). ✓
- Identity `tui_name` / `--as`, register name rule via `Register`: Tasks 1, 11. ✓
- Chat-client layout with unread counts, memory marker, sessions, compose, status bar: Task 7. ✓
- Search, memories, health overlays: Tasks 8, 9, 10. Message detail deferred per spec. ✓
- Bubble Tea + Lip Gloss: Task 2 pins versions. ✓
- Theming table, accepted values, rejection message, health "theme" heading: Tasks 2, 10, docs in 11. ✓
- Behaviour: start sequence (4, 5), live goroutine with ack (4), gaps divider (7), unread (5), status every 5 s (5), enter sends / memory channel creates (6), `r` reply (5, 6), search modes and badge (8), memories list/revision/edit/delete with y/n (9), toast 5 s or key (5, 7), quit ordering (4, 11). ✓
- Canvas keymap: `c` create channel and `s` toggle subscribe (6); `g`/`G`, pgup/pgdn (5); `↑` recall, `ctrl+u` (5); health `o` (10). "shift+enter" is `alt+enter` (interpretation 3). "enter detail" on the stream is the deferred message detail view.
- Out of scope list honoured: no mouse, one human, no in-place config edit, no reset, no truecolour.

**Placeholder scan.** No TBD/TODO. Every stub introduced in Task 5 and 7 is named, and the task that deletes it is named.

**Test-fixture constraints.** No production code uses `tea.Sequence` (its message type is unexported, so the synchronous fixture cannot unwrap it); multi-step work returns one message whose handler issues the next command. Timers go through the `tick` variable.

**Type consistency.** `client.subscribe(ch bus.Channel, from string)` is used with a `bus.Channel` everywhere (Task 5 `onBatch` builds one from the expired name). `memState.currentID()` returns `int64`; `revisionsMsg.id` and `memChangedMsg.id` are `int64`. `searchState.query` is declared (Task 8 note). `Theme.Sources` is a `map[string]string` keyed by env var, read by Task 10. `historyMsg.prepend == true` means "do not mark seen" in Task 5 and Task 8's `jumpTo` relies on that. `itoa` lives in `view.go` and is used by Tasks 8, 9 tests.
