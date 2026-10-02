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
