# ADR 0003: v2 implementation deviations

Status: Accepted 2026-09-08 (human approval by the user). The user approved
all 15 controller rulings below as changes to this ADR. Where they conflict
with the v2 spec, [ADR 0001](0001-discussion-decisions.md), or
[ADR 0002](0002-v2-decisions.md), this ADR supersedes; neither the spec nor
ADR 0002 is edited.

## Context

During task-by-task implementation and review of the v2 design (spec:
docs/superpowers/specs/2026-09-07-agentbus-local-design-v2.md), the
controller ruled on several points where an implementer's or reviewer's
finding required a product decision rather than a pure bug fix. Each ruling
below was made to keep implementation moving without blocking on the human
and was then presented to the user, who approved every one on 2026-09-08.
The "cost if wrong" notes are kept as the record of what a later reversal
would require.

## Rulings

1. **The maintenance tick no longer reaps idle subscriptions.**
   What: the periodic background tick used to delete subscriptions idle past
   `cursor_idle_hours`. It no longer does. Instead, `receive` reaps an idle
   subscription for its own owner and reports it as `expired` in the
   response; `register` with `resume` also reaps any idle subscription for
   the name being resumed, but drops it silently rather than reporting it.
   Why: the tick deleting a subscription erased the only information
   `receive` needs to report `expired` to the subscriber that owned it — by
   the time that owner next called `receive`, the row was already gone and
   the notice was lost. Reaping in `receive` (and silently in `register`)
   keeps the expiry evidence available until it is actually reported, or
   until the same owner intentionally resumes without reading it.
   Cost if wrong: the spec's maintenance-tick step list names idle
   subscription cleanup as a tick responsibility. If the human wants the
   tick to still be the one reaping (for example, to bound how long a dead
   subscription's row lingers regardless of whether `receive` is ever
   called again), the tick needs its own idle-reap step restored, and
   `receive`'s and `register`'s expiry reporting need to keep working
   against it.

2. **`reset` keeps `sqlite_sequence`, so `seq` stays monotonic across a
   reset.**
   What: `reset` wipes every table's rows but does not clear
   `sqlite_sequence`, so the next message or memory after a reset gets a
   `seq` higher than any ever issued, never reusing an old one.
   Why: an embedding HTTP call keyed only by `seq` can still be in flight
   when another process calls `reset`. If `seq` restarted at 1, that
   in-flight call could land its stale vector on an unrelated new message
   that happened to reuse the same `seq` after the reset.
   Cost if wrong: if the human wants `reset` to fully restart numbering
   (for example, for a clean demo or test fixture), the in-flight-embedding
   race needs a different guard (such as keying the insert to a per-reset
   generation number) before `sqlite_sequence` can be cleared safely.

3. **`register` bounds `context` to 1024 bytes and `parent` to 512 bytes (no
   control characters).**
   What: both fields are validated and rejected outside these bounds; the
   spec does not state a limit for either.
   Why: `context` and `parent` are stored on every directory record and can
   appear in `discover` results and notices; an unbounded value lets one
   registration push a directory listing past the hard 4 MiB result cap on
   its own.
   Cost if wrong: if the human wants larger values accepted (a long
   `context` hint, for instance), the bound needs to move to a higher
   number the directory-listing/result-cap accounting can still guarantee,
   rather than being removed outright.

4. **A single record larger than `result_default_kib` is still delivered on
   its own (soft-ceiling exception).**
   What: `result_default_kib` is enforced as a soft per-result budget: a
   result is trimmed to stay under it when possible, but the very first
   candidate record is still emitted even if it alone exceeds
   `result_default_kib`, as long as the whole serialized result stays under
   the 4 MiB hard cap. The hard cap itself is always enforced with no
   exception.
   Why: `max_message_kib` (up to 1024 KiB) can exceed `result_default_kib`
   (default 64 KiB) by configuration; without this exception, a single
   legally-sized message above the soft budget would wedge every cursor
   that reaches it, since trimming to zero records is not useful progress.
   Cost if wrong: if the human wants `result_default_kib` treated as a
   second hard ceiling with no exception, oversized single records need a
   different resolution (for example, a distinct "record too large for
   this budget" error) instead of being delivered.

5. **Search ranks at most the top 1000 candidates per call.**
   What: every `search` call (first page or a `cursor` continuation)
   re-runs the query, ranks at most 1000 candidates, and applies the
   cursor as a numeric offset into that call's ranking. A corpus with more
   than 1000 matches has candidates beyond rank 1000 that are never
   reachable through paging. Pages are not a snapshot: a write between two
   calls can shift a hit across a page boundary, so a hit may repeat or be
   skipped across pages under concurrent writes.
   Why: this is the plan's originally specified `searchPageMax` ceiling,
   carried through implementation; persisting a per-cursor snapshot was
   judged unnecessary cost for the common case (memory sets far below 1000
   hits, few concurrent writers).
   Cost if wrong: if the human wants every match reachable, or stable pages
   under concurrent writes, search needs a higher or no candidate ceiling
   and/or a persisted per-cursor result set.

6. **The maintenance tick's per-step deadline is best-effort for SQL, not a
   hard bound.**
   What: the tick's wall-clock deadline (half the configured cleanup
   interval) is enforced between chunks and for the HTTP embedding call.
   SQL statements themselves are not made context-aware: a lock wait or an
   `incremental_vacuum` call can still run past the deadline. `busy_timeout`
   (5 s) bounds only how long a statement waits for the database lock, not
   how long it executes once it holds it, so the overrun is not bounded to
   5 s (a long vacuum, or a capacity sweep that reacquires the writer for
   vacuum before its own next deadline check, can exceed it). This is
   harmless for correctness since every tick step is idempotent and safe
   to interrupt or repeat.
   Why: converting every SQL call in the tick to a context-aware/cancelable
   form was judged more implementation cost than the actual risk (an
   occasional, bounded overrun on an idempotent background step) justified.
   Cost if wrong: if the human wants a hard per-tick deadline regardless of
   SQL lock contention or vacuum duration, the tick's SQL calls need to be
   converted to context-aware forms (or the deadline check needs to run on
   a separate goroutine that can abort the connection).

7. **Task 16 was added for spec-required integration scenarios the
   implementation plan omitted.**
   What: an additional task (concurrent registration disambiguation, gap
   notices under concurrent age-based eviction, tick deadline responsiveness
   under a blocked embedding call, embedding immediate-pass and tick-drain
   behavior, lease contention between processes, and embedding-endpoint
   failure handling) was inserted into the implementation plan and executed
   as `internal/mcpserver/integration_more_test.go`.
   Why: the spec calls for these scenarios to be covered by integration
   tests, but the written implementation plan's task list did not include
   them; the gap was caught during implementation rather than planning.
   Cost if wrong: none expected — this only adds test coverage the spec
   already called for. If the human considers this out of scope for the
   plan as written, the task (and its tests) can be reverted without
   affecting any product code path.

8. **Send, EditMemory, and DeleteMemory run in a fixed order: receipt lookup
   first, hook and eviction outside any transaction, rate charge after
   acceptance.**
   What: each mutating call does auth and payload-shape validation, then the
   idempotency receipt lookup (a replay returns the stored result here,
   before any lookup of mutable state such as the channel, `reply_to`, or
   the live memory), then the inspection hook, then capacity eviction in
   its own transactions, and only then opens the immediate write transaction,
   where it re-runs auth, re-checks the receipt, charges the rate limiter,
   inserts, stores the receipt, and commits.
   Why: the plan's sequence ran inline eviction and the hook inside the
   write transaction, which deadlocks eviction against its own transaction
   and holds the writer lock for the hook's whole runtime, contradicting the
   spec's "serial within a process, concurrent across processes". Receipt
   lookup before mutable reads keeps a keyed retry replayable after the
   thing it named was edited, deleted, or evicted. Charging the token bucket
   after acceptance matches the spec's "accepted-operation rate".
   Cost if wrong: two concurrent same-key calls can both pass the outer
   receipt check; the in-transaction re-check (and ruling 14) closes that.
   Rejected operations are free, which is trivial.

9. **Over-cap `wait_seconds` on `receive` is clamped, not rejected.**
   What: a `wait_seconds` above `receive_max_wait_seconds` is silently
   reduced to the cap; a negative value is still a validation error.
   Why: consistent with how `count` is clamped in `receive` and `history`.
   Cost if wrong: a silent clamp instead of a loud error; trivial.

10. **Redelivery replays the original batch membership and re-stamps pending
    endpoints.**
    What: a batch token carries the `include_own` flag it was created with
    (a `:o0`/`:o1` suffix on the token), so redelivering an unacknowledged
    batch replays exactly the rows originally selected, ignoring the current
    call's `count` and `include_own`. If a redelivery has to be trimmed at
    the byte ceiling, every contributing subscription's `pending_end_seq` is
    re-stamped to the highest seq actually redelivered (a channel with no
    redelivered rows gets its cursor), so the next ack advances only past
    what was delivered.
    Why: the spec says redelivery returns "the same batch"; the plan's
    range-based redelivery under the current call's ceiling could omit an
    external message while the ack advanced past it (loss), and honoring the
    current `include_own` could drop or resurface own messages.
    Cost if wrong: none for external rows; own rows can only be affected by
    the original flag, which is preserved.

11. **`receive` reports at most 256 gap and 256 expired notices per call and
    delivers no messages while gap notices remain unreported.**
    What: `gaps` and `expired` are each bounded to 256 entries per call.
    Expiry deletes and gap cursor advances apply only to the reported
    entries; the rest surface on later calls. While any gap notice is
    deferred, the call returns metadata only (reported gaps, expired,
    notice) with no messages and no batch token; delivery resumes once the
    gap backlog drains. Deferred expiries do not block delivery (an
    expired-but-unreported subscription is simply not touched).
    Why: unbounded notice lists made a metadata-only result able to exceed
    the 4 MiB hard cap on its own, and a subscription whose gap was not yet
    reported could still contribute messages, so an ack could advance past
    the unreported gap permanently. With both bounds the worst-case
    metadata reserve leaves over 2 MiB of headroom under the hard cap.
    Cost if wrong: an agent with more than 256 gapped or expired channels
    learns about them over several calls.

12. **`agentbus mcp` may write one line to stderr on a fatal configuration
    error.**
    What: when the config cannot be loaded or the bus cannot be opened at
    startup, the process writes a single `agentbus: <error>` line to stderr
    and exits (also logging to the log file if it opened). Stdout is never
    touched, so the MCP transport is not corrupted.
    Why: the error occurs before any log file can exist.
    Cost if wrong: one stderr line.

13. **`embedding_api_key_file` readability is checked when the embedder is
    constructed, not at config load.**
    What: config load only resolves the path; a missing or unreadable key
    file fails `Open` (embedder construction) instead of `Load`.
    Why: the plan's own config test sets a nonexistent key file and requires
    `Load` to succeed.
    Cost if wrong: a bad key path fails at `Open` instead of at config load.

14. **Keyed idempotent operations serialize per (sender, key) within a
    process; inspection hook execution serializes per process.**
    What: `send`, `edit_memory`, and `delete_memory` with a non-empty
    `idempotency_key` take a process-local mutex keyed by (sender, key),
    held from the preflight receipt read through commit; unkeyed operations
    are unaffected. Hook runs take a bus-level mutex held only for the
    hook's runtime, never while a SQLite write transaction is open. The
    hook's `allow` field must be present and boolean; a missing or null
    value is treated as a malformed response (`inspection_unavailable`).
    Why: two concurrent identical-key calls could both miss the preflight
    receipt and both run the hook; the spec requires hook execution to be
    serial within a process. Decoding `allow` as a plain bool made `{}` and
    `null` silently decode to a non-retryable rejection.
    Cost if wrong: keyed operations serialize within a process, which is
    cheap since they are rare retries; the keyed-mutex map is never pruned.

15. **Process rulings with no product effect.**
    What: no new worktree was created (the branch was already isolated);
    every error the plan's code dropped is handled; helpers are shared
    rather than duplicated (`trimBytes`, `scanMessages`, and `status`
    reusing the bus's list calls); `cmd.WaitDelay` is set in the hook runner
    and the `embedRequestTimeout` typo is fixed; go-sdk's indirect requires
    in `go.mod` are accepted as within "exactly two direct dependencies";
    reviewer-driven test fixes were applied; and Task 16 observes chunking
    and lease contention at the database level (the tick logs no per-chunk
    or lease lines, and a single owner renewing the lease indefinitely is
    legal since the lease bounds duplicated effort, not fairness).
    Why: each was a reviewer or preflight finding whose fix was mechanical.
    Cost if wrong: none.
