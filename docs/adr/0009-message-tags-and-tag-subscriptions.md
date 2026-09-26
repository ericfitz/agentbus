# ADR 0009: Message tags and tag subscriptions

Status: Accepted 2026-09-23 (design; not yet implemented). Decided by the
user. Tracked in #5 (tags) and #6 (tag subscriptions).

## Context

Agents can only follow traffic by channel. We want messages to carry short
labels, and agents to follow a label across channels. Two questions were
open: whether channels and DM recipients should themselves become tags (one
uniform subscription system), and whether a subscription may be a tag
expression.

## Human decisions

1. **Tags.** `send` accepts an optional `tags: []string`. A tag is 1–20
   characters matching `^[A-Za-z0-9_-]{1,20}$`, case-insensitive, stored
   lowercase only (displayed lowercase). At most 10 per message, deduplicated
   after lowercasing; one invalid tag rejects the whole send. Tags are
   allowed on chat channels, DMs, and memories (per revision; `edit_memory`
   may replace them, omitting `tags` keeps them); not on task lists. Stored in
   a normalized `message_tags(seq, tag)` table indexed on `tag`. `history`
   and `search` take an any-of `tags` filter.
2. **Channels stay channels; subscriptions are unified internally (option
   C).** Channels remain the unit of storage, ordering, cursors, rate
   limits, eviction, kind, and the ADR 0004 DM guard. Internally, `receive`
   iterates subscription *sources*, each a predicate plus a cursor: a
   channel row is `channel = X`, the tag row is "tags ⊇ any of my sets".
   Channels were not re-implemented as tags: that would break per-channel
   seq cursors and require migrating every message and subscription.
3. **A tag subscription is a set of tags that must all match (AND).** OR is
   expressed as several subscriptions. No NOT and no expression grammar.
4. **Tag subscriptions are not scoped to a channel.** They match every
   channel the subscriber may see. Channel scoping can be added later by
   filling both predicate fields, without migration.
5. **One shared cursor for all of an agent's tag subscriptions.** The sets
   live in `tag_subscriptions(sender, tags_key, created_seq)` (`tags_key` is
   the sorted lowercase set); one pseudo-subscription row carries the
   cursor, pending batch, and idle expiry. A message matching several sets
   is delivered once. `created_seq` gives `resume=false` semantics: a new
   set matches only messages after it was added.
6. **Visibility.** The tag source matches ordinary chat channels only: never
   another agent's `dm/*`, never `tasks/*`, and never memory channels
   (consistent with ADR 0008: memories are searched, not pushed).
7. **Dedup against channel subscriptions.** The tag source skips messages in
   channels the agent is directly subscribed to; those arrive via the
   channel.
8. **Interfaces.** `subscribe`/`unsubscribe` take `channel` or `tags`
   (1–10 tags, same validation). `.local/agentbus.json` gains
   `tag_subscriptions: [[...], ...]`, applied by `register`. Tag-delivered
   messages carry `matched_tags`. `agentbus wait -filter @<name>` also wakes
   on tag matches. The TUI lists tag sets in a "tags" rail section; selecting
   one shows the merged matching messages.

## Consequences

- No eviction gaps are reported for tag deliveries (whether an evicted
  message would have matched is unknown).
- Channel-subscription behavior is unchanged; the source abstraction is a
  refactor of `receiveOnce` plus one new source kind.
- TUI tag display (chips with a `tag` theme color, `#tag` fallback) is in #3/#5.

## Amendment (2026-09-23): indexed tag matching

**Human decision (user, 2026-09-23):** "store tags in such a way that I can
do a join on indexed fields to get matching messages, and don't have to
iterate."

Item 5's `tag_subscriptions(sender, tags_key, created_seq)` held each AND
set as one comma-joined `tags_key` string, so matching a message against a
sender's sets required iterating messages (or, per candidate message, a
correlated per-set subquery) and re-splitting `tags_key` in SQL. `#13`
replaces that with:

- `tag_subscription_tags(sender, tags_key, tag)`: one row per tag of each
  AND set, primary key `(sender, tags_key, tag)`, `FOREIGN KEY (sender,
  tags_key) REFERENCES tag_subscriptions(sender, tags_key) ON DELETE
  CASCADE`. `tag_subscriptions` still owns `created_seq`; this table only
  fans a set's tags into individually indexable rows. The cascade means
  every existing `DELETE FROM tag_subscriptions` (unsubscribe, session
  cleanup, resume=false) drops the fanned-out rows without any code change.
- `message_tags_tag ON message_tags(tag)` is replaced by `message_tags_tag_seq
  ON message_tags(tag, seq)`. `message_tags` is a rowid table keyed by
  `(seq, tag)`, so a tag-only index can't range-scan by seq; the composite
  index can.
- `tagCond` now drives the match from the sender's few subscribed tags
  (`tag_subscription_tags`) into `message_tags` by `(tag, seq > floor)`, a
  join and `GROUP BY ... HAVING count(*) = set size`, so it reads only
  tagged messages above the cursor — never every message on a matched
  channel, and never a scan of `message_tags`. `TestTagCondUsesTagSeqIndex`
  asserts on `EXPLAIN QUERY PLAN` that `message_tags_tag_seq` is used and
  neither `messages` nor `message_tags` (aliased `mt`) is scanned.
- Schema v5 (migration step 4, `splitTagSets`): creates
  `tag_subscription_tags`, backfills one row per tag from each existing
  `tag_subscriptions.tags_key`, and drops `message_tags_tag`. 1.7.0 ships
  schema v5; a v4 database migrates on its next open.

Measured effect (`BenchmarkTagMatchFewMatches`: 20,000 untagged messages
plus 5 matching a subscribed tag, one `Receive`+`Ack` drain loop): roughly
11–24 ms/op before, ~0.87–0.95 ms/op after — about an order of magnitude,
consistent with reading 5 tagged rows through the index instead of every
message on the channel.

## Amendment (2026-09-26): TUI tag panes include DMs and memories

**Human decision (user, 2026-09-26):** "amend the ADR to include dms and
memories" in the TUI's tag panes.

Item 8's TUI tag pane drew only from ordinary chat channels, so a tag used
only in DMs or memories (e.g. `aws` on `dm/*` and `memory/*`) showed an
empty pane. A tag pane now merges the loaded messages of every chat,
memory, and DM channel in the rail; task lists are still excluded.

Item 6 is unchanged: the bus tag source that delivers to agents still
matches ordinary chat channels only. The TUI is the human operator's view
and already shows every DM and memory channel, so widening its tag panes
exposes nothing new.
