# ADR 0013: Per-effort task channels, inactivity expiry, memory expiry

Status: Accepted 2026-09-27. Decided by the user.

## Context

ADR 0007 made every bus create a machine-wide task list `tasks` and made
`agentbus init` create `tasks/<repo>`, both subscribed by `register`. In
use, task lists form around an effort (`tasks/aws-eip-logging`), not a
repository or an agent, and nothing ever removes them: task rows are
memories, which age cleanup skips, and a running TUI's subscriptions keep
the empty-channel reaper away. Memories accumulate the same way.

Design: `docs/superpowers/specs/2026-09-27-task-channel-and-memory-expiry-design.md`.

## Human decisions

1. **Task channels are per effort** (`tasks/<effort>`), created by whoever
   starts the effort. They are not per repository or per agent. `init` no
   longer creates `tasks/<repo>`.
2. **The default `tasks` channel goes away.** Supersedes ADR 0007
   decision 1 and the `tasks/<repo>` part of decision 2.
3. **Existing agents stop subscribing to `tasks`, `tasks/<repo>`, and
   `tasks/<agent>`**: `register` ignores task entries in
   `.local/agentbus.json`, `init` removes them, and the schema v7 migration
   drops every stored subscription to a task list.
4. **A task channel stays while there is activity and is deleted by a
   maintenance tick after 30 days without any**, with all its tasks
   whatever their status.
5. **The TUI marks a task channel idle after 7 days without changes.**
6. **Each memory has a last-accessed time**: set on create and edit,
   updated only when `get_memory` returns it or `search` hits it.
   Existing memories start at the upgrade time.
7. **A maintenance tick removes memories not accessed for 1 year.**
8. **All three periods have defaults and are configurable** in
   `config.json` (`task_idle_hours`, `task_expiry_hours`,
   `memory_expiry_hours`).

## Consequences

- Upgrading drops every task-list subscription, including effort lists
  in use; agents re-subscribe to the lists they work on.
- An abandoned effort's unfinished tasks are deleted 30 days after its last
  change.
- A memory that is only ever read through `history` or the TUI expires.
- Schema v7; all hosts must upgrade and restart together.
