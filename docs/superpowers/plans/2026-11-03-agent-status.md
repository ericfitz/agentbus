# Agent Status and History Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each agent sets a one-line, tagged status. The bus keeps the last N
statuses per identity. The TUI shows every agent's status on a board, and
shows each agent's history.

**Architecture:**
- **Storage:** a new `statuses` table (schema 13) behind
  `internal/bus/status.go`.
- **Agent surface:** two new MCP tools, plus a `status` field on
  `discover`/`register` `others`.
- **CLI:** two new commands.
- **Stop hook:** a nudge for stale statuses.
- **TUI:** rail icons, plus a board and history view in the main pane.

**Tech Stack:** Go, `modernc.org/sqlite`, the MCP Go SDK (`mcp.AddTool`), and
Bubble Tea and lipgloss for the TUI.

**Spec:** `docs/superpowers/specs/2026-11-03-agent-status-design.md`. Read it
before any task; its section numbers are cited below.

## Global Constraints

- **Settable tags,** exactly, in this order: `idle`, `planning`,
  `implementing`, `reviewing`, `waiting`, `blocked`, `needs-human`. `offline`
  is derived when read and never stored.
- **`text`:** trimmed, 1-200 runes, one line, no control characters.
- **`problem`:** trimmed, 0-200 runes, the same line rules.
- **Bad-tag error text, verbatim:** `tag must be one of idle, planning,
  implementing, reviewing, waiting, blocked, needs-human (offline is set by
  the bus)`.
- **Subagent error text, verbatim:** `subagents do not set a status; the
  top-level session's status covers them`.
- **Config:**

  | Key | Default | Range |
  | --- | --- | --- |
  | `status_history_count` | 20 | 1-200 |
  | `status_offline_hours` | 24 | 0-720 |
  | `status_stale_minutes` | 30 | 0-1440 |

- **Working tags** (the ones the nudge checks): `planning`, `implementing`,
  `reviewing`.
- **Ages** are formatted as `<n>m`, `<n>h` or `<n>d`, whichever is the largest
  whole unit (under 1 minute is `0m`), by `bus.ShortAge`.
- **Truncation** uses the existing `…` helpers (`cut`, `ansi.Truncate`).
- **Spelling:** American English. Run `gofmt`.
- **Done gate:** `make verify`.
- **Commits:** every commit ends with the session's attribution lines.

## Review Focus

1. **A status set from an MCP process whose session expired:** `SetStatus`
   must fail `not_registered`. It must not write a status for a name the
   caller no longer holds. Test in Task 1.
2. **Text containing a terminal escape sequence or wide runes:** validation
   rejects control characters, and the board cuts by display width, not by
   bytes. Tests in Tasks 1 and 7.
3. **An identity renamed with `rename: true`:** the old name shows as
   offline, and the new name shows as never set. Test in Task 2.
4. **An offline row whose DM channel does not exist yet:** selecting it must
   not error. Test in Task 6.
5. **A Stop hook when the bus database is locked or unavailable:** it fails
   open, with no output and exit 0. Test in Task 4.

---

### Task 1: Storage, config and the bus write/read API

**Files:**
- Modify: `internal/bus/schema.go`: add `statuses` DDL (spec §4) to `schema`;
  set `schemaVersion = 13`.
- Modify: `internal/bus/migrate.go`: add `12: addTables, // statuses (issue #43)`.
- Modify: `internal/config/config.go`: add three fields, defaults and bounds.
- Create: `internal/bus/status.go`, `internal/bus/status_test.go`.
- Modify: `internal/bus/migrate_test.go`, `internal/config/*_test.go`.

**Interfaces:**
- Produces:
  - `type AgentStatus struct { Identity string \`json:"identity"\`; Revision int64 \`json:"revision"\`; Tag string \`json:"tag"\`; Text string \`json:"text"\`; Problem string \`json:"problem,omitempty"\`; SetAt int64 \`json:"set_at"\` }`
  - `var SettableStatusTags = []string{"idle","planning","implementing","reviewing","waiting","blocked","needs-human"}`
  - `const StatusOffline = "offline"`
  - `func (b *Bus) SetStatus(as, tag, text, problem string) (AgentStatus, error)`:
    - runs `auth` on the transaction (MCP);
    - rejects a subagent name.
  - `func (b *Bus) SetStatusFor(name, tag, text, problem string) (AgentStatus, error)`:
    - for the CLI;
    - requires a live session for `name` (the `SessionLive` rule), else a
      `not_registered` error;
    - rejects a subagent name.
  - `func (b *Bus) LatestStatus(name string) (*AgentStatus, error)`: nil and
    no error when the name has no status.
  - `func (b *Bus) StatusHistory(name string) ([]AgentStatus, bool, error)`:
    oldest first; the bool reports whether the name is live.
  - `func (b *Bus) Authorize(as string) error`: `b.auth(b.db, as)`, exported
    for read-only MCP tools.
  - `func ShortAge(d time.Duration) string`.
  - Config fields: `StatusHistoryCount`, `StatusOfflineHours`,
    `StatusStaleMinutes`, with JSON keys as in Global Constraints.

- [ ] **Step 1: Write failing tests** in `status_test.go`, `migrate_test.go`
  and the config tests:
  - `TestSetStatusValidation`:
    - each settable tag succeeds;
    - each of these fails with code `validation` and the verbatim bad-tag
      text: `""`, `"offline"`, `"Idle"`, `"done"`;
    - text `""`, `"   "`, `"a\nb"`, `"a\x1b[31m"`, and 201 × `"é"` fail;
    - 200 × `"é"` succeeds;
    - problem with `"\n"` fails; 201 runes fail;
    - `as="Sam/helper"` fails with the verbatim subagent text.
  - `TestSetStatusRevisionsAndPrune`: with `status_history_count=3`, five
    sets leave revisions 3, 4, 5 in `StatusHistory`, oldest first; a sixth
    set gets revision 6.
  - `TestSetStatusIdenticalIsNewRevision`: two identical sets give revisions
    1 and 2.
  - `TestSetStatusConcurrent`: 10 goroutines set for one name, and the
    revisions are 1..10 with none missing (`status_history_count=20`).
  - `TestSetStatusExpiredSession`: delete the session row, then `SetStatus`
    returns `not_registered` and `LatestStatus` is still the old one.
  - `TestSetStatusForNeedsLiveSession`: a name with no session row returns
    `not_registered`.
  - `TestStatusHistoryUnknownName`: an empty list, `false`, no error.
  - `TestShortAge`: `30s→"0m"`, `3m→"3m"`, `59m→"59m"`, `5h→"5h"`,
    `49h→"2d"`.
  - `TestMigrate12To13`: a version-12 database migrates to 13, has
    `statuses`, and a second `Open` is a no-op.
  - Config: the defaults are 20/24/30; `status_history_count=0` and
    `status_offline_hours=721` are rejected the way the other bounds are.
- [ ] **Step 2:** Run `go test ./internal/bus/ ./internal/config/ -run 'Status|Migrate12|ShortAge|Config'`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.**
  - **Shared write path.** One unexported `setStatus(tx, name, …)` is shared
    by both entry points:
    1. trim the inputs, then validate them;
    2. `INSERT ... SELECT COALESCE(MAX(revision),0)+1`;
    3. `DELETE ... WHERE identity=? AND revision <= ? - status_history_count`.
  - **Transactions.** Use the bus's existing write-transaction helper.
    `SetStatus` calls `auth(tx, as)` inside the transaction.
  - **Bounds.** The config bounds go in the existing `bounds` table.
- [ ] **Step 4:** Rerun the tests from Step 2. Expected: PASS. Then run `go test ./internal/...`.
- [ ] **Step 5: Commit** `feat(bus): statuses table and status write/read API (#43)`.

### Task 2: Board query and status in discover/register

**Files:**
- Modify: `internal/bus/status.go`: add `Board`.
- Modify: `internal/bus/sessions.go`: add `Status` to `Session`.
- Modify: `internal/bus/admin.go`: `liveSessions` fills `Status` with one
  extra query.
- Test: `internal/bus/status_test.go`, `internal/bus/sessions_test.go`.

**Interfaces:**
- Consumes: `AgentStatus`, `LatestStatus`, `ShortAge` (Task 1).
- Produces:
  - `type BoardEntry struct { AgentStatus; Live bool \`json:"live"\` }`
  - `func (b *Bus) Board() ([]BoardEntry, error)`.
  - `Session.Status *AgentStatus \`json:"status,omitempty"\``.

- [ ] **Step 1: Write failing tests.**
  - `TestBoard`:
    - a live name with a status gives `Live=true` and the stored tag;
    - a live name with no status gives `Tag==""` and `Revision==0`;
    - an ended session whose status was set 23h ago gives `Live=false`,
      `Tag=="offline"`, with `Text` kept;
    - one set 25h ago is absent;
    - with `status_offline_hours=0`, no offline entries;
    - subagent sessions are absent.
  - `TestBoardRename`: after a `rename: true` register from Sam to Pat, Sam
    is offline with its last text, and Pat is live with `Tag==""`.
  - `TestDiscoverIncludesStatus`: `Discover` shows `Status` for a name that
    set one and nil for one that did not, and offline names are not listed.
- [ ] **Step 2:** Run `go test ./internal/bus/ -run 'Board|DiscoverIncludesStatus'`. Expected: FAIL.
- [ ] **Step 3: Implement `Board`.**
  - Take the latest revision per identity (`MAX(revision)` grouped).
  - Left-join it with the live sessions, judged by the same cutoff as
    `liveSessions`.
  - Map the tag to `offline` when the entry is not live.
  - Filter offline entries by `set_at >= now - status_offline_hours`.
  - Exclude names containing `/`.
  - The result is unsorted.
- [ ] **Step 4:** Rerun the tests from Step 2. Expected: PASS. Then run `go test ./internal/bus/`.
- [ ] **Step 5: Commit** `feat(bus): status board and status in discover/others (#43)`.

### Task 3: MCP tools

**Files:**
- Modify: `internal/mcpserver/server.go`: add `set_status` and
  `status_history`, using the existing `asIn`/`result` patterns.
- Create: `internal/mcpserver/status_test.go`.

**Interfaces:**
- Consumes: `SetStatus`, `StatusHistory`, `Authorize` (Task 1);
  `Session.Status` (Task 2).
- Produces:
  - Tool `set_status`, with input
    `{as string, tag string, text string, problem string (optional)}`; it
    returns the `AgentStatus` JSON.
  - Tool `status_history`, with input `{as string, name string}`; it returns
    `{"name","live","statuses":[...]}`.

- [ ] **Step 1: Write failing tests.**
  - `TestSetStatusTool`: a round trip returns revision 1, and a bad tag
    returns an error result whose text contains the verbatim bad-tag text.
  - `TestStatusHistoryTool`: after 3 sets, `statuses` has 3 entries, oldest
    first, and `live` is true; for an unknown name, `statuses` is `[]`.
  - `TestRegisterOthersStatus`: a second session's `register` `others` entry
    carries `status.tag`.
  - `TestSetStatusUnparks`: following `park_test.go`'s pattern, park, call
    `set_status`, and the session is no longer parked.
- [ ] **Step 2:** Run `go test ./internal/mcpserver/ -run 'Status'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - **`set_status` description:** `Agentbus: set your one-line status: what
    you are doing now. tag is one of idle, planning, implementing, reviewing,
    waiting, blocked, needs-human (offline is set by the bus when you have no
    live session). text: one line, 1-200 characters. problem: optional, one
    line, up to 200 characters; pass it with blocked or needs-human. Every
    call is a new revision; the bus keeps the last status_history_count.
    Subagents do not set a status.`
  - **`status_history` description:** `Agentbus: an identity's last
    statuses, oldest first, and whether it is live.`
  - **Unparking:** check how the existing tools unpark; `set_status` must
    use the same path.
- [ ] **Step 4:** Rerun the tests from Step 2. Expected: PASS.
- [ ] **Step 5: Commit** `feat(mcp): set_status and status_history tools (#43)`.

### Task 4: CLI commands and the Stop hook nudge

**Files:**
- Create: `internal/cli/statuscmd.go`, `internal/cli/statuscmd_test.go`.
- Modify: `main.go`: add `case "set-status"` and `case "statuses"` (follow
  `park`'s flag and identity pattern), plus the help text.
- Modify: `internal/cli/stophook.go`, `internal/cli/stophook_test.go`.
- Modify: `README.md`: the CLI command list and a short "Status" section.

**Interfaces:**
- Consumes: `SetStatusFor`, `Board`, `StatusHistory`, `LatestStatus`,
  `ShortAge`, `Parked`.
- Produces:
  - `func SetStatusCmd(cfg config.Config, as, cwd, tag, text, problem string, out, warn io.Writer) error`
    (`as` is resolved by `parkIdentity`).
  - `func Statuses(cfg config.Config, name, sort string, asJSON bool, out io.Writer) error`.
  - `func SortBoard(e []bus.BoardEntry, by string) error` (`by` is one of
    `time`, `name` or `tag`; any other value is an error naming the three).
    Shared with the TUI.
  - `func nudgeLine(st *bus.AgentStatus, now time.Time, staleMin int) string`:
    `""` when there is no nudge.

- [ ] **Step 1: Write failing tests.**
  - `TestSetStatusCmdJoinsWords`: `set-status implementing fix the board`
    stores `"fix the board"`, and the output is `status for <name>:
    revision 1`.
  - `TestStatusesTable`: three entries, by default sorted newest first; with
    `-sort name`, alphabetical; with `-sort tag`, alphabetical by tag, ties
    broken by name.
  - `TestStatusesBadSort`: `-sort age` is an error listing
    `time, name, tag`.
  - `TestStatusesJSON`: `-json` gives a valid array of `BoardEntry`.
  - `TestStatusesHistory`: the `<name>` form prints revisions oldest first.
  - `TestStopHookNudge`, table-driven, with 31m and 29m relative to
    `status_stale_minutes=30`:
    - `implementing` 31m old gives one block whose reason starts with
      `Agentbus: your status is "implementing: <text>" (31m); if that is no
      longer true, call set_status.`;
    - 29m old gives no output;
    - `idle` and `waiting` 2h old give no output;
    - a top-level name with no status gives the reason
      `Agentbus: you have no status; call set_status.`;
    - a parked name gives no output;
    - `stop_hook_active=true` gives no output;
    - `status_stale_minutes=0` gives no output;
    - a 120-rune text is cut to 80 with `…`;
    - pending messages plus a stale status give one block: the nudge, a
      space, then the existing message reason.
  - `TestStopHookNudgeFailsOpen`: a config pointing at an unreadable data
    directory gives no output and no panic.
- [ ] **Step 2:** Run `go test ./internal/cli/ -run 'SetStatusCmd|Statuses|StopHookNudge'`. Expected: FAIL.
- [ ] **Step 3: Implement the commands and the nudge.**
  - **Table output:** `text/tabwriter` with these columns: tag (or `—`),
    name, age, text, problem. The CLI prints the tag word, not the icon.
  - **Stop hook:**
    1. Compute the nudge after the identity resolves and before `Wait`.
    2. When there are no messages, the nudge alone is the reason.
    3. When there are messages, the reason is `nudge + " " + head + tail`.
- [ ] **Step 4:** Rerun the tests from Step 2. Expected: PASS. Then run `go test ./...` and `go build ./...`.
- [ ] **Step 5: Commit** `feat(cli): set-status, statuses, and the Stop hook status nudge (#43)`.

### Task 5: Protocol text and ADRs

**Files:**
- Modify: `internal/cli/skills/using-agentbus/SKILL.md`: add a "Status"
  section after "Session protocol", adding to that section's park line, and
  add one line to "Subagents".
- Modify: `internal/cli/identity.go`: the SessionStart protocol block
  (around the existing `agentbus park` lines).
- Modify: `internal/cli/agent_text_test.go` and/or
  `internal/bus/agent_text_test.go`, whichever pins this text.
- Modify: `docs/adr/0026-agent-status.md`: add "v1 decisions (Eric,
  2026-11-03)" with spec §3 decisions 1-7.
- Modify: `docs/adr/0024-rail-sections.md`: add an "Amendment (Eric,
  2026-11-03)" with spec §3 decision 8; the Status line points to it.

- [ ] **Step 1: Write failing tests.** The agent-text tests assert:
  - SKILL.md contains `## Status`, every settable tag, `set_status`,
    `status_history`, and `Set \`idle\` before \`agentbus park\``;
  - the SessionStart text contains `set_status` and `idle` before
    `agentbus park`.
- [ ] **Step 2:** Run `go test ./internal/cli/ ./internal/bus/ -run AgentText`. Expected: FAIL.
- [ ] **Step 3: Write the text.**
  - **Base:** ADR 0026's "Protocol text", with v1 names. `discover`'s
    `others` shows status; there is no "board" for agents.
  - **Tag table:** ADR 0026's table.
  - **SessionStart lines, verbatim:** `- Call set_status (tag, one-line
    text, optional problem) at the start of each task and when the kind of
    work changes; set idle when you have nothing in hand.` Then add to the
    park sentence: `set idle with set_status first, then run "agentbus
    park"`.
- [ ] **Step 4:** Rerun the tests from Step 2. Expected: PASS.
- [ ] **Step 5: Commit** `docs: status protocol text, ADR 0026 v1 decisions, ADR 0024 amendment (#43)`.

### Task 6: TUI data, icons, rail rows and header

**Files:**
- Modify: `internal/tui/icons.go`: add 8 `status_*` icons to `iconVars`,
  `emojiIcons` (via the variables' initial values) and `nerdIcons`.
- Modify: `internal/tui/msgs.go`: add `board []bus.BoardEntry` to
  `statusMsg`.
- Modify: `internal/tui/model.go`:
  - `statusCmd` also calls `c.b.Board()`;
  - `onStatus` stores `m.board`;
  - `sessionNames` adds offline board names.
- Modify: `internal/tui/view.go`: the sessions rail row prefix, dimmed
  offline rows, and the stream header for a selected session.
- Tests: `internal/tui/icons_test.go`, `internal/tui/rail_test.go`,
  `internal/tui/view_test.go`.

**Interfaces:**
- Consumes: `Board`, `BoardEntry`, `ShortAge`.
- Produces:
  - `m.board []bus.BoardEntry`.
  - `func (m *Model) statusOf(name string) (bus.BoardEntry, bool)`.
  - `func statusIcon(tag string) string`: `""` maps to `iconAgent`.

- [ ] **Step 1: Write failing tests.**
  - `TestStatusIconsMapped`: every settable tag and `offline` has a non-empty
    emoji icon and Nerd Font icon, and `icon_map` `{"status_blocked":"X"}`
    makes `statusIcon("blocked")=="X"`.
  - `TestRailSessionStatusIcon`: a live session with `implementing` renders
    its icon before its name.
  - `TestRailOfflineRow`: an offline board entry appears in the sessions
    section, dimmed, and selecting it opens `dm/<name>` with no error even
    though that channel does not exist (Review Focus 4).
  - `TestHeaderShowsStatus`: with a session selected, the header contains
    `implementing · <text> · <problem>`, cut with `…` at width 80.
- [ ] **Step 2:** Run `go test ./internal/tui/ -run 'StatusIcons|RailSessionStatus|RailOffline|HeaderShowsStatus'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Pick the emoji defaults; the spec leaves the
  glyphs to the builder:

  | Tag | Emoji |
  | --- | --- |
  | idle | 💤 |
  | planning | 📝 |
  | implementing | 🔨 |
  | reviewing | 🔍 |
  | waiting | ⏳ |
  | blocked | ⛔ |
  | needs-human | 🙋 |
  | offline | ⏻ |

  Pick the Nerd Font equivalents from Font Awesome, with a comment naming
  each glyph, as the existing entries do.
- [ ] **Step 4:** Rerun the tests from Step 2. Expected: PASS. Then run `go test ./internal/tui/`.
- [ ] **Step 5: Commit** `feat(tui): status icons in the sessions rail and header (#43)`.

### Task 7: TUI board view

**Files:**
- Create: `internal/tui/board.go`, `internal/tui/board_test.go`.
- Modify: `internal/tui/model.go`:
  - the key handling for `b` and `o`, and left, right, up and down while the
    board has focus;
  - showing the board when the sessions heading is selected.
- Modify: `internal/tui/view.go`: the main pane renders `m.renderBoard()`
  while the sessions heading is selected.
- Modify: `internal/tui/help.go`: add `b`, `o`, board left/right, `enter`
  and `esc`.

**Interfaces:**
- Consumes: `m.board`, `statusIcon`, `cli.SortBoard`. If importing `cli`
  from `tui` would create a cycle, move `SortBoard` to `bus`: check with
  `go list -deps`.
- Produces:
  - `type boardState struct { sort string; sel int; expanded map[string]bool; history string /* name, "" when off */ }`, held as `m.boardState`.
  - `func (m *Model) boardActive() bool`: true while the sessions heading is
    selected.
  - `func (m *Model) renderBoard(width, height int) string`.

- [ ] **Step 1: Write failing tests.**
  - `TestBoardOpensFromHeading`: up/down onto the sessions heading makes
    `boardActive()` true and the main pane shows the board rows; selecting
    any channel row restores that channel's stream.
  - `TestBoardOpensFromB`: from a channel, `b` selects the sessions heading
    and shows the board.
  - `TestBoardSortCycle`: the order cycles time → name → tag → time; time is
    newest first, name is alphabetical, and tag is alphabetical with ties
    broken by name.
  - `TestBoardExpandCollapse`: right on a row with 150-rune text renders
    wrapped lines below it; left folds them back.
  - `TestBoardTruncation`: at 80x24, a row with long text and a wide-rune
    problem stays within 80 display columns and ends in `…` (Review
    Focus 2).
  - `TestBoardEmpty`: no entries renders `no agents`.
  - `TestHeadingLegacyUnchanged`: the channels, memories, tasks and tags
    headings still leave the last selected content in the main pane.
- [ ] **Step 2:** Run `go test ./internal/tui/ -run 'Board|HeadingLegacy'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - **Columns:** icon, name, tag, age, then text and problem filling the
    rest. Text gets 60% of the rest.
  - **Keys:**
    - `tab` focuses the board, using the stream's pane slot.
    - `o` is handled only while the board is active and the health overlay
      is closed.
    - `b` is global except in compose.
- [ ] **Step 4:** Rerun the tests from Step 2. Expected: PASS. Then run `go test ./internal/tui/`.
- [ ] **Step 5: Commit** `feat(tui): status board on the sessions heading and b (#43)`.

### Task 8: TUI history view, docs and done gate

**Files:**
- Modify: `internal/tui/board.go`, `internal/tui/board_test.go`: the history
  view.
- Modify: `internal/tui/model.go`: a `historyMsg`, loaded through
  `c.b.StatusHistory`.
- Modify: `docs/` config reference (wherever the `task_expiry_hours` key is
  documented: `rg -n task_expiry_hours docs README.md`): the three new
  keys.

**Interfaces:**
- Consumes: `StatusHistory` (Task 1); `boardState.history` (Task 7).
- Produces: `func (m *Model) renderHistory(width, height int) string`.

- [ ] **Step 1: Write failing tests.**
  - `TestBoardEnterOpensHistory`: `enter` on a row with 3 revisions renders
    them oldest first, each with revision, local time `15:04`, icon, tag,
    text and problem.
  - `TestHistoryEscReturns`: `esc` returns to the board with the same row
    selected.
  - `TestHistoryScroll`: 20 revisions at height 10 respond to pgdn and to
    `g`/`G`.
  - `TestHistoryOffline`: an offline identity's history loads, and its
    header says `offline`.
- [ ] **Step 2:** Run `go test ./internal/tui/ -run 'History'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Write the config docs entries.
- [ ] **Step 4:** Rerun the tests from Step 2. Expected: PASS.
- [ ] **Step 5: Run the done gate.** Run `make verify`; it must pass with no
  failures. Then run `graphify update .` if `graphify-out/` exists.
- [ ] **Step 6: Commit** `feat(tui): status history view; docs for status config (#43)`.
