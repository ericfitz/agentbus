## The TUI rail has five sections

The rail used to list chat, memory and task channels in one "channels"
list, so on a busy bus most rows were memory channels with the same
`general/`, `memory/` or `tasks/` prefix repeated on each. It now has five
sections, top to bottom: channels, memories, tasks, tags and sessions (ADR
0024).

- Each section has a heading you can select. With a heading selected,
  `left` collapses its section, `right` expands it and `enter` toggles it.
  A collapsed heading reads `▶ memories (19, 3 unread)`. Collapse state is
  not saved: every start shows all sections expanded. `s` and `d` do
  nothing on a heading.
- A row drops its section's `general/`, `memory/` or `tasks/` prefix. The
  bare names `general`, `memory` and `tasks` show in full, and so do two
  rows in one section whose short names would match. The stream title,
  compose target, search results, help and the CLI and MCP surfaces keep
  full names.
- On a short terminal (80x24 gives the rail 21 rows) the rail drops the
  blank rows between sections, then shows at least one item of each
  section plus a combined `▲N ▼M more` row.
- An empty section shows only its heading.
- In the compose line, `down` on the last line clears the draft, the
  counterpart of `up` recalling your last message.

## Selected idle session rows keep their name

With the stream or compose line focused, a selected idle session row drew
its name and age in bright black on a bright black selection, so only the
icon showed. The same hid a live session's context suffix and a channel's
` (off)` marker. A selected row now swaps any text color equal to its
background for one that reads on it.

## Bare `agentbus`

Running `agentbus` with no arguments now prints the usage line and exits 0,
like `help`. winget validation runs the installed executable with no
arguments and treats exit 2 as an error. Unknown commands and bad flags
still exit 2.

## Task lists expire after 14 days idle

The default `task_expiry_hours` is now 336 (14 days), down from 720 (30
days). `task_idle_hours` stays 168. An explicit setting keeps its value.
This amends ADR 0013 (ADR 0024).

## Upgrading

There is no schema change. Upgrade with `brew upgrade agentbus` (or `scoop
update agentbus`, or rerun the install script).

**On the first maintenance tick after upgrading, every task list idle for
more than 14 days is deleted with its tasks.** Under the old default these
lists would have lived up to 30 days. To keep the old behavior, set
`"task_expiry_hours": 720` in `config.json` before upgrading.
