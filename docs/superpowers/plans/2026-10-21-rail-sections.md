# Rail Sections, Collapsing and v1.16.0 Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the TUI rail into five collapsible sections with short names, shorten task-list expiry to 14 days, fix the selected idle-session row, make compose `down` clear the draft, and prepare v1.16.0.

**Architecture:** `m.channels` stays one index space (`m.sel`), but `setChannels` orders it by section (channels, memories, tasks, tags), so each section is a contiguous range. The rail becomes five `railSpan`s; a new heading selection (`m.headSel`) and `m.collapsed` sit beside `m.sel`/`m.sessSel`. Layout stays in `railBudgets`/`railWindow`, extended with the short-screen rule.

**Tech Stack:** Go, Bubble Tea, bubbles/textarea, lipgloss v1.1.0, charmbracelet/x/ansi.

**Spec:** `docs/superpowers/specs/2026-10-21-rail-sections-design.md` (read it; it is the authority).

## Global Constraints

- Done gate: `make verify` (build, vet, vet-cross, gofmt, lint, unit tests) green after every task.
- Work on `main`, commit per task, never push. Commit trailers:
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_01X53Znpvz44fEKpn5uq6CFX`.
- Section order: channels, memories, tasks, tags, sessions.
- Prefixes hidden only in rail rows: `general/`, `memory/`, `tasks/`. Bare `general`, `memory`, `tasks` shown in full. Same short name twice in one section: both rows show full names.
- `TaskExpiryHours` default 336; `TaskIdleHours` stays 168.
- Collapse state is in-memory only; every start shows all sections expanded.
- Glyphs: collapsed heading `markSel` (▶), expanded heading `markOpen` (▼); both honor `icon_map`.
- American English; match surrounding comment density and style (the TUI files document invariants in doc comments).

## Review Focus

1. 80x24 terminal (rail = 21 rows) with Eric's mix (6 channels, 19 memories, 4 tasks, 0 tags, 4 sessions): every heading and at least one item of each non-empty section visible, nothing clipped. Test in Task 5.
2. A status refresh (`setChannels`) while a heading is selected or a section collapsed: headSel, collapse and the shown channel survive; a channel deleted by expiry while selected falls back cleanly. Test in Task 6.
3. Selected rows of every type stay one line with the nerd icon set at rail widths 12 and 24. Test in Task 3, extended to headings in Task 6.
4. Collapsing the section that holds the shown channel moves the selection to its heading and the stream keeps showing that channel. Test in Task 6.
5. Compose `down` on a multi-line draft with the cursor above the last line does not clear it. Test in Task 2.

---

### Task 1: 14-day task expiry default

**Files:**
- Modify: `internal/config/config.go:71` (and the doc comment near :26 if it states 30 days)
- Modify: `README.md:67`, `docs/install.md:265-269,319`, `docs/adr/0013-per-effort-task-channels-and-expiry.md` (add one line under Status: "Default `task_expiry_hours` changed to 336 (14 days) by ADR 0024.")
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Failing test** `TestDefaultTaskExpiry14Days`: `Default().TaskExpiryHours == 336` and `Default().TaskIdleHours == 168`.
- [ ] **Step 2:** `go test ./internal/config -run TestDefaultTaskExpiry14Days` fails (720).
- [ ] **Step 3:** Set the default to 336; update every doc mention of the 30-day/720 default (`rg -n '720|30 days' README.md docs internal/config`), leaving specs and release notes of past versions alone.
- [ ] **Step 4:** Test passes; `make verify` green.
- [ ] **Step 5:** Commit `feat(config): task lists expire after 14 days idle by default`.

### Task 2: Compose `down` clears the draft

**Files:**
- Modify: `internal/tui/model.go:414-419` (compose key switch), `internal/tui/help.go` (compose keys)
- Test: `internal/tui/compose_test.go`

- [ ] **Step 1: Failing tests** using the existing fixture helpers (`newFixture`, `f.key`):
  - `TestComposeDownClearsDraft`: type "hello", press `down` → `compose.Value() == ""`.
  - `TestComposeDownAfterRecallClears`: send "hi", `up` (draft "hi"), `down` → "".
  - `TestComposeDownMultilineMovesCursor`: draft "a" alt+enter "b", move cursor to line 0 (`up`), `down` → value still "a\nb"; `down` again (now on last line) → "".
  - `TestComposeDownEmptyStaysEmpty`.
- [ ] **Step 2:** Run `go test ./internal/tui -run TestComposeDown`; first, second and fourth fail or pass as expected for current behavior (record which).
- [ ] **Step 3:** Add `case "down":` — when `m.compose.Line() == m.compose.LineCount()-1`, `m.compose.Reset(); m.fitCompose(); return nil`; otherwise fall through to the textarea. Add `down  clear the draft` beside the `up` line in help.go.
- [ ] **Step 4:** Tests pass; `make verify` green.
- [ ] **Step 5:** Commit `feat(tui): down clears the compose draft`.

### Task 3: Selected idle-session row loses its name (bug)

**Files:**
- Modify: whichever of `internal/tui/view.go:539-560`, `internal/tui/theme.go:121-149`, `internal/tui/icons.go` the root cause is in
- Test: `internal/tui/rail_test.go`

Use superpowers:systematic-debugging. Symptom (spec section 5): with `"icons": "nerd"` (Eric's config), a DM peer seen only through CLI calls shows in sessions as idle icon + `name age`; selecting it shows only the idle icon; selecting another row restores it.

- [ ] **Step 1: Reproduce in a failing test** `TestSelectedIdleSessionKeepsName`: fixture with the nerd icon set applied (as `config` `icons: nerd` does), an idle session `cli-bot` (in `sessionsSeen`, not in `status.Sessions`), select it with the rail focused and unfocused, at rail widths 12, 16, 24. Assert `f.railLines()` contains a line with both `iconIdle` and `cli-bot`, and the rail has the same number of lines as with it unselected. If this does not reproduce, widen: DM inbox with unread count, emoji icon set, `RailSel` output measured by `ansi.StringWidth` vs `rail`, the stream header (`header_icon_test.go`). Do not fix anything before a test fails for the reported reason.
- [ ] **Step 2:** Record the root cause in the commit message.
- [ ] **Step 3:** Fix at the root. Whatever the cause, the selected row must never wrap: `RailSel`/`Highlight` output is exactly one line.
- [ ] **Step 4: Regression test** `TestSelectedRowsStayOneLine`: for each row type (chat, memory, task, tag, live session, idle session), each icon set (emoji, nerd), rail widths 12 and 24, focused and unfocused: rendered selected row has no `\n` and `ansi.StringWidth == rail`.
- [ ] **Step 5:** Search for the same pattern elsewhere (other `style.Width(...).Render` on content that can exceed width; `rg -n 'Width\(' internal/tui`) and fix or list them in the commit message.
- [ ] **Step 6:** `make verify` green; commit `fix(tui): selected idle session row keeps its name`.

### Task 4: Five sections with short names

**Files:**
- Modify: `internal/tui/view.go:341-357` (list constants, fixed rows, drop `railTagHint`), `:429-456` (`railSpans`), `:474-562` (`renderRails`); `internal/tui/model.go:1163-1200` (`setChannels` ordering), `:73` (`railTop`)
- Test: `internal/tui/rail_test.go`, `internal/tui/view_test.go`

**Interfaces (produces):**
- `const (secChannels = iota; secMemories; secTasks; secTags; secSessions; railSections)` replacing `listChannels/listTags/listSessions/railLists`.
- `func channelSection(c bus.Channel) int` — `secTags` for `isTagPane(c.Name)`, `secTasks` for `bus.IsTaskChannel`, `secMemories` for `Kind == "memory"`, else `secChannels`.
- `func shortName(name string, sec int) string` — strips `general/`, `memory/`, `tasks/` for their sections; full name otherwise.
- `func (m Model) railLabels() map[string]string` (or equivalent) — short names with the collision rule applied per section.
- `func (m Model) sectionRange(sec int) (lo, hi int)` — the `[lo,hi)` slice of `m.channels` for sections secChannels..secTags.
- `railTop [railSections]int`; `railSpans()` returns `[railSections]railSpan`.

- [ ] **Step 1: Failing tests:**
  - `TestChannelSection`: `general`→channels, `general/x`→channels, `ops/x`→channels, `memory`→memories, `memory/tmi`→memories, `tasks/aws`→tasks (with Kind "memory"), tag pane→tags.
  - `TestShortName` table from spec section 3 (all seven rows).
  - `TestShortNameCollision`: ordinary `agentbus` and `general/agentbus` both render full; memory-kind `tmi` and `memory/tmi` both render full.
  - `TestRailSectionsOrder`: fixture with `general`, `general/agentbus`, `memory`, `memory/tmi`, `tasks/aws-eip-logging`, one session → stripped rail lines in order: `channels`, `general`, `agentbus`, `memories`, `memory`, `tmi`, `tasks`, `aws-eip-logging`, `tags`, `sessions`, … (icons stripped by matching with `strings.Contains`).
  - `TestEmptySectionHeadingOnly`: no tag sets → `tags` heading followed by the blank separator/next heading; no `press t to create` anywhere.
  - Update `TestRailBudgets` literals to five entries.
- [ ] **Step 2:** Run `go test ./internal/tui -run 'TestChannelSection|TestShortName|TestRailSections|TestEmptySection'`; fails (undefined).
- [ ] **Step 3:** Implement. In `setChannels`, sort by `(channelSection, Name)` so sections are contiguous (tags still appended last); keep the selection-by-name rematch. Render the five sections under headings `channels`, `memories`, `tasks`, `tags`, `sessions` with rows built as today but labeled with the short name; `railFixedRows` becomes 9 (5 headings + 4 blanks). Up/down still walk `m.channels` then sessions, as today (headings arrive in Task 6). Update doc comments that say "three lists".
- [ ] **Step 4:** All `internal/tui` tests pass (fix existing assertions that matched full names or the tag hint, keeping their intent); `make verify` green.
- [ ] **Step 5:** Commit `feat(tui): split the rail into channels, memories and tasks with short names`.

### Task 5: Short-screen layout

**Files:**
- Modify: `internal/tui/view.go` `railBudgets`, `railWindow`, `renderRails` (separator rows, combined scroll row)
- Test: `internal/tui/rail_test.go`

**Interfaces:**
- Consumes: Task 4's `railSections`, `railSpans`.
- Produces: `func railLayout(rows int, n [railSections]int, collapsed [railSections]bool) (b [railSections]int, blanks bool, compact bool)` — budgets per section, whether blank separators are drawn, whether compact (one item + combined scroll row) floors are in use. A collapsed section's budget is 0 (its heading row is counted as fixed). Task 6 passes real collapse state; this task passes all false.
- `railWindow` gains a `compact bool` parameter: in compact mode the up and down arrows share one row (`▲2 ▼5 more`, omitting a zero part).

- [ ] **Step 1: Failing tests:**
  - `TestRailLayoutFitsAll`: rows ample → every section gets n, `blanks == true`, `compact == false`.
  - `TestRailLayoutDropsBlanksFirst`: rows = 21 (an 80x24 terminal), n = {6,19,4,0,4} → the 4-row floors need 5 headings + 16 = 21, so `blanks == false`, `compact == false`, each non-empty section ≥ `min(n,4)`.
  - `TestRailLayoutCompact`: rows = 16, same n → `compact == true`, every non-empty section ≥ `min(n,2)`, sum(b) + 5 headings ≤ 16.
  - `TestRailWindowCompactCombinedArrow`: n=19, budget 2, sel 10 → one item row plus one combined arrow row.
  - `TestRail24LinesShowsEverySection`: fixture at 80x24 with Eric's mix → stripped rail has all five headings, at least one item under each non-empty section, `len(lines) <= m.stream.Height+1`, and the last session is reachable by `down` (selected and visible).
- [ ] **Step 2:** Run them; fail.
- [ ] **Step 3:** Implement the three-step rule from spec section 1 (drop blanks → compact floor → MaxHeight clip as last resort); leftover rows still go round-robin.
- [ ] **Step 4:** Pass; `make verify` green.
- [ ] **Step 5:** Commit `feat(tui): fit five rail sections on a 24-line terminal`.

### Task 6: Selectable, collapsible headings

**Files:**
- Modify: `internal/tui/model.go` (Model fields near :70-73; key handling :479-519 and the rail's `enter`; `pane()` :639; `paneKey`/`focusPane` :659-700; `setChannels`), `internal/tui/view.go` (`renderRails`, `railSpans`)
- Test: `internal/tui/rail_test.go`

**Interfaces:**
- Consumes: Tasks 4-5 (`secX` constants, `sectionRange`, `railLayout`).
- Produces: `headSel int` (-1 = no heading selected; else a section index) and `collapsed [railSections]bool` on `Model`; `func (m Model) railPositions() []railPos` with `type railPos struct{ sec, item int }` (`item == -1` is the heading) giving the up/down order; `func (m *Model) selectRailPos(p railPos) tea.Cmd`; `func (m *Model) toggleSection(sec int, collapse bool)`.

Behavior (spec section 2): headings are rail rows; up/down walk `railPositions()` (heading, its items unless collapsed, next heading, …, sessions); moving onto an item calls `selectChannel`/`selectSession`; moving onto a heading sets `headSel` and leaves `m.sel`/`m.sessSel` (stream unchanged). With a heading selected and the rail focused: `left` collapses, `right` expands, `enter` toggles. On an item, `left`/`right` keep their stream meaning. Only the heading is highlighted while `headSel >= 0`. Collapsing the section that holds the shown item sets `headSel` to it. Collapsed heading: `markSel + "memories (19)"`, plus `", 3 unread"` inside the parentheses when the section's unread total > 0; expanded: `markOpen + "memories"`. `home` selects the channels heading. Tab from the stream returns to the heading if `headSel >= 0`. Any item selection clears `headSel`. New channels/messages never expand a section. `pane()` reports the rail pane while `headSel >= 0`.

- [ ] **Step 1: Failing tests:**
  - `TestRailDownWalksHeadings`: from the channels heading, repeated `down` visits headings and items in order, across all five sections, ending on the last session.
  - `TestHeadingLeftCollapsesRightExpands` and `TestHeadingEnterToggles`.
  - `TestCollapsedHeadingSummary`: 19 memories collapsed → `▶ memories (19)`; with 3 unread → `▶ memories (19, 3 unread)`; with an `icon_map` override for `collapsed` the override glyph is used.
  - `TestCollapsedSectionRowsGoElsewhere`: at 80x24, collapsing memories gives its rows to other sections (more channel/session items shown than before).
  - `TestCollapseShownSectionMovesToHeading`: show `memory/tmi`, collapse memories → `headSel == secMemories`, header still names `memory/tmi`.
  - `TestHeadingSelectedSingleHighlight`: with `headSel` set, exactly one rendered rail line carries the selection background.
  - `TestHeadingSurvivesRefresh`: collapse tasks, select memories heading, call `setChannels` with the same list plus one new task channel → still collapsed, still selected.
  - `TestHomeSelectsChannelsHeading`, `TestTabReturnsToHeading`.
  - `TestCollapseNotPersisted`: new Model from the same config → all expanded.
  - Extend `TestSelectedRowsStayOneLine` (Task 3) with heading rows, collapsed and expanded.
- [ ] **Step 2:** Run; fail.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** All tests pass, including the earlier rail/view tests (update only assertions about the old down-past-last-channel special case, which this generalizes); `make verify` green.
- [ ] **Step 5:** Commit `feat(tui): collapsible rail sections`.

### Task 7: Docs and ADR

**Files:**
- Create: `docs/adr/0024-rail-sections.md`
- Modify: `internal/tui/help.go:31-62`, `docs/install.md:267,274,506-511,524,655`, `README.md:163`, code comments still naming "three lists" (`rg -n 'three lists|channel list' internal/tui`)

- [ ] **Step 1:** ADR 0024 in the format of `docs/adr/0023-wake-grace.md`: Status Accepted 2026-10-21; states that the five sections, their order, collapsing (in-memory only), the short-screen rule (option A), hidden prefixes and the 14-day `task_expiry_hours` default are human decisions (Eric, 2026-10-21), and lists the builder's calls from the spec as Claude's.
- [ ] **Step 2:** Update help, install.md, README to match the shipped behavior (check each claim against the code, not the spec).
- [ ] **Step 3:** `go test ./internal/tui -run Help` passes (help tests may pin text); `make verify` green.
- [ ] **Step 4:** Commit `docs: rail sections, collapsing and 14-day task expiry (ADR 0024)`.

### Task 8: v1.16.0 release commit

**Files:**
- Modify: `internal/mcpserver/server.go:28` (`Version = "1.16.0"`)
- Create: `release/notes-v1.16.0.md` (format of `release/notes-v1.15.0.md`)

- [ ] **Step 1:** Notes sections: rail sections and collapsing; short names; compose `down`; selected idle-session row fix; bare `agentbus` prints usage and exits 0 (edc2814, for winget validation); task lists now expire after 14 days idle, and the first maintenance tick after upgrading deletes every task list idle longer than that (set `task_expiry_hours: 720` to keep the old behavior).
- [ ] **Step 2:** `make verify` green; `go run . version` prints `1.16.0`.
- [ ] **Step 3:** Commit `chore: release v1.16.0`. Do not tag, push or run `release/release.sh`.
