## Task lists per effort, and they expire

Task lists now form around an effort (`tasks/<effort>`), created by whoever
starts the work. ADR 0013.

- The default machine-wide `tasks` list is gone, and `agentbus init` no
  longer creates `tasks/<repo>`.
- `register` ignores task-list entries in `.local/agentbus.json` and reports
  them in `ignored_channels`; the skill tells agents to delete them and
  subscribe to the effort list they work on. `agentbus init` removes them.
- A maintenance tick deletes a task list, with all its tasks, after 30 days
  without any change. The TUI marks a task list idle after 7 days.

## Memories expire

Each memory tracks when it was last accessed (created, edited, returned by
`get_memory`, or hit by `search`). A maintenance tick removes memories not
accessed for a year. Existing memories start their clock at the upgrade.

All three periods are configurable in `config.json`: `task_idle_hours`,
`task_expiry_hours`, `memory_expiry_hours`.

## Fixes

- `agentbus status` no longer counts task-list rows in the embedding
  backlog, so the backlog can drain to zero.

## Upgrading

This release moves the database to **schema v7**. The first 1.10.0 process
to open it migrates it, and older binaries then refuse it. The migration
**drops every task-list subscription**, including effort lists in use;
agents re-subscribe to the lists they work on.

1. `brew upgrade agentbus`
2. `agentbus init --global` (the skill text changed)
3. Restart every harness session and the TUI together, so no 1.9.x process
   keeps running against the upgraded database.
4. Optional: re-run `agentbus init` in each repo to remove task-list entries
   from `.local/agentbus.json` (`register` ignores them either way).

Add the new config keys only after upgrading: older binaries reject config
keys they don't know.
