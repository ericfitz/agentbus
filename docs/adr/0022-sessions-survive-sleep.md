# ADR 0022: a live MCP server restores its own swept session rows

Status: Accepted 2026-10-20. Issue #40. Amends ADR 0004 (session end) and
ADR 0017 (identity from the harness process). The choice of fix is a human
decision: the user picked option 1 of the three in the issue on 2026-10-20.
The rulings below were drafted by Claude.

## Context

A session row is live while its heartbeat is less than 30 s old by the wall
clock (`attachmentExpiryMs`). Every `register` and every maintenance tick
deletes rows older than that. The MCP server heartbeats every 10 s on a Go
ticker, which runs on the monotonic clock and stops while macOS sleeps. After
a sleep longer than 30 s every row on the host looks stale and the next sweep
deletes them, although each server is still running. `Heartbeat` only updates
rows that still exist, so the row stays gone until the agent registers again,
and it may get a different name then (`agentbus2` comes back as `agentbus`).

Options considered (from the issue):

1. The owning process re-creates its swept rows, keeping each name unless
   another live session has taken it.
2. Sweeps skip rows whose recorded harness is still alive. A crashed MCP
   server under a live harness would hold its name, and its replacement
   would register under a suffix: the same swap the issue is about.
3. Measure staleness on a clock that stops during sleep. Needs a
   per-platform clock, a boot id, a schema migration and a change to every
   liveness query.

## Decision (human)

Option 1. Expiry still means "no heartbeat for 30 s of wall time", and other
processes still sweep stale rows. The process that owns a swept row puts it
back.

## Rulings (drafted by Claude)

- **What a process remembers.** The `Bus` keeps, in memory, the row it last
  wrote for each name it registered: sender, context, harness, harness
  version, harness pid and start, and registered_at. `register` adds or
  replaces the entry; `EndSessions` clears all of them.
- **When it restores.** On every heartbeat, and when `auth` finds no row for
  a name this process remembers. The second case covers the window of up to
  10 s between wake and the next tick, so the first tool call after a wake
  succeeds instead of failing with `not_registered`.
- **How.** An insert that does nothing when the name exists. The restored
  row keeps its original registered_at, so `IdentityForHarness` keeps
  choosing the same name for a harness, and gets a fresh heartbeat.
- **A name taken in the meantime.** If the name now belongs to another
  owner, the process forgets it and does not retry. `auth` then fails with
  `not_registered` as today, and the agent registers again under a new name.
  A restore never overwrites or deletes another process's row.
- **Subscriptions.** Unchanged: they are keyed by name and are not removed
  by the sweep, so a restored name resumes its cursors.
- **Reset.** `agentbus reset` still forces every live process to register again
  (ADR 0004's reset behavior). Reset bumps a generation number stored in the
  database; a process restores only rows it registered under the current
  generation and forgets the rest.
- **A dead server.** Nothing restores its rows, so they expire as before
  (ADR 0004).
- **Sweeps just after a wake.** Amended by ADR 0023: for 30 s after a wake
  the sweeps keep rows that were live when the machine slept, so a sleeping
  owner's row and tasks are not removed before its next heartbeat.
- **One top-level name per process.** Amended by ADR 0025 decision 6: a
  top-level register under a new name drops the process's other top-level rows
  and removes them from the remembered set, so a heartbeat does not restore
  them. Subagent rows and subscriptions are untouched.
