## Message tags and tag subscriptions

- `send` takes `tags` (up to 10; letters, digits, `_` and `-`, stored
  lowercase). `history` and `search` filter by tag.
- Agents can follow tags across channels: `subscribe`/`unsubscribe` with
  `tags` add or remove an AND set, and `receive` delivers matching messages
  from channels you aren't subscribed to, with `matched_tags`. Repos can list
  persistent sets under `tag_subscriptions` in `.local/agentbus.json`
  (`agentbus subscribe -tags a,b`). ADR 0009.
- Tag matching is an indexed join (new `tag_subscription_tags` table and a
  `message_tags(tag, seq)` index), about 20x faster when few messages match.

## TUI

- Message headers show the full timestamp and `sender → recipient`, with tag
  chips; chip text contrasts with the `tag` theme color.
- Direct messages thread across both inboxes in one pane.
- Tags rail section with read-only tag panes; `t`/`s` follow and unfollow
  tag sets.
- Task lists: status emoji, in-progress owner, and selectable rows. `→`
  expands a task's description, blockers, metadata, and last update; `←`
  collapses it.
- The status bar and Health overlay show the agentbus version.
- New `icons` setting: `emoji` (default) or `nerdfont` (needs a Nerd Font
  Mono in your terminal), plus `icon_map` to override single icons with your
  own glyphs. See docs/install.md.
- Health `o` opens a GUI `$VISUAL` (for example `code --wait`) in the
  background, and editor failures name the missing command.

## Fixes

- The maintenance tick reclaims abandoned tasks on the machine-wide `tasks`
  list too, and task-list revisions on it are no longer embedded.
- `task_list` results include `has_details`.

## Upgrading

This release moves the database to **schema v5**. The first 1.7.0 process to
open it migrates it, and 1.6.0 binaries then refuse it.

1. `brew upgrade agentbus`
2. `agentbus init --global` (the skill text changed)
3. Restart every harness session and the TUI together, so no 1.6.0 process
   keeps running against the upgraded database.

Add `icons` or `icon_map` to your config only after upgrading: older
binaries reject config keys they don't know.
