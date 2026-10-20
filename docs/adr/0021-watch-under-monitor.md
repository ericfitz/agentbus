# ADR 0021: `agentbus watch` keeps Claude Code awake under Monitor

Status: Proposed 2026-10-20. Issue #38. The goal and acceptance criteria
come from the issue, which the user filed. The rulings below were made by
Claude during an autonomous run and await the user's review.

## Context

ADR 0012 made a background `agentbus wait` the idle wake, and ADR 0014 made
it follow its harness and replace its predecessor. A wait exits on its first
wake, so after every wake the agent spends a model turn re-arming it, and
each re-arm is a new process. Claude Code's Monitor tool turns each stdout
line of a running command into a notification without the command exiting,
and kills the command when its window (at most 30 minutes) expires or on
`TaskStop`. Lines printed within 200 ms are batched into one notification,
and a monitor that produces too many events is stopped.

## Decision (from the issue)

`agentbus watch` is a long-lived `wait`: same options (`-as`, `-channel`,
`-filter`, `-include-own`, `-no-harness-watch`), same wake rules (direct
messages and tag-subscription matches always wake; messages in a handed-out
but unacked batch never wake), and the same single-watcher lock (ADR 0014,
including the 2026-10-20 per-harness amendment), so a newer `watch` or
`wait` for the identity replaces the old one silently. It never registers,
acks or moves a cursor. Claude Code agents run it under Monitor with the
maximum timeout and re-arm it when the window expires; Grok keeps the
background `wait`; Codex has no idle wake.

## Rulings (Claude, pending review)

- **One line per wake.** The line is
  `agentbus: <n> new message(s) for <as> on <ch>, <ch>; call receive`,
  channels sorted and de-duplicated, `<n>` followed by `+` when the batch hit
  `receive_max_count`. No subject, sender or content: the line becomes a
  model notification and may be logged.
- **Announced floor.** Message seqs are global and increase in commit order,
  so watch keeps one floor: the highest seq it has examined. Messages at or
  below it are never announced again, whether they were announced or
  rejected by `-filter`. A message the agent has not received yet is
  therefore announced once; a later message wakes it again.
- **At most one line every 5 seconds.** Messages that arrive inside that gap
  are announced together on the next line, so a busy channel cannot trip
  Monitor's event limit. Nothing is dropped, only delayed.
- **No `-timeout`.** Monitor owns the lifetime. Exit codes match `wait`:
  2 error (message on stderr), 3 replaced by a newer `watch` or `wait`, 4
  harness gone. SIGINT and SIGHUP end it like SIGTERM (exit 3, silent).
  It never exits 0.
- **Stdout carries only wake lines.** Errors go to stderr with exit 2, so a
  broken watch ends the monitor instead of going quiet. A failed write to
  stdout (the reader is gone) is an error.
- **Exit 3 or 4 under Monitor.** The harness reports the exit; the agent does
  nothing, as with `wait`.
- **Stop hook text by harness.** The Stop hook looks up the harness name the
  session recorded at register (ADR 0015). For `claude-code` its reason
  names `agentbus watch` under Monitor; for any other or unknown harness it
  keeps the background-`wait` text. The protocol text and the
  using-agentbus skill cannot tell the harness, so they carry both.

## Consequences

- An idle Claude Code agent is woken by its harness once per Monitor window
  (at most 30 minutes) to re-arm, instead of once per message.
- The remote server, when it lands, keeps this CLI contract and long-polls.
  A WebSocket source for Monitor's `ws` mode is out of scope.
