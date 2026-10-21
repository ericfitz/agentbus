# ADR 0024: the rail is five collapsible sections; task lists expire after 14 days

Status: Accepted 2026-10-21. Amends ADR 0013 (default `task_expiry_hours`).
The decisions under "Decisions (human)" were made by Eric on 2026-10-21.
The rulings under "Rulings (drafted by Claude)" are builder's calls from the
design (`docs/superpowers/specs/2026-10-21-rail-sections-design.md`) and may
be revisited.

## Context

The TUI rail listed chat, memory and task channels in one "channels" list.
On a real bus (30 channels: 6 chat, 19 memory, 4 task) most rows were
memory channels, and the `general/`, `memory/` and `tasks/` prefixes
repeated on every row. Stale task lists also lingered for 30 days.

## Decisions (human)

- **Five sections**: channels, memories, tasks, tags, sessions. Task channels (`bus.IsTaskChannel`) go in tasks, other memory
  channels in memories, everything else in channels. Each has a heading.
- **Collapsing.** Every section collapses to its heading. Collapse state
  lives only in the running TUI; every start shows all sections expanded.
- **Hidden prefixes.** A rail row drops its section's `general/`,
  `memory/` or `tasks/` prefix. The bare names `general`, `memory` and
  `tasks` show in full. Only rail rows change; the stream title, compose
  target, search results, help and the CLI/MCP surfaces keep full names.
- **Empty section** shows only its heading.
- **Short screens (option A).** When the per-section floor does not fit
  (an 80x24 terminal gives the rail 21 rows), first drop the blank
  separator rows, then lower each section's floor to one item plus one
  combined `▲N ▼M more` row. Clipping by `MaxHeight` remains the last
  resort.
- **`task_expiry_hours` defaults to 336** (14 days, was 720). An explicit
  setting keeps its value. `task_idle_hours` stays 168. On the first
  maintenance tick after upgrading, every task list idle for more than 14
  days is deleted with its tasks.
- **Compose:** `down` clears the draft, the counterpart of `up` recalling
  the last sent message.

## Rulings (drafted by Claude)

- Details of collapsing: headings are selectable rail rows; up and down
  walk headings and items as one sequence; with a heading selected and the
  rail focused, `left` collapses and `right` expands (on an item they keep
  their stream meaning); `enter` on a heading toggles it.
- A collapsed heading reads `▶ name (count)`, or `▶ name (count, N
  unread)`; an expanded one reads `▼ name`. Both glyphs honor `icon_map`.
- Selecting a heading leaves the stream, header and compose target on the
  last selected channel or session; only the heading is highlighted. `tab`
  from the stream returns to a selected heading. `home` selects the
  channels heading.
- `s` and `d` do nothing while a heading is selected, since they would
  otherwise act on a channel that is not highlighted (`d` deletes).
- A search hit in a collapsed section selects that section's heading, not a
  hidden row. New messages and new channels do not expand a section.
- Two rows in one section whose short names match (ordinary `agentbus` and
  `general/agentbus`) both show their full names.
- `down` clears the draft only on the draft's last line, so multi-line
  editing still moves the cursor.
- Removing the tags section's "press t to create" hint follows from the
  empty-section rule; `?` help still documents `t`.
- Section order (top to bottom: channels, memories, tasks, tags,
  sessions), the blank-row rule and the data structures are
  implementation choices; Eric did not object to the order.

## Consequences

- ADR 0013's 30-day expiry is now 14 days by default; its Status line
  points here.
- Collapse state is not persisted, so a restart never hides anything.
- The 80x24 layout with Eric's real mix shows every section's heading and
  at least one item.
