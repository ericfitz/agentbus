## One background wait per agent

Starting `agentbus wait` while another wait is running for the same
identity now replaces the old one. The old wait exits with status 3 and
prints nothing, so it no longer wakes the agent a second time. A stale
pidfile left by a dead or reused process never blocks a new wait (#26).

## Waits end with their harness

`agentbus wait` now watches the harness that started it, meaning the first
ancestor that isn't a shell. When the harness exits, crashes or is killed,
the wait exits with status 4 and prints nothing. No SessionEnd hook is
involved, so this works in every harness. `-no-harness-watch` turns the
watch off (#25, ADR 0014).

Exit codes are now: 0 woke, 1 timeout, 2 error, 3 replaced, 4 harness gone.
An agent should do nothing after a 3 or a 4.

## The health view shows each agent's harness

`register` now records the harness name and version that the MCP client
sends in `initialize` (for example `claude-code 2.1.0`). The TUI's `h` view
lists them per session, and `discover` returns them. Agents don't have to
send anything (#23, ADR 0015).

## Claude Code subagents can join the bus

`agentbus init --global` now installs a Claude Code SubagentStart hook. When
a dispatch prompt says "use agentbus" (optionally with `parent=<name>`) and
the parent is registered, the hook tells the subagent to register as its own
identity under its parent. Other subagents stay off the bus (#24, ADR 0016).

## TUI

- `<` and `>` narrow and widen the channel rail. The width is saved as
  `tui_rail_width` in the config file and clamped to the terminal. `,` and
  `.` still step memory versions (#21).
- A message header leaves out "→ <channel>" when you're viewing that
  channel. Tag panes, search results and DMs still show it (#22).

## Upgrading

This release changes the database schema (v8). Upgrade and restart every
host together: run `brew upgrade agentbus`, then `agentbus init --global` to
install the new hook and the updated skill, then restart every harness
session and `agentbus tui`. A 1.11.x process refuses a v8 database.
