# TUI Task Editing and Walkthrough Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the five TTY-walkthrough issues (completed icon size, memory version label, selection vs focus color, task lists that don't expand like message lists, no way to change a task from the TUI) plus the rail key/layout decisions, all inside the TUI.

**Architecture:** Every change is in `internal/tui` (plus two new theme keys in `internal/config`). Task lists get a visible-row projection (`taskRows`) over the tree `TaskList` already returns pre-ordered with `Parent` and `Depth`, so children collapse without a bus change; the cursor, `paneLen` and the jump paths index that projection. Task editing is a single in-model draft (`taskDraft`) that `space` cycles and `enter` writes through the existing `TaskClaim` / `TaskUpdate` API synchronously (the way `toggleSubscribe` already calls the bus), so no save can race the receive loop. The bus's rules stay authoritative: its refusal is the toast.

**Tech Stack:** Go 1.27, Bubble Tea v1 / Lip Gloss v1, `charmbracelet/x/ansi`, SQLite (modernc) through `internal/bus`.

**Spec:** `docs/superpowers/specs/2026-09-24-tui-task-editing-design.md` (approved 2026-09-24). Human decisions: `docs/adr/0011-tui-task-editing.md`. Do not change any of those decisions.

## Global Constraints

- American spelling ("color").
- TUI-only: no change under `internal/bus`, `internal/mcpserver` or the schema. `internal/config` changes only to add the theme keys `selection_inactive` (default `brightblack`) and `unsaved` (default `yellow`).
- Only the TUI's own identity (`m.c.as`) counts as the user; every other owner is an agent (ADR 0011 decision 1). Only unassigned tasks or tasks it owns can be changed from the TUI.
- One draft at a time, held only in the TUI; discarded on cursor move, focus leaving the task pane, channel change, or a bus revision of the drafted task (decision 2). Saves go through `TaskClaim` (into in progress) or `TaskUpdate` (anything else); the bus's rules are authoritative and a refusal keeps the draft.
- Task children start collapsed (decision 3). `→` opens details, then children; `←` hides children (whole subtree), then details. `▶` when anything is hidden, `▼` when something is open and nothing hidden, blank otherwise. Existing glyph vars only (`markSel`, `markOpen`).
- `space` is not a show/hide key anywhere (decision 4): in a task list it cycles the state; in a message list it does nothing.
- Memory stepping label is `r<revision> · <n> kept` (decision 5); the purge is unchanged.
- Rail (decision 7): `t` follows a tag set only with rail focus (`pane()` is `paneChannels` or `paneSessions`); a blank, unselectable line precedes the `tags` label; `d` and `s` on a tag set unfollow it without confirmation and move the selection to the next tag set, else the previous one, else the last channel.
- Exact copy: toasts `owned by <name>; only unassigned tasks or yours can be changed here`, `enter saves · esc cancels`, `change discarded`, `only a not-started task can be unassigned`. The `task lists are read-only here` toast and its constant go away.
- TUI fixture rules: no `tea.Sequence` in production code, timers only via the package `tick` var, static cursors on every text input. Bus calls made from a key handler may be synchronous (the pattern `toggleSubscribe` and `updateConfirmChannel` already use); anything that must come back later is a `tea.Cmd` returning one of the `msgs.go` types.
- Never run a dev build against `~/.local/share/agentbus`; use `AGENTBUS_DATA_DIR=~/.agentbus-dev`.
- No new dependencies. Use `rg`, not `grep`. macOS has no GNU `timeout`; if a command needs one, `perl -e 'alarm 120; exec @ARGV' go test ./...`.
- The gate, run at the end of every task: `gofmt -l . && golangci-lint run ./... && go build ./... && go test ./...` — all four clean (gofmt prints nothing). Then one commit per task on `feat/tui-task-editing`, ending with the trailer lines the session's attribution reminder gives (`Co-Authored-By:` and `Claude-Session:`). Do not push, tag, bump `Version`, or run `release/release.sh`.
- 1.8.0 ships this with message subjects; `release/notes-v1.8.0.md` already exists and Task 7 appends to it.

## Review Focus

1. **A task whose `Parent` is not in the list** (an agent's filtered view, or a stale summary). It shows as a root instead of vanishing from the pane, and the cursor can reach it (Task 5, `TestTaskRowsShowAnOrphanAsARoot`).
2. **An agent deletes the drafted task while the draft is pending.** The next tree refresh drops the row; the draft is discarded with `change discarded`, the cursor clamps, nothing panics (Task 6, `TestDraftIsDiscardedWhenTheTaskDisappears`).
3. **A selected task row with a hidden-child summary line.** The highlight covers both lines and the next row's `cursorLine` accounts for the summary, so `↓` scrolls to the right place (Task 5, `TestHiddenChildSummaryCountsAsALine`).
4. **`t` typed in the compose box.** It stays text; the tag prompt opens only from the rail (Task 4, `TestTagKeyOpensThePromptOnlyFromTheRail`).
5. **An error toast right after a hint toast.** The red style and `✗` prefix return; the hint style does not stick (Task 6, `TestBusRefusalKeepsTheDraftForEsc` asserts `toastHint` is false on the refusal).

---

### Task 1: Completed task icon (nerdfont) is U+F14A

**Files:**
- Modify: `internal/tui/icons.go:44`
- Modify: `docs/install.md:352` (icon table row `task_completed`)
- Test: `internal/tui/icons_test.go`

**Interfaces:**
- Consumes: `nerdIcons` map (icons.go).
- Produces: nothing new; `nerdIcons["task_completed"] == "\uf14a"`.

- [ ] **Step 1: Write the failing test.** Append to `internal/tui/icons_test.go`:

```go
// TestNerdfontCompletedIconFillsTheCell: the completed glyph is U+F14A
// (fa-square-check), drawn 600x600 in the Mono patches like the pending
// (U+F096) and in-progress (U+F152) glyphs. FA4's U+F046 check_square_o is
// 480x480 there (its check overhangs the box) and looked a size smaller.
func TestNerdfontCompletedIconFillsTheCell(t *testing.T) {
	if nerdIcons["task_completed"] != "\uf14a" {
		t.Fatalf("task_completed = %q, want U+F14A", nerdIcons["task_completed"])
	}
}
```

- [ ] **Step 2: Run it and confirm it fails.** `go test ./internal/tui -run TestNerdfontCompletedIconFillsTheCell -count=1` fails with `task_completed = "\uf046"`.

- [ ] **Step 3: Change the glyph.** In `internal/tui/icons.go` replace line 44:

```go
	"task_completed":   "\uf14a",    // fa-square-check solid (nf fa-square_check); FA4's check_square_o U+F046 is drawn 20% smaller in Mono patches
```

(The other entries keep their literal glyphs; a `\u` escape is fine here and keeps the fix visible in a diff.) In `docs/install.md` the icon table row becomes:

```
| `task_completed` | a completed task | ✅ | U+F14A fa-square-check |
```

- [ ] **Step 4: Run the icon tests, then the gate.** `go test ./internal/tui -run 'TestLoadIcons|TestNerdfont' -count=1` (the width-parity test still passes: U+F14A is one cell, padded to three), then the full gate.

- [ ] **Step 5: Commit.**

```bash
git add internal/tui/icons.go internal/tui/icons_test.go docs/install.md
git commit -m "fix(tui): nerdfont completed task icon is fa-square-check (U+F14A)

The FA4 check_square_o glyph is drawn 480x480 in the Mono patches against
600x600 for the pending and in-progress squares, so it looked smaller.
ADR 0011."
```

---

### Task 2: Memory version label shows the real revision and how many are kept

**Files:**
- Modify: `internal/tui/view.go:430-433` (`renderStream` label)
- Modify: `internal/tui/model_test.go:24-45` (`newFixture` split into `newFixtureWith`)
- Modify: `internal/tui/memories_test.go` (expected labels; new test)
- Modify: `internal/tui/body_test.go:156,161` (expected labels)

**Interfaces:**
- Consumes: `memView.version`, `memView.revs`, `bus.Message.Revision`.
- Produces: `func newFixtureWith(t *testing.T, cfg config.Config) *fixture` (test helper later tasks may use).

Behavior (spec section 2): while stepping, the label is `r<revision> · <n> kept` where `<revision>` is the shown version's own `Revision` and `<n>` is `len(m.mem.revs)`. The row's permanent `r<N>` label (the live row's revision, shown when > 1) is unchanged.

- [ ] **Step 1: Split the fixture constructor.** In `internal/tui/model_test.go`, add `"github.com/ericfitz/agentbus/internal/config"` to the imports and replace `newFixture`:

```go
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, testConfig(t))
}

// newFixtureWith is newFixture on a caller-shaped config (e.g. a purge
// window of zero).
func newFixtureWith(t *testing.T, cfg config.Config) *fixture {
	t.Helper()
	ab, sam := agent(t, cfg, "Sam")
	if _, err := ab.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := ab.CreateChannel(sam, "dev-notes", "memory"); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	f := &fixture{c: c, ab: ab, sam: sam}
	f.m = New(c, LoadTheme(cfg, io.Discard))
	f.m.width, f.m.height = 100, 32
	f.run(f.m.Init())
	f.key("i") // the TUI starts on the channel list; most tests type first
	return f
}
```

- [ ] **Step 2: Write the failing tests.** In `internal/tui/memories_test.go`, add `"context"` to the imports, change the expected strings in `TestMemoryVersionsStepOlderAndNewer` and `TestMemoryVersionResetsOnOtherKeys`:

```go
	f.streamHas(t, "Release v2 r2 · 3 kept")   // was "Release v2 r2 of 3"
	f.streamHas(t, "Release v1 r1 · 3 kept")   // was "Release v1 r1 of 3" (both occurrences)
	f.streamHas(t, "Release v3 r3 · 3 kept")   // was "Release v3 r3 of 3"
```

and in `TestMemoryVersionResetsOnOtherKeys` the final check becomes `strings.Contains(ansi.Strip(f.m.renderStream()), "kept")`. In `internal/tui/body_test.go` `TestBodyStaysOpenAcrossMemoryVersions`, `"r1 of 2"` becomes `"r1 · 2 kept"` and `"r2 of 2"` becomes `"r2 · 2 kept"`. Then append to `memories_test.go`:

```go
// TestMemoryVersionLabelUsesTheRealRevision (ADR 0011 decision 5): once the
// tombstone purge has removed revision 1, the memory's one surviving row is
// revision 2 and stepping reads "r2 · 1 kept", not "r1 of 1".
func TestMemoryVersionLabelUsesTheRealRevision(t *testing.T) {
	cfg := testConfig(t)
	cfg.TombstoneMinHours = -1 // every tombstone is old enough on the next tick (config is not validated here)
	f := newFixtureWith(t, cfg)
	first := f.agentSend(t, "dev-notes", "Release v1")
	if _, err := f.ab.EditMemory(f.sam, bus.EditInput{ID: *first.MemoryID, Content: "Release v2"}); err != nil {
		t.Fatal(err)
	}
	// Whichever of the two buses holds the maintenance lease runs the purge.
	f.ab.Tick(context.Background())
	f.c.b.Tick(context.Background())
	revs, err := f.c.b.MemoryRevisions(f.c.as, *first.MemoryID)
	if err != nil || len(revs) != 1 {
		t.Fatalf("fixture: want one surviving revision, got %d (%v)", len(revs), err)
	}
	f.key("esc")
	for i, c := range f.m.channels {
		if c.Name == "dev-notes" {
			f.run(f.m.selectChannel(i))
		}
	}
	f.run(f.m.loadHistory("dev-notes", nil))
	f.m.placeCursor(revs[0].Seq)
	f.key(".")
	f.streamHas(t, "Release v2 r2 · 1 kept")
	if s := ansi.Strip(f.m.renderStream()); strings.Contains(s, "r1") {
		t.Fatalf("the label must not count positions:\n%s", s)
	}
}
```

- [ ] **Step 3: Run them and confirm they fail.** `go test ./internal/tui -run 'TestMemoryVersion|TestBodyStaysOpenAcrossMemoryVersions' -count=1` fails on the old `r1 of 1` / `of 3` strings.

- [ ] **Step 4: Change the label.** In `internal/tui/view.go` `renderStream`, replace lines 430-433 with:

```go
		if v, ok := m.mem.version(x); ok {
			x.Sender, x.CreatedAt, x.Subject, x.Content = v.Sender, v.CreatedAt, v.Subject, v.Content
			// The shown version's own revision number, not its position:
			// replaced revisions are purged after tombstone_min_hours, so the
			// kept list can start above r1 (ADR 0011 decision 5).
			rev := strconv.Itoa(m.mem.idx + 1)
			if v.Revision != nil {
				rev = itoa(*v.Revision)
			}
			label = "r" + rev + " \u00b7 " + strconv.Itoa(len(m.mem.revs)) + " kept"
		}
```

- [ ] **Step 5: Run the TUI tests, then the gate.** `go test ./internal/tui -count=1`, then the full gate.

- [ ] **Step 6: Commit.**

```bash
git add internal/tui/view.go internal/tui/model_test.go internal/tui/memories_test.go internal/tui/body_test.go
git commit -m "fix(tui): memory version label is r<revision> · <n> kept

The stepper labeled a version by its position in the surviving list, so a
memory whose revision 1 was purged read r1 of 1 while its row said r2.
ADR 0011."
```

---

### Task 3: Theme keys `selection_inactive` and `unsaved`; unfocused rail selection

**Files:**
- Modify: `internal/config/config.go:85-125` (`Theme` fields, `DefaultTheme`, `Colors`)
- Modify: `internal/tui/theme.go:19-28,73-102,109-118` (`Theme` fields, `set`, `Highlight`, new `SelBG`)
- Modify: `internal/tui/view.go:273-363` (`renderRails`), `view.go:476` and `internal/tui/tasks.go:150` (`Highlight` callers)
- Modify: `docs/install.md:281-297` (theme table)
- Test: `internal/tui/theme_test.go`, `internal/tui/view_test.go:167-186`

**Interfaces:**
- Consumes: `config.Theme.Colors()`, `Theme.set`, `Model.pane()`.
- Produces: `config.Theme.SelectionInactive`, `config.Theme.Unsaved` (JSON `selection_inactive`, `unsaved`); `tui.Theme.SelInactive`, `tui.Theme.Unsaved lipgloss.TerminalColor`; `func (t Theme) Highlight(bg lipgloss.TerminalColor, line string, width int) string` (a background parameter is added); `func (t Theme) SelBG(focused bool) lipgloss.TerminalColor`. Task 6 uses `Theme.Unsaved`.

- [ ] **Step 1: Write the failing tests.** Append to `internal/tui/theme_test.go`:

```go
// TestThemeSelectionInactiveAndUnsavedKeys (ADR 0011): the two new keys
// default to brightblack and yellow, override like any other, and reach
// the Health overlay's Sources.
func TestThemeSelectionInactiveAndUnsavedKeys(t *testing.T) {
	cfg := config.Default()
	th := LoadTheme(cfg, io.Discard)
	if th.SelInactive != lipgloss.Color("8") || th.Unsaved != lipgloss.Color("3") {
		t.Fatalf("defaults: selection_inactive=%v unsaved=%v", th.SelInactive, th.Unsaved)
	}
	if th.SelBG(true) != th.Sel || th.SelBG(false) != th.SelInactive {
		t.Fatalf("SelBG: focused=%v unfocused=%v", th.SelBG(true), th.SelBG(false))
	}
	cfg.Themes[0].SelectionInactive, cfg.Themes[0].Unsaved = "green", "default"
	th = LoadTheme(cfg, io.Discard)
	if th.SelInactive != lipgloss.Color("2") || th.Unsaved != (lipgloss.NoColor{}) || th.Sources["selection_inactive"] != "green" || th.Sources["unsaved"] != "default" {
		t.Fatalf("overrides: %+v", th)
	}
}
```

In `internal/tui/view_test.go` `TestSelectedRowKeepsBackgroundAcrossSegments`, the rail assertion (the stream has focus there) becomes:

```go
	rail := strings.SplitN(f.m.renderRails(), "\n", 3)[1]
	if !strings.Contains(rail, markSel) || !strings.Contains(rail, "100m") || strings.Contains(rail, "44m") {
		t.Fatalf("selected channel row must keep a background, the inactive one while the stream has focus: %q", rail)
	}
```

and append:

```go
// TestRailSelectionUsesInactiveColorWithoutFocus (ADR 0011 section 3): the
// rail's selected channel row is on the selection color (blue, 44m) while
// the rail has focus and on selection_inactive (brightblack, 100m) while
// the stream does; the stream cursor stays on selection. Same for the
// sessions pane's selected row.
func TestRailSelectionUsesInactiveColorWithoutFocus(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.key("esc") // compose -> channel list: the rail has focus
	selectedRow := func() string {
		for _, l := range strings.Split(f.m.renderRails(), "\n") {
			if strings.Contains(l, markSel) {
				return l
			}
		}
		return ""
	}
	if row := selectedRow(); !strings.Contains(row, "44m") || strings.Contains(row, "100m") {
		t.Fatalf("focused rail row must use selection: %q", row)
	}
	f.key("tab") // stream
	if f.m.pane() != paneStream {
		t.Fatalf("pane = %v, want stream", f.m.pane())
	}
	if row := selectedRow(); !strings.Contains(row, "100m") || strings.Contains(row, "44m") {
		t.Fatalf("unfocused rail row must use selection_inactive: %q", row)
	}
	if first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]; !strings.Contains(first, "44m") {
		t.Fatalf("stream cursor keeps selection: %q", first)
	}
	f.key("home")
	f.toSessions()
	if f.m.pane() != paneSessions {
		t.Fatalf("pane = %v, want sessions", f.m.pane())
	}
	if row := selectedRow(); !strings.Contains(row, "44m") {
		t.Fatalf("focused session row must use selection: %q", row)
	}
	f.key("tab")
	if row := selectedRow(); !strings.Contains(row, "100m") || strings.Contains(row, "44m") {
		t.Fatalf("unfocused session row must use selection_inactive: %q", row)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/tui -run 'TestThemeSelectionInactive|TestRailSelectionUses|TestSelectedRowKeeps' -count=1` fails to compile (`th.SelInactive` undefined).

- [ ] **Step 3: Config.** In `internal/config/config.go`, add two fields to `Theme` after `Selection`:

```go
	Selection         string `json:"selection"`
	SelectionInactive string `json:"selection_inactive"`
	Unsaved           string `json:"unsaved"`
```

`DefaultTheme` gains `SelectionInactive: "brightblack", Unsaved: "yellow"` after `Selection: "blue"`, and `Colors()` appends after the `selection` pair:

```go
		{"selection", t.Selection},
		{"selection_inactive", t.SelectionInactive},
		{"unsaved", t.Unsaved},
```

Update the `Theme` doc comment if it enumerates keys (it says values are color names; leave it otherwise).

- [ ] **Step 4: TUI theme.** In `internal/tui/theme.go`:

```go
type Theme struct {
	BG, Text, Dim, Stamp, Tag, Agent, User, Mem, Tasks, Health, Warn, Error, Sel, SelInactive, Unsaved lipgloss.TerminalColor
```

In `set` add before the closing brace of the switch:

```go
	case "selection_inactive":
		t.SelInactive = c
	case "unsaved":
		t.Unsaved = c
```

Replace `Highlight`:

```go
// Highlight renders line on background bg (Sel for a focused selection and
// the stream cursor, SelInactive for a rail selection without focus).
// Styled segments inside the line end with a reset that would drop the
// background for the rest of that line, so it is re-applied after every
// reset.
func (t Theme) Highlight(bg lipgloss.TerminalColor, line string, width int) string {
	style := lipgloss.NewStyle().Background(bg)
	if pre, _, ok := strings.Cut(style.Render("\x00"), "\x00"); ok && pre != "" {
		line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+pre)
	}
	return style.Width(width).Render(line)
}

// SelBG is a rail selection's background: Sel while that rail has focus,
// SelInactive while focus is in the stream or compose (ADR 0011 section 3).
func (t Theme) SelBG(focused bool) lipgloss.TerminalColor {
	if focused {
		return t.Sel
	}
	return t.SelInactive
}
```

Callers: `internal/tui/view.go:476` becomes `line = th.Highlight(th.Sel, line, w)`; `internal/tui/tasks.go:150` becomes `line = th.Highlight(th.Sel, lipgloss.NewStyle().MaxWidth(w).Render(line), w)`.

- [ ] **Step 5: Rails.** In `internal/tui/view.go` `renderRails`, add after `trunc := ...`:

```go
	// Focus as pane() reports it: a rail row selected while the stream or
	// compose has focus takes the inactive selection color, so the one blue
	// row on screen is the one the arrow keys move.
	focus := m.pane()
```

and change the two highlight lines:

```go
		if m.sessSel < 0 && i == m.sel {
			line = th.Highlight(th.SelBG(focus == paneChannels), markSel+line, rail)
```

```go
		if i == m.sessSel {
			row = th.Highlight(th.SelBG(focus == paneSessions), markSel+row, rail)
```

(`renderRails` has a value receiver; `m.pane()` is a pointer method on the addressable local `m`, so this compiles unchanged.)

- [ ] **Step 6: Docs.** In `docs/install.md` theme table, after the `selection` row:

```
| `selection` | selected row background (focused pane) | `blue` |
| `selection_inactive` | selected rail row background while the messages or compose pane has focus | `brightblack` |
| `unsaved` | a task row whose drafted state is not saved yet (`space` in a task list; `enter` saves) | `yellow` |
```

- [ ] **Step 7: Run the TUI and config tests, then the gate.** `go test ./internal/tui ./internal/config -count=1`, then the full gate.

- [ ] **Step 8: Commit.**

```bash
git add internal/config/config.go internal/tui/theme.go internal/tui/view.go internal/tui/tasks.go internal/tui/theme_test.go internal/tui/view_test.go docs/install.md
git commit -m "feat(tui): selection_inactive and unsaved theme keys; unfocused rail selection dims

The rail's selected channel or session row uses selection_inactive
(default brightblack) while focus is in the stream or compose, so only
the focused pane's row is blue. unsaved (default yellow) is for Task 6's
drafted task rows. ADR 0011."
```

---

### Task 4: Rail keys and layout: `t` only from the rail, blank line before `tags`, `d`/`s` unfollow a tag set and move the selection

**Files:**
- Modify: `internal/tui/model.go:507-517` (`s`, `t`, `d` cases)
- Modify: `internal/tui/view.go:283-290,353-356` (`renderRails` tags separator and `extra`)
- Modify: `internal/tui/tags.go` (new `unfollowTagPane`)
- Modify: `internal/tui/compose.go:113-123` (`toggleSubscribe` tag branch)
- Modify: `internal/tui/help.go:45-46` (`t`, `d` lines)
- Test: `internal/tui/tags_test.go`

**Interfaces:**
- Consumes: `m.pane()`, `selectChannel`, `statusCmd`, `showToast`, `tagPaneSet`, `isTagPane`, `bus.UnsubscribeTags`.
- Produces: `func (m *Model) unfollowTagPane() tea.Cmd`. Task 6 extends the `t` case with the task-list branch; this task leaves the stream branch empty.

- [ ] **Step 1: Write the failing tests.** Append to `internal/tui/tags_test.go` (add `"slices"` to its imports):

```go
// followTags subscribes the TUI to each set and refreshes the rail.
func (f *fixture) followTags(t *testing.T, sets ...[]string) {
	t.Helper()
	for _, s := range sets {
		if err := f.c.b.SubscribeTags(f.c.as, s); err != nil {
			t.Fatal(err)
		}
	}
	f.run(f.m.statusCmd())
}

// TestTagKeyOpensThePromptOnlyFromTheRail (ADR 0011 decision 7): t is a
// rail key. In a message list it does nothing; in the compose box it types.
func TestTagKeyOpensThePromptOnlyFromTheRail(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "hello")
	f.receive(t)
	f.key("t") // compose has focus: text
	if f.m.prompt.active || f.m.compose.Value() != "t" {
		t.Fatalf("t in compose must type: prompt=%v value=%q", f.m.prompt.active, f.m.compose.Value())
	}
	f.key("ctrl+u")
	f.key("esc") // channel list
	f.key("tab") // stream
	if f.m.pane() != paneStream {
		t.Fatalf("pane = %v, want stream", f.m.pane())
	}
	f.key("t")
	if f.m.prompt.active {
		t.Fatal("t in a message list must not open the tag prompt")
	}
	f.key("home")
	f.key("t")
	if !f.m.prompt.active || !strings.HasPrefix(f.m.prompt.label, "tags") {
		t.Fatalf("t in the channel list opens the tag prompt: active=%v label=%q", f.m.prompt.active, f.m.prompt.label)
	}
	f.key("esc")
	f.toSessions()
	f.key("t")
	if !f.m.prompt.active {
		t.Fatal("t in the sessions pane opens the tag prompt")
	}
}

// TestRailBlankLineBeforeTagsIsNotSelectable: a blank row separates the
// last channel from the "tags" label, like the one before "sessions", and
// the down key steps over it (it is not a row of m.channels).
func TestRailBlankLineBeforeTagsIsNotSelectable(t *testing.T) {
	f := newFixture(t)
	f.followTags(t, []string{"docs"})
	lines := strings.Split(ansi.Strip(f.m.renderRails()), "\n")
	// JoinVertical pads every rail line to the column width, so match trimmed.
	i := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "tags" })
	if i < 2 || strings.TrimSpace(lines[i-1]) != "" || strings.TrimSpace(lines[i-2]) == "" {
		t.Fatalf("want exactly one blank line before tags:\n%s", strings.Join(lines, "\n"))
	}
	f.key("esc")
	downs := 0
	for !isTagPane(f.m.selName()) {
		if downs > len(f.m.channels) {
			t.Fatalf("down never reached the tag set, at %q", f.m.selName())
		}
		f.key("down")
		downs++
	}
	if downs != len(f.m.channels)-1 {
		t.Fatalf("the blank line must take no key: %d downs for %d rail entries", downs, len(f.m.channels))
	}
}

// TestDAndSOnATagSetUnfollowAndMoveTheSelection (ADR 0011 decision 7): no
// confirmation; the selection goes to the next tag set, else the previous
// one, else the last channel, and focus stays in the rail.
func TestDAndSOnATagSetUnfollowAndMoveTheSelection(t *testing.T) {
	f := newFixture(t)
	f.followTags(t, []string{"a"}, []string{"b"}, []string{"c"})
	var lastChannel string
	for _, c := range f.m.channels {
		if !isTagPane(c.Name) {
			lastChannel = c.Name
		}
	}
	f.selectTagPane(t, tagPanePrefix+"b")
	f.key("d")
	if f.m.mode != modeNormal || f.m.pane() != paneChannels || f.m.selName() != tagPanePrefix+"c" {
		t.Fatalf("d on b: mode=%v pane=%v sel=%q, want the next set c with no confirmation", f.m.mode, f.m.pane(), f.m.selName())
	}
	f.key("s")
	if f.m.selName() != tagPanePrefix+"a" {
		t.Fatalf("s on the last set: sel=%q, want the previous set a", f.m.selName())
	}
	f.key("d")
	if f.m.selName() != lastChannel {
		t.Fatalf("d on the only set: sel=%q, want the last channel %q", f.m.selName(), lastChannel)
	}
	if sets, _ := f.c.b.TagSubscriptions(f.c.as); len(sets) != 0 {
		t.Fatalf("every set must be unfollowed: %v", sets)
	}
	for _, c := range f.m.channels {
		if isTagPane(c.Name) {
			t.Fatalf("rail still lists a set: %v", f.m.channels)
		}
	}
}
```

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/tui -run 'TestTagKeyOpensThePromptOnlyFromTheRail|TestRailBlankLine|TestDAndSOnATagSet' -count=1`: the first fails (prompt opens from the stream), the second on the missing blank line, the third with `d` entering `modeConfirmChannel`... no: `d` is a no-op on a tag pane today, so it fails on the unchanged selection.

- [ ] **Step 3: Unfollow helper.** Append to `internal/tui/tags.go` (its imports already include `slices` and `strings`):

```go
// unfollowTagPane (s or d on a tag set's rail row) unsubscribes the
// selected set with no confirmation (re-following is one t away) and moves
// the selection to the next tag set, else the previous one, else the last
// channel. Tag panes sit at the end of m.channels, so once index i is
// deleted the row now at i is the next set and the row at i-1 is whichever
// of the other two exists. The status refresh that follows re-syncs the
// rail by name.
func (m *Model) unfollowTagPane() tea.Cmd {
	ch := m.selected()
	if ch == nil || !isTagPane(ch.Name) {
		return nil
	}
	name := ch.Name // ch points into m.channels, which is edited below
	if err := m.c.b.UnsubscribeTags(m.c.as, tagPaneSet(name)); err != nil {
		return m.showToast("unsubscribe: " + errText(err))
	}
	m.channels = slices.Delete(m.channels, m.sel, m.sel+1)
	return tea.Batch(
		m.selectChannel(min(m.sel, len(m.channels)-1)),
		m.showToast("unfollowed tags "+strings.TrimPrefix(name, tagPanePrefix)),
		m.statusCmd(),
	)
}
```

In `internal/tui/compose.go` `toggleSubscribe`, replace the tag branch:

```go
	if isTagPane(ch.Name) {
		return m.unfollowTagPane()
	}
```

- [ ] **Step 4: Keys.** In `internal/tui/model.go` `updateNormal`, the `t` and `d` cases become:

```go
	case "t":
		// A rail key (ADR 0011 decision 7): from a message list it means
		// nothing, and Task 6 gives it to take in a task list.
		if p := m.pane(); p == paneChannels || p == paneSessions {
			return m.tagPrompt()
		}
	case "d":
		if m.sessSel >= 0 || m.selected() == nil {
			return nil
		}
		if isTagPane(m.selName()) {
			return m.unfollowTagPane()
		}
		m.mode = modeConfirmChannel
```

- [ ] **Step 5: Rail layout.** In `internal/tui/view.go` `renderRails`, the tags header write becomes:

```go
			if !tagsShown {
				l.WriteString("\n" + dim.Render("tags") + "\n") // a blank row before the label, like the one before sessions
				tagsShown = true
			}
```

and the row budget:

```go
	extra := 0
	if tagsShown {
		extra = 2 // the blank separator and the "tags" label
	}
```

- [ ] **Step 6: Help.** In `internal/tui/help.go` `helpLines`:

```go
		{"t", "follow a tag set: <tag>[,<tag>...] (AND); from the channel or session list"},
		{"d", "delete the channel and all its messages (asks first); on a tag set, unfollow it (no confirmation); no-op in the sessions pane"},
```

- [ ] **Step 7: Run the TUI tests, then the gate.** `go test ./internal/tui -count=1` (`TestTagKeySubscribesAndSUnsubscribes` still passes: `s` on the set unfollows and the rail drops it), then the full gate.

- [ ] **Step 8: Commit.**

```bash
git add internal/tui/model.go internal/tui/view.go internal/tui/tags.go internal/tui/compose.go internal/tui/help.go internal/tui/tags_test.go
git commit -m "feat(tui): t follows tags only from the rail; blank line before tags; d unfollows a tag set

d and s on a tag set's rail row unfollow it with no confirmation and move
the selection to the next set, else the previous, else the last channel.
ADR 0011 decision 7."
```

---

### Task 5: Task tree: children collapse, `→`/`←` layer details then children, hidden-child count, jumps reveal the target

**Files:**
- Modify: `internal/tui/model.go:72-74,140-143` (`taskExpanded` field and init), `model.go:245-282` (`tasksMsg`), `model.go:461-471` (`right`/`left`), `model.go:630-635` (`paneLen`), `model.go:681-703` (`placeTaskCursor`)
- Modify: `internal/tui/tasks.go` (`taskRow`, `taskRows`, `taskIndex`, `revealTask`, `cursorTaskRow`, `cursorTask`, `openTask`, `closeTask`, `renderTasks`)
- Modify: `internal/tui/help.go:58` (task lists line)
- Modify: `internal/tui/tasks_test.go` (five existing tests adjusted for collapsed-by-default)
- Test: `internal/tui/tasktree_test.go` (new)

**Interfaces:**
- Consumes: `bus.TaskSummary{ID, Parent, Depth, HasDetails, ...}` in `TaskList`'s pre-order (a parent precedes its children: `taskTree` in `internal/bus/tasks.go` walks the tree), `expandTask`, `collapseTask`, `taskOpen`, `markSel`/`markOpen`, `scrollCursorIntoView`.
- Produces: `type taskRow struct{ t bus.TaskSummary; hidden int; open bool }`; `func (m *Model) taskRows(ch string) []taskRow`; `func (m *Model) taskIndex(ch string, id int64) int` (-1 if not visible); `func (m *Model) revealTask(ch string, id int64)`; `func (m *Model) cursorTaskRow() (taskRow, bool)`; `func (m *Model) openTask() tea.Cmd`; `func (m *Model) closeTask()`; `Model.taskExpanded map[int64]bool`. `cursorTask` keeps its signature and now reads the projection. Task 6 relies on `cursorTask`, `taskRows` and `renderTasks`'s per-row loop.

Behavior (spec section 4, task lists): only top-level tasks show at first. `→` opens the cursor task's details if it has any and they are closed; otherwise it shows the task's direct children. `←` hides shown children (the whole subtree, so a later `→` shows direct children only); otherwise it closes the details. `▶` when details are closed with `HasDetails` or any child is hidden; `▼` when details are open or children are shown, nothing hidden; blank otherwise. A row with hidden children is followed by a dim summary line `N subtasks` (`1 subtask`) at the row's indent, part of the highlighted block when selected. Jumps (`placeCursor` on a task channel, the pending-cursor path in `tasksMsg`) expand the target's ancestors first.

- [ ] **Step 1: Write the failing tests.** Create `internal/tui/tasktree_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

// ids lists the task ids of rows, in order.
func ids(rs []taskRow) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.t.ID
	}
	return out
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTaskChildrenStartCollapsedAndArrowsLayerDetailsThenChildren (ADR
// 0011 decision 3): a (details, child a1 with its own child a1a) and b.
// Only roots show at first, with a's hidden count; → opens a's details,
// then its child; ← hides the whole subtree, then the details.
func TestTaskChildrenStartCollapsedAndArrowsLayerDetailsThenChildren(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	a, a1, b := f.taskTree(t) // cursor on the last visible row, b
	a1a, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a1a", Parent: a1.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.run(f.m.loadTasks("tasks/work"))
	rows := f.m.taskRows("tasks/work")
	if !sameIDs(ids(rows), []int64{a.ID, b.ID}) || rows[0].hidden != 2 || rows[0].open {
		t.Fatalf("collapsed rows = %+v", rows)
	}
	if f.m.paneLen("tasks/work") != 2 {
		t.Fatalf("paneLen = %d, want the visible rows", f.m.paneLen("tasks/work"))
	}
	lines := strings.Split(f.stream(), "\n")
	if !strings.HasPrefix(lines[0], markSel+" ") || strings.TrimSpace(lines[1]) != "2 subtasks" || !strings.Contains(lines[2], "b") {
		t.Fatalf("collapsed stream:\n%s", f.stream())
	}
	f.key("up") // a
	f.key("right")
	if !f.m.taskOpen[a.ID] || len(f.m.taskRows("tasks/work")) != 2 {
		t.Fatal("first → opens the details only")
	}
	if lines = strings.Split(f.stream(), "\n"); !strings.HasPrefix(lines[0], markSel+" ") {
		t.Fatalf("children still hidden: a keeps %s:\n%s", markSel, f.stream())
	}
	f.key("right")
	rows = f.m.taskRows("tasks/work")
	if !sameIDs(ids(rows), []int64{a.ID, a1.ID, b.ID}) || !rows[0].open || rows[0].hidden != 0 || rows[1].hidden != 1 {
		t.Fatalf("second → shows a's direct children: %+v", rows)
	}
	if got, ok := f.m.cursorTask(); !ok || got.ID != a.ID {
		t.Fatalf("→ moved the cursor: %+v", got)
	}
	lines = strings.Split(f.stream(), "\n")
	if !strings.HasPrefix(lines[0], markOpen+" ") {
		t.Fatalf("a shows %s once nothing is hidden: %q", markOpen, lines[0])
	}
	f.key("down") // a1
	f.key("right")
	if !sameIDs(ids(f.m.taskRows("tasks/work")), []int64{a.ID, a1.ID, a1a.ID, b.ID}) {
		t.Fatalf("→ on a1 shows a1a: %v", ids(f.m.taskRows("tasks/work")))
	}
	f.key("up") // a
	f.key("left")
	if !sameIDs(ids(f.m.taskRows("tasks/work")), []int64{a.ID, b.ID}) || f.m.taskExpanded[a1.ID] || !f.m.taskOpen[a.ID] {
		t.Fatalf("← hides the whole subtree and keeps the details: rows=%v expanded=%v open=%v", ids(f.m.taskRows("tasks/work")), f.m.taskExpanded, f.m.taskOpen)
	}
	f.key("left")
	if f.m.taskOpen[a.ID] {
		t.Fatal("second ← closes the details")
	}
	f.key("right")
	f.key("right")
	if !sameIDs(ids(f.m.taskRows("tasks/work")), []int64{a.ID, a1.ID, b.ID}) {
		t.Fatalf("re-showing a's children shows direct children only: %v", ids(f.m.taskRows("tasks/work")))
	}
}

// TestHiddenChildSummaryCountsAsALine: the summary line under a collapsed
// parent is part of that row's block (highlighted with it) and shifts the
// next row's cursorLine.
func TestHiddenChildSummaryCountsAsALine(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	_, _, b := f.taskTree(t) // cursor on b
	if got, ok := f.m.cursorTask(); !ok || got.ID != b.ID {
		t.Fatalf("cursor = %+v, want b", got)
	}
	if f.m.cursorLine != 2 {
		t.Fatalf("cursorLine = %d, want 2 (a's row, its summary, then b)", f.m.cursorLine)
	}
	f.key("up") // a, whose block is two lines
	if f.m.cursorLine != 0 {
		t.Fatalf("cursorLine = %d, want 0", f.m.cursorLine)
	}
	lines := strings.Split(f.m.renderStream(), "\n")
	if w0, w1 := ansi.StringWidth(lines[0]), ansi.StringWidth(lines[1]); w0 != w1 || w0 < f.m.stream.Width {
		t.Fatalf("both lines of the selected block are padded to the pane width: %d, %d (width %d)", w0, w1, f.m.stream.Width)
	}
}

// TestTaskRowsShowAnOrphanAsARoot: a summary whose parent is not in the
// list (a filtered or stale view) is shown as a root, not dropped.
func TestTaskRowsShowAnOrphanAsARoot(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.send(tasksMsg{ch: "tasks/work", tasks: []bus.TaskSummary{
		{ID: 1, Subject: "root", Status: "pending"},
		{ID: 7, Subject: "orphan", Status: "pending", Parent: 999, Depth: 1},
	}})
	rows := f.m.taskRows("tasks/work")
	if !sameIDs(ids(rows), []int64{1, 7}) || rows[0].hidden != 0 {
		t.Fatalf("rows = %+v", rows)
	}
	f.key("tab")
	if got, ok := f.m.cursorTask(); !ok || got.ID != 7 {
		t.Fatalf("cursor reaches the orphan: %+v", got)
	}
}

// TestJumpIntoTaskListRevealsTheTarget (spec section 4): placing the
// cursor on a nested task (what a search hit does through placeCursor)
// expands its ancestors so its row is visible, both with the tree loaded
// and through the pending path when it is not.
func TestJumpIntoTaskListRevealsTheTarget(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	_, a1, _ := f.taskTree(t)
	f.receive(t) // m.msgs["tasks/work"] holds the revision rows placeTaskCursor resolves through
	var seq int64
	for _, x := range f.m.msgs["tasks/work"] {
		if x.MemoryID != nil && *x.MemoryID == a1.ID {
			seq = x.Seq
		}
	}
	if seq == 0 {
		t.Fatal("fixture: no revision row for a1")
	}
	if got, _ := f.m.cursorTask(); got.ID == a1.ID {
		t.Fatal("fixture: a1 must start hidden")
	}
	f.m.placeCursor(seq)
	if got, ok := f.m.cursorTask(); !ok || got.ID != a1.ID {
		t.Fatalf("placeCursor must reveal and land on a1, got %+v", got)
	}
	// The pending path: the tree arrives after the jump asked for a1.
	f.m.taskExpanded = map[int64]bool{}
	f.m.pendingTaskCursorCh, f.m.pendingTaskCursorID = "tasks/work", a1.ID
	f.run(f.m.loadTasks("tasks/work"))
	if got, ok := f.m.cursorTask(); !ok || got.ID != a1.ID {
		t.Fatalf("tasksMsg must reveal and land on the pending a1, got %+v", got)
	}
}
```

Then adjust the existing tests in `internal/tui/tasks_test.go` for collapsed-by-default:

`TestTaskChannelRendersTree`: replace everything from `lines := strings.Split(...)` to the end of the function with:

```go
	// Children start collapsed: a's row (▶, its child hidden), the hidden
	// count, then b.
	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], markSel+" "+taskPending+"#") || strings.TrimSpace(lines[1]) != "1 subtask" {
		t.Fatalf("collapsed tree = %q", lines)
	}
	f.key("tab")   // cursor on b
	f.key("up")    // a
	f.key("right") // a has no details, so → shows its child
	lines = strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) < 3 {
		t.Fatalf("want at least 3 lines, got %d: %q", len(lines), lines)
	}
	// a's child is shown and nothing is hidden, so a carries ▼.
	if !strings.HasPrefix(lines[0], markOpen+" "+taskPending+"#") || !strings.Contains(lines[0], "a") {
		t.Fatalf("line0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  "+"  "+taskInProgress+"#") || !strings.Contains(lines[1], ansi.Strip(iconAgent)+"Sam") {
		t.Fatalf("line1 = %q", lines[1])
	}
	// "b" is blocked, so it has details and shows the collapsed triangle.
	if !strings.HasPrefix(lines[2], markSel+" "+taskPending+"#") || !strings.Contains(lines[2], "blocked by #"+itoa(a.ID)) {
		t.Fatalf("line2 = %q", lines[2])
	}
```

`TestTaskCursorMovesOverTasks`: after the `f.key("right")` block that checks the cursor stayed on a, insert:

```go
	f.key("right") // details are open: the second → shows a's child
	if got, ok := f.m.cursorTask(); !ok || got.ID != a.ID {
		t.Fatalf("showing a's children moved the cursor: %+v", got)
	}
```

`TestTaskExpandCollapse`: the `lines[1]` check becomes

```go
	if strings.TrimSpace(lines[1]) != "1 subtask" {
		t.Fatalf("a1 is hidden; a's summary line follows a: %q", lines[1])
	}
```

the `markOpen` check after `f.run(cmd)` becomes (a's child is still hidden, so a keeps `▶` even with its details open):

```go
	if lines := strings.Split(expanded, "\n"); !strings.HasPrefix(lines[0], markSel+" ") {
		t.Fatalf("a keeps %q while its child is hidden, even with details open: %q", markSel, lines[0])
	}
```

and the tail, from `f.key("down") // cursor on a1, no details`, becomes:

```go
	f.key("right") // details again
	f.key("right") // then a's child
	f.key("down")  // cursor on a1, no details
	if got, ok := f.m.cursorTask(); !ok || got.Subject != "a1" {
		t.Fatalf("cursor = %+v, want a1", got)
	}
	if cmd := f.m.expandTask(); cmd != nil {
		t.Fatal("right on a task with no details should do nothing")
	}
```

`TestTaskExpansionSurvivesRevision`: after `f.run(f.m.expandTask())` insert `f.key("right") // and a's child, so nothing is hidden and a shows ▼`.

`TestTaskIndentIsCapped`: after `f.selectTaskChannel(t, "tasks/work")` insert:

```go
	f.m.revealTask("tasks/work", parent) // the deepest task: every ancestor's children shown
	f.m.refreshStream()
```

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/tui -run 'TestTask|TestHiddenChild|TestJumpInto' -count=1` fails to compile (`taskRow` undefined).

- [ ] **Step 3: Model state.** In `internal/tui/model.go` add to `Model` after `taskOpen`:

```go
	taskExpanded map[int64]bool               // task id -> its direct children are shown (children start collapsed)
```

and in `New` after `taskOpen:     map[int64]bool{},`:

```go
		taskExpanded: map[int64]bool{},
```

`paneLen` becomes:

```go
// paneLen is the stream pane's cursor bound for ch: the visible task rows
// of a task channel (renderTasks draws one row per shown task, not per raw
// revision in m.msgs), else the row count rows(ch) shows (fewer than
// paneMsgs(ch) when a thread's replies are collapsed).
func (m *Model) paneLen(ch string) int {
	if bus.IsTaskChannel(ch) {
		return len(m.taskRows(ch))
	}
	return len(m.rows(ch))
}
```

In the `tasksMsg` case, replace from `var cursorID int64` through the clamp with:

```go
		var cursorID int64
		if msg.ch == m.selName() {
			if t, ok := m.cursorTask(); ok {
				cursorID = t.ID
			}
		}
		if msg.ch == m.pendingTaskCursorCh {
			if msg.ch == m.selName() {
				cursorID = m.pendingTaskCursorID
			}
			m.pendingTaskCursorCh, m.pendingTaskCursorID = "", 0
		}
		m.tasks[msg.ch] = msg.tasks
		if cursorID != 0 {
			// A pending jump target may sit under collapsed parents.
			m.revealTask(msg.ch, cursorID)
			if i := m.taskIndex(msg.ch, cursorID); i >= 0 {
				m.cursor = i
			}
		}
		if msg.ch == m.selName() && len(msg.tasks) > 0 {
			m.cursor = min(m.cursor, m.paneLen(msg.ch)-1)
		}
```

`placeTaskCursor`'s body after resolving `id` becomes:

```go
	m.cursor = -1
	if id != 0 {
		m.revealTask(ch, id)
		m.cursor = m.taskIndex(ch, id)
	}
	if m.cursor < 0 && id != 0 {
		m.pendingTaskCursorCh, m.pendingTaskCursorID = ch, id
	}
	m.follow = false
	m.refreshStream()
	m.scrollCursorIntoView()
```

The `right` / `left` cases in `updateNormal`:

```go
	case "right":
		if bus.IsTaskChannel(m.selName()) {
			return m.openTask()
		}
		m.openCursor()
	case "left":
		if bus.IsTaskChannel(m.selName()) {
			m.closeTask()
		} else {
			m.closeCursor()
		}
```

Extend the comment above the `down` case with: `In a task list right opens the task's details, then its children; left hides its children (whole subtree), then its details.`

- [ ] **Step 4: Projection and keys.** In `internal/tui/tasks.go`, replace `cursorTask`, `expandTask`'s doc comment stays, and add after `loadTask`:

```go
// taskRow is one visible row of a task tree.
type taskRow struct {
	t      bus.TaskSummary
	hidden int  // descendants not shown under this row (a summary line follows)
	open   bool // at least one direct child is shown
}

// taskRows projects ch's tree (TaskList's order: a parent before its
// children) onto the rows shown. Children start collapsed (ADR 0011
// decision 3): a task is shown only when its parent is shown with its
// children expanded (m.taskExpanded); otherwise the nearest shown ancestor
// counts it as hidden. A task whose parent is not in the list is a root.
func (m *Model) taskRows(ch string) []taskRow {
	var out []taskRow
	at := map[int64]int{}    // shown task id -> its row index
	under := map[int64]int{} // hidden task id -> the row index counting it
	for _, t := range m.tasks[ch] {
		pi, shown := at[t.Parent]
		hi, hidden := under[t.Parent]
		switch {
		case shown && !m.taskExpanded[t.Parent]:
			out[pi].hidden++
			under[t.ID] = pi
			continue
		case shown:
			out[pi].open = true
		case hidden:
			out[hi].hidden++
			under[t.ID] = hi
			continue
		}
		at[t.ID] = len(out)
		out = append(out, taskRow{t: t})
	}
	return out
}

// taskIndex is id's row index in taskRows(ch), or -1 while it is hidden or
// absent.
func (m *Model) taskIndex(ch string, id int64) int {
	for i, r := range m.taskRows(ch) {
		if r.t.ID == id {
			return i
		}
	}
	return -1
}

// revealTask shows the children of every ancestor of id so its row is
// visible (a search jump or pending cursor into a collapsed subtree).
func (m *Model) revealTask(ch string, id int64) {
	byID := make(map[int64]bus.TaskSummary, len(m.tasks[ch]))
	for _, t := range m.tasks[ch] {
		byID[t.ID] = t
	}
	p := byID[id].Parent
	for n := 0; p != 0 && n < len(byID); n++ { // bounded: the bus forbids cycles, this never trusts it
		m.taskExpanded[p] = true
		p = byID[p].Parent
	}
}

// cursorTaskRow returns the visible row under the cursor in the selected
// task channel.
func (m *Model) cursorTaskRow() (taskRow, bool) {
	rs := m.taskRows(m.selName())
	if m.cursor < 0 || m.cursor >= len(rs) {
		return taskRow{}, false
	}
	return rs[m.cursor], true
}

// cursorTask returns the task at the cursor in the selected task channel.
func (m *Model) cursorTask() (bus.TaskSummary, bool) {
	r, ok := m.cursorTaskRow()
	return r.t, ok
}

// openTask (→) opens the cursor task's details when it has any and they
// are closed; otherwise it shows the task's direct children.
func (m *Model) openTask() tea.Cmd {
	r, ok := m.cursorTaskRow()
	if !ok {
		return nil
	}
	if r.t.HasDetails && !m.taskOpen[r.t.ID] {
		return m.expandTask()
	}
	if r.hidden > 0 {
		m.taskExpanded[r.t.ID] = true
		m.refreshStream()
		m.scrollCursorIntoView()
	}
	return nil
}

// closeTask (←) hides the cursor task's shown children, the whole subtree
// (so the next → shows direct children only); otherwise it closes the
// task's details.
func (m *Model) closeTask() {
	r, ok := m.cursorTaskRow()
	if !ok {
		return
	}
	if !r.open {
		m.collapseTask()
		return
	}
	under := map[int64]bool{r.t.ID: true}
	for _, t := range m.tasks[m.selName()] { // tree order: a parent precedes its children
		if under[t.Parent] {
			under[t.ID] = true
			delete(m.taskExpanded, t.ID)
		}
	}
	delete(m.taskExpanded, r.t.ID)
	m.refreshStream()
	m.scrollCursorIntoView()
}
```

(Delete the old `cursorTask` at lines 36-43; `expandTask` and `collapseTask` stay as the details half.)

- [ ] **Step 5: Render.** Replace `renderTasks` in `internal/tui/tasks.go` (add `"strconv"` to the imports):

```go
// renderTasks draws ch's task tree in place of the revision stream: one row
// per visible task (taskRows; children start collapsed), indented by depth
// (capped at six levels), a tree mark, a status mark, then the row's
// suffixes: an in-progress task's owner with the rail icon and color, a
// pending task's assignee as a dim arrow, blockers, and the lease.
// Completed rows are dimmed whole. The tree mark is ▶ when anything is
// hidden (closed details, hidden children), ▼ when details or children are
// open with nothing hidden, blank otherwise; a row with hidden children is
// followed by a dim "N subtasks" line. The normal-mode cursor highlights
// its row; an expanded task's block (description, blockers, metadata, last
// update) follows it, word-wrapped at the task's indent.
func (m *Model) renderTasks(ch string) string {
	th := m.theme
	dim := th.Style(th.Dim)
	rs := m.taskRows(ch)
	m.cursorLine = -1
	if len(rs) == 0 {
		return dim.Render("no tasks yet in " + ch)
	}
	w := max(m.stream.Width, 20)
	byID := make(map[int64]bus.TaskSummary, len(m.tasks[ch]))
	for _, t := range m.tasks[ch] {
		byID[t.ID] = t
	}
	var b strings.Builder
	lineNum := 0
	for i, r := range rs {
		t := r.t
		mark := taskPending
		switch t.Status {
		case "in_progress":
			mark = taskInProgress
		case "completed":
			mark = taskCompleted
		}
		selected := i == m.cursor && m.mode == modeNormal
		rowDim := dim
		if selected {
			rowDim = th.Style(th.Text)
		}
		indent := strings.Repeat("  ", min(t.Depth, 6))
		detailsOpen := t.HasDetails && m.taskOpen[t.ID]
		var treeMark string
		switch {
		case (t.HasDetails && !detailsOpen) || r.hidden > 0:
			treeMark = rowDim.Render(markSel + " ")
		case detailsOpen || r.open:
			treeMark = rowDim.Render(markOpen + " ")
		default:
			treeMark = "  "
		}
		prefix := indent + treeMark
		body := mark + "#" + itoa(t.ID) + " " + t.Subject
		var suffix string
		switch {
		case t.Status == "in_progress" && t.Owner != "":
			icon, style := iconAgent, th.Style(th.Agent)
			if t.Owner == m.c.as {
				icon, style = iconUser, th.Style(th.User)
			}
			body += "  " + icon + style.Render(t.Owner)
		case t.Status == "pending" && t.Owner != "":
			suffix += iconArrow + t.Owner
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
			body += rowDim.Render(suffix)
		}
		if t.Status == "completed" {
			body = rowDim.Render(body)
		}
		pw := lipgloss.Width(prefix)
		line := prefix + body
		if r.hidden > 0 {
			summary := strconv.Itoa(r.hidden) + " subtasks"
			if r.hidden == 1 {
				summary = "1 subtask"
			}
			line += "\n" + strings.Repeat(" ", pw) + rowDim.Render(summary)
		}
		line = lipgloss.NewStyle().MaxWidth(w).Render(line)
		if selected {
			line = th.Highlight(th.Sel, line, w)
		}
		if i == m.cursor {
			m.cursorLine = lineNum
		}
		b.WriteString(line + "\n")
		lineNum += strings.Count(line, "\n") + 1
		if detailsOpen {
			block := m.renderTaskBlock(t, byID, pw+2, w)
			b.WriteString(block + "\n")
			lineNum += strings.Count(block, "\n") + 1
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
```

- [ ] **Step 6: Help.** In `internal/tui/help.go`:

```go
		{"(task lists)", "tasks/ channels show the task tree: → opens a task's details, then its subtasks; ← hides its subtasks, then its details; read-only"},
```

- [ ] **Step 7: Run the TUI tests, then the gate.** `go test ./internal/tui -count=1` (the search jump tests and the icon width-parity test use flat lists and pass unchanged), then the full gate.

- [ ] **Step 8: Commit.**

```bash
git add internal/tui/model.go internal/tui/tasks.go internal/tui/help.go internal/tui/tasks_test.go internal/tui/tasktree_test.go
git commit -m "feat(tui): task lists nest like threads: children collapse, → opens details then subtasks

taskRows projects TaskList's tree onto the visible rows; the cursor,
paneLen and the jump paths index it, and a jump expands the target's
ancestors. ▶ marks anything hidden, a dim N subtasks line counts hidden
children. ADR 0011 decision 3."
```

---

### Task 6: Task editing: `space` cycles a draft, `enter` saves, `esc` cancels, `t` takes, `u` unassigns; hint toasts; `space` leaves message lists

**Files:**
- Modify: `internal/tui/model.go:106-108,166-176,322-336` (`draft`, `toastHint`; `dropStaleDraft` at the end of `Update`), `model.go:360-363` (insert `enter`), `model.go:398-530` (`i`, `enter`, `esc`, `" "`, `r`, `t`, `u`), `model.go:936-944` (`onBatch` revision discard), `model.go:1110-1116` (`showToast`, new `showHint`)
- Modify: `internal/tui/tasks.go:13-15` (remove `tasksReadOnlyToast`; add draft types, toasts and key actions; `renderTasks` draft hunks)
- Modify: `internal/tui/view.go:219-221,598-601` (toast line; new `toastLine`)
- Modify: `internal/tui/help.go` (`esc`, `enter`, `space`, `t`, `u`, task lists lines)
- Modify: `internal/tui/tasks_test.go:81-109` (`TestTaskChannelIsReadOnly` → `TestTaskChannelHasNoCompose`), `internal/tui/thread_test.go:57-89` (`space` → `right`), `internal/tui/body_test.go:90-93` (`space` is a no-op)
- Test: `internal/tui/taskedit_test.go` (new)

**Interfaces:**
- Consumes: `cursorTask`, `taskRows`, `renderTasks` (Task 5); `Theme.Unsaved` (Task 3); `bus.TaskClaim(as, id, leasedUntil, key)`, `bus.TaskUpdate(as, bus.TaskPatch{ID, Status *string, Owner *string})`, `bus.Message.MemoryID`, `errText`, `loadTasks`.
- Produces: `type taskDraft struct{ ch string; id int64; status, owner string }`; `Model.draft taskDraft`; `Model.toastHint bool`; consts `draftHintToast`, `draftDiscardedToast`, `unassignRefusedToast`; `func ownedToast(owner string) string`; `func (m *Model) taskEditable(t bus.TaskSummary) bool`; `func (m *Model) shownTask(ch string, t bus.TaskSummary) (bus.TaskSummary, bool)`; `func (m *Model) cycleTaskState() tea.Cmd`; `func (m *Model) saveDraft() tea.Cmd`; `func (m *Model) dropDraft() tea.Cmd`; `func (m *Model) dropStaleDraft() tea.Cmd`; `func (m *Model) takeTask() tea.Cmd`; `func (m *Model) unassignTask() tea.Cmd`; `func (m *Model) showHint(s string) tea.Cmd`; `func (m Model) toastLine() string`. `tasksReadOnlyToast` is removed.

Behavior (spec section 5, plus decision 4 for message lists):
- Editable: `t.Owner == "" || t.Owner == m.c.as`. Anything else: `space`, `t`, `u` show `owned by <owner>; only unassigned tasks or yours can be changed here` (error style).
- `space` cycles the shown state `pending → in_progress → completed → pending`, starting from the row as shown (draft applied). On a task unassigned on the bus, the draft owner is `m.c.as` while the drafted status is not `pending`, else `""`. A draft equal to the saved (status, owner) is cleared. Otherwise the row's body renders in `Theme.Unsaved` and the hint `enter saves · esc cancels` shows.
- `enter` with a draft saves synchronously: drafted `in_progress` → `TaskClaim(m.c.as, id, 0, "")` (no lease; the bus reclaims on the TUI's heartbeat like any agent's); anything else → `TaskUpdate` with `Status` and `Owner` both set to the draft's values (the owner is sent every time so the saved task matches the row: a bare `status: pending` would make the bus drop the assignment the row shows). Success clears the draft and reloads the tree; a refusal shows `task: <code>: <message>` (error style) and keeps the draft. `enter` with no draft does nothing on a task channel.
- `esc` with a draft clears only the draft. Without one, it behaves as today.
- The draft is dropped with the hint `change discarded` when, after any message, the cursor row of the focused task pane is no longer the drafted task (cursor moved, focus left, channel changed, the task vanished), and when a receive batch carries a revision of the drafted task (`MemoryID == draft.id` on its channel).
- `t` on a task channel: hint if a draft is pending; owned-by-other toast; no-op if already yours; else `TaskUpdate{Owner: m.c.as}` then reload. `u`: hint if a draft is pending; owned-by-other toast; no-op if unassigned; `only a not-started task can be unassigned` unless `pending`; else `TaskUpdate{Owner: ""}` then reload.
- `i` and `r` on a task channel, and `enter` in insert mode there, do nothing (no toast). `space` in a message list does nothing.
- Toasts: `showToast` is the red error with `✗`; `showHint` is the same toast in `Theme.Text` without the prefix. Same lifetime and key dismissal.

- [ ] **Step 1: Write the failing tests.** Create `internal/tui/taskedit_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/muesli/termenv"
)

// openTasks creates tasks/work with unassigned tasks of the given subjects,
// selects it and focuses the stream (cursor on the last task).
func (f *fixture) openTasks(t *testing.T, subjects ...string) []bus.Task {
	t.Helper()
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	var out []bus.Task
	for _, s := range subjects {
		tk, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: s})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tk)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.key("tab")
	return out
}

// task reads id back from the bus as the TUI.
func (f *fixture) task(t *testing.T, id int64) bus.Task {
	t.Helper()
	tk, err := f.c.b.TaskGet(f.c.as, id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// ansiProfile forces real escape codes for color assertions.
func ansiProfile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// TestSpaceCyclesADraftAndAssignsAnUnassignedTask (ADR 0011 decisions 2,
// 6): space drafts pending → in progress → completed → pending, taking the
// task while off pending; the row shows the draft in the unsaved color
// with a hint toast; nothing reaches the bus; a full cycle clears it.
func TestSpaceCyclesADraftAndAssignsAnUnassignedTask(t *testing.T) {
	ansiProfile(t)
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	unsaved, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Unsaved).Render("\x00"), "\x00")
	f.key(" ")
	if d := f.m.draft; d.ch != "tasks/work" || d.id != a.ID || d.status != "in_progress" || d.owner != f.c.as {
		t.Fatalf("draft after one space = %+v", d)
	}
	if f.m.toast != draftHintToast || !f.m.toastHint {
		t.Fatalf("toast = %q hint=%v", f.m.toast, f.m.toastHint)
	}
	raw := f.m.renderStream()
	if !strings.Contains(raw, unsaved+taskInProgress) || !strings.Contains(ansi.Strip(raw), f.c.as) {
		t.Fatalf("drafted row must show in progress with the TUI's name in the unsaved color: %q", raw)
	}
	if got := f.task(t, a.ID); got.Status != "pending" || got.Owner != "" {
		t.Fatalf("a draft must not touch the bus: %+v", got)
	}
	f.key(" ")
	if d := f.m.draft; d.status != "completed" || d.owner != f.c.as {
		t.Fatalf("draft after two = %+v", d)
	}
	if raw = f.m.renderStream(); !strings.Contains(raw, unsaved+taskCompleted) {
		t.Fatalf("drafted completed row is unsaved-colored, not dim: %q", raw)
	}
	f.key(" ")
	if f.m.draft.id != 0 {
		t.Fatalf("cycling back to the saved state clears the draft: %+v", f.m.draft)
	}
	if raw = f.m.renderStream(); strings.Contains(raw, unsaved+taskPending) {
		t.Fatalf("color returns to normal: %q", raw)
	}
}

// TestEnterSavesTheDraftAsAClaimThenAnUpdate: into in progress is a claim
// (owner and status), the rest a task_update carrying status and owner;
// the tree refreshes after each save and reopening your task keeps it yours.
func TestEnterSavesTheDraftAsAClaimThenAnUpdate(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key(" ")
	f.key("enter")
	if f.m.draft.id != 0 || f.m.toast != "" {
		t.Fatalf("enter clears the draft quietly: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	if got := f.task(t, a.ID); got.Status != "in_progress" || got.Owner != f.c.as {
		t.Fatalf("enter must claim: %+v", got)
	}
	if got, ok := f.m.cursorTask(); !ok || got.Status != "in_progress" || got.Owner != f.c.as {
		t.Fatalf("the tree refreshes after a save: %+v", got)
	}
	f.key(" ") // completed
	f.key("enter")
	if got := f.task(t, a.ID); got.Status != "completed" || got.Owner != f.c.as {
		t.Fatalf("complete: %+v", got)
	}
	f.key(" ") // back to pending, still yours
	if d := f.m.draft; d.status != "pending" || d.owner != f.c.as {
		t.Fatalf("reopening a task you own keeps the assignment in the draft: %+v", d)
	}
	f.key("enter")
	if got := f.task(t, a.ID); got.Status != "pending" || got.Owner != f.c.as {
		t.Fatalf("reopen keeps it yours: %+v", got)
	}
	f.key("enter")
	if f.m.toast != "" {
		t.Fatalf("enter with no draft does nothing: %q", f.m.toast)
	}
}

// TestBusRefusalKeepsTheDraftForEsc: b is blocked, so claiming it is
// refused; the bus's message shows in the error style (hint style does not
// stick), the draft stays, and esc cancels only the draft.
func TestBusRefusalKeepsTheDraftForEsc(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	_, _, b := f.taskTree(t) // cursor on b, blocked by a1
	f.key(" ")
	if f.m.toast != draftHintToast || !f.m.toastHint {
		t.Fatalf("hint first: %q", f.m.toast)
	}
	f.key("enter")
	if !strings.Contains(f.m.toast, "blocked") || f.m.toastHint {
		t.Fatalf("refusal toast = %q hint=%v, want the bus's message in the error style", f.m.toast, f.m.toastHint)
	}
	if f.m.draft.id != b.ID {
		t.Fatalf("a refused save keeps the draft: %+v", f.m.draft)
	}
	if got := f.task(t, b.ID); got.Status != "pending" || got.Owner != "" {
		t.Fatalf("nothing saved: %+v", got)
	}
	f.key("esc")
	if f.m.draft.id != 0 || f.m.cursor < 0 {
		t.Fatalf("esc cancels the draft and only the draft: draft=%+v cursor=%d", f.m.draft, f.m.cursor)
	}
	if got := f.task(t, b.ID); got.Status != "pending" {
		t.Fatalf("esc writes nothing: %+v", got)
	}
}

// TestDraftIsDiscardedOnCursorMoveFocusChangeAndRevision (decision 2): the
// draft goes, with "change discarded", when the cursor leaves its row, when
// focus leaves the task pane, and when the bus shows a new revision of the
// drafted task; a revision of another task leaves it alone.
func TestDraftIsDiscardedOnCursorMoveFocusChangeAndRevision(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "a", "b") // cursor on b
	f.key(" ")
	f.key("up")
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast || !f.m.toastHint {
		t.Fatalf("cursor move: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	f.key(" ") // draft on a
	f.key("home")
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast {
		t.Fatalf("focus change: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	f.key("tab") // stream again, cursor on the last task (b)
	f.key(" ")
	drafted, ok := f.m.cursorTask()
	if !ok || f.m.draft.id != drafted.ID {
		t.Fatalf("draft=%+v cursor=%+v", f.m.draft, drafted)
	}
	other := ts[0].ID
	if other == drafted.ID {
		other = ts[1].ID
	}
	desc := "changed elsewhere"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: other, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	if f.m.draft.id != drafted.ID {
		t.Fatalf("a revision of another task keeps the draft: %+v", f.m.draft)
	}
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: drafted.ID, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast {
		t.Fatalf("revision of the drafted task: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
}

// TestDraftIsDiscardedWhenTheTaskDisappears: the drafted task drops out of
// the next tree; the cursor clamps and the draft is discarded.
func TestDraftIsDiscardedWhenTheTaskDisappears(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "a", "b") // cursor on b
	f.key(" ")
	f.send(tasksMsg{ch: "tasks/work", tasks: []bus.TaskSummary{{ID: ts[0].ID, Subject: "a", Status: "pending"}}})
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast || f.m.cursor != 0 {
		t.Fatalf("draft=%+v toast=%q cursor=%d", f.m.draft, f.m.toast, f.m.cursor)
	}
}

// TestAgentOwnedTaskCannotBeChanged (decision 1): a task owned by anyone
// else, in progress or merely assigned, refuses space, t and u with the
// owned-by toast and never drafts.
func TestAgentOwnedTaskCannotBeChanged(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "theirs", "assigned") // cursor on "assigned"
	if _, err := f.ab.TaskClaim(f.sam, ts[0].ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	owner := f.sam
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: ts[1].ID, Owner: &owner}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	want := ownedToast(f.sam)
	for _, id := range []int64{ts[1].ID, ts[0].ID} {
		for _, k := range []string{" ", "t", "u"} {
			f.key(k)
			if f.m.toast != want || f.m.toastHint || f.m.draft.id != 0 {
				t.Fatalf("task %d key %q: toast=%q hint=%v draft=%+v", id, k, f.m.toast, f.m.toastHint, f.m.draft)
			}
		}
		f.key("up")
	}
	if got := f.task(t, ts[0].ID); got.Owner != f.sam || got.Status != "in_progress" {
		t.Fatalf("untouched: %+v", got)
	}
	if got := f.task(t, ts[1].ID); got.Owner != f.sam || got.Status != "pending" {
		t.Fatalf("untouched: %+v", got)
	}
}

// TestTakeAndUnassign (decision 6): t takes an unassigned task at once
// without changing its state; u clears your not-started task and refuses
// any other state; both only hint while a draft is pending; u on an
// unassigned task does nothing.
func TestTakeAndUnassign(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key("u")
	if f.m.toast != "" {
		t.Fatalf("u on an unassigned task does nothing: %q", f.m.toast)
	}
	f.key("t")
	if f.m.prompt.active {
		t.Fatal("t in a task list is take, not the tag prompt")
	}
	if got := f.task(t, a.ID); got.Owner != f.c.as || got.Status != "pending" {
		t.Fatalf("take: %+v", got)
	}
	if got, ok := f.m.cursorTask(); !ok || got.Owner != f.c.as {
		t.Fatalf("the tree refreshes after take: %+v", got)
	}
	f.key("t")
	if got := f.task(t, a.ID); got.Owner != f.c.as || f.m.toast != "" {
		t.Fatalf("t on your own task is a no-op: %+v toast=%q", got, f.m.toast)
	}
	f.key("u")
	if got := f.task(t, a.ID); got.Owner != "" || got.Status != "pending" {
		t.Fatalf("unassign: %+v", got)
	}
	f.key(" ")
	f.key("enter") // in progress, yours
	f.key("u")
	if f.m.toast != unassignRefusedToast || f.m.toastHint {
		t.Fatalf("u on an in-progress task: toast=%q", f.m.toast)
	}
	if got := f.task(t, a.ID); got.Owner != f.c.as {
		t.Fatalf("refused unassign changed the task: %+v", got)
	}
	f.key(" ") // draft: completed
	for _, k := range []string{"t", "u"} {
		f.key(k)
		if f.m.toast != draftHintToast || !f.m.toastHint || f.m.draft.id != a.ID {
			t.Fatalf("%q while a draft is pending: toast=%q draft=%+v", k, f.m.toast, f.m.draft)
		}
	}
	if got := f.task(t, a.ID); got.Status != "in_progress" {
		t.Fatalf("t/u while a draft is pending change nothing: %+v", got)
	}
}
```

In `internal/tui/tasks_test.go`, replace `TestTaskChannelIsReadOnly` with:

```go
// TestTaskChannelHasNoCompose: a task list has no compose line, so i and r
// do nothing there (no toast: the read-only toast is gone, ADR 0011
// decision 6), enter without a draft does nothing, tab never reaches
// compose, and d still asks before deleting the channel.
func TestTaskChannelHasNoCompose(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")

	for _, k := range []string{"i", "enter", "r"} {
		f.key(k)
		if f.m.mode != modeNormal || f.m.toast != "" {
			t.Fatalf("key %q: mode %v toast %q", k, f.m.mode, f.m.toast)
		}
	}

	f.key("tab")
	if f.m.pane() != paneChannels {
		t.Fatalf("tab moved pane to %v, want paneChannels", f.m.pane())
	}

	f.key("d")
	if f.m.mode != modeConfirmChannel {
		t.Fatalf("d mode = %v, want modeConfirmChannel", f.m.mode)
	}
}
```

In `internal/tui/thread_test.go`, rename `TestNewReplyPeeksThenSpaceToggles` to `TestNewReplyPeeksThenRightShowsChildren`, replace `f.key(" ")` with `f.key("right")` and its failure text with `"right on the root shows direct children only: %v"`. In `internal/tui/body_test.go` `TestRightOpensBodyThenRepliesLeftClosesRepliesThenBody`, the tail becomes:

```go
	f.key(" ")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"step one"}) || strings.Contains(f.stream(), "step one") {
		t.Fatalf("space no longer shows or hides replies (ADR 0011 decision 4): %v\n%s", got, f.stream())
	}
```

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/tui -run 'TestSpaceCycles|TestEnterSaves|TestBusRefusal|TestDraftIs|TestAgentOwned|TestTakeAndUnassign|TestTaskChannelHasNoCompose|TestNewReplyPeeks|TestRightOpensBody' -count=1` fails to compile (`f.m.draft` undefined).

- [ ] **Step 3: Draft state, toasts and actions.** In `internal/tui/tasks.go`, delete the `tasksReadOnlyToast` const and its comment (lines 13-15) and add after `loadTask`:

```go
// taskDraft is the one unsaved state change space builds on a task row
// (ADR 0011 decision 2): the task, and the status and owner its row shows
// until enter saves or anything else discards it. id 0 means no draft.
type taskDraft struct {
	ch     string
	id     int64
	status string
	owner  string
}

const (
	draftHintToast       = "enter saves \u00b7 esc cancels"
	draftDiscardedToast  = "change discarded"
	unassignRefusedToast = "only a not-started task can be unassigned"
)

// ownedToast is the refusal for a task some other identity owns: the bus
// has no user/agent kind, so only the TUI's own identity counts as the
// user (ADR 0011 decision 1).
func ownedToast(owner string) string {
	return "owned by " + owner + "; only unassigned tasks or yours can be changed here"
}

// nextStatus is space's cycle.
var nextStatus = map[string]string{"pending": "in_progress", "in_progress": "completed", "completed": "pending"}

// taskEditable reports whether the TUI may change t: unassigned, or its own.
func (m *Model) taskEditable(t bus.TaskSummary) bool {
	return t.Owner == "" || t.Owner == m.c.as
}

// shownTask is t as its row shows it, with the draft applied when t holds
// it; drafted reports which.
func (m *Model) shownTask(ch string, t bus.TaskSummary) (shown bus.TaskSummary, drafted bool) {
	if m.draft.id == t.ID && m.draft.ch == ch {
		t.Status, t.Owner = m.draft.status, m.draft.owner
		return t, true
	}
	return t, false
}

// cycleTaskState (space) drafts the cursor task's next state: pending →
// in progress → completed → pending, from whatever the row shows. A task
// unassigned on the bus is drafted as the TUI's own while off pending; a
// draft that lands back on the saved status and owner is dropped.
func (m *Model) cycleTaskState() tea.Cmd {
	t, ok := m.cursorTask()
	if !ok {
		return nil
	}
	if !m.taskEditable(t) {
		return m.showToast(ownedToast(t.Owner))
	}
	ch := m.selName()
	shown, _ := m.shownTask(ch, t)
	d := taskDraft{ch: ch, id: t.ID, status: nextStatus[shown.Status], owner: shown.Owner}
	if t.Owner == "" {
		d.owner = m.c.as
		if d.status == "pending" {
			d.owner = ""
		}
	}
	if d.status == t.Status && d.owner == t.Owner {
		m.draft = taskDraft{}
		m.refreshStream()
		return nil
	}
	m.draft = d
	m.refreshStream()
	return m.showHint(draftHintToast)
}

// saveDraft (enter) writes the draft as the TUI's identity: into in
// progress is a claim (TaskClaim, so the lease follows the TUI's
// heartbeat like an agent's), anything else a task_update carrying the
// drafted status and owner (both, so the saved task matches the row: a
// bare status pending would make the bus drop the assignment). The call
// is synchronous, like toggleSubscribe's, so no receive batch can race it.
// A refusal shows the bus's message and keeps the draft for esc.
func (m *Model) saveDraft() tea.Cmd {
	d := m.draft
	var err error
	if d.status == "in_progress" {
		_, err = m.c.b.TaskClaim(m.c.as, d.id, 0, "")
	} else {
		status, owner := d.status, d.owner
		_, err = m.c.b.TaskUpdate(m.c.as, bus.TaskPatch{ID: d.id, Status: &status, Owner: &owner})
	}
	if err != nil {
		return m.showToast("task: " + errText(err))
	}
	m.draft = taskDraft{}
	return m.loadTasks(d.ch)
}

// dropDraft discards the draft with the "change discarded" hint.
func (m *Model) dropDraft() tea.Cmd {
	m.draft = taskDraft{}
	m.refreshStream()
	return m.showHint(draftDiscardedToast)
}

// dropStaleDraft discards the draft once its row is no longer the cursor
// row of the focused task pane: the cursor moved, focus left, the channel
// changed, or the task vanished from the tree (ADR 0011 decision 2). Update
// runs it after every message.
func (m *Model) dropStaleDraft() tea.Cmd {
	if m.draft.id == 0 {
		return nil
	}
	if t, ok := m.cursorTask(); ok && m.pane() == paneStream && m.selName() == m.draft.ch && t.ID == m.draft.id {
		return nil
	}
	return m.dropDraft()
}

// takeTask (t in a task list) assigns an unassigned cursor task to the TUI
// at once, without changing its state.
func (m *Model) takeTask() tea.Cmd {
	t, ok := m.cursorTask()
	if !ok {
		return nil
	}
	if m.draft.id != 0 {
		return m.showHint(draftHintToast)
	}
	if !m.taskEditable(t) {
		return m.showToast(ownedToast(t.Owner))
	}
	if t.Owner != "" {
		return nil // already yours
	}
	as := m.c.as
	if _, err := m.c.b.TaskUpdate(as, bus.TaskPatch{ID: t.ID, Owner: &as}); err != nil {
		return m.showToast("task: " + errText(err))
	}
	return m.loadTasks(m.selName())
}

// unassignTask (u in a task list) clears the owner of the TUI's own
// not-started cursor task at once.
func (m *Model) unassignTask() tea.Cmd {
	t, ok := m.cursorTask()
	if !ok {
		return nil
	}
	if m.draft.id != 0 {
		return m.showHint(draftHintToast)
	}
	if !m.taskEditable(t) {
		return m.showToast(ownedToast(t.Owner))
	}
	if t.Owner == "" {
		return nil
	}
	if t.Status != "pending" {
		return m.showToast(unassignRefusedToast)
	}
	none := ""
	if _, err := m.c.b.TaskUpdate(m.c.as, bus.TaskPatch{ID: t.ID, Owner: &none}); err != nil {
		return m.showToast("task: " + errText(err))
	}
	return m.loadTasks(m.selName())
}
```

In `renderTasks`, the first line of the loop body `t := r.t` becomes `t, drafted := m.shownTask(ch, r.t)`, and the completed dimming becomes:

```go
		switch {
		case drafted:
			body = th.Style(th.Unsaved).Render(body) // unsaved (ADR 0011 section 5)
		case t.Status == "completed":
			body = rowDim.Render(body)
		}
```

Add to `renderTasks`'s doc comment: `A row holding the draft shows the drafted status and owner in the unsaved color.`

- [ ] **Step 4: Model fields, toast styles, key cases.** In `internal/tui/model.go`, `Model` gains after `toastSeq`:

```go
	toastHint  bool // the toast is a hint (text color, no error prefix), not an error
	draft      taskDraft // the one unsaved task state change; id 0 for none
```

Replace `showToast` and add `showHint`:

```go
// showToast shows s as an error (red, ✗ prefix) for toastFor.
func (m *Model) showToast(s string) tea.Cmd {
	m.toast = s
	m.toastHint = false
	m.toastSeq++
	m.layout()
	seq := m.toastSeq
	return tick(toastFor, func(time.Time) tea.Msg { return toastClearMsg{seq: seq} })
}

// showHint is showToast in the hint style: the text color, no prefix.
func (m *Model) showHint(s string) tea.Cmd {
	cmd := m.showToast(s)
	m.toastHint = true
	return cmd
}
```

At the end of `Update`, before `return m, tea.Batch(cmds...)`:

```go
	// A draft lives only on the cursor row of the focused task pane (ADR
	// 0011 decision 2): whatever the message above did, if that is no longer
	// where the draft is, the draft goes.
	if cmd := m.dropStaleDraft(); cmd != nil {
		cmds = append(cmds, cmd)
	}
```

In `updateInsert`, the `enter` case's task-channel guard becomes `if bus.IsTaskChannel(m.selName()) { return nil }`. In `updateNormal`:

```go
	case "i":
		if bus.IsTaskChannel(m.selName()) {
			return nil // no compose in a task list
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		m.mode = modeInsert
		return m.compose.Focus()
	// enter performs the pane's action: save the task draft in a task list,
	// reply to the cursor message in the stream, compose to the selected
	// channel otherwise.
	case "enter":
		if bus.IsTaskChannel(m.selName()) {
			if m.draft.id != 0 {
				return m.saveDraft()
			}
			return nil
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		if r, ok := m.cursorRow(); ok {
			if !m.replyAllowed() {
				return m.showToast(replyRefusedToast)
			}
			m.replyTo = &r.msg
			m.layout()
		}
		m.mode = modeInsert
		return m.compose.Focus()
	case "esc":
		if m.draft.id != 0 {
			m.draft = taskDraft{} // cancel the draft, and only the draft
			m.refreshStream()
			return nil
		}
		m.replyTo = nil
		m.cursor = -1
		m.layout()
```

```go
	case " ":
		// Not a show/hide key anywhere (ADR 0011 decision 4): the arrows do
		// that; in a task list space cycles the state.
		if bus.IsTaskChannel(m.selName()) {
			return m.cycleTaskState()
		}
	case "r":
		if bus.IsTaskChannel(m.selName()) {
			return nil // no compose in a task list
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		if !m.replyAllowed() {
			return m.showToast(replyRefusedToast)
		}
		if rs := m.rows(m.selName()); m.cursor >= 0 && m.cursor < len(rs) {
			target := rs[m.cursor].msg
			m.replyTo = &target
		} else if n := len(rs); n > 0 {
			target := rs[n-1].msg
			m.replyTo = &target
		}
		m.layout()
		m.mode = modeInsert
		return m.compose.Focus()
```

```go
	case "t":
		// Rail: follow a tag set. Task list: take (ADR 0011 decision 7).
		switch m.pane() {
		case paneChannels, paneSessions:
			return m.tagPrompt()
		case paneStream:
			if bus.IsTaskChannel(m.selName()) {
				return m.takeTask()
			}
		}
	case "u":
		if bus.IsTaskChannel(m.selName()) {
			return m.unassignTask()
		}
```

In `onBatch`, the task-channel branch becomes:

```go
		if bus.IsTaskChannel(ch) {
			cmds = append(cmds, m.loadTasks(ch))
			// A revision of the drafted task on the bus discards the draft
			// (ADR 0011 decision 2). Saves are synchronous and clear the
			// draft first, so this is always someone else's change.
			if m.draft.id != 0 && ch == m.draft.ch {
				for _, x := range ms {
					if x.MemoryID != nil && *x.MemoryID == m.draft.id {
						cmds = append(cmds, m.dropDraft())
						break
					}
				}
			}
		}
```

- [ ] **Step 5: Toast rendering.** In `internal/tui/view.go`, `View` uses `parts = append(parts, m.toastLine())` in place of the `Error`-styled line, `overlay` uses `footer = m.toastLine() + "\n" + footer`, and add after `View`:

```go
// toastLine renders the toast: an error in red with the ✗ prefix, or a
// hint (draft guidance) in the text color with no prefix.
func (m Model) toastLine() string {
	if m.toastHint {
		return m.theme.Style(m.theme.Text).Render(m.toast)
	}
	return m.theme.Style(m.theme.Error).Render(iconError + m.toast)
}
```

- [ ] **Step 6: Help.** In `internal/tui/help.go` `helpLines`:

```go
		{"esc", "leave compose; cancel a task draft; clear the message cursor"},
		{"i", "compose (in the sessions pane, sends a direct message)"},
		{"enter", "reply to the cursor message, else compose; in a task list, save the draft"},
```

```go
		{"space", "task list: cycle the cursor task's state (not started, in progress, completed) as a draft"},
```

```go
		{"t", "rail: follow a tag set: <tag>[,<tag>...] (AND); task list: take the unassigned cursor task"},
		{"u", "task list: unassign your not-started cursor task"},
```

```go
		{"(task lists)", "tasks/ channels show the task tree: → opens a task's details, then its subtasks; ← hides its subtasks, then its details; only unassigned tasks or yours can be changed"},
```

- [ ] **Step 7: Run the TUI tests, then the gate.** `go test ./internal/tui -count=1`, then the full gate. `golangci-lint` flags an unused `tasksReadOnlyToast`; it must be gone.

- [ ] **Step 8: Commit.**

```bash
git add internal/tui/model.go internal/tui/tasks.go internal/tui/view.go internal/tui/help.go internal/tui/tasks_test.go internal/tui/thread_test.go internal/tui/body_test.go internal/tui/taskedit_test.go
git commit -m "feat(tui): change task state from the TUI: space drafts, enter saves, esc cancels, t takes, u unassigns

One draft at a time, shown in the unsaved color with a hint toast;
enter claims (into in progress) or updates through the task API as the
TUI's identity, and a refusal keeps the draft. Only unassigned tasks or
the TUI's own can change. Cursor move, focus change, channel change and
a bus revision discard the draft. space is no longer a show/hide key in
message lists. ADR 0011 decisions 1, 2, 4, 6."
```

---

### Task 7: Docs: install.md keys, README, release notes for 1.8.0

**Files:**
- Modify: `docs/install.md:225-247` (TUI keys paragraph)
- Modify: `README.md:79-80`
- Modify: `release/notes-v1.8.0.md` (TUI section)

**Interfaces:**
- Consumes: the key and theme names from Tasks 3-6.
- Produces: nothing.

- [ ] **Step 1: install.md.** In the TUI bullet of `docs/install.md`, replace the sentences from `Arrow keys never` through `carrying all of its tags; \`s\` on it unfollows).` with:

```
  Arrow keys never
  change pane: `↑`/`↓` move within the focused one, `→` opens the selected
  message's body if it's closed and has one, else shows its direct replies;
  `←` hides shown replies (the whole subtree), else closes the body. On a
  memory, `.` (or `>`) shows the next older version and `,` (or `<`) the
  next newer one, labeled `r<revision> · <n> kept`; any other key returns
  it to the latest. `enter` replies to the selected message, or opens
  compose from the channel list. Colors come from the `theme` and `themes`
  config settings; see below. The first TUI launch after upgrading
  subscribes to every existing inbox from its oldest retained message, so
  retained direct messages show as unread once.
  A `tasks/` channel shows its task tree: subtasks start hidden under
  their parent (`N subtasks`), `→` opens a task's details, then its
  subtasks, `←` hides its subtasks, then its details; ❎ pending, ⏱️ in
  progress (with the owner's name beside it), ✅ completed (dimmed); a
  blocked task keeps ❎ with `blocked by #n`. You can change tasks that are
  unassigned or yours (the TUI's identity): `space` cycles the selected
  task's state (not started, in progress, completed) as a draft shown in
  the `unsaved` color, `enter` saves it (a move into in progress claims the
  task), `esc` cancels it; moving the cursor or leaving the pane discards
  it. `t` takes an unassigned task, `u` unassigns your not-started one. A
  task owned by an agent cannot be changed here. From the channel or
  session list, `t` follows a tag set (a `tags` section lists yours; select
  one to see every chat message carrying all of its tags; `s` or `d` on it
  unfollows, no confirmation).
```

- [ ] **Step 2: README.** In `README.md`, `\`agentbus tui\` renders a list as a read-only tree.` becomes:

```
`agentbus tui` shows a list as a tree and lets you change the state and
ownership of tasks that are unassigned or your own.
```

- [ ] **Step 3: Release notes.** In `release/notes-v1.8.0.md`, replace the `## TUI` section's third bullet (the one starting `` `→` opens the cursor message's body ``) so it no longer claims `space` toggles replies, and append bullets, so the section reads:

```
## TUI

- Message rows collapse to the header plus one line: the subject, or the
  content's first line cut to the pane width. This applies to channels,
  DM inboxes, the merged DM pane and tag panes.
- `→` opens the cursor message's body (subject line and full content),
  then its direct replies; `←` hides its replies, then its body. `▶`
  marks anything hidden, `▼` an open body. `space` no longer shows or
  hides replies; the arrow keys do that everywhere.
- Stepping memory versions (`.`/`>` and `,`/`<`) shows each version's
  subject and keeps the body open. The label is now `r<revision> · <n>
  kept`: the real revision number and how many revisions survive the
  tombstone purge.
- Search hit rows show the subject when there is one.
- Compose is unchanged: messages sent from the TUI have no subject and
  show their first line.
- Task lists nest like threads: subtasks start hidden under their parent
  with an `N subtasks` line; `→` opens a task's details, then its
  subtasks; `←` hides its subtasks, then its details. A search jump
  expands the target's ancestors.
- Task lists are no longer read-only. On a task that is unassigned or
  yours (the TUI's identity), `space` cycles the state (not started, in
  progress, completed) as a draft shown in the new `unsaved` color,
  `enter` saves it (into in progress claims the task, so the lease follows
  the TUI's heartbeat), `esc` cancels it; moving the cursor, leaving the
  pane, switching channels, or a new revision of the task on the bus
  discards it. `t` takes an unassigned task; `u` unassigns your
  not-started task. The bus's rules are authoritative: a refusal shows
  its message and keeps the draft. Tasks owned by agents cannot be changed
  from the TUI (ADR 0011).
- `t` follows a tag set only from the channel or session list. A blank
  line separates the channels from the `tags` section. `d` on a tag set
  unfollows it like `s`, with no confirmation, and the selection moves to
  the next set, else the previous, else the last channel.
- The rail's selected channel or session row uses the new theme key
  `selection_inactive` (default `brightblack`) while the messages or
  compose pane has focus, so only the focused pane's row is `selection`
  blue. New theme key `unsaved` (default `yellow`) colors a drafted task
  row.
- The `nerdfont` icon set's completed task glyph is U+F14A
  (`fa-square-check`), the same size as the pending and in-progress ones.
```

- [ ] **Step 4: Gate, then commit.** Run the full gate (docs only, but the gate is the rule), then:

```bash
git add docs/install.md README.md release/notes-v1.8.0.md
git commit -m "docs: TUI task editing, task tree, rail keys and theme keys in install, README and 1.8.0 notes"
```

The version bump, tag, `release/release.sh v1.8.0`, the tap update and `PROGRESS.md` stay with the controller and the user.

---

## Self-review notes

- Spec coverage: section 1 (Task 1); section 2 (Task 2); section 3 (Task 3); section 4 message lists (Task 6 `space`, Task 7 docs) and task lists (Task 5); section 5 in full — who can change what, `space`, `enter` claim vs update, `esc`, losing a draft (cursor, focus, channel, revision), `t`, `u`, hint toast style, read-only toast removed (Task 6); section 6 (Task 4); rollout (Task 7). Every item under the spec's "Testing" maps to a named test above.
- Interpretations made where the spec meets the code (listed for the report): the drafted owner is sent on every `TaskUpdate` save (not only when it changed) so the saved task equals the row; "a task-list refresh shows the drafted task's revision changed" is detected on the receive batch that triggers that refresh (`MemoryID == draft.id`), since `TaskSummary` carries no revision; `i`, `r` and insert-mode `enter` on a task list become silent no-ops once the read-only toast goes; `t` on your own task and `u` on an unassigned task are no-ops; the hidden-child count is a dim second line under the row, like a thread's `N replies`; a task whose parent is absent from the list is shown as a root.
- Names used across tasks: `newFixtureWith` (2); `Theme.SelInactive`, `Theme.Unsaved`, `Highlight(bg, line, width)`, `SelBG` (3); `unfollowTagPane` (4); `taskRow`, `taskRows`, `taskIndex`, `revealTask`, `cursorTaskRow`, `openTask`, `closeTask`, `taskExpanded` (5); `taskDraft`, `draft`, `toastHint`, `showHint`, `toastLine`, `cycleTaskState`, `saveDraft`, `dropDraft`, `dropStaleDraft`, `takeTask`, `unassignTask`, `shownTask`, `taskEditable`, `ownedToast`, `draftHintToast`, `draftDiscardedToast`, `unassignRefusedToast` (6) — each defined once and referenced by that name elsewhere.
