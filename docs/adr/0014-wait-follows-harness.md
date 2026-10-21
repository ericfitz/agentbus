# ADR 0014: `agentbus wait` follows its harness and replaces its predecessor

Status: Accepted 2026-10-02. Decided by the user.

## Context

ADR 0012 made a standing background `agentbus wait` the idle wake. Two
problems followed. #25: a wait can block or outlive a clean harness shutdown
(Claude Code exiting, a crash, `kill -9`), leaving orphaned waits. #26: every
re-arm started a new wait without ending the old one, so waits piled up for
one identity.

## Human decisions (2026-10-02)

1. **No SessionEnd hook.** `agentbus wait` exits on its own when the harness
   that started it is gone. This is harness-agnostic and also covers
   crashes and `kill -9` (#25).
2. **The exit is silent and has a distinct status** (4), so a dead harness's
   shell never prints a wake line.
3. **A new wait for an identity replaces the old one** silently (exit 3),
   so re-arming is always safe (#26).
4. **A stale or orphaned wait never blocks re-arming.**

## Implementation rulings

- **Ancestor rule.** The direct parent is usually the background shell
  (`zsh -c ...`), which can outlive the harness. The harness is the first
  ancestor whose command name (basename, leading `-` of a login shell
  stripped) is not `sh`, `bash`, `zsh`, `dash`, `ksh`, or `fish`. If the walk
  reaches pid 1 or below, the harness is already gone and the wait exits 4
  at once. If the process table cannot be read (unsupported platform), the
  watch is skipped.
- **Pid reuse guard.** The harness is recorded as pid plus process start
  time; it counts as alive only while both match.
- **Polling.** A watcher checks every 2 seconds. kqueue `NOTE_EXIT` on macOS
  and pidfd on Linux would be faster and are deferred as an optimization.
- **Opt out.** `-no-harness-watch` keeps the wait running without its
  harness.
- **Exit codes.** 0 woke, 1 timeout, 2 error, 3 replaced by a newer wait,
  4 harness gone. Exits 3 and 4 print nothing; the caller does nothing (no
  `receive`, no restart). "Silent" means empty stdout and stderr; the
  harness may still show its own task-finished notice. Exit 4 releases the
  pidfile like any other exit.
- **Single wait per identity.** The wait records `pid start` in
  `<data dir>/wait/<identity>.pid` under a short-lived flock and sends
  SIGTERM to the previous wait only if its pid and start time still match.
  A stale or reused pid is overwritten, never signaled. Release removes the
  file only if it still names this wait.
- **Shared-identity caveat.** Two sessions that share one identity replace
  each other's wait.

## Amendment (2026-10-20): one top-level wait per harness (#39)

Status: Accepted 2026-10-20. Drafted by Claude during an autonomous run;
ratified by the user on 2026-10-20.

**Problem.** A session's identity can change while its harness and its
wait keep running. After a laptop sleep longer than the 30-second
attachment expiry, every session on the host looks stale: the heartbeat
ticker runs on the monotonic clock, which stops during sleep, but expiry is
measured on the wall clock. The next register by any process deletes the
stale rows (`Register` sweeps them, and so does the maintenance tick in
`internal/bus/maintenance.go`), and `Heartbeat` only updates rows it
still owns, so the session row stays gone until the agent registers again.
When it does, it can get a different name: a session that was `agentbus2`
asks for `agentbus`, which is now free. Its next wait, resolved through the
harness lookup (ADR 0017), uses `agentbus.pid` and never replaces the old
wait in `agentbus2.pid`. The old wait's harness is still alive, so the
harness watch does not end it either: it stays up and later wakes the agent
for messages addressed to a name it no longer holds.

**Ruling.** A wait for a top-level identity (no `/` in the name) also
replaces every other live wait that a top-level identity started from the
same harness (pid and start time), whatever identity that wait resolved.
The pidfile records `pid start harness_pid harness_start source`, where
source is how the identity was chosen (`as` for `-as`, `session` for the
harness lookup, `cwd` for the working-directory fallback), so the next
unexplained wait can be diagnosed from the file. Readers accept the old
two-field form. Waits for subagent identities (`parent/name`) record no
harness and are replaced only by identity, as before. The whole
read-terminate-write sequence runs under one directory-wide lock
(`wait/.lock`), taken before the per-identity lock, so two waits for
different identities from one harness cannot deadlock and the newer one
always wins.

**Mixed versions.** A wait from an older binary reads the new pidfile as
garbage and overwrites it without signaling, and it does not take the
directory lock. Upgrade all hosts' binaries together, as with schema bumps.

**Shared-harness caveat.** Any two top-level waits or watches from one
harness replace each other, whatever `-as` says. Panes of tmux or screen
share the tmux server as their harness, so waits started in different panes
for different sessions replace each other. A `-no-harness-watch` wait still
records its harness and is replaced the same way.

**Not fixed here.** The session row lost to the sweep while its MCP server
lives on (the deeper cause) was fixed separately: see ADR 0022.
