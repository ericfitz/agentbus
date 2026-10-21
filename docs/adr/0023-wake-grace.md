# ADR 0023: after a wake, liveness is judged from before the sleep

Status: Accepted 2026-10-20. Issue #41. Amends ADR 0005 (task reclaim) and
ADR 0022 (restoring swept session rows). The choice of approach is a human
decision: the user picked option A on 2026-10-20. The rulings below were
drafted by Claude.

## Context

A session is live while its heartbeat is less than 30 s old by the wall
clock. Heartbeats come from a Go ticker on the monotonic clock, which stops
while the machine sleeps. After a sleep longer than 30 s, every heartbeat on
the host looks stale until each MCP server's next tick, up to 10 s after the
wake. In that window:

- task reclaim (`abandoned`, `abandonedSet`, the maintenance tick) returns a
  sleeping agent's `in_progress` tasks to `pending`;
- the stale-row sweeps delete the rows ADR 0022 later restores;
- readers such as `IdentityForHarness` and `SessionLive` report live
  sessions as gone.

Options considered:

- **A.** Long-running processes notice the wake from their own clocks and
  record it in the database. Every liveness check measures staleness from
  before the sleep until the host has been awake for the expiry.
- **B.** Read each operating system's total-time-asleep counter and subtract
  it from heartbeat ages. More exact, but needs per-platform code and tests.
- **C.** A longer expiry for task owners. Rejected: no fixed limit outlasts
  a long sleep.

## Decision (human)

Option A.

## Rulings (drafted by Claude)

- **Detecting a wake.** Every long-running `Bus` (MCP server, TUI) keeps
  its last clock sample, a wall reading and a monotonic reading. On every
  heartbeat and every liveness check it takes a new sample. If wall time
  advanced more than the monotonic clock by over 5 s, the machine slept.
  The sleep lasted the difference between the two gaps and ended now, so it
  started at the current wall time minus that difference, not at the
  previous sample. Another process's sample of the same wake can give a
  slightly different start; the merge keeps the earlier one. A
  forward jump of the wall clock looks the same, and the only effect is a
  grace period nobody needed.
- **Recording it.** The detecting process writes `slept_at` and `woke_at`
  (wall ms) to the `meta` table from ADR 0022, in the same transaction as a
  read of the current values. If the stored `woke_at` is within the expiry
  of the new one, this is the same wake seen by another process: keep the
  earlier `slept_at` and the later `woke_at`. Otherwise the new pair
  replaces the old one. Short-lived processes (CLI commands, hooks) only
  read the pair. They detect nothing, because they have no earlier sample.
- **The cutoff.** Every check that compares a heartbeat with
  `now - attachmentExpiryMs` uses one helper instead. While
  `now < woke_at + attachmentExpiryMs` (the grace period), the cutoff is
  `slept_at - attachmentExpiryMs`. Otherwise it is `now - attachmentExpiryMs`
  as before. A session that was live when the machine went to sleep stays
  live through the grace period. One already stale before the sleep is
  still swept and still loses its tasks.
- **Covered checks.** The stale-row sweeps in `register` and the
  maintenance tick, task reclaim (owner liveness), `IdentityForHarness`,
  `SessionHarness`, `SessionClient`, `SessionLive`, discovery, and any other
  heartbeat comparison found by searching for `attachmentExpiryMs`.
- **Leases.** `leased_until` is an absolute time the agent chose (ADR 0005),
  so it keeps wall-clock meaning, but nothing is reclaimed because of a
  lease during the grace period. A lease that ran out during the sleep is
  reclaimed after the grace period unless the owner renews it first.
- **Limits.** A wake that no long-running process notices before a
  short-lived process judges liveness still looks like expiry. That
  happens when no agentbus server or TUI was running across the sleep, and
  also for short-lived processes (CLI commands, hooks) that run in the gap
  between the wake and the first sample by a long-running process, up to
  one heartbeat interval (10 s), even with servers running. On Windows, Go's
  own monotonic clock counts sleep, so `Bus.Mono` reads the unbiased
  interrupt time (`QueryUnbiasedInterruptTime`), which does not, and falls
  back to Go's clock (no detection) if that call is unavailable. Wake
  detection on Windows is covered by a clock unit test only; it has not
  been tried across a real sleep on Windows hardware. A stored
  pair that is not `0 < slept_at <= woke_at <= now` (for instance after the
  wall clock stepped back) is ignored, and two sleeps less than the expiry
  apart merge into one longer grace.
