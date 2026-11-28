# Memory hygiene: serve counter, memory_feedback, soft pruning (#53)

Status: design approved section by section in conversation on 2026-11-27.
Items marked "orchestrator's call during planning" were added on 2026-11-27 while the
implementation plan was written; they are not human decisions.
Amends ADR 0013 items 6-7 (a new ADR records the amendment).

## Intent

Keep the memory channels accurate and small. Today a memory lives as long as
MCP searches keep hitting it (ADR 0013: a search hit touches `accessed_at`),
so a memory that matches unrelated searches but never helps lives forever,
and a wrong memory is never demoted. Nothing records whether a memory was
useful. The live bus held 583 memories after seven weeks, and under the
1-year rule nothing expires before October 2027.

The pruning added here is advisory: it hides memories from agents without
deleting them, so the retained data can later be mined for which kinds of
memories pay off. That analysis feeds better guidance in the using-agentbus
skill and docs; it is not part of this change.

## Goals

- Count how often each memory is served to agents through MCP.
- Let agents report, in one cheap call, that a memory was `USED` (useful) or
  `INCORRECT`.
- Soft-prune memories that are noise (served often, never used) and memories
  flagged incorrect that nobody corrected, hiding them from agents while
  keeping them in the database.
- Show the counts, the flag and the pruned state to the human in the TUI,
  with restore and real-delete actions.
- Give the human a CLI export of memories with their counters for analysis.

## Non-goals

- Mining the data or changing guidance based on it (later work).
- Technical enforcement that agents send feedback.
- Any change to task-channel rows (they have `memory_id` but no
  `memory_access` row; the task-channel expiry rule is unchanged).
- Any change to chat or DM retention.

## Anti-goals

- A pruned memory leaking back to an agent through any MCP tool.
- An agent being able to revive a pruned memory.
- Good memories deleted because agents forgot to send feedback: pruning
  never hard-deletes, and the noise rule needs many serves with zero `USED`.
- Per-call token cost on every search (no nudge text, no counts in hits).

## Human decisions (Eric, 2026-11-27)

1. **Pruning is the noise rule, advisory only.** A memory served at least N
   times with zero `USED`, past a grace period, is marked pruned rather than
   deleted, and behaves as deleted for agents.
2. **Pruned memories are hidden from agents, visible to the human.** MCP
   `search`, `get_memory`, `history`, `edit_memory`, `delete_memory` and
   `memory_feedback` treat a pruned memory as nonexistent. The TUI shows it
   dimmed and labeled pruned, with actions to restore it or delete it for
   real. The CLI can export pruned rows for analysis.
3. **The ADR 0013 1-year rule stays a hard delete.** Only the new rules
   soft-prune.
4. **INCORRECT handling.** A flagged memory is marked on search hits and
   ranked last. It becomes eligible for soft-pruning (reason `incorrect`)
   only when all hold: at least 336 hours since the flag; served in more
   than 5 MCP calls since the flag; no `USED` since the flag; not corrected
   (an `edit_memory` clears the flag). A flagged memory no search returns is
   not pruned by this rule; it lives until the 1-year rule, giving people and
   agents time to notice.
5. **Pruned memories follow the same 1-year rule.** No exemption and no
   separate purge setting: a pruned memory is hard-deleted when its
   `accessed_at` is older than `memory_expiry_hours`, like any other.
6. **Approach A** (below): extend `memory_access`.

Settled as proposals Eric approved ("lgtm"):

7. "Served" means a memory appears in the result of an MCP `search` (a hit)
   or `get_memory`, counted once per memory per call. `history`, the TUI and
   CLI reads do not count.
8. Noise rule settings in hours: `memory_noise_grace_hours` (default 720,
   measured from the memory's grace start, see below) and
   `memory_noise_serves` (default 20). Either at 0 disables the rule.
9. INCORRECT settings: `memory_incorrect_prune_hours` (default 336) and
   `memory_incorrect_prune_serves` (default 5; "more than 5" means served
   count since the flag > 5). Either at 0 disables the rule.
10. `memory_feedback` takes a list of `{memory_id, verdict}` pairs, validates
    every item before applying any, and rejects unknown verdicts, unknown,
    deleted or pruned ids, and task or chat ids with an error naming each bad
    item. `USED` from the memory's own author counts.
11. `edit_memory` clears the INCORRECT flag and restarts the noise grace
    period; the counters carry over (they are keyed by `memory_id`).
12. A TUI restore clears the pruned state and restarts the grace period.
13. The TUI shows served and used counts and the flag on the memory's
    metadata line. Agents see the flag on hits (`incorrect: true`), not the
    counts.
14. Agents are told to send feedback through the using-agentbus skill, the
    register instructions and the SessionStart hook text. No nudge in
    search or receive results.

Orchestrator's calls during planning (2026-11-27; not human decisions,
recorded so the spec and the plan agree):

15. **The noise rule counts serves since the grace start.** `memory_access`
    gains `grace_served_base`, set to `served_count` whenever `grace_from`
    is set (create, edit, TUI restore, migration backfill). The noise rule
    compares `served_count - grace_served_base` with `memory_noise_serves`,
    so a restored or edited memory needs that many new serves, not only a
    new grace period, before it can be pruned again. Refines items 8, 11
    and 12.
16. **`get_memory` carries the flag.** Its result has `incorrect: true` when
    the memory is flagged (omitted otherwise), the same JSON field as search
    hits. Extends item 13.
17. **Accepted as they are, for a follow-up issue:** `list_channels` message
    counts include pruned memories; `send` with `reply_to` naming a pruned
    memory's seq still succeeds; `memory_feedback`, restore and clear-flag
    do not run the inspection hook (the TUI delete does); the flagged-last
    ordering applies within text search's existing 1000-candidate ceiling.

## Approaches considered

- **A. Extend `memory_access` (chosen).** Add counters, feedback, flag and
  pruned fields to the existing per-`memory_id` table, which already survives
  revisions and is already dropped on delete and expiry. Agent-facing reads
  exclude pruned memories through one shared predicate; a test walks every
  MCP tool against a pruned memory. One migration, no new table. Cost: every
  agent-facing reader needs the filter, guarded by that test.
- **B. `pruned` column on `messages`.** The filter sits beside the existing
  ~20 `tombstone=0` checks, but counters still need a per-memory table (an
  edit changes `seq`), splitting state across two tables, and the purge and
  edit paths must carry the column. Rejected for the split.
- **C. Archive table.** Pruning moves the row and stats into
  `pruned_memories` and tombstones the original. Agent reads cannot leak,
  but the 72-hour tombstone purge drops the original, so restore must
  re-create the revision, and the TUI needs a second source to show pruned
  memories in place. Rejected for restore and TUI complexity.

## Design

### Schema (migration 14 -> 15)

`memory_access` gains these columns (`ALTER TABLE ADD COLUMN`, all with
defaults so existing rows are valid; `memoryAccessDDL` gains them too for
fresh databases):

| column | type | meaning |
|---|---|---|
| `served_count` | INTEGER NOT NULL DEFAULT 0 | MCP serves, ever |
| `used_count` | INTEGER NOT NULL DEFAULT 0 | `USED` verdicts, ever |
| `used_at` | INTEGER (null) | last `USED`, unix ms |
| `grace_from` | INTEGER NOT NULL DEFAULT 0 | start of the noise grace period |
| `grace_served_base` | INTEGER NOT NULL DEFAULT 0 | `served_count` when `grace_from` was last set (orchestrator's call during planning) |
| `incorrect_at` | INTEGER (null) | when the current INCORRECT flag was set |
| `incorrect_by` | TEXT (null) | identity that set it |
| `incorrect_served_base` | INTEGER NOT NULL DEFAULT 0 | `served_count` when the flag was set |
| `pruned_at` | INTEGER (null) | non-null means pruned |
| `pruned_reason` | TEXT (null) | `noise` or `incorrect` |

Backfill: `grace_from` = the migration time (my call, after the builder
check). Using each live revision's `created_at` would leave most existing
memories already past the 720-hour grace, so a memory served 20 times in the
first days after upgrade, before agents have learned to send `USED`, would be
pruned at once. Migration time gives every existing memory the full grace
period, the same reasoning as ADR 0013's `accessed_at` backfill
(migrate.go:206). `grace_served_base` is backfilled to the row's
`served_count` in the same statement (orchestrator's call during planning).
The step is idempotent: re-running the
version check finds user_version 15 and skips it, and the ADD COLUMN step
only runs inside the 14 -> 15 migration under the existing write-lock
re-read. `admin.go`'s table list needs no change (same table).

### Write paths

- **Create** (`insertMessage` on a memory channel): inserts the row as today
  with counters 0, `grace_from = now` and `grace_served_base = 0`.
- **Edit** (`EditMemory`): as today sets `accessed_at = now`; also sets
  `grace_from = now` and `grace_served_base = served_count`, and clears
  `incorrect_at`, `incorrect_by`, `incorrect_served_base`. Counters carry over. An edit of a pruned memory
  is refused as `not_found`, the same error and wording as a deleted memory
  (agents); the TUI restores first. Filtering in `liveRevision`'s agent path
  is the likely single place for edit, delete and feedback.
- **Serve** (`TouchMemoryAccess`, called from the MCP `search` and
  `get_memory` handlers exactly where it is today): also increments
  `served_count` by 1 for each distinct id in the call. Duplicate ids within
  one call count once. Every returned hit with a `memory_id` on a non-task
  channel counts, including channel-scoped and semantic-only hits; only hits
  actually returned (after the limit) count, not candidates. Still skipped
  for task channels and for TUI and CLI callers (they call the bus directly,
  not through the MCP handlers). A failed update stays a rate-limited warning
  at the call site, as today.
- **Feedback** (`memory_feedback`, new): see below.
- **Delete** and **1-year expiry**: unchanged; they drop the whole row.

### `memory_feedback` MCP tool

Input: `{as, items: [{memory_id, verdict}]}` with 1 to 50 items (the cap is
the builder's call within reason; reject an empty list). `verdict` is the
string enum `USED` or `INCORRECT`, case-sensitive, anything else rejected.

Validation, all before any write, in one transaction: each `memory_id` must
name a live, unpruned memory on a memory channel that is not a task channel.
On any failure nothing is written and the error names every bad item with
its reason (`unknown verdict`, `not found`, `task row`). A pruned memory
fails as `not found` with the same wording as a deleted one, so feedback
never reveals pruned state (decision 2). Chat and
DM ids have no `memory_id` and fail as not found. The same id may appear
more than once; each occurrence applies, in list order, with one `now` shared
by the whole call.

Effects:
- `USED`: `used_count += 1`, `used_at = now`. Also sets `accessed_at = now`
  (a use is an access).
- `INCORRECT`: sets `incorrect_at = now`, `incorrect_by = as`,
  `incorrect_served_base = served_count`. A second flag while one is set
  overwrites all three (who and when move to the latest flagger, and the
  serve count restarts from then).

Result: the list of applied items with each memory's id. Rate-limited like
other writes. Idempotency keys are not needed: a retried `USED` double-counts
harmlessly for this purpose (builder may add `idempotency_key` for
consistency with `send` if cheap).

### Agent-facing reads exclude pruned memories

One SQL predicate fragment (for example `NOT EXISTS (SELECT 1 FROM
memory_access a WHERE a.memory_id = m.memory_id AND a.pruned_at IS NOT
NULL)`) is applied on the agent path of: text search, semantic search (the
vector candidate set and the final rows), `get_memory`, `history` on a
memory channel, `edit_memory`, `delete_memory`, and `memory_feedback`.
`receive` never returns memories (memory channels are not pushed), but the
leak test covers it anyway.

The TUI calls `Bus.Search`, `Bus.History` and memory reads directly. The bus
methods take an include-pruned switch that the MCP input structs cannot set:
an unexported field is unreachable from `internal/tui`, so use a separate
method or an options parameter on `Search` and `History` (never a JSON
field), so it does not appear in any MCP tool schema. Default is exclude.
`MemoryRevisions` has no MCP tool and is TUI-only: it includes pruned
memories unconditionally.

### Search ranking and the flag

Agent search hits on a memory with `incorrect_at` set carry
`incorrect: true` (omitted otherwise). Those hits rank after all unflagged
hits; order among flagged hits follows the normal score. The flag-last
ordering is applied after text/semantic fusion and before the limit cut.
`incorrect` is a JSON field on the shared hit struct, so the TUI sees it too.
Counts are not included in hits. `get_memory` returns the same
`incorrect: true` field on a flagged memory, omitted otherwise
(orchestrator's call during planning).

### Maintenance tick

A new "memory hygiene" step runs after "memory expiry", in chunks like
`memoryExpiryChunk`, setting `pruned_at = now` and `pruned_reason`:

- **Noise:** `pruned_at IS NULL AND used_count = 0 AND served_count -
  grace_served_base >= memory_noise_serves AND grace_from < now -
  memory_noise_grace_hours` (serves since the grace start; orchestrator's call during planning).
- **Incorrect:** `pruned_at IS NULL AND incorrect_at IS NOT NULL AND
  incorrect_at < now - memory_incorrect_prune_hours AND served_count -
  incorrect_served_base > memory_incorrect_prune_serves AND (used_at IS NULL
  OR used_at < incorrect_at)`. A `USED` at the same millisecond as the flag
  (for example both in one `memory_feedback` call) counts as used since the
  flag and blocks this rule.

If both match, the reason is `incorrect`. Each rule is skipped when either of
its settings is 0. The 1-year rule ("memory expiry") is unchanged and
applies to pruned memories too; since nothing touches a pruned memory's
`accessed_at`, it is hard-deleted one year after its last access.

Pruning keeps the revision's embedding and FTS row (restore needs no
re-embed); the agent predicate keeps them out of results.

Capacity eviction is unchanged: memories, pruned or not, are never evicted
automatically (builder's note, not a change).

### Config

Four new keys in `config.go` with range checks alongside
`memory_expiry_hours` (each 0 to 87600 hours; serves 0 to 1,000,000):
`memory_noise_grace_hours` (720), `memory_noise_serves` (20),
`memory_incorrect_prune_hours` (336), `memory_incorrect_prune_serves` (5).
The TUI health view shows them next to "memory expiry". The builder
greps for every other place config keys are listed (README, docs, config
tests, install goldens) and updates them.

### TUI

- The memory's metadata line shows `served N · used N`, plus `incorrect`
  (with who and when) when flagged, plus `pruned (noise|incorrect)` when
  pruned.
- Pruned memories appear in their channel and in TUI search, dimmed.
- Actions on a pruned memory: restore (clears `pruned_at` and
  `pruned_reason`, sets `grace_from = now` and `grace_served_base =
  served_count`; leaves the counters)
  and delete. The TUI has no memory delete today (only the MCP
  `delete_memory` handler calls `DeleteMemory`), so add a TUI delete for any
  memory, pruned or not (my call): a bus method that resolves the live
  revision including pruned ones and runs the same `tombstoneMemoryRow`
  write, acting as the TUI's own identity, with no idempotency receipt
  (a human action, not a retried call); it still runs the inspection hook
  like other TUI writes do, if they do (builder confirms). Restore also clears any INCORRECT
  flag (my call: a human restoring a memory has judged it worth keeping, and
  a kept flag would let the incorrect rule re-prune it on the next tick).
  An action to clear an INCORRECT flag
  on any memory. Key bindings and the help overlay entry are the builder's
  call, following the existing TUI conventions.

### CLI export

A new subcommand (name the builder's call, for example
`agentbus memories export`) writes one JSON object per live memory to stdout:
channel, memory_id, revision, sender, created_at, subject, content, tags,
and every `memory_access` column, including pruned ones. A flag limits output
to pruned memories. Read-only; it does not touch counters.

### Docs and agent guidance

- Tool descriptions: `memory_feedback` (new), `search` (the `incorrect`
  field, pruned memories never appear), `get_memory` (the `incorrect`
  field), `edit_memory` (clears the flag).
- The using-agentbus skill, the register tool description or instructions,
  and the SessionStart hook text: after relying on a memory, send `USED`; on
  finding one wrong, correct it with `edit_memory`, or flag it `INCORRECT`
  when the right answer is unknown; batch feedback in one call. Regenerate
  install goldens and the skill hash as the existing tests require.
- README memory section.
- New ADR amending ADR 0013 items 6-7: what counts as access and serving,
  the feedback signals, the soft-prune rules, and that pruned memories are
  hidden from agents but still subject to the 1-year hard delete. It quotes
  the human decisions above, labeled as such.

## Edge cases and state

- **Second migration run:** no-op (user_version already 15).
- **Two processes migrating at once:** the existing immediate transaction and
  user_version re-read apply it once.
- **Tick overlap or rerun:** the prune updates guard on `pruned_at IS NULL`,
  so a second run changes nothing; a tick interrupted mid-chunk leaves a
  consistent state (each chunk is a transaction).
- **Old binary** opening a v15 database refuses it (ADR 0003 A6); sessions
  must restart, as with every migration.
- **Feedback on a memory edited between read and call:** applies to the
  `memory_id`, which is stable across revisions.
- **Feedback racing a prune or delete:** validation and write share one
  transaction; whichever commits first wins, and a feedback call that loses
  gets `not found`.
- **Restore then immediate re-prune:** prevented by the fresh `grace_from`
  (noise rule) and the cleared flag (incorrect rule). A later noise prune
  also needs `memory_noise_serves` new serves, counted from
  `grace_served_base`.

## Left to the builder

- Exact Go shapes, the include-pruned switch mechanism, chunk size.
- `memory_feedback` item cap and whether to add `idempotency_key`.
- TUI key bindings, styling of dimmed rows, CLI subcommand name and flags.
- Test structure, including the MCP leak test that calls every tool against
  a pruned memory and the counter tests on both MCP and TUI/CLI paths.

## Acceptance

The acceptance criteria in #53, adjusted by the decisions above (pruning is
soft, the 1-year rule stays hard), plus `make verify` green.
