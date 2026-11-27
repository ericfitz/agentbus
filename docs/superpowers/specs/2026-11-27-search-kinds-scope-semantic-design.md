# Search kinds, channel scope, and semantic search over chat and tasks (#52)

Status: approved (Eric, 2026-11-27). Full design (project).

## Intent

An agent or the human in the TUI should be able to say "search only the
task lists", "search only chat", or "search just `general/agentbus`", and a
semantic search should find a chat message or task by meaning, not only a
memory. Today search always covers every kind, has no kind filter, the TUI
cannot scope to a channel, a text search for `status` matches every task
(task rows are indexed as raw JSON), and semantic search sees only memories.

## Human decisions (Eric)

Recorded in ADR 0030 (2026-11-27) and ADR 0032 (2026-11-27):

1. With no `kinds`, search covers every kind (memories, chat, tasks), as
   text search does today; `kinds` only narrows (ADR 0030).
2. When `channel` is set, the channel's own kind decides what is searched
   and `kinds` is ignored silently, never an error and never zero results
   because of a mismatch (#52 issue text, confirmed in the brainstorm).
3. Whenever an embedding endpoint is configured, every kind is embedded:
   memories, chat (ordinary channels), DMs and tasks. No new config key, no
   per-kind opt-in or opt-out (ADR 0032, option A of four).
4. Build approach A (of three): kind predicate from the existing channel
   rules in `searchFilters`; the FTS trigger indexes task rows by subject
   plus description; the embed backlog stops excluding chat and task rows.
5. The design playback was approved as a whole. Calls marked "Claude's
   call" in the playback were approved with it; those added after the
   builder check (freshness, unchanged-text reuse, disk note, `channel:`
   validation set) are Claude's calls for Eric to check.

## Goals

- `SearchInput.Kinds []string` (MCP `kinds`), values `memories`, `chat`,
  `tasks`, any combination, deduplicated and case-sensitive lowercase.
  Unknown values are a `validation` error naming every bad value.
  - `memories`: channels whose `channels.kind='memory'` that are not task
    lists (`IsTaskChannel` false).
  - `chat`: ordinary channels plus DM inboxes the caller may read (the
    existing unscoped DM visibility rule in `searchFilters` is unchanged).
  - `tasks`: task-list channels (`tasks`, `tasks/...`).
- Channel scope: when `Channel` is set, `Kinds` is ignored (decision 2).
- Task hits match only on the task's subject and description; field names
  and other JSON values (`status`, `owner`, `pending`) never match. Only the
  current revision is live (older ones are tombstoned already), so no
  revision logic is added.
- Semantic and both modes rank chat, DM and task rows alongside memories.
- Works with the tags-all-of, sender, since, until and thread filters, and
  with tags-only search (no query terms).
- TUI: `in:memories|chat|tasks` (repeatable) and `channel:<name>` tokens;
  the overlay title shows the scope; a task hit opens that task in the task
  tree.
- Docs: MCP tool description, using-agentbus skill, README, ADR 0032.

## Non-goals

- No change to a search with neither `kinds` nor `channel` (same rows as
  today, except task rows no longer match on JSON keys).
- No new embedding config (`embed_kinds` was considered and rejected).
- No change to the search hit shape: a task hit stays a `Message` whose
  `content` is the task JSON and whose `memory_id` is the task id.
- No per-kind ranking or quotas.
- No "this channel only" toggle key in the TUI overlay (Claude's call:
  `channel:` covers it; add later if wanted).

## Anti-goals

- A DM the caller is not part of must never appear, in any mode or kind.
- A scoped search must never return nothing merely because `kinds`
  disagrees with the channel.
- Semantic or both mode must never silently drop a kind: when the embedder
  is absent or fails, the existing text fallback and `semantic_unavailable`
  apply to every kind.
- The migration must not leave the bus unopenable on malformed task JSON
  (the `json_valid` guard used by `addSubject`, migrate.go).

## Constraints (current code)

- `internal/bus/search.go`: `searchFilters` builds the shared WHERE suffix
  for `textSearch`, `tagsOnlySearch` and `semanticSearch`; `semanticSearch`
  restricts candidates to `m.memory_id IS NOT NULL`.
- `internal/bus/schema.go`: `messagesFTSDDL` indexes `subject, content`
  for every row through `messages_ai` / `messages_ad`; `schemaVersion = 13`.
- `internal/bus/embed.go`: `unembeddedFrom` selects
  `memory_id IS NOT NULL` and excludes task channels; the insert guards in
  `embedBatch` (`WHERE EXISTS ... memory_id IS NOT NULL AND tombstone=0`)
  do the same; `embedText(subject, content)` builds the embedded text.
  `StatusReport` counts the backlog with the same `unembeddedFrom`.
- Embeddings cascade on message delete (channels.go comment, line ~291),
  so chat retention expiry and tombstone purge clean them up.
- `internal/mcpserver/server.go` search handler touches `memory_access` for
  every hit with a `MemoryID`; `TouchMemoryAccess` only updates existing
  rows, and task rows never have one, so task hits are harmless there.
- Measured on Eric's live bus (2026-11-27, read-only): about 12 chat and
  12 DM rows a day, 7-day chat retention (~170 live chat/DM rows), 22 task
  rows, 572 memories (all embedded); average 360-770 characters, none under
  40. Embedding cost on text-embedding-3-small: well under a cent a month.

## Approaches (decision 4)

- **A, chosen: derive kind from channel rules.** One kind predicate in
  `searchFilters` shared by text and semantic search; FTS trigger change
  for task rows; embed backlog widened. Smallest change, one migration.
- B, rejected: a stored `messages.kind` column, backfilled and set on
  insert. Bigger migration and a second source of truth for rules that
  already exist; no measurable speedup at this volume.
- C, rejected: one query per kind fused with RRF. Per-kind ranking (set
  aside in the playback), more code, awkward paging.

## Design

### Kind predicate (bus)

Add `Kinds` to `SearchInput` (`json:"kinds,omitempty"`, with a jsonschema
description). In `Search`, before the mode switch: reject unknown values
(`validation`, message lists the bad values, e.g. `unknown kind "chats";
kinds are memories, chat, tasks`); dedupe; if `Channel` is set, clear
`Kinds`. An empty or all-three set adds no predicate.

In `searchFilters`, when `Kinds` is a proper subset, append
`AND (<p1> OR <p2> ...)` where:

- tasks: `(m.channel='tasks' OR m.channel LIKE 'tasks/%')`
- memories: `(m.channel IN (SELECT name FROM channels WHERE kind='memory') AND NOT (<tasks>))`
- chat: `(m.channel NOT IN (SELECT name FROM channels WHERE kind='memory'))`
  (DM inboxes are not memory-kind, so they fall in chat; the existing DM
  visibility clause still applies on top.)

The predicate must agree with `IsTaskChannel`. DM inboxes are `channels`
rows of kind `ordinary` (`ensureInbox`, dm.go:57), and task lists are
memory-kind (channels.go:15), hence the `NOT (<tasks>)` in `memories`.

### Task text in the FTS index (migration 13 -> 14)

Replace `messagesFTSDDL` so the insert and delete triggers index, for task
rows, `subject` and `json_extract(content,'$.description')` (empty string
when absent or JSON is invalid) instead of raw `content`; other rows are
unchanged. The delete trigger must pass the exact same values that were
inserted (external-content FTS5 requirement), so both triggers use one
shared SQL expression. The 13 -> 14 step drops and recreates the triggers
and the FTS table and repopulates it unconditionally, so it also corrects
whatever the older steps wrote: `addSubject` (v5 -> v6, migrate.go:153)
and `foldRefs` (v10 -> 11, migrate.go:421) keep using a frozen copy of the
v13 DDL and raw-content FTS writes; give the new DDL its own constant so
those historical steps do not change meaning. Because `rebuild` on an external-content
table reads the source columns directly, the builder must make the rebuild
produce the task-specific text too: either repopulate with an explicit
`INSERT INTO messages_fts(rowid, subject, content) SELECT ...` over live
rows instead of `'rebuild'`, or switch to a contentless/`content=''`
table. Builder's call which, with a test that a migrated v13 file no longer
matches `status` on tasks and still matches a description word. The
`'rebuild'` in `admin.go:168` runs after every message row is deleted, so
it only needs changing if a contentless table makes `'rebuild'` an error.

### Semantic search over every kind

- `unembeddedFrom`: drop `memory_id IS NOT NULL` and the task-channel
  exclusion; select every live (`tombstone=0`) row. The `embed_failures`
  exclusion stays. `StatusReport`'s backlog follows automatically.
- Embedded text: for task rows, `embedText(subject, description)` from the
  task JSON; for every other row, `embedText(subject, content)` as today.
  A task update writes a new revision row, so re-embedding on update is
  the normal backlog path; the tombstoned old revision's embedding stays
  until purge cascades it, and `semanticSearch` already filters
  `tombstone=0`.
- `embedBatch` insert guards: drop `memory_id IS NOT NULL`, keep
  `tombstone=0`.
- `semanticSearch`: drop `m.memory_id IS NOT NULL`; the shared
  `searchFilters` (DM visibility, kinds, channel, tags, etc.) applies.
- Ranking: one mixed ranking across kinds, dot product as today; mode
  `both` fuses with the existing `rrf` (Claude's call: one embedding model
  gives comparable scores; `kinds` narrows when wanted).
- Fallback: unchanged (`rankedSearch` text fallback plus
  `semantic_unavailable`). Rows not yet embedded (backlog) are simply
  absent from the semantic half; mode `both` still finds them by text.
- Existing chat, DM and task rows are embedded by the running backlog job;
  no separate backfill step (Claude's call).
- Freshness (Claude's call): ordinary and DM `Send`, `TaskCreate` and every
  task write that inserts a revision call `embedSoon`, as memory writes do
  (messages.go:415, memories.go:288). `embedSoon` collapses overlapping
  calls into one batched pass (maintenance.go:420), so a burst of chat is
  a few batched requests, not one per message.
- No re-embed of unchanged task text (Claude's call): when a new task
  revision's subject and description equal the previous revision's, the
  write copies the previous revision's live embeddings (every model row) to
  the new seq in the same transaction, so a claim, release or status change
  costs no endpoint call. A changed subject or description goes through the
  backlog as usual.
- Disk (Claude's call, doc note only): vectors count toward
  `sqlite_budget_mib` because `Usage` measures the whole file (bus.go:247).
  At about 6 KB per 1536-dimension vector, ~200 extra live rows add ~1.2 MB
  against the 2048 MiB default; no budget change. ADR 0032 notes it.
- `memory_access`: the MCP handler should collect only hits on memory
  channels (not task lists) for `TouchMemoryAccess`; behavior is the same
  today, but this makes intent explicit (Claude's call; builder may keep
  the current call if a test shows no difference).

### MCP tool

`search` description: replace "mode=semantic ranks memories by meaning"
with wording that semantic ranks messages, memories and tasks by meaning;
document `kinds` (values, default all, ignored when `channel` is set) and
that `channel` searches one channel of any kind.

### TUI

- `searchparse.go`: add table rows `in:` (values `memories`, `chat`,
  `tasks`, case-insensitive, repeatable, unknown value is a parse error
  naming it; `in:` empty is an error) and `channel:` (one value; empty or a
  second `channel:` is an error). `searchSpec` gains `Kinds []string` and
  `Channel string`. Quoted words stay literal text, as for `tag:`.
- `channel:` is validated against channels the TUI can open: its rail
  channel list (including task lists) and the DM inboxes of sessions in the
  sessions pane (`sessionNames()`), the same set `jumpTo` can reach
  (Claude's call: every hit stays openable). An unknown name shows the
  error in the overlay instead of running the search.
- `search.go`: pass `Kinds` and `Channel` into `bus.SearchInput`.
- Overlay title shows the scope: `in chat, tasks` or `in <channel>`
  (nothing extra for the default scope).
- Hit rows keep the existing task/memory icons; chat has no mark.
- Opening a task hit selects that task list (`selectChannel`, as
  `jumpTo` does, search.go:131) and puts the task tree cursor on the task
  with that id (`revealTask(ch, id)` / `taskIndex`, tasks.go:334,345),
  instead of jumping to a message row.

### Docs and ADR

- ADR 0032: every kind embedded whenever an endpoint is configured (human
  decision 3), with the measured volume and the privacy note that chat and
  DM text now goes to the endpoint.
- README and using-agentbus skill: `kinds`, `channel` scoping, semantic
  over chat and tasks, TUI `in:` / `channel:` tokens.
- Release notes for the next release mention that semantic search now
  covers chat, DMs and tasks and that chat/DM text is sent to the
  configured embedding endpoint.

## Tests (acceptance)

- No `kinds`: hits from memories, chat, DMs and tasks.
- `kinds:[chat]`: only ordinary and readable DM hits; another agent's DM
  never appears (text and semantic).
- `kinds:[tasks]`: only task-list hits; `kinds:[memories,tasks]` unions.
- `kinds:["chats","x"]`: validation error naming both.
- `channel:"tasks/x"` with `kinds:[chat]`: returns that list's matches;
  `channel:"memory/x"` with `kinds:[chat]`: searches the memory channel.
- Task text: query `status` on a list whose tasks never say "status"
  returns nothing; a description word matches; migrated v13 file behaves
  the same.
- Semantic (fake embedder, as in embed_test.go): chat, DM and task rows
  get embedded by the backlog; semantic mode returns a chat hit and a task
  hit; DM visibility holds; a tombstoned task revision never appears;
  backlog count in `StatusReport` includes the new kinds and drains to 0;
  a chat send and a task write trigger `embedSoon`; a status-only task
  update copies the previous embedding and makes no endpoint call, while a
  description change re-embeds.
- Fallback: embedder absent or failing with `kinds:[chat]` returns text
  hits and `semantic_unavailable=true`.
- Combined with tags-all-of, sender, since, until, thread and tags-only.
- TUI parser table tests for `in:` and `channel:` (valid, repeated,
  unknown, empty, quoted literal, mixed with `tag:`); overlay scope title;
  task hit opens the task tree on the right task.
- `make verify` green.

## Left to the builder

- FTS repopulation mechanism (explicit insert vs contentless table).
- Exact predicate SQL, after confirming how DM inboxes appear in
  `channels`.
- Wording of error messages and the overlay scope title, within the
  examples above.
