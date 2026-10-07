# Structured tags and prefix matching

Status: Approved 2026-10-06 (design). Implemented on branch impl/2026-10-07 (2026-10-07). Tracked in #20.
Amends ADR 0009.

## Goal

Let agents write tags that say which dimension they belong to
(`env:prod`, `repo:tmi`, `area:aws`), and let readers and subscribers
match a whole dimension (`env:*`), without the bus learning what a key
means.

Evidence from the live bus on 2026-10-06 (951 tag uses, 112 distinct
tags, no tag subscriptions):

- Repository names are used as tags (`tmi`, `tmi-ux`, `agentbus`, `rpi`),
  and nothing tells a reader they are repositories rather than areas or
  activities.
- Synonyms drift despite the skill's rule (`deploy`/`deployment`,
  `deps`/`dependency`, `skill`/`skills`, `needs-review`/`review`).
- No stored tag, tag subscription, or `.local/agentbus.json` entry under
  `~/Projects` contains `_`.

## Human decisions (user, 2026-10-06)

1. **The colon is an ordinary tag character.** The bus gives it no
   meaning. A tag may contain at most one colon, never first or last.
2. **Prefix matching everywhere.** A filter value ending in `*` matches by
   prefix in `history`, `search`, tag subscriptions, and the TUI. `env:*`
   and `env*` are both valid.
3. **No vocabulary enforcement.** The bus does not check keys or values.
   Projects that want rules put them in their AGENTS.md.
4. **Drop `_` from the tag alphabet,** as part of #20.
5. **Key the dimensions, keep triage tags flat** in the skill's
   vocabulary (see Conventions).
6. **A tag is at most 32 characters in total** (was 20).

## Human decisions (user, 2026-10-07)

7. **`tagCond` uses a two-branch condition** (exact: `tag = lo AND seq > ?`;
   prefix: `tag BETWEEN lo AND hi AND seq > ?`) instead of a single
   `BETWEEN`, so exact tags keep the `(tag, seq)` seek above the cursor.
   Proposed in the implementation plan; approved by Eric on 2026-10-07.

Rejected: a vocabulary or key registry (decision 3); keying every tag,
e.g. `status:failed` (decision 5); a per-part length limit (decision 6);
prefix matching in filters only, with exact-tag subscriptions (decision
2).

## Tag syntax

A stored tag matches `^[a-z0-9-]+(:[a-z0-9-]+)?$` and is 1-32 characters.
Input is lowercased first, as today; the per-message cap of 10 and
deduplication are unchanged.

- `NormalizeTags` (`internal/bus/tags.go`) applies the new rule. Its error
  states it: `tag %q must be 1-32 characters of a-z, 0-9 and -, with at
  most one : between other characters`.
- Stored tags are not rewritten. A stored tag that breaks the new rule
  (one with `_`, none known) stays readable but can no longer be named in
  a filter.
- `.local/agentbus.json` `tags` entries that break the rule come back in
  `ignored_tags`, as invalid entries do today (`internal/repoconfig`).

## Tag patterns

A pattern is what a filter takes: either an exact tag, or a prefix
followed by a single trailing `*`.

- The prefix is 1-32 characters (a prefix matches a tag equal to
  itself, so it may be as long as a tag) and must be the start of some valid tag:
  `^[a-z0-9-]+(:[a-z0-9-]*)?$`. So `env:*`, `env*` and `env:pr*` are valid;
  `*`, `:*`, `e*v`, `env**` and `env_*` are not.
- `*` is never valid in a stored tag (`send`, `edit_memory`, repo `tags`).
- `env*` matches the tag `env` itself, and also `environment` or
  `env:prod`. `env:*` matches only `env:<value>` tags.
- A new `NormalizeTagPatterns` validates, lowercases, deduplicates and
  sorts patterns, with the same cap of 10.

Patterns are accepted by:

- `history` and `search` `tags` (any-of, as today),
- `subscribe` and `unsubscribe` `tags` (an AND set, as today),
- `.local/agentbus.json` `tag_subscriptions`,
- the TUI follow prompt (`t`) and tag panes.

### Matching

Every legal tag character (`-`, `0-9`, `:`, `a-z`) sorts below `~`
(0x7e). A pattern therefore maps to an inclusive byte range:

| Pattern | lo | hi |
|---|---|---|
| exact `t` | `t` | `t` |
| prefix `p*` | `p` | `p~` |

A tag matches when `lo <= tag <= hi`. In SQL this is `tag BETWEEN lo AND
hi`, never `LIKE`: SQLite's `LIKE` is case-insensitive and so cannot use
the `message_tags(tag, seq)` index, whose collation is BINARY.

`bus.MatchTag(pattern, tag string) bool` implements the same rule in Go,
for `matched_tags` and the TUI, and is tested against the SQL path.

### history and search

`tagsFilter` builds one range condition per pattern inside its existing
`EXISTS (SELECT 1 FROM message_tags t WHERE t.seq = <alias>.seq AND (...))`.

### Tag subscriptions

- `tag_subscription_tags(sender, tags_key, tag)` gains `lo TEXT NOT NULL`
  and `hi TEXT NOT NULL`, computed when the set is subscribed. `tag`
  holds the pattern as written (`env:*`), and `tags_key` joins the sorted
  patterns, as today.
- `tagCond` joins `message_tags mt` with a two-branch condition: an exact
  tag (`st.lo = st.hi`) matches `mt.tag = st.lo AND mt.seq > ?`, a prefix
  matches `mt.tag BETWEEN st.lo AND st.hi AND mt.seq > ?` (both keeping
  `mt.seq > ts.created_seq`), so the planner can still seek on
  `(tag, seq)` for exact tags (human decision 7), and a set
  matches when `count(DISTINCT st.tag)` equals the set's size, so a
  message carrying `env:prod` and `env:staging` counts once against
  `env:*`.
- Exact patterns keep today's seek on `(tag, seq)` above the cursor. A
  prefix range-scans the index over its tag range, reading every tagged
  message in that range and not only those above the cursor; retention
  bounds this. `TestTagCondUsesTagSeqIndex` is extended to a prefix set
  and still asserts that `message_tags_tag_seq` is used and that neither
  `messages` nor `message_tags` is scanned.
- `matched_tags` lists the message's own tags that satisfied a matching
  set (`env:prod`), not the patterns (`env:*`), sorted and deduplicated.
- `matchedTags` uses `MatchTag`.

### Schema v10

One migration step, `migrations[9]` (`addTagRanges`), run by the existing
`runMigration`:

1. `ALTER TABLE tag_subscription_tags ADD COLUMN lo TEXT NOT NULL DEFAULT
   ''` and the same for `hi` (SQLite requires a default to add a NOT NULL
   column), then `UPDATE tag_subscription_tags SET lo = tag, hi = tag`
   (every existing row is an exact tag).
2. `runMigration` stamps `user_version` 10 and commits.

`schema.go`'s DDL gains the two columns for new databases.

- Second run: `migrate` starts at the stored `user_version`, so a v10
  database runs nothing.
- Interrupted run: the step is one transaction, so a failure leaves the
  database at v9 without the columns, and the next open runs it again.
- Two processes opening a v9 database together: `runMigration` re-reads
  `user_version` under the write lock, so the second sees 10 and skips the
  step.
- An older binary refuses a v10 database (ADR 0003 A6): every host and
  session upgrades together, as for v9.

## Conventions (using-agentbus skill)

The vocabulary keys the dimensions and leaves triage tags flat:

- `env:` `prod` `staging` `dev` `local`
- `repo:<name>`, on channels several repositories share
- `area:` `aws` `terraform` `go` `node` `docker` `gh` `macos`, plus the
  change areas `api-schema` `db-schema` `config` `dependency`
- `kind:` `gotcha` `workaround` `howto`, for memories

Flat, unchanged: activities (`deployment` `release` `migration` `ci`
`rollback` `infra`), lifecycle (`started` `succeeded` `failed`), attention
(`blocked` `needs-human` `breaking`), `change`, `handoff`, `review`.

- Examples change accordingly: `["deployment", "started", "env:prod"]`;
  a change is `change` plus `area:<area>`; following every change is
  `subscribe ["change", "area:*"]`.
- The skill says older messages carry the flat forms (`prod`, `tmi`), so a
  search over history includes both: `["env:prod", "prod"]`.
- The skill states the tag rule and the pattern rule in its Tags section.
- A repository's own vocabulary may use keyed tags (`"tags":
  ["repo:tmi"]`); `register` returns them in `repo_tags` unchanged.

## TUI

- Tag chips show the whole tag (`env:prod`); no grouping by key.
- The follow prompt (`t`, `tagPrompt`) accepts patterns. The pane name is
  still the comma-joined set (`env:*,failed`).
- `tagPaneMsgs` matches loaded messages with `MatchTag` instead of exact
  comparison.

## MCP tool descriptions

- `send` and `edit_memory`: tags are 1-32 characters of a-z, 0-9 and `-`,
  with at most one `:` between other characters; stored lowercase.
- `history`, `search`, `subscribe`, `unsubscribe`: a tag ending in `*`
  matches every tag with that prefix (`env:*`).

## Release

Release notes tag the release `breaking`: tags containing `_` are now
rejected, and a tag may be 32 characters and contain one `:`. A client
built against the old rule keeps working unless it sends `_`.

## Testing

- `NormalizeTags` table: valid keyed tag; colon first, last, doubled;
  lengths 32 and 33; `_`; uppercase lowercased; `*` rejected.
- `NormalizeTagPatterns` table: `*`, `:*`, `e*v`, `env**`, `env_*`
  rejected; `env:*`, `env*`, `env:pr*`, an exact tag, and a 32-character
  prefix accepted; a 33-character prefix rejected.
- `MatchTag` table, including `env*` matching `env`, `env:*` not matching
  `env`, and agreement with the SQL range on the same cases.
- `history` and `search` with prefix and mixed exact/prefix filters.
- Subscriptions: `["env:*", "failed"]` delivers a message tagged
  `env:prod, env:staging, failed` once with `matched_tags` `[env:prod,
  env:staging, failed]`; a message tagged only `env:prod` is not
  delivered; `unsubscribe ["env:*", "failed"]` removes the set.
- `EXPLAIN QUERY PLAN` assertion for exact and prefix sets.
- Migration: a v9 fixture with tag subscriptions opens as v10 with `lo` =
  `hi` = `tag`; reopening runs nothing; a step that fails midway leaves
  v9 and succeeds on the next open.
- `repoconfig`: `tags` with `_` or `*` go to `ignored_tags`;
  `tag_subscriptions` accept patterns.
- TUI: `splitTags` and the follow prompt with `env:*,failed`;
  `tagPaneMsgs` with a prefix pane.
- `make verify`.
