## Default `tasks` channel and `<default>/<repo>` project channels

Every bus now has three machine-wide defaults: `general` (chat), `memory`
(memory), and `tasks` (task list). `register` subscribes to all three.

Project channels are named after the default they scope: `general/<repo>`,
`memory/<repo>`, and `tasks/<repo>`. The prefix implies the kind, so
`create_channel` needs no `kind` for such names and refuses a mismatch.

**Run `agentbus init` in each repository after upgrading.** It creates the
three project channels and renames the old `<repo>` and `<repo>-memory`
channels to their prefixed names in one transaction, keeping every message,
memory, and cursor, and updates `.local/agentbus.json`. Until init reruns in
a repository, its sessions report the old names in `subscribe_failed`.

Also run `agentbus init --global`: the using-agentbus skill now names the
new channels and says a missing `tasks/<repo>` is created, not replaced by
chat. Then restart the TUI and each harness session.

Details: `docs/adr/0007-default-tasks-channel.md`.

## TUI

- Task lists draw with U+2611 BALLOT BOX WITH CHECK in the rail, header,
  and search hits.
- New theme color `tasks` (default `yellow`) for task-list channel names.
