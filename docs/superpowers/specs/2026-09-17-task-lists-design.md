# Task lists

Date: 2026-09-17. Status: design approved by the human in session; this
document records it for review before planning. Tracks ericfitz/agentbus#2.
Decisions are recorded in `docs/adr/0005-task-lists.md`.

## Goal

A shared work queue on the bus. One identity (a controller agent or the
human) posts tasks to a list; several agents claim them atomically, work
them, and complete or release them. A task whose owner dies or whose lease
runs out returns to the queue. Tasks are ordered, may have a parent, and may
depend on other tasks.

## Human decisions (2026-09-17)

These were decided by the human, not the controller:

1. A task list is a shared work queue with an atomic claim, not a personal
   todo list.
2. Reclaim uses both session liveness (always) and an optional per-task
   lease. The lease is an absolute UTC timestamp, `leased_until`, because
   the bus has no central logic maintaining counters. `0` clears it; any
   other value in the past is a validation error.
3. Dependencies (`blocked_by`) are in scope; "blocked" is derived, never
   stored.
4. Hierarchy is in scope: a task may name a parent that is an existing task
   in the same list. A task cannot be deleted while another task names it as
   parent. Deriving a parent's status from its children is out of scope.
5. Only the owner can change an `in_progress` task that is not abandoned;
   anyone can release an abandoned task. `force: true` lets any identity
   override the owner guard, and the override is recorded.
6. The first version ships MCP tools and a read-only TUI view. The TUI
   renders the hierarchy by indenting subtasks. No CLI, no TUI editing.
7. Storage is approach A: a task list is a memory channel named
   `tasks/<name>`, a task is a memory whose content is JSON, and there is no
   schema change. Each task list is its own channel.
8. `task_claim` and `task_release` exist as separate tools with clear
   semantics. They are thin wrappers around `task_update`; all enforcement
   lives in `task_update`.
9. Abandoned tasks are detected and fixed up by `task_get` and `task_list`
   as well as by `task_update`.
10. Task lists are ordered, and the order survives inserts and deletes: each
    task stores a rank key; callers position tasks with `before` / `after`.

## Storage

A task list is a channel of kind `memory` whose name is `tasks/<name>`;
`<name>` follows the existing channel-name rule. `create_channel` accepts
the `tasks/` prefix only with kind `memory`; the bare name `tasks` is
refused, as `dm` is. Subscribe, unsubscribe (including `persistent`),
`history`, `search`, `list_channels`, channel deletion, and retention treat
it as any memory channel. There is no schema change and no `user_version`
bump, so older binaries keep opening the database; they see task lists as
memory channels holding JSON.

A task is a memory. Its id is the `memory_id`. Its content is one JSON
object:

```json
{
  "subject": "Port the limiter tests",
  "description": "…",
  "status": "pending",
  "owner": "",
  "parent": 0,
  "rank": "V",
  "blocked_by": [12, 15],
  "leased_until": 0,
  "metadata": {"k": "v"}
}
```

| Field | Meaning |
|---|---|
| `subject` | Required, at most 256 bytes, no control characters. |
| `description` | Optional free text. |
| `status` | `pending`, `in_progress`, or `completed`. Deletion tombstones the memory; it is not a status. |
| `owner` | Identity name, or empty. |
| `parent` | Task id in the same list, or `0` for a root task. |
| `rank` | Base-62 string ordering the task among its siblings. Set by the bus only. |
| `blocked_by` | Up to 64 task ids in the same list. |
| `leased_until` | Unix milliseconds UTC, or `0` for no lease. |
| `metadata` | String-to-string map, as on `send`. |

Every change is a new revision of the memory, so subscribers hear about it
through `receive` and `MemoryRevisions` is the audit trail. The envelope's
`sender` is the identity whose call wrote the revision. The envelope's
`type` is empty for ordinary changes, `reclaimed` for a fix-up of an
abandoned task, and `forced` for a change that used `force`.

The whole JSON is subject to `max_message_kib` like any message. Task rows
are skipped by the embedder (semantic search over JSON is noise); text
search finds them.

`send`, `edit_memory`, and `delete_memory` on a `tasks/` channel return
`validation` with a message naming the task tools. `get_memory` works and
returns the raw JSON.

## Tools

| Tool | Input | Result |
|---|---|---|
| `task_create` | `channel`, `subject`, `description?`, `parent?`, `before?`, `after?`, `blocked_by?`, `metadata?`, `idempotency_key?` | `task_id` and the task |
| `task_claim` | `task_id`, `leased_until?`, `idempotency_key?` | the task |
| `task_release` | `task_id`, `idempotency_key?` | the task |
| `task_update` | `task_id` and any of `status`, `owner`, `subject`, `description`, `parent`, `before`, `after`, `add_blocked_by`, `remove_blocked_by`, `leased_until`, `metadata`, `delete`, `force`, `idempotency_key` | the task and the revision it replaced |
| `task_get` | `task_id` | the task, plus derived `blocked` and `open_blockers` |
| `task_list` | `channel`, `status?`, `owner?` | summaries: `id`, `subject`, `status`, `owner`, `parent`, `open_blockers`, `leased_until` |

`task_claim` is `task_update {owner: <caller>, status: "in_progress",
leased_until}`. `task_release` is `task_update {owner: "", status:
"pending", leased_until: 0}`. The wrappers check nothing themselves; the
same patch sent through `task_update` behaves identically.

`task_update` is a patch: absent fields are unchanged. `owner: ""` clears
the owner, `parent: 0` moves the task to the root, `leased_until: 0` clears
the lease. `metadata` keys merge; an empty value removes a key. `delete:
true` may not be combined with other changes.

`task_list` returns tasks in depth-first tree order: each task followed by
its children, siblings by `rank` then id. Filters select rows but keep that
order. The result is trimmed whole-record against the result-size cap like
other list results. Descriptions are omitted.

## Rules

All of these are enforced in `Bus.TaskUpdate` (and `Bus.TaskCreate` for the
creation-time subset), inside one `BEGIN IMMEDIATE` write transaction that
re-runs auth, fixes up the task if it is abandoned, reads the live
revision, applies and validates the patch, and inserts the new revision.
Idempotency keys work as in `edit_memory`, with the receipt check before
any mutable-state lookup.

### Abandonment and fix-up

A task is **abandoned** when it is `in_progress` and either its
`leased_until` is non-zero and in the past, or its owner has no live
session (no `sessions` row with a fresh heartbeat).

One function, `reclaimAbandoned`, writes the revision that returns an
abandoned task to `pending` with owner and lease cleared, envelope type
`reclaimed`, sender the identity whose call detected it. The tick has no
identity, so its reclaim revisions carry an empty sender, which no identity
can hold and no `include_own` filter hides; the TUI shows it as `bus`. It is
called from:

- `TaskUpdate`, for the task being updated, before the patch is applied. A
  claim on an abandoned task is therefore a reclaim followed by a claim: two
  revisions in one transaction.
- `TaskGet`, for the task being read.
- `TaskList`, for every abandoned task in the list.
- The maintenance tick, for every task list, so subscribers that only
  `receive` still hear about it.

Correctness never depends on the tick. Reads open a write transaction only
when they find something to fix. Reclaim revisions are not charged to the
caller's rate limit. All processes share one machine clock.

### Status

| From | To | Conditions and effects |
|---|---|---|
| `pending` | `in_progress` | Owner must be non-empty after the patch; task must not be blocked. |
| `in_progress` | `pending` | Clears owner and lease (release). |
| `in_progress` | `completed` | Clears lease; owner stays as the record of who did it. |
| `pending` | `completed` | Allowed; the caller becomes owner of record if none is set. |
| `completed` | `pending` | Reopen; clears owner. |
| `completed` | `in_progress` | Refused (`validation`); reopen first. |

Setting `owner` on a `pending` task without a status change is an
assignment; it does not start the task. A non-empty `owner` must be an
identity that has registered before (its inbox channel `dm/<owner>` exists,
the same test direct messages use), else `not_found`.

### Ownership guard

While a task is `in_progress` and not abandoned, only its owner may change
or delete it; anyone else gets `conflict` naming the owner. Taking ownership
(setting `owner` to a different non-empty name) requires the owner to be
empty. `force: true` bypasses both for any identity and marks the revision
`forced`. `force` bypasses nothing else: blocking, hierarchy, and validation
rules still apply. A `pending` or `completed` task may be changed by anyone.

Two racing claims serialize on the write transaction: one wins, the other
gets `conflict` naming the winner.

### Lease

`leased_until` is settable on claim and through `task_update`. A non-zero
value not in the future is `validation`. The bus never extends a lease; the
owner renews it by sending a later timestamp. A lease on a task that is not
`in_progress` after the patch is cleared.

### Dependencies

Each `blocked_by` id must be a live task in the same list, not the task
itself, and adding it must not create a cycle (depth-first search over the
list's live tasks); otherwise `validation`. A task is **blocked** while any
blocker is live and not `completed`; a deleted blocker stops blocking. A
blocked task cannot move to `in_progress`: `conflict`, listing the open
blockers. Reopening a completed blocker does not disturb dependents already
in progress.

### Hierarchy

`parent` must be a live task in the same list, not the task itself, and the
parent chain must not loop; otherwise `validation`. A task cannot be deleted
while a live task names it as parent: `conflict`, listing the children;
`force` does not bypass this. Deleting the channel deletes everything. A
parent's status is independent of its children. On read, a task whose parent
is missing (for example after retention) is treated as a root task.

### Order

Siblings (tasks with the same `parent`) sort by `rank`, then id. `rank` is a
base-62 string. Appending takes a rank after the last sibling; inserting
between two siblings takes the string midpoint of their ranks, which always
exists because a string can grow by one character, so no rebalancing pass
exists. Deleting needs no ordering work.

`before` and `after` name a sibling task id and are mutually exclusive. The
named task must be live and have the same parent as this task after the
patch; otherwise `validation`. With neither, `task_create` appends under the
parent, and a `task_update` that changes `parent` appends under the new
parent. A move rewrites only the moved task, so it is subject to the owner
guard on that task alone. Ranks are computed inside the write transaction
that reads the neighbors, so concurrent inserts at one spot get distinct
ranks.

### Errors

No new error codes. `validation` for malformed input and rule violations
that the caller can fix by changing the request; `conflict` (not retryable)
when the task's current state refuses the change (owned by someone else,
blocked, has children, lost a claim race); `not_found` for an unknown task,
channel, or owner; `not_registered` and `rate_limited` as elsewhere.

### Ceiling

`// ponytail:` list, fix-up, cycle checks, and rank lookups parse the JSON
of every live task in the list. Fine into the thousands of tasks. The
upgrade path is an additive `task_index` table (`IF NOT EXISTS`, no version
bump) keyed by `memory_id`.

## TUI (read-only)

`tasks/` channels appear in the rail with a 📋 icon and the memory color.
Selecting one shows, instead of the revision stream, the current task tree:
one row per live task in `task_list` order, indented two cells per level
(capped at six levels of indent), with a status mark (`○` pending, `◐` in
progress, `●` completed, `⊘` blocked), id, subject, owner, open blockers,
and time left on a lease. It re-renders when a revision arrives through the
normal receive loop. Compose, reply, and the memories overlay are disabled
there with the toast "task lists are read-only here"; `d` still deletes the
channel. Fixture rules hold: no `tea.Sequence`, timers through `tick`,
static cursors.

## Agent guidance

Tool descriptions state the rules briefly. The `using-agentbus` skill gains
a "Task lists" section: when to use a list rather than chat; claim before
working; complete or release when you stop; set `leased_until` only on tasks
you will keep renewing; subscribe to the list to hear changes. The hook
protocol text gains one line. `agentbus init` does not create a task list;
agents create `tasks/<repo>` when they need one.

## Testing

Bus unit tests, one per rule above. The ones that matter most: two racing
claims across two `Bus` handles on one database yield one winner; claim of a
task whose owner's session is gone; claim of a task whose lease expired;
fix-up through each of get, list, update, and tick; reclaims not
rate-charged; `force`; dependency and parent cycle rejection; blocked and
unblocked through complete, delete, and reopen; delete refused with
children; rank midpoint (property: for any a < b, a < mid(a,b) < b, including
adjacent and unequal-length keys); `before`/`after` validation; order stable
across inserts and deletes; guards on `send`, `edit_memory`,
`delete_memory`; idempotent replay; embedder skips task rows. One MCP
integration scenario over stdio with two processes: claim contention and a
subscriber receiving the revisions. TUI fixture tests for tree rendering and
the disabled keys.

## Out of scope

Deriving parent status from children; a CLI; TUI editing; the index table;
dependencies or parents across lists; implicit lease renewal.
