# Backlog: TUI polish, message tags, tag subscriptions, task-list docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close GitHub issues #7, #9, #3, #4 (TUI), #5 and #6 (message tags and tag subscriptions in the bus, MCP, CLI, and TUI), and #8 (task-list usage docs), one issue per commit group.

**Architecture:** The TUI work is confined to `internal/tui` (rendering, a merged DM view, a tag view computed from already-loaded messages). Tags add one normalized table (`message_tags`) read back through a single columns helper so every message query returns tags. Tag subscriptions add `tag_subscriptions` plus one pseudo-subscription row (`tags/`) that rides the existing cursor, pending-batch, ack, and idle-expiry code in `receiveOnce`, which is refactored to build its SQL from per-source predicates.

**Tech Stack:** Go 1.27, modernc.org/sqlite, bubbletea 1.3.10 / lipgloss 1.1.0 / bubbles 1.0.0, go-sdk MCP.

**Spec:** GitHub issues ericfitz/agentbus #7, #9, #3, #4, #5, #6, #8 (`gh issue view N --repo ericfitz/agentbus`; the issue bodies are the agreed designs) and `docs/adr/0009-message-tags-and-tag-subscriptions.md` (human decisions, binding). Context: ADRs 0004 (DMs), 0005 (task lists), 0008 (memory channels are search-only).

## Global Constraints

- Go, run from the repo root: `go build ./...`, `go test ./...` (focused: `go test ./internal/tui/ -run TestX`), `golangci-lint run ./...` stays clean, `gofmt -l .` prints nothing.
- TUI test fixture rules (structural): no `tea.Sequence` in production code; timers only through the package `tick` var (`internal/tui/model.go:53`); every text input uses `Cursor.SetMode(cursor.CursorStatic)`.
- Charm v1 libs only: bubbletea 1.3.10, lipgloss 1.1.0, bubbles 1.0.0. No new dependencies (`github.com/charmbracelet/x/ansi` is already a direct dependency and may be used).
- American English spelling. Match the surrounding comment density and idiom (each file explains *why*, not what).
- Theme colors are ANSI 0-15 or `default` only (`config.ColorIndex`).
- macOS host: no GNU `timeout`; search with `rg`, never `grep`.
- DM delivery guard (ADR 0004, `dmReadable`) holds at delivery; `resume=false` means future-only (no backlog from cursor or from start).
- Schema migrations follow `internal/bus/migrate.go`: bump `schemaVersion`, add a `migrations[v]` step, keep `CREATE ... IF NOT EXISTS` DDL in `schema.go` (the DDL runs after `migrate`, so a step that only adds tables is a no-op function).
- Tags: `^[A-Za-z0-9_-]{1,20}$`, lowercased, deduplicated, at most 10 per message, one bad tag rejects the whole call (ADR 0009 item 1).
- Each issue ends in its own commit(s), message suffixed with the issue, e.g. `feat(tui): ... (#3)`. Do not commit `HANDOFF.md`.
- Terminal checks: where an issue asks for a real-TTY check (Terminal.app + Source Code Pro), note it in the commit body as "TTY check pending" rather than blocking; the orchestrator runs the walkthrough.

## Review Focus

1. A `tasks/` channel drawn while `m.msgs` holds its raw revisions (search jump, live receive) must never show a message header row: only status-emoji task rows. Pinned in Task 2 (`TestTaskChannelNeverShowsMessageHeaders`).
2. A message from the empty sender (tick reclaim, shown as `bus`) must render a header with the agent icon and no panic. Pinned in Task 3 (`TestHeaderBusSender`).
3. A reply started from the TUI's own outgoing DM row (merged into its inbox pane) must go to the partner's inbox, not back to `dm/<self>`. Pinned in Task 4 (`TestReplyFromOwnOutgoingRowTargetsPartner`).
4. `edit_memory` with `tags: []` (present, empty) clears tags; omitting `tags` keeps them. Pinned in Task 5 (`TestEditMemoryTagsKeepAndClear`).
5. The `tags/` pseudo-subscription row must survive the maintenance orphan sweep (`reapEmptyChannels`) and `receiveOnce`'s "channel no longer exists" sweep, or every tag subscription silently dies at the next tick. Pinned in Task 6 (`TestTagRowSurvivesSweeps`).

---

### Task 1: Task-list rail icon is a wide emoji (#7)

**Files:**
- Modify: `internal/tui/view.go:52-70` (icon constants)
- Test: `internal/tui/view_test.go`

**Interfaces:**
- Produces: `iconTasks = "\U0001F4CB️ "` (📋, East Asian Width W), no CSI wrapper. Task 2 and Task 3 reuse it.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/view_test.go`:

```go
// The task-list icon is a Wide emoji like chat and memory: same measured
// width, no erase/cursor escapes, so rail, compose, search, and the task
// pane title all line up on the same column.
func TestIconTasksIsWideEmojiWithoutEscapes(t *testing.T) {
	if w := lipgloss.Width(iconTasks); w != lipgloss.Width(iconChat) {
		t.Fatalf("iconTasks width %d, want %d", w, lipgloss.Width(iconChat))
	}
	if strings.Contains(iconTasks, "\x1b[") {
		t.Fatalf("iconTasks must not need the CSI erase trick: %q", iconTasks)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tui/ -run TestIconTasksIsWideEmojiWithoutEscapes -v`
Expected: FAIL with "must not need the CSI erase trick".

- [ ] **Step 3: Replace the constant**

In `internal/tui/view.go` replace line 69:

```go
	iconTasks = "\x1b[2X☑️\x1b[1C " // ballot box with check; Neutral width, same treatment as the gear
```

with:

```go
	iconTasks = "\U0001F4CB️ " // clipboard; Wide like chat and memory, so no CSI trick
```

- [ ] **Step 4: Run the TUI tests**

Run: `go test ./internal/tui/`
Expected: PASS (`TestTaskChannelRailIcon` and `viewSearch` use the constant, not a literal).

- [ ] **Step 5: Lint, format, commit**

```bash
gofmt -l . && golangci-lint run ./... && go build ./...
git add internal/tui/view.go internal/tui/view_test.go
git commit -m "feat(tui): task-list icon is the clipboard emoji (#7)

Wide (W) like the chat and memory icons, so it needs no CSI 2X/1C erase
trick and lines up in the rail, compose line, search results, and task
pane title. TTY check pending (Terminal.app + Source Code Pro)."
```

---

### Task 2: Task pane with status emoji and in-progress owner (#9)

**Files:**
- Modify: `internal/tui/tasks.go:25-76` (`renderTasks`)
- Modify: `internal/tui/view.go:52-70` (three status emoji constants)
- Modify: `docs/install.md:239-240` (legend)
- Test: `internal/tui/tasks_test.go`

**Interfaces:**
- Consumes: `bus.TaskSummary{ID, Subject, Status, Owner, Depth, OpenBlockers, LeasedUntil}` (`internal/bus/tasks.go:46-55`); `iconAgent`, `iconUser` (`view.go:66-67`); `m.c.as` (TUI identity).
- Produces: constants `taskPending = "❎ "`, `taskInProgress = "⏱️ "`, `taskCompleted = "✅ "` (each 2 columns + space).
- The bus already refuses an ownerless `in_progress` task (`internal/bus/tasks_update.go:202`); do not add a bus rule.

- [ ] **Step 1: Update and add tests**

In `internal/tui/tasks_test.go`, change the assertions in `TestTaskChannelRendersTree` (lines 52-60) to:

```go
	if !strings.HasPrefix(lines[0], taskPending+"#") || !strings.Contains(lines[0], "a") {
		t.Fatalf("line0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  "+taskInProgress+"#") || !strings.Contains(lines[1], ansi.Strip(iconAgent)+"Sam") {
		t.Fatalf("line1 = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], taskPending+"#") || !strings.Contains(lines[2], "blocked by #"+itoa(a.ID)) {
		t.Fatalf("line2 = %q", lines[2])
	}
```

In `TestTaskTreeRefreshesOnRevision` replace `"⊘"` with `"blocked by"` (both occurrences) and `"●"` with `taskCompleted`.

Append:

```go
// Row suffixes (issue #9): a pending task with an owner shows a dim "→ owner";
// only an in-progress row shows the owner with the rail icon; completed rows
// are dimmed as a whole; the lease suffix follows the owner.
func TestTaskRowSuffixes(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	assigned, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "assigned"})
	if err != nil {
		t.Fatal(err)
	}
	owner := f.sam
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: assigned.ID, Owner: &owner}); err != nil {
		t.Fatal(err)
	}
	working, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "working"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskClaim(f.sam, working.ID, time.Now().Add(time.Hour).UnixMilli(), ""); err != nil {
		t.Fatal(err)
	}
	done, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "done"})
	if err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: done.ID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	raw := strings.Split(f.m.renderStream(), "\n")
	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 rows: %q", lines)
	}
	if !strings.Contains(lines[0], "→ Sam") || strings.Contains(lines[0], "⚙") {
		t.Fatalf("pending with owner shows a dim arrow, not the icon: %q", lines[0])
	}
	agentOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Agent).Render("\x00"), "\x00")
	if !strings.Contains(raw[1], iconAgent+agentOpen+"Sam") || !strings.Contains(lines[1], "lease 59m") && !strings.Contains(lines[1], "lease 60m") {
		t.Fatalf("in-progress row shows icon + colored owner then the lease: %q", raw[1])
	}
	if idx := strings.Index(lines[1], "lease"); idx < strings.Index(lines[1], "Sam") {
		t.Fatalf("lease must follow the owner: %q", lines[1])
	}
	dimOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Dim).Render("\x00"), "\x00")
	if !strings.HasPrefix(raw[2], dimOpen+taskCompleted) {
		t.Fatalf("completed row (icon included) must be dim: %q", raw[2])
	}
}

// A task channel never shows message-style header rows, whatever m.msgs
// holds for it (live revisions arrive through receive): every rendered
// line is a task row.
func TestTaskChannelNeverShowsMessageHeaders(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.receive(t)
	f.selectTaskChannel(t, "tasks/work")
	if len(f.m.msgs["tasks/work"]) == 0 {
		t.Fatal("fixture: expected raw revisions in m.msgs")
	}
	for _, l := range strings.Split(ansi.Strip(f.m.renderStream()), "\n") {
		l = strings.TrimLeft(l, " ")
		if !strings.HasPrefix(l, taskPending) && !strings.HasPrefix(l, taskInProgress) && !strings.HasPrefix(l, taskCompleted) {
			t.Fatalf("non-task row in task pane: %q", l)
		}
	}
}
```

Add imports `"time"`, `"github.com/charmbracelet/lipgloss"`, `"github.com/muesli/termenv"` to `tasks_test.go`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestTask' -v`
Expected: compile error (`taskPending` undefined).

- [ ] **Step 3: Add the constants and rewrite renderTasks**

In `internal/tui/view.go`, after the rail icon block (line 70), add:

```go
// Task status marks. ⏱ U+23F1 is text-presentation by default (like the
// gear) and carries U+FE0F for the color glyph; if a terminal font draws
// its own half-width ⏱, give it the gear's CSI 2X/1C treatment.
const (
	taskPending    = "❎ "       // negative squared cross mark
	taskInProgress = "⏱️ " // stopwatch
	taskCompleted  = "✅ "       // white heavy check mark
)
```

Replace `renderTasks` in `internal/tui/tasks.go` (lines 25-76) with:

```go
// renderTasks draws ch's task tree in place of the revision stream: one row
// per task in the tree order TaskList returns, indented by depth (capped at
// six levels), a status mark, then the row's suffixes: an in-progress task's
// owner with the rail icon and color, a pending task's assignee as a dim
// arrow, blockers, and the lease. Completed rows are dimmed whole.
func (m Model) renderTasks(ch string) string {
	th := m.theme
	dim := th.Style(th.Dim)
	ts := m.tasks[ch]
	if len(ts) == 0 {
		return dim.Render("no tasks yet in " + ch)
	}
	w := max(m.stream.Width, 20)
	lines := make([]string, len(ts))
	for i, t := range ts {
		mark := taskPending
		switch t.Status {
		case "in_progress":
			mark = taskInProgress
		case "completed":
			mark = taskCompleted
		}
		line := strings.Repeat("  ", min(t.Depth, 6)) + mark + "#" + itoa(t.ID) + " " + t.Subject
		var suffix string
		switch {
		case t.Status == "in_progress" && t.Owner != "":
			icon, style := iconAgent, th.Style(th.Agent)
			if t.Owner == m.c.as {
				icon, style = iconUser, th.Style(th.User)
			}
			line += "  " + icon + style.Render(t.Owner)
		case t.Owner != "":
			suffix += " → " + t.Owner
		}
		if len(t.OpenBlockers) > 0 {
			ids := make([]string, len(t.OpenBlockers))
			for j, id := range t.OpenBlockers {
				ids[j] = "#" + itoa(id)
			}
			suffix += " blocked by " + strings.Join(ids, " ")
		}
		if t.LeasedUntil != 0 {
			if left := time.Until(time.UnixMilli(t.LeasedUntil)); left > 0 {
				suffix += " lease " + shortDur(left)
			} else {
				suffix += " lease expired"
			}
		}
		if suffix != "" {
			line += dim.Render(suffix)
		}
		if t.Status == "completed" {
			line = dim.Render(line)
		}
		lines[i] = lipgloss.NewStyle().MaxWidth(w).Render(line)
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Update the install guide legend**

In `docs/install.md` replace lines 239-240:

```
  A `tasks/` channel shows its task tree (read-only): subtasks indented,
  `○ ◐ ● ⊘` for pending, in progress, completed, blocked.
```

with:

```
  A `tasks/` channel shows its task tree (read-only): subtasks indented,
  ❎ pending, ⏱️ in progress (with the owner's name beside it), ✅ completed
  (dimmed); a blocked task keeps ❎ with `blocked by #n`.
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/tui/`
Expected: PASS.

- [ ] **Step 6: Lint, format, commit**

```bash
gofmt -l . && golangci-lint run ./... && go build ./...
git add internal/tui/tasks.go internal/tui/view.go internal/tui/tasks_test.go docs/install.md
git commit -m "feat(tui): task pane with status emoji and in-progress owner (#9)

❎ pending, ⏱️ in progress, ✅ completed; the owner appears with the rail
icon on in-progress rows only, a pending assignee as a dim arrow, then
blockers and lease. TTY check pending for ⏱ half-glyph rendering."
```

---

### Task 3: Message header redesign, timestamp/tag theme keys, chips (#3)

Tag chips render from `bus.Message.Tags`. This task adds that field (JSON `tags,omitempty`) to `internal/bus/messages.go` with no storage behind it; Task 5 populates it. Tests here construct messages with `Tags` set and feed them through `m.addMessages`.

**Files:**
- Modify: `internal/bus/messages.go:29-42` (`Message.Tags`)
- Modify: `internal/config/config.go:82-118` (`Theme.Timestamp`, `Theme.Tag`, defaults, `Colors`)
- Modify: `internal/tui/theme.go:19-25,70-95` (`Theme.Stamp`, `Theme.Tag`, `set`)
- Modify: `internal/tui/view.go:37-44` (`clock` stays; add `stamp`), `231-318` (`renderStream`)
- Modify: `docs/install.md:273-285` (theme table)
- Test: `internal/tui/view_test.go`, `internal/tui/theme_test.go`

**Interfaces:**
- Produces: `func stamp(ms int64) string` ("YYYY-MM-DD HH:MM:SS" or "(today)    HH:MM:SS"); `func (m Model) tagChips(tags []string) string`; `func (m Model) agentLabel(name string) string`; `func (m Model) channelLabel(name string) string`; theme fields `Theme.Stamp`, `Theme.Tag`; config keys `timestamp` (default `brightblack`), `tag` (default `brightblack`). Tasks 4 and 6 reuse `renderStream` unchanged.
- Chip text color: the issue names one key, `tag`, with "brightblack background, brightwhite text". Resolution: `tag` is the chip background; chip text is fixed `brightwhite` (ANSI 15). The `#tag` fallback triggers when a chip renders without any escape sequence (tag color `default`, or lipgloss's ASCII profile), which needs no termenv import.
- Header width rule: the header line is never wrapped. If `stamp + sender --> recipient + chips` exceeds the row width, the chips are truncated (dropped when fewer than 4 columns remain), then the whole header is truncated with `…` via `ansi.Truncate`.

- [ ] **Step 1: Add the config and theme keys with tests**

In `internal/config/config.go`, `Theme` struct (line 82-95): add after `Dim`:

```go
	Timestamp  string `json:"timestamp"`
	Tag        string `json:"tag"`
```

`DefaultTheme()` (line 99): add `Timestamp: "brightblack", Tag: "brightblack"`. `Colors()` (line 104-118): insert `{"timestamp", t.Timestamp}, {"tag", t.Tag},` after the `dim` pair.

In `internal/tui/theme.go`: line 20 becomes

```go
	BG, Text, Dim, Stamp, Tag, Agent, User, Mem, Tasks, Health, Warn, Error, Sel lipgloss.TerminalColor
```

and `set` gains:

```go
	case "timestamp":
		t.Stamp = c
	case "tag":
		t.Tag = c
```

Append to `internal/tui/theme_test.go`:

```go
func TestThemeTimestampAndTagKeys(t *testing.T) {
	cfg := config.Default()
	th := LoadTheme(cfg, io.Discard)
	if th.Stamp != lipgloss.Color("8") || th.Tag != lipgloss.Color("8") {
		t.Fatalf("defaults: stamp=%v tag=%v", th.Stamp, th.Tag)
	}
	cfg.Themes[0].Timestamp, cfg.Themes[0].Tag = "green", "default"
	th = LoadTheme(cfg, io.Discard)
	if th.Stamp != lipgloss.Color("2") || th.Tag != (lipgloss.NoColor{}) || th.Sources["tag"] != "default" {
		t.Fatalf("overrides: %+v", th)
	}
}
```

(add `"io"` to its imports). Run: `go test ./internal/config/ ./internal/tui/ -run 'TestTheme|TestThemes|TestDefault'` — Expected: PASS.

- [ ] **Step 2: Add `Message.Tags`**

In `internal/bus/messages.go` `Message` struct (line 29-42), add after `Revision`:

```go
	// Tags is the message's lowercase tag set, sorted; empty until tags
	// are stored (#5).
	Tags []string `json:"tags,omitempty"`
```

Run: `go build ./...` — Expected: OK.

- [ ] **Step 3: Write the failing view tests**

Append to `internal/tui/view_test.go`:

```go
// msgAt injects one already-loaded message so a header test controls every
// field (sender, channel, time, tags) without a bus round trip.
func (f *fixture) msgAt(ch, sender string, seq, createdAt int64, content string, tags ...string) {
	f.m.addMessages(ch, []bus.Message{{Seq: seq, Channel: ch, Sender: sender, CreatedAt: createdAt, Content: content, Tags: tags}})
	f.m.refreshStream()
}

func TestStampTodayAndPast(t *testing.T) {
	if got := stamp(time.Now().UnixMilli()); !strings.HasPrefix(got, "(today)    ") || len([]rune(got)) != 19 {
		t.Fatalf("today: %q", got)
	}
	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local).UnixMilli()
	if got := stamp(past); got != "2026-01-02 03:04:05" {
		t.Fatalf("past: %q", got)
	}
}

func TestHeaderShowsSenderArrowChannelAndDM(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "hello")
	f.receive(t)
	first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0])
	want := ansi.Strip(iconAgent) + "Sam --> " + iconChat + "dev"
	if !strings.Contains(first, want) || !strings.Contains(first, "(today)") {
		t.Fatalf("channel header %q lacks %q", first, want)
	}
	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "hello") || strings.Contains(lines[0], "hello") {
		t.Fatalf("body must start on the next line: %q", lines)
	}

	f.run(f.m.statusCmd())
	f.agentSend(t, "dm/"+f.c.as, "psst")
	f.receive(t)
	f.key("esc")
	f.toSessions()
	for i, n := range f.m.sessionNames() {
		if n == f.c.as {
			f.run(f.m.selectSession(i))
		}
	}
	first = ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0])
	want = ansi.Strip(iconAgent) + "Sam --> " + iconUser + f.c.as
	if !strings.Contains(first, want) {
		t.Fatalf("DM header %q lacks %q", first, want)
	}
}

func TestHeaderBusSender(t *testing.T) {
	f := newFixture(t)
	f.msgAt("dev", "", 9001, time.Now().UnixMilli(), "reclaimed")
	first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0])
	if !strings.Contains(first, ansi.Strip(iconAgent)+"bus --> ") {
		t.Fatalf("empty sender renders as bus: %q", first)
	}
}

func TestSelectedRowSwapsDimToText(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.m.theme.Text = lipgloss.Color("7")
	f.agentSend(t, "dev", "one")
	f.agentSend(t, "dev", "two")
	f.receive(t)
	f.key("shift+tab") // cursor on "two"
	lines := strings.Split(f.m.renderStream(), "\n")
	stampOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Stamp).Render("\x00"), "\x00")
	textOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Text).Render("\x00"), "\x00")
	if !strings.Contains(lines[0], stampOpen+"(today)") {
		t.Fatalf("unselected row keeps the timestamp color: %q", lines[0])
	}
	if !strings.Contains(lines[2], textOpen+"(today)") || strings.Contains(lines[2], stampOpen+"(today)") {
		t.Fatalf("selected row swaps dim text to the text color: %q", lines[2])
	}
	if f.m.cursorLine != 2 {
		t.Fatalf("cursorLine=%d, want 2 (header + body of the first row)", f.m.cursorLine)
	}
}

func TestTagChipsAndFallback(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.msgAt("dev", "Sam", 9001, time.Now().UnixMilli(), "tagged", "release", "bug")
	chip := lipgloss.NewStyle().Background(f.m.theme.Tag).Foreground(lipgloss.Color("15"))
	first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]
	if !strings.Contains(first, chip.Render(" release ")+" "+chip.Render(" bug ")) {
		t.Fatalf("chips missing: %q", first)
	}
	f.key("shift+tab") // selected row keeps the chip background
	if first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]; !strings.Contains(first, chip.Render(" release ")) {
		t.Fatalf("selected row lost chip background: %q", first)
	}
	f.m.theme.Tag = lipgloss.NoColor{}
	if first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0]); !strings.Contains(first, "#release #bug") {
		t.Fatalf("fallback: %q", first)
	}
}

func TestHeaderNeverWrapsOnNarrowPane(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 40)
	if _, err := f.ab.CreateChannel(f.sam, long, "ordinary"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.m.width = 60
	f.m.layout()
	f.msgAt(long, "Sam", 9001, time.Now().UnixMilli(), "body", "a", "b", "c")
	for i, c := range f.m.channels {
		if c.Name == long {
			f.run(f.m.selectChannel(i))
		}
	}
	lines := strings.Split(f.m.renderStream(), "\n")
	if w := lipgloss.Width(lines[0]); w > f.m.stream.Width {
		t.Fatalf("header wrapped or overflowed: width %d > %d: %q", w, f.m.stream.Width, lines[0])
	}
	if s := ansi.Strip(lines[0]); !strings.Contains(s, "…") || strings.Contains(s, " a ") {
		t.Fatalf("tags go first, then the recipient is cut with …: %q", s)
	}
	if !strings.Contains(ansi.Strip(lines[1]), "body") {
		t.Fatalf("body still on line 2: %q", lines)
	}
}
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test ./internal/tui/ -run 'TestStamp|TestHeader|TestSelectedRowSwaps|TestTagChips' -v`
Expected: compile error (`stamp` undefined).

- [ ] **Step 5: Implement stamp, labels, chips, and the header**

In `internal/tui/view.go`, after `clock` (line 44) add:

```go
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
```

Add `"github.com/charmbracelet/x/ansi"` to view.go's imports.

Replace the body of the `for i, r := range m.rows(ch)` loop in `renderStream` (lines 265-316) with:

```go
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
				summary += " · " + clock(r.latest)
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
```

Update the `renderStream` doc comment (line 231-233) to end with: "Each message is a header line (timestamp, sender --> recipient, tag chips) followed by its body at the row's depth."

- [ ] **Step 6: Run the TUI tests**

Run: `go test ./internal/tui/`
Expected: PASS. If `TestSelectedRowKeepsBackgroundAcrossSegments` fails on the `44m` count, the header still has three styled segments (stamp, sender, recipient); check that `Highlight` is applied to the whole two-line row.

- [ ] **Step 7: Document the theme keys**

In `docs/install.md` theme table (line 273-285), after the `dim` row add:

```
| `timestamp` | message timestamps (unselected rows) | `brightblack` |
| `tag` | tag chip background (`default` falls back to dim `#tag` words) | `brightblack` |
```

and change the `dim` row's "Used for" to `dividers, help, summaries`. Add one sentence after the table: "On the selected message row, dim text (timestamp, thread summary) switches to the `text` color so it stays readable on `selection`."

- [ ] **Step 8: Lint, format, commit**

```bash
gofmt -l . && golangci-lint run ./... && go build ./... && go test ./...
git add internal/bus/messages.go internal/config/config.go internal/tui/theme.go internal/tui/theme_test.go internal/tui/view.go internal/tui/view_test.go docs/install.md
git commit -m "feat(tui): message header with full timestamp and sender --> recipient (#3)

Header line: YYYY-MM-DD HH:MM:SS ((today) for today's messages), sender
and recipient with rail icons and colors, tag chips; body on the next
line at the row's depth. New theme keys timestamp and tag; the selected
row swaps dim text to the text color. Message.Tags is added as a field
only; #5 stores it."
```

---

### Task 4: DM pane merges inbox and outgoing DMs, threaded across channels (#4)

**Files:**
- Modify: `internal/tui/thread.go:26-31,105-118,156-167` (`rows`, `rootSeq`, `collapseCursor`)
- Modify: `internal/tui/model.go:521,771-787` (`paneKey`, `showSelected`)
- Modify: `internal/tui/compose.go:34-38` (`submitCompose` reply target)
- Test: `internal/tui/dm_test.go`

**Interfaces:**
- Consumes: `m.msgs`, `m.dms`, `m.loaded`, `m.loadHistory(ch, nil)` (`model.go:962`), `bus.DMPrefix`, `bus.DMChannel`.
- Produces: `func (m *Model) paneMsgs(ch string) []bus.Message` — for `dm/X`: `dm/X` plus every loaded `dm/*` message sent by X, ascending by seq; for any other name, `m.msgs[ch]`. Task 6 extends it for tag panes.
- Unread counts, the "new" divider, `markSeen`, and gap listing keep reading `m.msgs[ch]` (inbox only).
- Bus untouched: the TUI observer is already subscribed to every `dm/*` (ADR 0004 guard is unaffected).

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/dm_test.go`:

```go
// dmSetup registers a second agent and returns a sender for each identity:
// the TUI ("eric"), Sam, and Kim, all on the same bus file.
func (f *fixture) selectSessionNamed(t *testing.T, name string) {
	t.Helper()
	f.key("esc")
	f.toSessions()
	for i, n := range f.m.sessionNames() {
		if n == name {
			f.run(f.m.selectSession(i))
			return
		}
	}
	t.Fatalf("no session %q in %v", name, f.m.sessionNames())
}

func (f *fixture) sendAs(t *testing.T, b *bus.Bus, as, ch string, replyTo *int64, content string) bus.SendResult {
	t.Helper()
	r, err := b.Send(as, bus.SendInput{Channel: ch, Content: content, ReplyTo: replyTo})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A two-agent back-and-forth is one thread in both panes even though every
// other message lives in the other inbox.
func TestDMPaneThreadsConversationAcrossInboxes(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	q1 := f.sendAs(t, f.c.b, f.c.as, "dm/Sam", nil, "q1")
	a1 := f.sendAs(t, f.ab, f.sam, "dm/"+f.c.as, &q1.Seq, "a1")
	q2 := f.sendAs(t, f.c.b, f.c.as, "dm/Sam", &a1.Seq, "q2")
	f.sendAs(t, f.ab, f.sam, "dm/"+f.c.as, &q2.Seq, "a2")
	f.receive(t)
	for _, seq := range []int64{q1.Seq, a1.Seq, q2.Seq} {
		f.m.expanded[seq] = true
	}
	want := []string{"q1", ">a1", ">>q2", ">>>a2"}
	if got := contents(f.m.rows("dm/Sam")); !eq(got, want) {
		t.Fatalf("Sam's pane: %v", got)
	}
	if got := contents(f.m.rows("dm/" + f.c.as)); !eq(got, want) {
		t.Fatalf("eric's pane: %v", got)
	}
	f.selectSessionNamed(t, "Sam")
	first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0])
	if !strings.Contains(first, f.c.as+" --> ") || !strings.Contains(first, "Sam") {
		t.Fatalf("header shows the real direction: %q", first)
	}
}

// A pane that mixes two partners lists each conversation; unread counts and
// the divider come from the inbox alone.
func TestDMPaneMixesPartnersAndCountsInboxOnly(t *testing.T) {
	f := newFixture(t)
	kb, kim := agent(t, f.c.cfg, "Kim")
	f.run(f.m.statusCmd())
	f.sendAs(t, f.ab, f.sam, "dm/"+f.c.as, nil, "from sam")
	f.sendAs(t, kb, kim, "dm/"+f.c.as, nil, "from kim")
	f.sendAs(t, f.c.b, f.c.as, "dm/Kim", nil, "to kim")
	f.receive(t)
	if got := contents(f.m.rows("dm/" + f.c.as)); !eq(got, []string{"from sam", "from kim", "to kim"}) {
		t.Fatalf("eric's pane: %v", got)
	}
	if got := contents(f.m.rows("dm/Kim")); !eq(got, []string{"from kim", "to kim"}) {
		t.Fatalf("Kim's pane: %v", got)
	}
	if got := contents(f.m.rows("dm/Sam")); !eq(got, []string{"from sam"}) {
		t.Fatalf("Sam's pane: %v", got)
	}
	if f.m.unread("dm/Kim") != 1 || f.m.unread("dm/"+f.c.as) != 2 {
		t.Fatalf("unread counts inbox only: kim=%d eric=%d", f.m.unread("dm/Kim"), f.m.unread("dm/"+f.c.as))
	}
	f.selectSessionNamed(t, "Kim")
	s := ansi.Strip(f.m.renderStream())
	// "from kim" is Kim's own outgoing message (not unread); the divider
	// sits after it and before "to kim", the first unread inbox message.
	if strings.Count(s, " new ") != 1 || strings.Index(s, " new ") < strings.Index(s, "from kim") || strings.Index(s, " new ") > strings.Index(s, "to kim") {
		t.Fatalf("divider sits before the first unread inbox message:\n%s", s)
	}
}

// Selecting a DM pane loads history for every inbox, so outgoing messages
// sent before the TUI started still show.
func TestDMPaneLoadsOtherInboxHistory(t *testing.T) {
	f := newFixture(t)
	f.sendAs(t, f.c.b, f.c.as, "dm/Sam", nil, "earlier")
	f.drainAndAck(t)
	f.run(f.m.statusCmd())
	f.selectSessionNamed(t, f.c.as)
	if got := contents(f.m.rows("dm/" + f.c.as)); !eq(got, []string{"earlier"}) {
		t.Fatalf("own pane after history: %v", got)
	}
}

// r on the TUI's own outgoing row replies to the partner, not to dm/self.
func TestReplyFromOwnOutgoingRowTargetsPartner(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.sendAs(t, f.c.b, f.c.as, "dm/Sam", nil, "ping")
	f.receive(t)
	f.selectSessionNamed(t, f.c.as)
	f.key("r") // no cursor: replies to the last row, our own "ping"
	for _, r := range "pong" {
		f.key(string(r))
	}
	f.key("enter")
	ms, _ := f.ab.History(f.sam, "dm/Sam", nil, nil, 10)
	if len(ms) != 2 || ms[1].Content != "pong" || ms[1].ReplyTo == nil || *ms[1].ReplyTo != ms[0].Seq {
		t.Fatalf("reply must land in Sam's inbox threaded on ping: %+v", ms)
	}
}
```

Add `"github.com/charmbracelet/x/ansi"` to dm_test.go's imports.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/tui/ -run 'TestDMPane|TestReplyFromOwn' -v`
Expected: FAIL (Sam's pane shows `["a1", ">a2"]`-style orphan roots; reply lands in `dm/eric`).

- [ ] **Step 3: Implement paneMsgs and use it**

In `internal/tui/thread.go`, before `rows`, add:

```go
// paneMsgs returns the messages a pane shows. A dm/X pane merges X's
// inbox with every loaded direct message X sent (DMs are one-way, so a
// conversation alternates between two inboxes); everything else shows its
// own channel. Unread counts and the divider stay on m.msgs[ch] (the
// inbox), see unread and dividerFor.
func (m *Model) paneMsgs(ch string) []bus.Message {
	owner, ok := strings.CutPrefix(ch, bus.DMPrefix)
	if !ok {
		return m.msgs[ch]
	}
	out := append([]bus.Message{}, m.msgs[ch]...)
	for name, ms := range m.msgs {
		if name == ch || !strings.HasPrefix(name, bus.DMPrefix) {
			continue
		}
		for _, x := range ms {
			if x.Sender == owner {
				out = append(out, x)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}
```

Add `"strings"` to thread.go imports. Then:
- `rows` line 27: `ms := m.paneMsgs(ch)`.
- `rootSeq` line 108: `for _, x := range m.paneMsgs(ch) {`.
- `collapseCursor` line 162: `for _, x := range m.paneMsgs(m.selName()) {`.

In `internal/tui/model.go`:
- `paneKey` line 521: replace `len(m.msgs[m.selName()]) > 0` with `len(m.rows(m.selName())) > 0`.
- `showSelected` (line 783-786): replace

```go
	if ch != "" && !m.loaded[ch] {
		return m.loadHistory(ch, nil)
	}
	return nil
```

with

```go
	var cmds []tea.Cmd
	if ch != "" && !m.loaded[ch] {
		cmds = append(cmds, m.loadHistory(ch, nil))
	}
	// A DM pane merges what its owner sent to every other inbox, so those
	// inboxes need their latest page too. ponytail: one page per inbox on
	// first DM visit; a bus-side conversation query is out of scope (#4).
	if strings.HasPrefix(ch, bus.DMPrefix) {
		for _, d := range m.dms {
			if d.Name != ch && !m.loaded[d.Name] {
				cmds = append(cmds, m.loadHistory(d.Name, nil))
			}
		}
	}
	return tea.Batch(cmds...)
```

In `internal/tui/compose.go` `submitCompose` (lines 34-38) replace the reply-routing block with:

```go
	// A reply while viewing the TUI's own inbox goes back to the sender, not
	// into the inbox being viewed. The pane also shows the TUI's own outgoing
	// messages (#4); a reply to one of those stays in the partner's inbox,
	// the channel it was sent to.
	if m.replyTo != nil && ch == bus.DMChannel(m.c.as) {
		ch = bus.DMChannel(m.replyTo.Sender)
		if m.replyTo.Sender == m.c.as {
			ch = m.replyTo.Channel
		}
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tui/`
Expected: PASS, including the existing `TestCrossChannelReplyRendersTopLevel` (a `dev` parent is not merged into a DM pane).

- [ ] **Step 5: Lint, format, commit**

```bash
gofmt -l . && golangci-lint run ./... && go build ./...
git add internal/tui/thread.go internal/tui/model.go internal/tui/compose.go internal/tui/dm_test.go
git commit -m "feat(tui): thread direct messages across both inboxes in the DM pane (#4)

A dm/X pane shows X's inbox plus every DM X sent, threaded by reply_to
across channels; unread counts and the new divider stay inbox-only.
Display only: the ADR 0004 delivery guard is untouched."
```

---

### Task 5: Message tags (#5)

**Files:**
- Modify: `internal/bus/schema.go` (`schemaVersion = 3`, `message_tags`)
- Modify: `internal/bus/migrate.go:18-20` (`migrations[2]`)
- Create: `internal/bus/tags.go`
- Modify: `internal/bus/messages.go:14-22,44-77,153-212,268-433` (`SendInput.Tags`, `messageCols`, `scanMessages`, `envelopeUpperBound`, `insertMessage`, `Send`, `History`)
- Modify: `internal/bus/memories.go:9-16,29,52,92-227` (`EditInput.Tags`, `EditMemory`)
- Modify: `internal/bus/search.go:11-21,48-56,73-100,106-140,276` (`SearchInput.Tags`, `searchFilters`, `textSearch`)
- Modify: `internal/bus/receive.go:419,433`, `internal/bus/wait.go:83` (`messageCols("messages")`)
- Modify: `internal/mcpserver/server.go:66-72,359-379` (`historyIn.Tags`, descriptions)
- Modify: `README.md:42-52`, `internal/cli/skills/using-agentbus/SKILL.md` (new "Tags" section before "Session protocol")
- Test: `internal/bus/tags_test.go` (new), `internal/bus/migrate_test.go`, `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: `bus.Message.Tags` (Task 3).
- Produces: `func NormalizeTags(tags []string) ([]string, error)` (exported; repoconfig uses it in Task 6): lowercases, validates `^[a-z0-9_-]{1,20}$`, dedupes, sorts, rejects more than 10 after dedupe; `nil` for no tags. `func messageCols(alias string) string` replaces `messageColumns` and `qualifiedColumns`; the 13th column is the message's tags as one space-separated string. `func insertTags(tx *sql.Tx, seq int64, tags []string) error`. `func tagsFilter(alias string, tags []string) (string, []any)` (any-of `EXISTS` clause, empty for no tags). `SendInput.Tags []string`, `EditInput.Tags []string` (nil keeps the current revision's tags; empty non-nil clears), `SearchInput.Tags []string`, `History(as, channel string, before, after *int64, count int, tags ...string)` (variadic keeps the 13 existing call sites unchanged).
- Storage: `message_tags(seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE, tag TEXT NOT NULL, PRIMARY KEY(seq, tag))` plus index on `tag`. Purge and eviction `DELETE FROM messages` cascade (foreign_keys=1 in the DSN, like `embeddings`). `Reset` (`admin.go:119`) must list `message_tags` if its table list is explicit; check with `rg -n 'range \[\]string' internal/bus/admin.go`.
- There is no CLI `send`; "MCP and CLI" in #5 reduces to MCP (`sendIn` embeds `bus.SendInput`, so `tags` is exposed with no server change beyond the description).

- [ ] **Step 1: Write the failing bus tests**

Create `internal/bus/tags_test.go`:

```go
package bus

import (
	"slices"
	"testing"
)

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{"Release", "bug", "release", "a_b-1"})
	if err != nil || !slices.Equal(got, []string{"a_b-1", "bug", "release"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := NormalizeTags(nil); err != nil || got != nil {
		t.Fatalf("no tags: %v %v", got, err)
	}
	for _, bad := range [][]string{{""}, {"has space"}, {"x/y"}, {"ünïcode"}, {"123456789012345678901"}} {
		if _, err := NormalizeTags(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "t" + string(rune('a'+i))
	}
	wantCode(t, func() error { _, err := NormalizeTags(eleven); return err }(), "validation")
	if _, err := NormalizeTags(append(eleven[:10], "TA")); err != nil {
		t.Fatalf("duplicates are removed before the limit applies: %v", err)
	}
}

func TestSendStoresTagsAndHistorySearchFilter(t *testing.T) {
	b, sam, kim := setupTwo(t)
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "one", Tags: []string{"Release", "bug", "release"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "two", Tags: []string{"docs"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "three"}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, func() error { _, err := b.Send(sam, SendInput{Channel: "dev", Content: "x", Tags: []string{"ok", "not ok"}}); return err }(), "validation")
	wantCode(t, func() error { _, err := b.Send(sam, SendInput{Channel: "tasks", Content: "x", Tags: []string{"a"}}); return err }(), "validation")

	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r.Messages) != 3 || !slices.Equal(r.Messages[0].Tags, []string{"bug", "release"}) || r.Messages[2].Tags != nil {
		t.Fatalf("receive tags: %+v %v", r.Messages, err)
	}
	h, err := b.History(sam, "dev", nil, nil, 10, "docs", "release")
	if err != nil || len(h) != 2 || h[0].Content != "one" || h[1].Content != "two" {
		t.Fatalf("history any-of filter: %+v %v", h, err)
	}
	if h, _ := b.History(sam, "dev", nil, nil, 10); len(h) != 3 || !slices.Equal(h[1].Tags, []string{"docs"}) {
		t.Fatalf("history returns tags: %+v", h)
	}
	wantCode(t, func() error { _, err := b.History(sam, "dev", nil, nil, 10, "bad tag"); return err }(), "validation")
	s, err := b.Search(sam, SearchInput{Query: "one two three", Mode: "text"})
	if err != nil || len(s.Hits) != 0 {
		t.Fatalf("implicit AND: %+v %v", s.Hits, err)
	}
	s, err = b.Search(sam, SearchInput{Query: "two", Mode: "text", Tags: []string{"docs"}})
	if err != nil || len(s.Hits) != 1 || !slices.Equal(s.Hits[0].Tags, []string{"docs"}) {
		t.Fatalf("search tags filter: %+v %v", s.Hits, err)
	}
	if s, _ := b.Search(sam, SearchInput{Query: "two", Mode: "text", Tags: []string{"release"}}); len(s.Hits) != 0 {
		t.Fatalf("search filter excludes: %+v", s.Hits)
	}
}

func TestEditMemoryTagsKeepAndClear(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	r, err := b.Send(sam, SendInput{Channel: "memory", Content: "v1", Tags: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	id := *r.MemoryID
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); !slices.Equal(m.Tags, []string{"go"}) {
		t.Fatalf("omitted tags keep the current ones: %+v", m)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v3", Tags: []string{"sqlite", "GO"}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); !slices.Equal(m.Tags, []string{"go", "sqlite"}) {
		t.Fatalf("replace: %+v", m)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v4", Tags: []string{}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); m.Tags != nil {
		t.Fatalf("empty tags clear: %+v", m)
	}
	revs, _ := b.MemoryRevisions(sam, id)
	if len(revs) != 4 || !slices.Equal(revs[0].Tags, []string{"go"}) || !slices.Equal(revs[2].Tags, []string{"go", "sqlite"}) {
		t.Fatalf("tags belong to each revision: %+v", revs)
	}
	wantCode(t, func() error { _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v5", Tags: []string{"no way"}}); return err }(), "validation")
}

func TestSendTagsAreIdempotentAndCascadeOnDelete(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	a, err := b.Send(sam, SendInput{Channel: "general", Content: "k", Tags: []string{"B", "a"}, IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Send(sam, SendInput{Channel: "general", Content: "k", Tags: []string{"a", "b"}, IdempotencyKey: "k1"})
	if err != nil || c.Seq != a.Seq {
		t.Fatalf("normalized tags fingerprint the same: %+v %+v %v", a, c, err)
	}
	if _, err := b.db.Exec("DELETE FROM messages WHERE seq=?", a.Seq); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM message_tags WHERE seq=?", a.Seq).Scan(&n); err != nil || n != 0 {
		t.Fatalf("message_tags must cascade: %d %v", n, err)
	}
}
```

Append to `internal/bus/migrate_test.go`:

```go
// TestMigrateV2AddsMessageTags: a schema version 2 file opens, gains
// message_tags through the shared DDL, and is stamped 3.
func TestMigrateV2AddsMessageTags(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DROP TABLE message_tags"); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v2 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv, n int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv < 3 {
		t.Fatalf("user_version = %d, %v", uv, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='message_tags'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("message_tags missing: %d %v", n, err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/bus/ -run 'TestNormalizeTags|TestSendStoresTags|TestEditMemoryTags|TestSendTagsAre|TestMigrateV2' -v`
Expected: compile error (`NormalizeTags`, `Tags` fields undefined).

- [ ] **Step 3: Schema and migration**

`internal/bus/schema.go`: `const schemaVersion = 3`; after the `embeddings` table add:

```sql
CREATE TABLE IF NOT EXISTS message_tags (
  seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  PRIMARY KEY (seq, tag)
);
CREATE INDEX IF NOT EXISTS message_tags_tag ON message_tags(tag);
```

`internal/bus/migrate.go` line 18-20:

```go
var migrations = map[int]func(tx *sql.Tx) error{
	1: dropMessagesBytes,
	2: addTables, // message_tags (ADR 0009)
}

// addTables is the step for a version that only adds tables: the schema DDL
// (CREATE ... IF NOT EXISTS) runs right after migrate and creates them, so
// the step itself has nothing to do beyond stamping the version.
func addTables(*sql.Tx) error { return nil }
```

`internal/bus/admin.go:119` (`Reset`) iterates an explicit table list; add `"message_tags"` before `"messages"` (the cascade covers it, but keep the list honest).

- [ ] **Step 4: Tag helpers**

Create `internal/bus/tags.go`:

```go
package bus

import (
	"database/sql"
	"regexp"
	"slices"
	"strings"
)

// maxTags is the per-message (and per-subscription) tag ceiling (ADR 0009).
const maxTags = 10

var tagRe = regexp.MustCompile(`^[a-z0-9_-]{1,20}$`)

// NormalizeTags lowercases tags, rejects any that fail the tag rule (one bad
// tag fails the whole call), drops duplicates, sorts, and caps the result at
// maxTags. nil in, nil out. Exported for repoconfig's persistent tag sets.
func NormalizeTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if !tagRe.MatchString(t) {
			return nil, errf("validation", false, "tag %q must be 1-20 characters of a-z, 0-9, _ or -", t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > maxTags {
		return nil, errf("validation", false, "at most %d tags per message", maxTags)
	}
	slices.Sort(out)
	return out, nil
}

// insertTags stores a message's (already normalized) tags.
func insertTags(tx *sql.Tx, seq int64, tags []string) error {
	for _, t := range tags {
		if _, err := tx.Exec("INSERT INTO message_tags(seq, tag) VALUES(?,?)", seq, t); err != nil {
			return err
		}
	}
	return nil
}

// tagsOf reads a stored message's tags, sorted; nil when it has none.
func tagsOf(q querier, seq int64) ([]string, error) {
	rows, err := q.Query("SELECT tag FROM message_tags WHERE seq=? ORDER BY tag", seq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// tagsFilter is the any-of tags clause for History and search: messages
// carrying at least one of tags. Empty tags adds nothing.
func tagsFilter(alias string, tags []string) (string, []any) {
	if len(tags) == 0 {
		return "", nil
	}
	args := make([]any, len(tags))
	for i, t := range tags {
		args[i] = t
	}
	return " AND EXISTS (SELECT 1 FROM message_tags t WHERE t.seq=" + alias + ".seq AND t.tag IN (" + strings.Repeat("?,", len(tags)-1) + "?))", args
}
```

`querier` is defined in `receive.go:562`; `*sql.Tx` and `*sql.DB` satisfy it.

- [ ] **Step 5: Messages: columns, scan, insert, send, history**

In `internal/bus/messages.go`:

`SendInput` (line 14-22): add `Tags []string \`json:"tags,omitempty"\`` after `Refs`.

Replace line 44 (`messageColumns`) with:

```go
// messageCols lists the message columns qualified by alias, plus the
// message's tags as one space-separated string. message_tags rows are keyed
// (seq, tag), so the ordered subquery walks the primary key and the string
// comes back sorted; scanMessages splits it on spaces.
func messageCols(alias string) string {
	cols := strings.Split("seq, channel, sender, context, created_at, type, content, reply_to, metadata, refs, memory_id, revision", ", ")
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ") + ", (SELECT group_concat(tag, ' ') FROM (SELECT tag FROM message_tags WHERE seq=" + alias + ".seq ORDER BY tag))"
}
```

(add `"strings"` to imports). `scanMessages` (line 62-77): scan a trailing `var tags *string` and, after `decodeJSONFields`, `if tags != nil { m.Tags = strings.Fields(*tags) }`.

`envelopeUpperBound` (line 153-177): add `Tags: in.Tags,` to the `Message` literal.

`insertMessage` (line 181-212): before `return seq, nil`, add:

```go
	if err := insertTags(tx, seq, in.Tags); err != nil {
		return 0, err
	}
```

`Send` (line 268): right after `validateSendShape` and the task-channel check, before `key := in.IdempotencyKey`:

```go
	// Normalize before the receipt lookup so a keyed retry with the same
	// tags in another case or order fingerprints identically.
	var err error
	if in.Tags, err = NormalizeTags(in.Tags); err != nil {
		return SendResult{}, err
	}
```

(then change the later `kind, err := b.validateSendRefs(...)` to `kind, err = ...`).

`History` (line 382): signature `func (b *Bus) History(as, channel string, before, after *int64, count int, tags ...string) ([]Message, error)`; after the count clamp:

```go
	tags, err := NormalizeTags(tags)
	if err != nil {
		return nil, err
	}
	q := "SELECT " + messageCols("messages") + " FROM messages WHERE channel=? AND tombstone=0"
	args := []any{channel}
	if f, a := tagsFilter("messages", tags); f != "" {
		q += f
		args = append(args, a...)
	}
```

(the `rows, err :=` below becomes `rows, err =`).

Replace the other `messageColumns` uses: `memories.go:29,52` → `messageCols("messages")`; `receive.go:419,433` and `wait.go:83` → `messageCols("messages")`. In `search.go` delete `qualifiedColumns` (line 48-56) and use `messageCols("m")` at lines 112 and 276; in `textSearch`'s `rows.Scan` (line 125) add `var tags *string` scanned before `&rank`, then `if tags != nil { h.Tags = strings.Fields(*tags) }` after `decodeJSONFields`.

- [ ] **Step 6: Memories and search inputs**

`internal/bus/memories.go` `EditInput` (line 9-16): add `Tags []string \`json:"tags,omitempty"\`` after `Refs`, with the comment `// nil keeps the current revision's tags; an empty list clears them`. In `EditMemory`:
- after `validateRefs` (line 99-101):

```go
	if in.Tags != nil {
		var err error
		if in.Tags, err = NormalizeTags(in.Tags); err != nil {
			return EditResult{}, err
		}
		if in.Tags == nil {
			in.Tags = []string{} // stays non-nil: "clear", not "keep"
		}
	}
```

- after the pre-transaction `liveRevision` (line 131-137) resolve tags for the preflight envelope: `liveSeq, _, channel, err := liveRevision(b.db, in.ID)` and

```go
	tags := in.Tags
	if tags == nil {
		if tags, err = tagsOf(b.db, liveSeq); err != nil {
			return EditResult{}, internal(err)
		}
	}
	send := SendInput{Channel: channel, Content: in.Content, Type: in.Type, Metadata: in.Metadata, Refs: in.Refs, Tags: tags}
```

- inside the transaction after the authoritative `liveRevision(tx, in.ID)` (line 182-185): re-resolve the same way from `curSeq` with `tagsOf(tx, curSeq)` and assign `send.Tags` before `sendEnvelope(tx, ...)`.

`internal/bus/search.go` `SearchInput`: add `Tags []string \`json:"tags,omitempty"\``. In `Search` after the cursor parse: `if in.Tags, err = NormalizeTags(in.Tags); err != nil { return SearchResult{}, err }` (declare `var err error` above the `switch` or reorder). In `searchFilters` append `tagsFilter("m", in.Tags)` output after the `Thread` clause.

- [ ] **Step 7: Run the bus tests**

Run: `go test ./internal/bus/`
Expected: PASS.

- [ ] **Step 8: MCP surface and test**

`internal/mcpserver/server.go`: `historyIn` (line 66-72) add `Tags []string \`json:"tags,omitempty" jsonschema:"only messages carrying at least one of these tags"\`` and pass `in.Tags...` at line 369. Descriptions:
- `send` (line 359): append ` tags (up to 10, letters, digits, _ and -, stored lowercase) label the message; other agents can subscribe to tags and filter history and search by them.`
- `history` (line 367): append ` tags filters to messages carrying any of the given tags.`
- `search` (line 371): in the Filters list add `tags (any of)`.
- `edit_memory` (line 379): append ` tags replaces the memory's tags; omit it to keep them.`
- `get_memory`/`receive`: no change (tags ride on the message).

Append to `internal/mcpserver/server_test.go`:

```go
func TestSendTagsRoundTripOverMCP(t *testing.T) {
	cs := testSession(t)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	call(t, cs, "send", map[string]any{"as": "Sam", "channel": "general", "content": "tagged", "tags": []string{"Release", "bug"}})
	call(t, cs, "send", map[string]any{"as": "Sam", "channel": "general", "content": "plain"})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "history", Arguments: map[string]any{"as": "Sam", "channel": "general", "tags": []string{"bug"}}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `"tags":["bug","release"]`) || strings.Contains(text, "plain") {
		t.Fatalf("history tags filter and tags field: %s", text)
	}
	_, bad := call(t, cs, "send", map[string]any{"as": "Sam", "channel": "general", "content": "x", "tags": []string{"no spaces"}})
	if !bad.IsError {
		t.Fatal("invalid tag must be rejected")
	}
}
```

Run: `go test ./internal/mcpserver/` — Expected: PASS.

- [ ] **Step 9: Docs**

`README.md` tool bullets (line 42-52): `send` becomes "post a message, or, on a memory channel, create a memory; `tags` (up to 10 short lowercase labels) let others follow it across channels"; `search` gains "; `tags` narrows either to messages carrying any of them (so does `history`)".

`internal/cli/skills/using-agentbus/SKILL.md`: insert before `## Session protocol`:

```markdown
## Tags

`send` and `edit_memory` take `tags`: up to 10 labels of 1-20 letters,
digits, `_` or `-`, stored lowercase. Tag a message when agents outside
its channel should be able to follow it by topic (`release`, `schema`,
`bug`, a feature name). `history` and `search` take `tags` to return only
messages carrying any of them; a memory's tags belong to the revision, so
omit `tags` on `edit_memory` to keep them and pass `[]` to clear them.
Task lists do not take tags.
```

- [ ] **Step 10: Full check, commit**

```bash
gofmt -l . && golangci-lint run ./... && go build ./... && go test ./...
git add internal/bus/schema.go internal/bus/migrate.go internal/bus/tags.go internal/bus/messages.go internal/bus/memories.go internal/bus/search.go internal/bus/receive.go internal/bus/wait.go internal/bus/admin.go internal/bus/tags_test.go internal/bus/migrate_test.go internal/mcpserver/server.go internal/mcpserver/server_test.go README.md internal/cli/skills/using-agentbus/SKILL.md
git commit -m "feat: message tags (#5)

send and edit_memory take tags (1-20 chars of a-z 0-9 _ -, lowercased,
deduplicated, at most 10, one bad tag rejects the call); tags belong to
each memory revision. Stored in message_tags (schema 3) and returned on
every message from receive, history, search, and get_memory; history
and search take an any-of tags filter. ADR 0009."
```

---

### Task 6: Tag subscriptions (#6)

**Files:**
- Modify: `internal/bus/schema.go` (`schemaVersion = 4`, `tag_subscriptions`), `internal/bus/migrate.go` (`migrations[3]`)
- Modify: `internal/bus/tags.go` (subscriptions, sets, `tagCond`, `matchedTags`)
- Modify: `internal/bus/messages.go:29-42` (`Message.MatchedTags`)
- Modify: `internal/bus/receive.go:150-155,244-383,400-541` (`subRow.tags`, sweeps, sources, attribution)
- Modify: `internal/bus/wait.go:51-91` (tag source in `peek`)
- Modify: `internal/bus/sessions.go:12-29,155-171,197-213` (`Registration.TagSubscriptions`, resume=false, pending listing)
- Modify: `internal/bus/channels.go:297` (orphan sweep excludes the pseudo row)
- Modify: `internal/repoconfig/repoconfig.go` (`TagSubscriptions`, `AddTagSet`, `RemoveTagSet`)
- Modify: `internal/mcpserver/server.go:47-57,156-209,323-358` (inputs, `applyPersistent`, handlers, descriptions)
- Modify: `internal/cli/subscribe.go`, `internal/cli/wait.go:39-49`, `main.go:177-196`
- Create: `internal/tui/tags.go` (tag pane helpers, `t` prompt)
- Modify: `internal/tui/model.go:876,900-945,952-958` (`onStatus`, `setChannels`, `statusCmd`, key guards, `showSelected`), `internal/tui/view.go` (rail section, compose label, header), `internal/tui/thread.go` (`paneMsgs`), `internal/tui/compose.go:109-127` (`toggleSubscribe`), `internal/tui/msgs.go:12-15` (`statusMsg.tags`), `internal/tui/help.go`
- Modify: `README.md`, `docs/install.md` (TUI keys), `internal/cli/skills/using-agentbus/SKILL.md`
- Test: `internal/bus/tags_test.go`, `internal/bus/migrate_test.go`, `internal/repoconfig/repoconfig_test.go`, `internal/mcpserver/server_test.go`, `internal/cli/wait_test.go`, `internal/cli/subscribe_test.go`, `internal/tui/tags_test.go` (new)

**Interfaces:**
- Consumes: `NormalizeTags`, `messageCols`, `Message.Tags` (Task 5); `paneMsgs`, `renderStream` (Tasks 3-4).
- Produces (bus): `const tagSource = "tags/"` (the pseudo-subscription channel; no real channel can be named this: `ChannelNameRule` rejects the trailing `/`, `dmOwner` and `IsTaskChannel` do not match it); `type tagSet struct{ tags []string; createdSeq int64 }`; `func (b *Bus) SubscribeTags(as string, tags []string) error`; `func (b *Bus) UnsubscribeTags(as string, tags []string) error`; `func (b *Bus) TagSubscriptions(as string) ([][]string, error)`; `func (b *Bus) tagSets(q querier, as string) ([]tagSet, error)`; `func tagCond(as string, sets []tagSet) (string, []any)`; `func matchedTags(sets []tagSet, m Message) []string`; `Message.MatchedTags []string \`json:"matched_tags,omitempty"\``; `Registration.TagSubscriptions [][]string \`json:"tag_subscriptions,omitempty"\``.
- Produces (repoconfig): `func (f *File) TagSubscriptions() (sets [][]string, bad []string)` reading key `tag_subscriptions` (`[["release"],["agentbus","bug"]]`), `func (f *File) AddTagSet(tags []string) ([][]string, error)`, `func (f *File) RemoveTagSet(tags []string) ([][]string, error)`.
- Produces (cli): `func SubscribeTags(cwd string, tags []string, out io.Writer) error`, `func UnsubscribeTags(cwd string, tags []string, out io.Writer) error`; `agentbus subscribe -tags a,b` / `agentbus unsubscribe -tags a,b`.
- Produces (tui): `const tagPanePrefix = "tags:"`; synthetic `bus.Channel{Name: "tags:" + strings.Join(set, ","), Kind: "tags"}` entries appended to `m.channels` under a "tags" rail section; `t` key prompt "tags <tag>[,<tag>...]" subscribes; `s` on a tag row unsubscribes. A tag pane is read-only; its messages are the loaded messages of every ordinary (chat) channel in the rail that carry every tag of the set. The TUI identity is directly subscribed to every channel, so the bus's tag source delivers it nothing; the view is computed from `m.msgs`.
- Semantics fixed here: the `tags/` row is created (cursor at head) by the first `SubscribeTags`, deleted by the last `UnsubscribeTags`, and dropped together with the sets when it idle-expires (receive `Expired` lists `tags/`) or on `resume=false` (`register` then re-applies `.local/agentbus.json`, so the file's sets come back future-only, like channels). No gaps are reported for tag deliveries. The `receive` `channels` filter treats `tags/` like a channel name.

- [ ] **Step 1: Write the failing bus tests**

Append to `internal/bus/tags_test.go`:

```go
// tagSetup: Sam posts, Kim follows tags only (no channel subscription
// besides the inbox register mints).
func tagSetup(t *testing.T) (*Bus, string, string) {
	t.Helper()
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	kim := reg(t, b, "Kim")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	return b, sam, kim
}

func sendTagged(t *testing.T, b *Bus, as, ch, content string, tags ...string) SendResult {
	t.Helper()
	r, err := b.Send(as, SendInput{Channel: ch, Content: content, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTagSubscriptionAndMatching(t *testing.T) {
	b, sam, kim := tagSetup(t)
	wantCode(t, b.SubscribeTags(kim, nil), "validation")
	wantCode(t, b.SubscribeTags(kim, []string{"bad tag"}), "validation")
	sendTagged(t, b, sam, "dev", "before", "release")
	if err := b.SubscribeTags(kim, []string{"Release"}); err != nil {
		t.Fatal(err)
	}
	if err := b.SubscribeTags(kim, []string{"bug", "agentbus"}); err != nil {
		t.Fatal(err)
	}
	if sets, _ := b.TagSubscriptions(kim); len(sets) != 2 || sets[0][0] != "agentbus" || sets[1][0] != "release" {
		t.Fatalf("sets: %v", sets)
	}
	sendTagged(t, b, sam, "dev", "one tag only", "bug")
	sendTagged(t, b, sam, "dev", "and set", "agentbus", "bug", "extra")
	sendTagged(t, b, sam, "general", "both sets", "release", "bug", "agentbus")
	sendTagged(t, b, sam, "dev", "untagged")
	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r.Messages) != 2 {
		t.Fatalf("AND sets, created_seq, once per message: %+v %v", r.Messages, err)
	}
	if !slices.Equal(r.Messages[0].MatchedTags, []string{"agentbus", "bug"}) || !slices.Equal(r.Messages[1].MatchedTags, []string{"agentbus", "bug", "release"}) {
		t.Fatalf("matched_tags: %+v", r.Messages)
	}
	// Ack advances the shared tag cursor; an unacked batch redelivers.
	r2, _ := b.Receive(kim, ReceiveInput{})
	if !r2.Redelivered || len(r2.Messages) != 2 {
		t.Fatalf("redeliver: %+v", r2)
	}
	sendTagged(t, b, sam, "dev", "later", "release")
	r3, _ := b.Receive(kim, ReceiveInput{Ack: r.Batch})
	if len(r3.Messages) != 1 || r3.Messages[0].Content != "later" || r3.Redelivered {
		t.Fatalf("ack advances: %+v", r3)
	}
	if err := b.UnsubscribeTags(kim, []string{"release"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "gone", "release")
	if r4, _ := b.Receive(kim, ReceiveInput{Ack: r3.Batch}); len(r4.Messages) != 0 {
		t.Fatalf("unsubscribed set no longer matches: %+v", r4.Messages)
	}
	_ = b.UnsubscribeTags(kim, []string{"bug", "agentbus"})
	var n int
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=? AND channel=?", kim, tagSource).Scan(&n)
	if n != 0 {
		t.Fatal("last unsubscribe drops the tags/ row")
	}
}

func TestTagSourceExcludesDirectDMTaskAndMemory(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if _, err := b.CreateChannel(sam, "memory/x", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(kim, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "direct", "t")
	sendTagged(t, b, sam, "dm/Kim", "dm", "t")
	sendTagged(t, b, sam, "dm/Sam", "other dm", "t")
	sendTagged(t, b, sam, "memory", "mem", "t")
	sendTagged(t, b, sam, "memory/x", "mem2", "t")
	sendTagged(t, b, sam, "general", "via tag", "t")
	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range r.Messages {
		got = append(got, m.Content)
		if m.Channel == "dev" && m.MatchedTags != nil {
			t.Fatalf("a directly subscribed channel is not a tag delivery: %+v", m)
		}
	}
	if !slices.Equal(got, []string{"direct", "dm", "via tag"}) {
		t.Fatalf("delivered %v", got)
	}
}

func TestTagSubscriptionsResumeFalseAndExpiry(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "old", "t")
	if _, err := b.Register("Kim", "", "r", false); err != nil {
		t.Fatal(err)
	}
	if sets, _ := b.TagSubscriptions(kim); len(sets) != 0 {
		t.Fatalf("resume=false drops tag sets: %v", sets)
	}
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "new", "t")
	if r, _ := b.Receive(kim, ReceiveInput{}); len(r.Messages) != 1 || r.Messages[0].Content != "new" {
		t.Fatalf("future-only after resume=false: %+v", r.Messages)
	}
	// Idle expiry drops the row and its sets and reports tags/.
	idle := int64(b.cfg.CursorIdleHours)*3_600_000 + 1
	if _, err := b.db.Exec("UPDATE subscriptions SET last_activity=last_activity-? WHERE sender=? AND channel=?", idle, kim, tagSource); err != nil {
		t.Fatal(err)
	}
	r, _ := b.Receive(kim, ReceiveInput{})
	if !slices.Contains(r.Expired, tagSource) {
		t.Fatalf("expired must name tags/: %+v", r)
	}
	if sets, _ := b.TagSubscriptions(kim); len(sets) != 0 {
		t.Fatalf("expiry drops the sets: %v", sets)
	}
}

func TestTagRowSurvivesSweeps(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	if err := b.reapEmptyChannels(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Receive(kim, ReceiveInput{}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=? AND channel=?", kim, tagSource).Scan(&n); err != nil || n != 1 {
		t.Fatalf("tags/ row swept: %d %v", n, err)
	}
	// general, not dev: dev is empty with no live subscriber, so the reap
	// above legitimately dropped it.
	sendTagged(t, b, sam, "general", "still", "t")
	if r, _ := b.Receive(kim, ReceiveInput{}); len(r.Messages) != 1 {
		t.Fatalf("still delivering: %+v", r.Messages)
	}
	reg, err := b.Register("Kim", "", "r", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range reg.Pending {
		if p.Channel == tagSource {
			t.Fatalf("pending must not list the pseudo row: %+v", reg.Pending)
		}
	}
}

func TestWaitWakesOnTagMatch(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "no mention", "t")
	msgs, err := b.Wait(kim, nil, false, nil, time.Second)
	if err != nil || len(msgs) != 1 || !slices.Equal(msgs[0].MatchedTags, []string{"t"}) {
		t.Fatalf("wait peeks the tag source with matched_tags: %+v %v", msgs, err)
	}
}
```

Add `"time"` to the imports. Append to `internal/bus/migrate_test.go` a `TestMigrateV3AddsTagSubscriptions` mirroring `TestMigrateV2AddsMessageTags` with `PRAGMA user_version = 3`, `DROP TABLE tag_subscriptions`, and `uv < 4`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/bus/ -run 'TestTag|TestWaitWakesOnTag|TestMigrateV3' -v`
Expected: compile error (`tagSource`, `SubscribeTags` undefined).

- [ ] **Step 3: Schema, migration, tag subscription helpers**

`schema.go`: `schemaVersion = 4`; after `message_tags_tag`:

```sql
CREATE TABLE IF NOT EXISTS tag_subscriptions (
  sender TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  created_seq INTEGER NOT NULL,
  PRIMARY KEY (sender, tags_key)
);
```

`migrate.go`: `3: addTables, // tag_subscriptions (ADR 0009)`.

Append to `internal/bus/tags.go`:

```go
// tagSource is the pseudo-subscription row that carries the cursor, pending
// batch, and idle expiry shared by all of a sender's tag sets (ADR 0009
// item 5). No channel can bear this name: ChannelNameRule rejects the
// trailing slash, and it is neither a dm/ inbox nor a task list.
const tagSource = "tags/"

// tagSet is one AND set: a message matches when it carries every tag and
// was written after the set was added.
type tagSet struct {
	tags       []string
	createdSeq int64
}

func tagsKey(tags []string) string { return strings.Join(tags, ",") }

// SubscribeTags adds an AND set of 1-10 tags. The first set also creates the
// shared tags/ row at the current head, so nothing before it is delivered
// (like Subscribe from now); a set is idempotent.
func (b *Bus) SubscribeTags(as string, tags []string) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	tags, err := NormalizeTags(tags)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return errf("validation", false, "tags must name at least one tag")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.auth(tx, as); err != nil {
		return err
	}
	var head int64
	if err := tx.QueryRow("SELECT coalesce(max(seq),0) FROM messages").Scan(&head); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO tag_subscriptions(sender,tags_key,created_seq) VALUES(?,?,?)", as, tagsKey(tags), head); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,?,?)", as, tagSource, head, b.nowMs()); err != nil {
		return internal(err)
	}
	return internal(tx.Commit())
}

// UnsubscribeTags removes one set; removing the last set drops the tags/
// row too, so an idle identity with no sets holds no cursor.
func (b *Bus) UnsubscribeTags(as string, tags []string) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	tags, err := NormalizeTags(tags)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return errf("validation", false, "tags must name at least one tag")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.auth(tx, as); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM tag_subscriptions WHERE sender=? AND tags_key=?", as, tagsKey(tags)); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=? AND channel=? AND NOT EXISTS (SELECT 1 FROM tag_subscriptions WHERE sender=?)", as, tagSource, as); err != nil {
		return internal(err)
	}
	return internal(tx.Commit())
}

// TagSubscriptions lists as's sets, each sorted, in key order.
func (b *Bus) TagSubscriptions(as string) ([][]string, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	sets, err := b.tagSets(b.db, as)
	if err != nil {
		return nil, err
	}
	out := make([][]string, len(sets))
	for i, s := range sets {
		out[i] = s.tags
	}
	return out, nil
}

func (b *Bus) tagSets(q querier, as string) ([]tagSet, error) {
	rows, err := q.Query("SELECT tags_key, created_seq FROM tag_subscriptions WHERE sender=? ORDER BY tags_key", as)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []tagSet
	for rows.Next() {
		var key string
		var s tagSet
		if err := rows.Scan(&key, &s.createdSeq); err != nil {
			return nil, internal(err)
		}
		s.tags = strings.Split(key, ",")
		out = append(out, s)
	}
	return out, internal(rows.Err())
}

// tagCond is the tag source's predicate over messages: ordinary chat
// channels only (dm/ inboxes are ordinary-kind and excluded by name;
// memory channels and task lists are memory-kind), skipping channels the
// sender is directly subscribed to (those arrive through the channel), and
// carrying every tag of at least one set added before the message.
func tagCond(as string, sets []tagSet) (string, []any) {
	args := []any{as}
	ors := make([]string, len(sets))
	for i, s := range sets {
		ors[i] = "(messages.seq>? AND (SELECT count(*) FROM message_tags t WHERE t.seq=messages.seq AND t.tag IN (" + strings.Repeat("?,", len(s.tags)-1) + "?))=?)"
		args = append(args, s.createdSeq)
		for _, t := range s.tags {
			args = append(args, t)
		}
		args = append(args, len(s.tags))
	}
	return "messages.channel IN (SELECT name FROM channels WHERE kind='ordinary' AND name NOT LIKE 'dm/%') AND messages.channel NOT IN (SELECT channel FROM subscriptions WHERE sender=?) AND (" + strings.Join(ors, " OR ") + ")", args
}

// matchedTags is the union of the sets m satisfies, sorted.
func matchedTags(sets []tagSet, m Message) []string {
	var out []string
	for _, s := range sets {
		if m.Seq <= s.createdSeq {
			continue
		}
		all := true
		for _, t := range s.tags {
			if !slices.Contains(m.Tags, t) {
				all = false
			}
		}
		if all {
			for _, t := range s.tags {
				if !slices.Contains(out, t) {
					out = append(out, t)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}
```

`internal/bus/messages.go` `Message`: add after `Tags`:

```go
	// MatchedTags is set on a message delivered through a tag subscription:
	// the tags of the sets it satisfied. Absent on channel deliveries.
	MatchedTags []string `json:"matched_tags,omitempty"`
```

- [ ] **Step 4: receiveOnce as subscription sources**

In `internal/bus/receive.go`:

`subRow` (line 150-155): add `tags bool // the tags/ pseudo row (tagSource)`.

In the load loop (line 261-291), after `rows.Scan`: `s.tags = s.channel == tagSource`.

Expiry deletion (line 298-302) becomes:

```go
	for _, ch := range res.Expired {
		if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=? AND channel=?", as, ch); err != nil {
			return res, internal(err)
		}
		// The tag sets ride the tags/ row's cursor; without it they would
		// never deliver again, so they go with it (the agent sees tags/ in
		// expired and resubscribes).
		if ch == tagSource {
			if _, err := tx.Exec("DELETE FROM tag_subscriptions WHERE sender=?", as); err != nil {
				return res, internal(err)
			}
		}
	}
```

Orphan sweep (line 335-349): at the top of the loop body add `if s.tags { kept = append(kept, s); continue }`. Gap loop (line 353-383): first line `if subs[i].tags { continue }` (no gaps are reported for tag deliveries).

After `subs = kept` and before the notice read, load the sets:

```go
	// The tag source's sets. A tags/ row with no sets left (a partial
	// cleanup) is inert this round.
	var sets []tagSet
	if slices.ContainsFunc(subs, func(s subRow) bool { return s.tags }) {
		if sets, err = b.tagSets(tx, as); err != nil {
			return res, err
		}
		if len(sets) == 0 {
			subs = slices.DeleteFunc(subs, func(s subRow) bool { return s.tags })
		}
	}
	// srcOf attributes a delivered message to its source row: its channel
	// when directly subscribed, else the tags/ row. Pending/ack bookkeeping
	// and matched_tags both key on it.
	direct := map[string]bool{}
	for _, s := range subs {
		if !s.tags {
			direct[s.channel] = true
		}
	}
	srcOf := func(m Message) string {
		if direct[m.Channel] {
			return m.Channel
		}
		return tagSource
	}
	attribute := func(msgs []Message) {
		for i := range msgs {
			if srcOf(msgs[i]) == tagSource {
				msgs[i].MatchedTags = matchedTags(sets, msgs[i])
			}
		}
	}
```

(add `"slices"` to imports). Replace `buildBounded` and `buildNew` (line 412-439) with:

```go
	// srcCond is one source's clause: a channel row selects its channel
	// past its cursor; the tag row selects tag matches (tagCond) past the
	// shared cursor. bounded adds the pending batch's end.
	srcCond := func(s subRow, bounded bool) (string, []any) {
		q, a := "(channel=? AND seq>?", []any{s.channel, s.cursor}
		if s.tags {
			tq, ta := tagCond(as, sets)
			q, a = "(messages.seq>? AND "+tq, append([]any{s.cursor}, ta...)
		}
		if bounded {
			return q + " AND seq<=?)", append(a, s.pendingEnd)
		}
		return q + ")", a
	}
	build := func(set []subRow, bounded bool, own string, limit bool) (string, []any) {
		var conds []string
		var a []any
		for _, s := range set {
			c, ca := srcCond(s, bounded)
			conds = append(conds, c)
			a = append(a, ca...)
		}
		q := "SELECT " + messageCols("messages") + " FROM messages WHERE (" + strings.Join(conds, " OR ") + ") AND tombstone=0" + own + " ORDER BY seq"
		if own != "" {
			a = append(a, as)
		}
		if limit {
			q += " LIMIT ?"
			a = append(a, in.Count)
		}
		return q, a
	}
	ownNew := ownClause(in.IncludeOwn)
```

Keep the two comments that explained `buildBounded` ("reproduces the same batch") and `buildNew` above `build`. Then: line 478 `q, a := build(pending, true, ownClause(tokenIncludesOwn(batch)), false)`; line 519 `q, a := build(inScope, false, ownNew, true)`. After each `res.Messages = trimToBytes(...)` call `attribute(res.Messages)`. In the redelivery re-stamp (line 493-496) key `shown[srcOf(m)] = m.Seq`; in the new-selection branch (line 531-534) `ends[srcOf(m)] = m.Seq`.

- [ ] **Step 5: wait, register, orphan sweep**

`internal/bus/wait.go`: replace `peek` (line 51-91) with (the subscription rows are read into a slice first so `tagSets` can query while no cursor is open):

```go
// peek selects, without side effects, the messages Receive would deliver
// next for as: everything past each subscription's cursor, the tag source
// (receive's tags/ row) included. skipBelow raises the floor per channel
// for messages a caller has already rejected.
func (b *Bus) peek(as string, channels []string, includeOwn bool, skipBelow map[string]int64) ([]Message, error) {
	rows, err := b.db.Query("SELECT channel, cursor_seq FROM subscriptions WHERE sender=? ORDER BY channel", as)
	if err != nil {
		return nil, internal(err)
	}
	type sub struct {
		channel string
		cursor  int64
	}
	var subs []sub
	for rows.Next() {
		var s sub
		if err := rows.Scan(&s.channel, &s.cursor); err != nil {
			_ = rows.Close()
			return nil, internal(err)
		}
		// Same guard as receive's delivery loop: a leftover observer-only row
		// (or a CLI `agentbus wait` process, never an observer) must not peek
		// another identity's inbox.
		if !b.dmReadable(as, s.channel) {
			continue
		}
		if len(channels) > 0 && !contains(channels, s.channel) {
			continue
		}
		subs = append(subs, s)
	}
	if err := rows.Close(); err != nil {
		return nil, internal(err)
	}
	var conds []string
	var args []any
	var sets []tagSet
	direct := map[string]bool{}
	for _, s := range subs {
		floor := max(s.cursor, skipBelow[s.channel])
		if s.channel == tagSource {
			if sets, err = b.tagSets(b.db, as); err != nil {
				return nil, err
			}
			if len(sets) == 0 {
				continue
			}
			tq, ta := tagCond(as, sets)
			conds = append(conds, "(messages.seq>? AND "+tq+")")
			args = append(args, append([]any{floor}, ta...)...)
			continue
		}
		direct[s.channel] = true
		conds = append(conds, "(channel=? AND seq>?)")
		args = append(args, s.channel, floor)
	}
	if len(conds) == 0 {
		return nil, errf("not_found", false, "%q has no matching subscriptions; register first", as)
	}
	q := "SELECT " + messageCols("messages") + " FROM messages WHERE (" + strings.Join(conds, " OR ") + ") AND tombstone=0"
	if !includeOwn {
		q += " AND sender<>?"
		args = append(args, as)
	}
	q += " ORDER BY seq LIMIT ?"
	args = append(args, b.cfg.ReceiveMaxCount)
	msgs, err := queryMessages(b.db, q, args)
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		if !direct[msgs[i].Channel] {
			msgs[i].MatchedTags = matchedTags(sets, msgs[i])
		}
	}
	return msgs, nil
}
```

`internal/bus/sessions.go`:
- `Registration` (line 12-29): add `TagSubscriptions [][]string \`json:"tag_subscriptions,omitempty"\`` with the comment `// TagSubscriptions reports the persistent tag sets register applied (.local/agentbus.json).`
- `!resume` branch (line 155-161): add `if _, err := tx.Exec("DELETE FROM tag_subscriptions WHERE sender=?", display); err != nil { return Registration{}, internal(err) }`.
- after the resume-branch idle sweep (line 163-171) add, outside the if/else: `if _, err := tx.Exec("DELETE FROM tag_subscriptions WHERE sender=? AND NOT EXISTS (SELECT 1 FROM subscriptions WHERE sender=? AND channel=?)", display, display, tagSource); err != nil { return Registration{}, internal(err) }` with the comment `// Sets without their tags/ row (idle-swept above) are dead; drop them so a persistent re-apply starts them fresh.`
- pending loop (line 205-207): `if !b.dmReadable(display, p.Channel) || p.Channel == tagSource { continue }`.

`internal/bus/channels.go:297`: `DELETE FROM subscriptions WHERE channel NOT IN (SELECT name FROM channels) AND channel<>?` with `tagSource` as the argument, and a comment: `// The tags/ pseudo row (tag subscriptions) has no channel behind it on purpose.`

`internal/bus/admin.go:119` (`Reset`): add `"tag_subscriptions"` to the table list.

Run: `go test ./internal/bus/` — Expected: PASS.

- [ ] **Step 6: repoconfig, MCP, CLI**

`internal/repoconfig/repoconfig.go` append:

```go
// TagSubscriptions returns the persistent tag sets under "tag_subscriptions"
// (a list of tag lists), each normalized; entries that are not a list of
// valid tags are returned in bad and omitted. Absent key: none.
func (f *File) TagSubscriptions() (sets [][]string, bad []string) {
	list, _ := f.Raw["tag_subscriptions"].([]any)
	for _, e := range list {
		raw, ok := e.([]any)
		tags := make([]string, 0, len(raw))
		for _, v := range raw {
			s, isStr := v.(string)
			ok = ok && isStr
			tags = append(tags, s)
		}
		norm, err := bus.NormalizeTags(tags)
		if !ok || err != nil || len(norm) == 0 {
			bad = append(bad, fmt.Sprint(e))
			continue
		}
		sets = append(sets, norm)
	}
	return sets, bad
}

// AddTagSet appends a normalized set (idempotent), writes, and returns the list.
func (f *File) AddTagSet(tags []string) ([][]string, error) {
	norm, err := bus.NormalizeTags(tags)
	if err != nil || len(norm) == 0 {
		return nil, fmt.Errorf("tags %q: must be 1-10 tags of 1-20 characters a-z, 0-9, _ or -", tags)
	}
	sets, _ := f.TagSubscriptions()
	if !slices.ContainsFunc(sets, func(s []string) bool { return slices.Equal(s, norm) }) {
		sets = append(sets, norm)
	}
	return sets, f.setTagSets(sets)
}

// RemoveTagSet removes a set (a no-op that still writes when absent).
func (f *File) RemoveTagSet(tags []string) ([][]string, error) {
	norm, err := bus.NormalizeTags(tags)
	if err != nil {
		return nil, err
	}
	sets, _ := f.TagSubscriptions()
	sets = slices.DeleteFunc(sets, func(s []string) bool { return slices.Equal(s, norm) })
	if sets == nil {
		sets = [][]string{}
	}
	return sets, f.setTagSets(sets)
}

func (f *File) setTagSets(sets [][]string) error {
	arr := make([]any, len(sets))
	for i, s := range sets {
		inner := make([]any, len(s))
		for j, t := range s {
			inner[j] = t
		}
		arr[i] = inner
	}
	f.Raw["tag_subscriptions"] = arr
	return f.write()
}
```

(add `"slices"`). Append to `internal/repoconfig/repoconfig_test.go`:

```go
func TestTagSubscriptionsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f, err := Create(dir, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if sets, bad := f.TagSubscriptions(); len(sets) != 0 || len(bad) != 0 {
		t.Fatalf("absent key: %v %v", sets, bad)
	}
	if _, err := f.AddTagSet([]string{"Bug", "agentbus"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.AddTagSet([]string{"agentbus", "bug"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.AddTagSet([]string{"bad tag"}); err == nil {
		t.Fatal("invalid tag must be rejected")
	}
	g, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sets, bad := g.TagSubscriptions()
	if len(sets) != 1 || sets[0][0] != "agentbus" || sets[0][1] != "bug" || len(bad) != 0 {
		t.Fatalf("%v %v", sets, bad)
	}
	g.Raw["tag_subscriptions"] = []any{[]any{"ok"}, "not a list", []any{"no spaces"}}
	if sets, bad := g.TagSubscriptions(); len(sets) != 1 || len(bad) != 2 {
		t.Fatalf("bad entries reported: %v %v", sets, bad)
	}
	if sets, err := f.RemoveTagSet([]string{"bug", "agentbus"}); err != nil || len(sets) != 0 {
		t.Fatalf("%v %v", sets, err)
	}
}
```

`internal/mcpserver/server.go`:
- `subscribeIn` and `unsubscribeIn`: `Channel string \`json:"channel,omitempty"\``, add `Tags []string \`json:"tags,omitempty" jsonschema:"instead of channel: 1-10 tags that must all be on a message for it to be delivered (an AND set); subscribe again with another set for OR"\``.
- Handlers: exactly one of channel/tags:

```go
			if (in.Channel == "") == (len(in.Tags) == 0) {
				return nil, nil, &bus.Error{Code: "validation", Message: "pass channel or tags, not both"}
			}
			if len(in.Tags) > 0 {
				if err := b.SubscribeTags(in.As, in.Tags); err != nil {
					return nil, nil, err
				}
				out := map[string]any{"subscribed_tags": in.Tags}
				if in.Persistent {
					f, err := persistFile(cwd)
					if err != nil {
						return nil, nil, persistErr(err)
					}
					if _, err := f.AddTagSet(in.Tags); err != nil {
						return nil, nil, persistErr(err)
					}
					out["persistent"] = true
				}
				return result(out, nil)
			}
```

(mirror for unsubscribe with `UnsubscribeTags`/`RemoveTagSet`/`"unsubscribed_tags"`). Check `bus.Error` is constructible this way (`rg -n 'type Error struct' internal/bus/`); it is used the same way in `persistErr`.
- `applyPersistent` (line 162-209): after the channel loop, when `f != nil`:

```go
	if f != nil {
		sets, bad := f.TagSubscriptions()
		for _, s := range bad {
			if reg.SubscribeFailed == nil {
				reg.SubscribeFailed = map[string]string{}
			}
			reg.SubscribeFailed["tags "+s] = "invalid tag set in " + f.Path
		}
		for _, s := range sets {
			if err := b.SubscribeTags(reg.Sender, s); err != nil {
				if reg.SubscribeFailed == nil {
					reg.SubscribeFailed = map[string]string{}
				}
				reg.SubscribeFailed["tags "+strings.Join(s, ",")] = err.Error()
				continue
			}
			reg.TagSubscriptions = append(reg.TagSubscriptions, s)
		}
	}
```

(the `f` variable must be in scope: it already is, declared at line 166; add `"strings"` import).
- Descriptions: `register` append ` Also applies the file's tag_subscriptions (sets of tags to follow across channels) and reports them in tag_subscriptions.`; `subscribe` append ` Or pass tags instead of channel: an AND set of 1-10 tags; matching messages from any chat channel you are not already subscribed to arrive through receive with matched_tags, starting from now.`; `unsubscribe` append ` Or pass tags to drop that tag set.`; `receive` append ` Messages delivered through a tag subscription carry matched_tags.`

Append to `internal/mcpserver/server_test.go`:

```go
func TestTagSubscriptionOverMCP(t *testing.T) {
	cs := testSession(t)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	call(t, cs, "register", map[string]any{"name": "Kim"})
	if out, res := call(t, cs, "subscribe", map[string]any{"as": "Kim", "tags": []string{"Release"}}); res.IsError || out["subscribed_tags"] == nil {
		t.Fatalf("%v %+v", out, res)
	}
	if _, res := call(t, cs, "subscribe", map[string]any{"as": "Kim", "channel": "general", "tags": []string{"x"}}); !res.IsError {
		t.Fatal("channel and tags together must be rejected")
	}
	call(t, cs, "send", map[string]any{"as": "Sam", "channel": "general", "content": "ship it", "tags": []string{"release"}})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "receive", Arguments: map[string]any{"as": "Kim"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"matched_tags":["release"]`) {
		t.Fatalf("receive: %s", text)
	}
	if _, res := call(t, cs, "unsubscribe", map[string]any{"as": "Kim", "tags": []string{"release"}}); res.IsError {
		t.Fatal("unsubscribe tags")
	}
}
```

`internal/cli/wait.go` line 48: `match = func(m bus.Message) bool { return m.Channel == inbox || len(m.MatchedTags) > 0 || re.MatchString(m.Content) }` and extend the doc comment: "A tag-subscription match is addressed to this identity too, so it wakes regardless of the filter." Append to `internal/cli/wait_test.go`:

```go
func TestWaitWakesOnTagMatchDespiteFilter(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	for _, n := range []string{"Pat", "Sam"} {
		if _, err := b.Register(n, "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.SubscribeTags("Pat", []string{"release"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("Sam", bus.SendInput{Channel: "general", Content: "cut v2", Tags: []string{"release"}}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Wait(WaitOptions{Config: cfg, As: "Pat", Filter: "@Pat", Timeout: 2 * time.Second}, &out); err != nil {
		t.Fatalf("a tag match must wake the waiter: %v", err)
	}
	if !strings.Contains(out.String(), `"matched_tags":["release"]`) {
		t.Fatalf("%q", out.String())
	}
}
```

`internal/cli/subscribe.go` append:

```go
// SubscribeTags adds a tag set to the persistent list in
// cwd/.local/agentbus.json ("tag_subscriptions"), creating the file when absent.
func SubscribeTags(cwd string, tags []string, out io.Writer) error {
	f, err := openRepoFile(cwd, true)
	if err != nil {
		return err
	}
	sets, err := f.AddTagSet(tags)
	if err != nil {
		return err
	}
	return printTagSets(out, f.Identity, sets)
}

// UnsubscribeTags removes a tag set from the persistent list.
func UnsubscribeTags(cwd string, tags []string, out io.Writer) error {
	f, err := openRepoFile(cwd, false)
	if err != nil {
		return err
	}
	sets, err := f.RemoveTagSet(tags)
	if err != nil {
		return err
	}
	return printTagSets(out, f.Identity, sets)
}

func printTagSets(out io.Writer, identity string, sets [][]string) error {
	shown := make([]string, len(sets))
	for i, s := range sets {
		shown[i] = strings.Join(s, ",")
	}
	list := strings.Join(shown, " ")
	if list == "" {
		list = "(none)"
	}
	_, err := fmt.Fprintf(out, "Agentbus: persistent tag subscriptions for %s: %s\nApplied at the next register in this repository.\n", identity, list)
	return err
}
```

Append to `internal/cli/subscribe_test.go`:

```go
func TestSubscribeTagsWritesTagSets(t *testing.T) {
	root := repoRoot(t, "myrepo")
	var out bytes.Buffer
	if err := SubscribeTags(root, []string{"Bug", "agentbus"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Agentbus: persistent tag subscriptions for myrepo: agentbus,bug\n") {
		t.Fatalf("%q", out.String())
	}
	f, err := repoconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if sets, _ := f.TagSubscriptions(); len(sets) != 1 {
		t.Fatalf("%v", sets)
	}
	out.Reset()
	if err := UnsubscribeTags(root, []string{"agentbus", "bug"}, &out); err != nil || !strings.Contains(out.String(), "(none)") {
		t.Fatalf("%v %q", err, out.String())
	}
}
```

`main.go` (line 177-196) replace the `subscribe`/`unsubscribe` case with:

```go
	case "subscribe", "unsubscribe":
		fs := flag.NewFlagSet("agentbus "+cmd, flag.ContinueOnError)
		tags := fs.String("tags", "", "comma-separated tag set to follow instead of a channel")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if (fs.NArg() != 1) == (*tags == "") {
			fmt.Fprintf(os.Stderr, "usage: agentbus %s <channel> | agentbus %s -tags <tag>[,<tag>...]\n", cmd, cmd)
			return 2
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		switch {
		case *tags != "" && cmd == "subscribe":
			err = cli.SubscribeTags(cwd, strings.Split(*tags, ","), os.Stdout)
		case *tags != "":
			err = cli.UnsubscribeTags(cwd, strings.Split(*tags, ","), os.Stdout)
		case cmd == "subscribe":
			err = cli.Subscribe(cwd, fs.Arg(0), os.Stdout)
		default:
			err = cli.Unsubscribe(cwd, fs.Arg(0), os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
```

(add `"strings"` to main.go's imports; it is not imported today). Run: `go test ./internal/repoconfig/ ./internal/mcpserver/ ./internal/cli/ && go build ./...` — Expected: PASS.

- [ ] **Step 7: TUI tags rail section, tag panes, `t`/`s` keys (failing tests first)**

Create `internal/tui/tags_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

func (f *fixture) selectTagPane(t *testing.T, name string) {
	t.Helper()
	f.key("esc")
	for i, c := range f.m.channels {
		if c.Name == name {
			f.run(f.m.selectChannel(i))
			return
		}
	}
	t.Fatalf("no tag pane %q in %v", name, f.m.channels)
}

func TestTagRailSectionListsSetsAndPaneShowsMatches(t *testing.T) {
	f := newFixture(t)
	if err := f.c.b.SubscribeTags(f.c.as, []string{"bug", "agentbus"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	rail := ansi.Strip(f.m.renderRails())
	if !strings.Contains(rail, "tags") || !strings.Contains(rail, "agentbus") {
		t.Fatalf("rail lacks the tags section:\n%s", rail)
	}
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dev", Content: "match", Tags: []string{"agentbus", "bug", "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "general", Content: "partial", Tags: []string{"bug"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dev-notes", Content: "memory", Tags: []string{"agentbus", "bug"}}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	f.selectTagPane(t, tagPanePrefix+"agentbus,bug")
	s := ansi.Strip(f.m.renderStream())
	if !strings.Contains(s, "match") || strings.Contains(s, "partial") || strings.Contains(s, "memory") {
		t.Fatalf("tag pane shows chat messages carrying every tag:\n%s", s)
	}
	if !strings.Contains(s, "Sam --> "+iconChat+"dev") {
		t.Fatalf("rows keep the #3 header:\n%s", s)
	}
	for _, k := range []string{"i", "enter", "r"} {
		f.key(k)
		if f.m.mode != modeNormal || f.m.toast != tagReadOnlyToast {
			t.Fatalf("key %q on a tag pane: mode=%v toast=%q", k, f.m.mode, f.m.toast)
		}
	}
}

func TestTagKeySubscribesAndSUnsubscribes(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("t")
	for _, r := range "Release, docs" {
		f.key(string(r))
	}
	f.key("enter")
	sets, err := f.c.b.TagSubscriptions(f.c.as)
	if err != nil || len(sets) != 1 || sets[0][0] != "docs" || sets[0][1] != "release" {
		t.Fatalf("t prompt subscribes a normalized set: %v %v", sets, err)
	}
	f.selectTagPane(t, tagPanePrefix+"docs,release")
	f.key("s")
	if sets, _ := f.c.b.TagSubscriptions(f.c.as); len(sets) != 0 {
		t.Fatalf("s on a tag row unsubscribes: %v", sets)
	}
	for _, c := range f.m.channels {
		if strings.HasPrefix(c.Name, tagPanePrefix) {
			t.Fatalf("rail still lists the set: %v", f.m.channels)
		}
	}
}
```

Run: `go test ./internal/tui/ -run TestTag -v` — Expected: compile error (`tagPanePrefix` undefined).

- [ ] **Step 8: Implement the TUI tag view**

`internal/tui/msgs.go`: `statusMsg` gains `tags [][]string`. `internal/tui/model.go` `statusCmd` (line 952-958):

```go
func (m *Model) statusCmd() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		st, err := c.b.StatusReport()
		if err != nil {
			return statusMsg{st: st, err: err}
		}
		tags, err := c.b.TagSubscriptions(c.as)
		return statusMsg{st: st, tags: tags, err: err}
	}
}
```

In `onStatus` (line 876): `cmds := []tea.Cmd{m.setChannels(msg.st.Channels, msg.tags, "oldest")}`. In `setChannels(chans []bus.Channel, tags [][]string, from string)`: after `m.channels = channels` (line 925) append:

```go
	// Tag sets are rail entries too (a "tags" section under the channels):
	// synthetic, never subscribed on the bus, drawn as chips, rendered from
	// the messages already loaded for the chat channels.
	for _, set := range tags {
		m.channels = append(m.channels, bus.Channel{Name: tagPanePrefix + strings.Join(set, ","), Kind: "tags"})
	}
```

Add to model.go near `tasksReadOnlyToast`'s sibling in tasks.go, or at the top of a new `internal/tui/tags.go`:

```go
package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

// tagPanePrefix names a tag set's rail entry: "tags:" plus the sorted set
// joined by commas. bus channel names never contain ':' in that position
// with this prefix (see bus.ChannelNameRule; the list is synthetic anyway).
const tagPanePrefix = "tags:"

const tagReadOnlyToast = "tag views are read-only; select a channel to post"

func isTagPane(ch string) bool { return strings.HasPrefix(ch, tagPanePrefix) }

func tagPaneSet(ch string) []string { return strings.Split(strings.TrimPrefix(ch, tagPanePrefix), ",") }

// tagPaneMsgs is the loaded messages of every chat channel in the rail
// that carry all of the pane's tags, ascending by seq.
func (m *Model) tagPaneMsgs(ch string) []bus.Message {
	set := tagPaneSet(ch)
	var out []bus.Message
	for _, c := range m.channels {
		if c.Kind != "ordinary" {
			continue
		}
		for _, x := range m.msgs[c.Name] {
			all := true
			for _, t := range set {
				if !slices.Contains(x.Tags, t) {
					all = false
				}
			}
			if all {
				out = append(out, x)
			}
		}
	}
	slices.SortFunc(out, func(a, b bus.Message) int { return int(a.Seq - b.Seq) })
	return out
}

// tagPrompt asks for a comma-separated tag set and subscribes to it; the
// status refresh that follows adds it to the rail.
func (m *Model) tagPrompt() tea.Cmd {
	return m.openPrompt("tags <tag>[,<tag>...]", func(m *Model, v string) tea.Cmd {
		c := m.c
		tags := strings.Split(v, ",")
		return func() tea.Msg { return subscribedMsg{channel: tagPanePrefix + v, err: c.b.SubscribeTags(c.as, tags)} }
	})
}
```

Wire it:
- `thread.go` `paneMsgs`: first line `if isTagPane(ch) { return m.tagPaneMsgs(ch) }`.
- `model.go` `updateNormal`: `case "t": return m.tagPrompt()`; in the `"i"`, `"enter"`, `"r"`, `"m"` cases add `if isTagPane(m.selName()) { return m.showToast(tagReadOnlyToast) }` next to the task-channel guard; `"d"`: `if m.sessSel < 0 && m.selected() != nil && !isTagPane(m.selName())`. `updateInsert` `"enter"`: same guard. `paneKey` line 523: `&& !isTagPane(m.selName())`.
- `showSelected`: guard the history load with `&& !isTagPane(ch)`; and add, after the DM block:

```go
	// A tag pane is drawn from the chat channels' loaded messages, so it
	// needs their latest pages; one page per chat channel on first visit.
	if isTagPane(ch) {
		for _, c := range m.channels {
			if c.Kind == "ordinary" && !m.loaded[c.Name] {
				cmds = append(cmds, m.loadHistory(c.Name, nil))
			}
		}
	}
```

- `compose.go` `toggleSubscribe`: at the top, after the nil check:

```go
	if isTagPane(ch.Name) {
		if err := m.c.b.UnsubscribeTags(m.c.as, tagPaneSet(ch.Name)); err != nil {
			return m.showToast("unsubscribe: " + errText(err))
		}
		return tea.Batch(m.showToast("unsubscribed from tags "+strings.TrimPrefix(ch.Name, tagPanePrefix)), m.statusCmd())
	}
```

- `view.go` `renderRails` channel loop: before the loop track `tagsShown := false`; inside, for `isTagPane(c.Name)`: if `!tagsShown { l.WriteString(dim.Render("tags") + "\n"); tagsShown = true }` and `line := m.tagChips(tagPaneSet(c.Name))` instead of the icon+name (skip the unread and `(off)` suffixes: `if !isTagPane(c.Name) && !m.c.isSubscribed(...)`). Account for the extra label row: `chanRows := min(len(m.channels)+1+btoi(tagsShown), ...)` — write it as `extra := 0; if tagsShown { extra = 1 }`.
- `renderHeader`: for a tag pane, `s = m.tagChips(tagPaneSet(ch.Name)) + fmt.Sprintf(" · %d messages", len(m.rows(ch.Name)))` (rows count is fine; the pane has no unread).
- `renderCompose`: for a tag pane, `label = m.tagChips(tagPaneSet(ch.Name))` and skip the memory hint.
- `help.go`: add `{"t", "follow a tag set: <tag>[,<tag>...] (AND); s on its rail row unfollows"}` after the `s` row and `{"(tags)", "the tags rail section shows chat messages carrying every tag of a set; read-only"}` at the end.

Run: `go test ./internal/tui/` — Expected: PASS.

- [ ] **Step 9: Docs**

`README.md`: `subscribe` bullet: append "; `tags: [...]` instead of `channel` follows an AND set of tags across every chat channel you are not already subscribed to (matches arrive through `receive` with `matched_tags`); `persistent` stores it too". `agentbus wait` paragraph: after "-filter <regexp> (wake only for matching content, e.g. @myname" add ", plus any direct message or tag match". Add to the subscribe CLI mention (if none, add one sentence after the `wait` paragraph): "`agentbus subscribe -tags a,b` / `agentbus unsubscribe -tags a,b` edit the persistent `tag_subscriptions` list the same way `agentbus subscribe <channel>` edits `channels`."

`docs/install.md` TUI paragraph (line 228-240): add "`t` follows a tag set (a `tags` section lists yours; select one to see every chat message carrying all of its tags; `s` on it unfollows)."

`SKILL.md` "Tags" section (Task 5): append:

```markdown
To follow a topic without joining every channel, `subscribe` with `tags`
instead of `channel`: `["release"]` delivers every chat message tagged
release; `["agentbus", "bug"]` only messages carrying both (AND). Subscribe
twice for OR. Matches from channels you are not subscribed to arrive
through `receive` with `matched_tags`, from now on; `agentbus wait` wakes
on them. Tag subscriptions never cover inboxes, memory channels, or task
lists. `persistent: true` records the set in `.local/agentbus.json`
(`tag_subscriptions: [["release"], ["agentbus","bug"]]`), which `register`
applies each session; `unsubscribe` with the same `tags` drops it.
```

- [ ] **Step 10: Full check, commit**

```bash
gofmt -l . && golangci-lint run ./... && go build ./... && go test ./...
git add internal/bus internal/repoconfig internal/mcpserver internal/cli internal/tui main.go README.md docs/install.md
git status --short   # confirm only the files above; HANDOFF.md stays untracked
git commit -m "feat: tag subscriptions (#6)

subscribe/unsubscribe take tags (an AND set of 1-10 tags; OR is another
set). Sets live in tag_subscriptions (schema 4) behind one shared tags/
pseudo-subscription row, so receiveOnce's cursor, pending batch, ack,
and idle expiry apply unchanged; receiveOnce now builds its query from
per-source predicates. Matches come from ordinary chat channels the
agent is not directly subscribed to, carry matched_tags, wake agentbus
wait, and are applied from .local/agentbus.json by register. The TUI
lists tag sets in a tags rail section (t to follow, s to unfollow) and
shows the merged matching messages. ADR 0009."
```

---

### Task 7: Task-list usage guidance (#8)

**Files:**
- Modify: `internal/cli/identity.go:89-91` (protocol bullet)
- Modify: `internal/cli/skills/using-agentbus/SKILL.md:62-89` ("Task lists")
- Modify: `internal/mcpserver/server.go:391-414` (`task_*` descriptions)
- Modify: `README.md:58-67` ("Task lists")
- Test: `internal/cli/cli_test.go:123-140` (`TestIdentityLineIsPrescriptive`)

**Interfaces:**
- Consumes: the bus rules already enforced (`tasks_update.go`: an `in_progress` task needs an owner; `task_claim` semantics in `server.go:395`).
- Produces: prose only. The protocol bullet stays short and points at the skill.

- [ ] **Step 1: Update the protocol test**

In `internal/cli/cli_test.go` `TestIdentityLineIsPrescriptive`, replace the want line `"- For work shared between agents, use a task list"` with `"- Use a task list (tasks/<repo>; machine-wide: tasks) for multi-step work"` and add `"claim before you start"`.

Run: `go test ./internal/cli/ -run TestIdentityLineIsPrescriptive` — Expected: FAIL (old text).

- [ ] **Step 2: Rewrite the protocol bullet**

In `internal/cli/identity.go` replace lines 89-91 with:

```go
- Use a task list (tasks/<repo>; machine-wide: tasks) for multi-step work,
  work shared or handed between agents or sessions, and plans that must
  outlive your session; use chat for everything else. Check task_list first,
  claim before you start (task_claim), complete or release when you stop,
  and never force another live agent's task. Details: the using-agentbus
  skill, "Task lists".
```

Run: `go test ./internal/cli/` — Expected: PASS.

- [ ] **Step 3: Expand the skill section**

Replace `## Task lists` (SKILL.md lines 62-89) with:

```markdown
## Task lists

A task list is a memory channel named `tasks/<name>`. A repository's list
is `tasks/<repo>`, created by `agentbus init` and subscribed by `register`;
the machine-wide list `tasks` exists on every bus for work that is not
about one repository. If `tasks/<repo>` is missing, create it yourself with
`create_channel` (`kind: memory`) and `subscribe` to it; do not fall back
to chat.

### When to use a list, when to use chat

Use a list for multi-step work, for work another agent or a later session
could pick up, and for a plan that should survive your context window.
Use chat for announcements, questions, and one-off status. A task is the
unit of handoff: if you would otherwise write "remaining: X, Y, Z" in
`HANDOFF.md`, create three tasks.

### How to structure tasks

- One deliverable per task, with an imperative subject ("Add tags column",
  not "tags"). Put acceptance details in `description`.
- `parent` breaks a task down; subtasks are ordered under it.
- `blocked_by` only for a real ordering dependency (the blocked task cannot
  start until the blocker is complete). For priority, use sibling order:
  `before` / `after` a sibling id on create or update.
- Keep `metadata` for machine-readable hints (branch, issue number).

### Lifecycle

1. `task_list` before starting work: see what is claimed and what is
   blocked.
2. `task_claim` before you start. `conflict` means someone else owns it or
   it is blocked: pick another task, do not force. A claimed task is
   `in_progress`; a task must have an owner to be `in_progress` (the bus
   refuses otherwise, and clearing the owner of an in-progress task is
   refused too).
3. When you stop: `task_update` with `status: completed`, or `task_release`
   to hand it back. Never leave a task claimed that you are not working on.
4. Long-running work: claim with `leased_until` and renew it with
   `task_claim` again before it passes; an expired lease hands the task to
   the next claimer, and a `task_update` after expiry changes nothing.
5. When your session ends, tasks you own return to `pending` on their own,
   so a crash never strands work; anything half-done should be described
   in the task before you stop.

### Coordinating through a list

- `force: true` overrides another owner and is recorded. Use it only when
  the user tells you to, never over a live agent's task.
- Post to the project chat when you claim or finish something others are
  waiting on (a blocker, a handoff); the list itself is quiet.
- `receive` delivers a task's latest revision, not every intermediate one,
  and never a deletion; `task_list` is the full picture.
- `send`, `edit_memory`, and `delete_memory` are refused on task lists.

### Worked example

```
task_list channel=tasks/<repo>                       # nothing claimed
task_create channel=tasks/<repo> subject="Add message tags"
task_create ... subject="Schema migration" parent=1
task_create ... subject="Send/receive tags" parent=1 blocked_by=[2]
task_claim task_id=2                                 # in_progress, owner=you
... work, then post "schema 3 landed on main" to general/<repo> ...
task_update task_id=2 status=completed               # 3 is now unblocked
task_claim task_id=3
```
```

- [ ] **Step 4: Tool descriptions and README**

`internal/mcpserver/server.go`:
- `task_create` (line 391): append ` One deliverable per task with an imperative subject; blocked_by only for real ordering dependencies, sibling order for priority.`
- `task_claim` (line 395): append ` Call task_list first; claim before starting work.`
- `task_release` (line 399): unchanged.
- `task_update` (line 403): append ` An in_progress task always has an owner.`
- `task_list` (line 411): prepend after "Agentbus: " nothing; append ` Check it before starting work to see what is claimed or blocked.`

`README.md` "Task lists" (line 58-67): append a paragraph:

```markdown
Use a list for multi-step work, for work another agent or a later session
may pick up, and for plans that must outlive a session; use chat for
announcements. One deliverable per task with an imperative subject; `parent`
for breakdown, `blocked_by` only for real ordering dependencies, sibling
order (`before`/`after`) for priority. Claim before starting, complete or
release when you stop, renew leases on long work, and never `force` over a
live agent's task; a session that ends returns its tasks to pending. The
`using-agentbus` skill (`internal/cli/skills/using-agentbus/SKILL.md`,
installed by `agentbus init`) has the full guidance and a worked example.
```

- [ ] **Step 5: Full check, commit**

```bash
gofmt -l . && golangci-lint run ./... && go build ./... && go test ./...
git add internal/cli/identity.go internal/cli/cli_test.go internal/cli/skills/using-agentbus/SKILL.md internal/mcpserver/server.go README.md
git commit -m "docs: task-list usage guidance for agents (#8)

When to use a list vs chat, how to structure tasks, the claim/complete/
release lifecycle and leases, coordination rules, project vs machine-wide
lists, and a worked example, in the skill; the protocol bullet stays
short and points to it; task_* tool descriptions and README updated."
```

---

## Self-review notes

- Spec coverage: #7 (Task 1), #9 rows 1-5 and no-header rule (Task 2), #3 format/keys/selected row/chips/summary/cursorLine/tests (Task 3), #4 merge/threading/inbox-only counts/tests (Task 4), #5 validation/migration/return fields/filters/MCP/docs (Task 5; CLI has no `send`), #6 subscribe/unsubscribe/migration/receiveOnce sources/ack-pending-expiry-resume/matched_tags/wait/register/.local/TUI rail/tests/docs (Task 6), #8 all bullets (Task 7).
- Type consistency: `NormalizeTags` (exported) used by bus, repoconfig; `messageCols(alias)` everywhere `messageColumns` was; `tagSource` in bus only; `tagPanePrefix`/`isTagPane`/`tagPaneSet` in tui; `History(..., tags ...string)`; `statusMsg.tags`; `setChannels(chans, tags, from)`.
- Review Focus items are pinned by `TestTaskChannelNeverShowsMessageHeaders`, `TestHeaderBusSender`, `TestReplyFromOwnOutgoingRowTargetsPartner`, `TestEditMemoryTagsKeepAndClear`, `TestTagRowSurvivesSweeps`.
