# ADR 0005: Task lists

Status: Accepted 2026-09-17. The human decisions below were made by the user
in session on 2026-09-17; the controller decisions were made while designing
and are open to veto. Where this ADR conflicts with the v2 spec or earlier
ADRs, this ADR supersedes; the v2 spec is not edited. Design:
`docs/superpowers/specs/2026-09-17-task-lists-design.md`. Tracks
ericfitz/agentbus#2.

## Human decisions

1. A task list is a shared work queue with an atomic claim, not a personal
   todo list.
2. A claimed task is reclaimed on either of two conditions: its owner has no
   live session (always applies), or its optional lease has run out.
3. The lease is an absolute UTC timestamp, `leased_until`, not a duration:
   the bus has no central logic maintaining counters. `0` clears the lease;
   any other value that is not in the future is a validation error.
4. Dependencies (`blocked_by`) are in scope. "Blocked" is derived from the
   blockers' status and is never stored.
5. Hierarchy is in scope. A task may have a parent, which must be an
   existing task in the same list. A task cannot be deleted while another
   task names it as parent. Auto-deriving a parent's status from its
   children is out of scope. (This replaced an earlier in-session choice of
   "dependencies only".)
6. Only the owner can change an `in_progress` task that is not abandoned;
   anyone can release an abandoned task. `force: true` lets any identity
   override the owner guard.
7. The first version ships MCP tools and a read-only TUI view that indents
   subtasks. No CLI and no TUI editing.
8. Storage is a name prefix on memory channels (`tasks/<name>`), with each
   task a memory whose content is JSON. No schema change. Each task list is
   its own channel.
9. `task_claim` and `task_release` are separate tools with plain semantics,
   implemented as thin wrappers around `task_update`. All enforcement is in
   `task_update` alone.
10. `task_get` and `task_list` detect and fix up abandoned tasks, as
    `task_update` does.
11. Task lists are ordered and the order survives inserts and deletes. Each
    task stores a rank key; callers position tasks with `before` / `after`
    and never compute ranks.

## Controller decisions (open to veto)

1. Rank keys are base-62 strings with string-midpoint insertion, so no
   rebalancing pass exists. Order is per sibling group; `task_list` returns
   depth-first tree order.
2. Fix-up and forced writes are marked through the message `type`
   (`reclaimed`, `forced`). Tick reclaims carry an empty sender.
3. No new error code: the owner guard, a lost claim, a blocked start, and a
   delete with children all return `conflict`.
4. Reclaim revisions are not charged to the caller's rate limit.
5. `send`, `edit_memory`, and `delete_memory` are refused on `tasks/`
   channels; the embedder skips task rows.
6. `pending` to `completed` is allowed directly; `completed` to
   `in_progress` is not (reopen first). Completing keeps the owner as the
   record of who did the work.
7. An owner must be an identity whose inbox exists, the "known identity"
   test direct messages use.
8. `active_form` from the Anthropic task model is dropped; `metadata` is
   string-to-string as on `send`.
9. Known ceiling: every operation parses the JSON of the list's live tasks.
   The upgrade path is an additive `task_index` table.

## Consequences

Older binaries keep working against the same database and see task lists as
memory channels of JSON; they do not enforce the task rules, so a session on
an old binary can still `send` or `edit_memory` into a `tasks/` channel
until it is restarted. As with direct messages, names are not credentials:
the owner guard prevents accidents between cooperating agents, not a
hostile one.
