# Agent status and history (issue #43): design

Date: 2026-11-03. Status: approved section by section by Eric on 2026-11-03.

## 1. Goal

Each agent reports, in one line, what it is doing now and, optionally, what is
in its way. A person watching the TUI sees every agent's status at once,
without opening each one, and can read an agent's last N statuses in order.
Issue: ericfitz/agentbus#43.

## 2. Decisions already made (ADR 0026, Eric, 2026-10-27)

Shared with agentbus2 ADR 0014 and not reopened here:

- **Tags.** Eight tags. Seven are settable: `idle`, `planning`,
  `implementing`, `reviewing`, `waiting`, `blocked`, `needs-human`. `offline`
  is bus-only.
- **Offline is derived when read**, from "no live session". It is never
  stored, and agents cannot set it.
- **Retention.** Each write prunes to N, where N is a setting with a default
  of 20.
- **Pull only.** Status changes are not pushed to agents.
- **Revisions.** Every set creates a new revision, even when nothing changed.
- **`problem` is optional** with every tag.
- **Protocol text.** Set status at the start of each task and when the kind
  of work changes. Set `idle` before `park`. Subagents do not set a status.

## 3. Decisions made in this design (Eric, 2026-11-03)

These are human decisions, recorded in ADR 0026's new "v1 decisions" section
and, for decision 8, in ADR 0024.

1. **Storage: a dedicated `statuses` table** (option A). Not chosen:
   memory-style revisions in `messages` on `status/<name>` channels (B), and
   columns on the `sessions` row (C).
2. **Which agents the TUI shows** (option A): live sessions, plus offline
   identities whose last status was set within `status_offline_hours`
   (default 24). Not chosen: every identity that has ever set a status (B),
   and live sessions only (C).
3. **TUI: a status icon in the rail, plus a board in the main pane**
   (option A). The board has two entry points: the `b` key, and selecting
   the sessions heading with up/down. It sorts by time since last update
   (the default, newest first), by name, or by tag. Not chosen: rail and
   header only (B), and a board only (C).
4. **Agent and CLI surface** (option A): the MCP tools `set_status` and
   `status_history`, status in `discover`/`register` `others`, and the CLI
   commands `set-status` and `statuses`. Not chosen: MCP only (B), and no
   `status_history` over MCP (C).
5. **Truncation and expansion.** One-line cells that do not fit are cut with
   `…`. Left and right expand and collapse a board row's full text, the
   builder's choice, made to match how the stream treats messages.
6. **Stop hook nudge** (option B). When an agent stops with a stale working
   status, or with no status at all, the Stop hook asks it to update the
   status. The nudge quotes the current status so the agent never needs to
   query its own status first. Not chosen: protocol text only (A), and
   protocol text now with the nudge later (C).
7. **API versus prompts.** The API enforces what the bus can check: tag
   membership, the shape of `text` and `problem`, and top-level identities
   only. The skill suggests what only the agent can judge: which tag fits and
   when to change it. Transitions between tags are not enforced, and
   `problem` is accepted with any tag.
8. **ADR 0024 amendment: what a heading shows in the main pane.** A heading
   that exists today keeps showing the last selected content (legacy). Any
   heading added or changed from now on shows a blank main pane, or a board
   relevant to its section. The sessions heading is the first to show a
   board.

## 4. Storage and bus API

### Schema (migration 12 to 13)

```sql
CREATE TABLE IF NOT EXISTS statuses (
  identity TEXT NOT NULL,
  revision INTEGER NOT NULL,
  tag      TEXT NOT NULL CHECK (tag IN ('idle','planning','implementing','reviewing','waiting','blocked','needs-human')),
  text     TEXT NOT NULL,
  problem  TEXT NOT NULL DEFAULT '',
  set_at   INTEGER NOT NULL,
  PRIMARY KEY (identity, revision)
);
```

- The same DDL goes in `schema.go` for fresh databases, and in a `migrate.go`
  step for existing ones.
- `schemaVersion` becomes 13.
- A second run of the migration does nothing (`IF NOT EXISTS`, and the
  `user_version` check).
- The step runs in the migration transaction, like the existing steps. A run
  that fails leaves `user_version` at 12, and the retry creates the table.

### Bus functions (`internal/bus/status.go`)

```go
type Status struct {
    Identity string `json:"identity"`
    Revision int64  `json:"revision"`
    Tag      string `json:"tag"`
    Text     string `json:"text"`
    Problem  string `json:"problem,omitempty"`
    SetAt    int64  `json:"set_at"`
}
```

- **`SetStatus(as, tag, text, problem string) (Status, error)`.** One write
  transaction:
  1. `auth(as)`.
  2. Reject a subagent name (it contains `/`).
  3. Validate the input (below).
  4. Insert `revision = COALESCE(MAX(revision), 0) + 1` for the identity.
  5. Delete that identity's rows with `revision <= new - status_history_count`.

  Revisions only increase, and pruning leaves gaps. SQLite's write lock
  serializes two concurrent sets for one identity: both succeed, as
  consecutive revisions.
- **`CurrentStatuses(as string) ([]BoardEntry, error)`.** It returns one
  entry per identity, holding the latest status and `Live bool`. Live is true
  when the identity has a session row and the row is not expired, judged the
  same way as `liveSessions`. The entries are:
  - Every live top-level identity. One that has never set a status gets an
    empty `Tag`.
  - Every non-live identity whose latest `set_at` is within
    `status_offline_hours`. Its reported tag is `offline`, and its last
    stored status is kept in the entry so the board can show its text.

  The result is unsorted; callers sort.
- **`StatusHistory(as, name string) ([]Status, bool, error)`.** It returns
  up to `status_history_count` revisions, oldest first, plus whether `name`
  is live. An unknown name returns an empty list and no error.
- **`Session`** gains `Status *Status \`json:"status,omitempty"\``, filled
  by `liveSessions` for `discover` and `register` `others`.

### Validation (all `validation` errors, not retryable)

- **`tag`.** It must be one of the seven settable tags, compared exactly
  (lowercase). A missing or unknown tag, or `offline`, is rejected with:
  `tag must be one of idle, planning, implementing, reviewing, waiting,
  blocked, needs-human (offline is set by the bus)`.
- **`text`.** It is trimmed, then must be 1 to 200 characters (runes), with
  no newline and no control characters.
- **`problem`.** It is trimmed, then must be 0 to 200 characters, under the
  same line rules.
- **`as`.** A subagent name is rejected with: `subagents do not set a
  status; the top-level session's status covers them`.

### Config (`internal/config`)

| Key | Default | Range | Meaning |
| --- | --- | --- | --- |
| `status_history_count` | 20 | 1-200 | revisions kept per identity, the current one included |
| `status_offline_hours` | 24 | 0-720 | how long an offline identity's last status stays on the board; 0 shows live sessions only |
| `status_stale_minutes` | 30 | 0-1440 | Stop hook nudge threshold for a working tag; 0 turns the nudge off |

Values outside the range are handled the same way as the existing ranged
keys.

### Renames and deletion

- A `rename: true` register does not move statuses. The old name keeps its
  rows, and appears on the board as offline until its window passes.
- Nothing deletes a dead identity's rows. The set of names is small and
  stable (ADR 0025), and pruning bounds each name to N rows.

## 5. MCP and CLI

### MCP (`internal/mcpserver`)

- **`set_status {as, tag, text, problem?}`.** Returns the stored `Status`.
  Like every MCP call except `receive`, it unparks the session.
- **`status_history {as, name}`.** Returns `{name, live, statuses: [...]}`,
  oldest first.
- **`discover`** and **`register` `others`.** Each entry gains `status`,
  omitted when the agent has never set one. `others` stays live-only.
- **Tool descriptions.** They state the tag set, the line limits, and that
  `offline` is set by the bus.

### CLI (`internal/cli`)

The identity is resolved the same way as for the other CLI commands (`-as`,
or the harness or cwd identity).

- **`agentbus set-status [-as N] [-problem P] <tag> <text...>`.** The words
  after the tag are joined with spaces to form the text. It prints the
  stored revision.
- **`agentbus statuses [-sort time|name|tag] [-json]`.** It prints the
  board: icon, tag, name, age, text and problem, tab-aligned, sorted by time
  (newest first) by default.
- **`agentbus statuses [-json] <name>`.** It prints that identity's history,
  oldest first.
- **Names.** `agentbus status` stays the health command. The new commands
  appear in the CLI help and the README.

## 6. TUI

### Icons

- **Names.** Eight icon names, `status_idle`, `status_planning`,
  `status_implementing`, `status_reviewing`, `status_waiting`,
  `status_blocked`, `status_needs-human` and `status_offline`, go in
  `iconVars`.
- **Defaults** come in both the emoji set and the Nerd Font set, and
  `icon_map` can override them.
- **No status.** A live agent that has never set a status shows the existing
  `agent` icon.

### Rail (sessions section)

- **Row icon.** Each agent row starts with its status icon.
- **Rows.** The section lists live sessions, as it does now. It also lists
  offline identities within `status_offline_hours`, dimmed, with the offline
  icon. Selecting an offline row opens its DM channel, as for any session.
- **Stream header.** With a session selected, the header shows `icon tag ·
  text · problem`, cut with `…`.

### Board (main pane)

- **Opening it.** Selecting the sessions heading, by up/down in the rail or
  by `b` from anywhere, which selects that heading, replaces the stream with
  the board.
- **Focus.** `tab` moves focus to the board, as it does for the stream.
  Selecting any rail row other than the sessions heading restores the normal
  stream for that row.
- **Rows.** Each row shows icon, name, tag, age (`3m`, `5h`, `2d`), text,
  and problem. Text and problem share the remaining width and are cut with
  `…`.
- **Keys.**
  - Up and down move between rows.
  - Right expands the selected row, wrapping its full text and problem on
    the lines below it. Left collapses it.
  - `o` cycles the sort: time (newest first, the default), then name, then
    tag. `o` is otherwise used only inside the modal health overlay, so the
    two do not conflict.
  - `enter` replaces the board with the selected agent's history, oldest
    first. Each line shows revision, local time, icon, tag, text and
    problem. It scrolls with pgup/pgdn and g/G. `esc` returns to the board
    with the same row selected.
- **State.** The sort and expansion state last only while the TUI runs,
  like section collapse state.
- **Refresh.** Statuses load on the existing tick that refreshes sessions;
  no new polling is added.
- **Empty board.** It shows `no agents`.
- **Help.** `?` documents `b`, `o`, the board's left/right and `enter`, and
  `esc` from history.

## 7. Stop hook and protocol text

### Stop hook (`internal/cli/stophook.go`)

The checks run in this order:

1. The existing `stop_hook_active` check returns before anything else, so
   the hook can never keep an agent awake in a loop.
2. Identity is resolved as it is now.
3. The hook reads the identity's latest status. A read error means no nudge
   (fail open).
4. It builds a nudge when all of these hold:
   - the session is not parked;
   - `status_stale_minutes` is greater than 0;
   - the identity is top-level;
   - and either the latest tag is `planning`, `implementing` or `reviewing`
     with `set_at` older than the threshold, or there is no status at all.
5. Nudge text:
   - With a status: `Agentbus: your status is "<tag>: <text>" (<age>); if
     that is no longer true, call set_status.` The text is cut to 80
     characters with `…`.
   - With none: `Agentbus: you have no status; call set_status.`
6. Output:
   - **Messages waiting:** the nudge goes before the existing message reason,
     in the same `block`.
   - **No messages:** the nudge alone is the block reason.
   - **Nothing to say:** the hook prints nothing.

### Protocol text

- **`internal/cli/skills/using-agentbus/SKILL.md`.**
  - A new "Status" section: ADR 0026's text with v1 names (`set_status`,
    `status_history`, `agentbus park`), the tag table, and "pass `problem`
    with `blocked` or `needs-human`".
  - The "Session protocol" section gets the park rule: set `idle` before
    `agentbus park`, never after, because `set_status` unparks.
  - The "Subagents" section: subagents do not set a status.
- **`internal/cli/identity.go` (SessionStart text).** Two lines:
  - Call `set_status` at the start of each task and when the kind of work
    changes.
  - Set `idle` before `agentbus park`.
- **Follow-up outside this repo.** The `/session:handoff` skill
  (efitz-skills) should set `idle` before `agentbus park`.

### ADRs

- **ADR 0026** gets a "v1 decisions (Eric, 2026-11-03)" section with
  section 3's decisions 1 to 7.
- **ADR 0024** gets decision 8 as an amendment, and its Status line points
  to it.

## 8. Testing

- **Bus.**
  - Validation: each settable tag is accepted. `offline`, an unknown tag, an
    empty tag, a multi-line or control-character text or problem, 201
    characters, and a subagent name are each rejected.
  - Pruning: N+3 sets keep the last N, and revisions keep increasing.
  - Liveness: offline is derived after a session ends or expires, and the
    offline window includes and excludes identities at its edge.
  - Concurrency: parallel sets for one identity produce consecutive
    revisions.
  - Migration: a 12-to-13 upgrade, a second migrate does nothing, and a
    fresh schema matches the migrated one.
- **MCP.**
  - `set_status` and `status_history` round trips, and the error text.
  - `status` present or absent in `others`.
  - `set_status` unparks.
- **CLI.**
  - The `set-status` argument join.
  - The `statuses` table, each `-sort`, and `-json`.
  - History output.
  - Unknown flags and sort values are rejected.
- **Stop hook.**
  - Fresh, stale, `idle` or `waiting` (no nudge), never set, and parked.
  - A nudge combined with waiting messages.
  - `stop_hook_active`.
  - A read error fails open.
  - `status_stale_minutes=0`.
- **TUI.**
  - The board opens from `b` and from the heading, and other rows restore
    the stream.
  - Sort cycling, expand and collapse, `enter` into history, and `esc` back
    to the same row.
  - `…` truncation at 80x24.
  - Offline rows in the rail, the header line, and `icon_map` overrides.
- **Done gate:** `make verify`.

## 9. Out of scope

- Pushing status changes, or waking agents on a change (ADR 0026 decision
  13; a follow-up through agentbus2#13).
- Enforcing tag transitions.
- Deleting the statuses of identities that are gone.
- Changing the `/session:handoff` skill, which is a follow-up in
  efitz-skills.
