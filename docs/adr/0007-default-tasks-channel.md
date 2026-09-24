# ADR 0007: Default `tasks` channel, `<default>/<repo>` project channels, task-list icon and color

Status: Accepted 2026-09-17. Decided by the user.

## Human decisions

1. **Every bus has a machine-wide task list named `tasks`**, created on open
   like `general` and `memory` (ADR 0002) and subscribed by `register` by
   default. The bare name `tasks` was already reserved for task lists (ADR
   0005); `IsTaskChannel` now matches it as well as the `tasks/` prefix, so
   the task tools, the TUI tree, and the send refusal all apply to it.
2. **Project channels are `general/<repo>`, `memory/<repo>`, and
   `tasks/<repo>`**: each is named after the machine-wide default it scopes,
   and the prefix implies the kind (`create_channel` accepts an empty kind
   for such names and refuses a mismatch). This replaces `<repo>` and
   `<repo>-memory` from ADR 0002, which predate the prefix convention that
   `dm/` (ADR 0004) and `tasks/` (ADR 0005) introduced. `agentbus init`
   creates all three and persists them, so the skill can say who creates the
   project task list. Init renames a pre-existing `<repo>` or `<repo>-memory`
   channel to its prefixed name in one transaction, carrying messages and
   subscriptions (cursors keep their seqs), and drops the old name from
   `.local/agentbus.json`. Decided 2026-09-18.
3. **Task-list channels draw with U+2611 BALLOT BOX WITH CHECK** in the rail,
   header, and search hits (was the clipboard), rendered with the same
   Neutral-width escape treatment as the gear.

   Amended 2026-09-23 (#7): the icon is now 📋 U+1F4CB, Wide, with no escape
   treatment.
4. **Task-list channel names use a new theme color `tasks`, default
   `yellow`**, rather than the memory color.

## Consequences

- A pre-existing user-created channel named `tasks` (impossible since v1.3.0,
  which reserved the name) would be treated as a task list.
- Existing `.local/agentbus.json` files that materialized the channel list
  keep the old names until `agentbus init` runs again in that repository;
  until then their sessions subscribe to channels that no longer exist after
  the rename and see them in `subscribe_failed`.
- Names with a slash outside the `general/`, `memory/`, `tasks/`, and `dm/`
  prefixes remain invalid.
