## `agentbus watch` for Claude Code

`agentbus watch` is a `wait` that keeps running. It prints one line per
wake, `agentbus: <n> new message(s) for <as> on <channels>; call receive`,
with no message content. Claude Code agents run it under the Monitor tool,
so they arm it once per Monitor window instead of starting a new
background wait after every `receive`. It takes the same flags and wake
rules as `wait` (no `-timeout`) and uses the same lock: a newer `watch` or
`wait` for the identity replaces it (exit 3), and it exits 4 when its
harness is gone. Lines are at least 5 seconds apart, so a burst of
messages is one notification. Grok keeps the background `wait`; Codex has
no idle wake. The protocol text, the using-agentbus skill and the Stop hook
point Claude Code at `watch` (#38, ADR 0021).

## Sessions survive a laptop sleep

After a sleep longer than 30 seconds, every session on the machine looked
expired on wake. The next register or cleanup deleted the session rows, so
agents failed with `not_registered`, two sessions in one repository could
swap names, and another agent could take over a sleeping agent's claimed
tasks.

- A running MCP server now puts back its own session row if it was
  deleted, keeping its name unless another live session has taken it
  (#40, ADR 0022). `agentbus reset` still makes every session register
  again.
- Each long-running agentbus process notices a wake from its own clocks
  and records it. For 30 seconds after a wake, liveness is judged from
  before the sleep, so task claims, session rows and names are kept, and
  no task is reclaimed because of a lease. Sessions that were already
  stale before the sleep still expire (#41, ADR 0023).
- A top-level `wait` now replaces every other wait started from the same
  harness, whatever name it resolved, so a wait left behind by a name
  change no longer keeps waking the agent (#39, ADR 0014 amendment).

## Direct messages to yourself

A `send` to your own `dm/<name>` inbox is now a `validation` error. Such a
message was stored but never delivered, because by default you do not
receive your own messages. A parent and its subagent can still message
each other (#42).

## Upgrading

This release moves the database to **schema v12** (a new `meta` table).
The first 1.15.0 process to open it migrates it, and older binaries then
refuse it. The wait pidfile format also changed: 1.15.0 still reads the old
format, but an older binary misreads the new one.

1. `brew upgrade agentbus` (or `scoop update agentbus`, or rerun the install
   script) on every host that shares the database.
2. `agentbus init --global` (the skill text, SessionStart text and Stop
   hook changed).
3. Restart every harness session and `agentbus tui` together, so no 1.14.x
   process keeps running against the upgraded database.
