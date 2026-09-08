# ADR 0003: v2 implementation deviations

Status: Proposed — controller rulings made during implementation on
2026-09-08; awaiting human approval. [ADR 0001](0001-discussion-decisions.md)
and [ADR 0002](0002-v2-decisions.md) stand unless the user approves these.

## Context

During task-by-task implementation and review of the v2 design (spec:
docs/superpowers/specs/2026-09-07-agentbus-local-design-v2.md), the
controller ruled on several points where an implementer's or reviewer's
finding required a product decision rather than a pure bug fix. Each ruling
below was made to keep implementation moving without blocking on the human;
none has been explicitly approved. If the user rejects one, the linked
behavior needs a follow-up fix.

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

5. **Search ranks the top 1000 candidates and pages that fixed list, rather
   than paging over a call-by-call recomputed ranking.**
   What: `search` selects the best-ranked 1000 matches once, and cursor
   paging with `count`/`cursor` moves through that fixed set; a corpus with
   more than 1000 matches has candidates beyond rank 1000 that are never
   reachable through paging.
   Why: this is the plan's originally specified `searchPageMax` ceiling,
   carried through implementation; recomputing full ranking on every page
   call was judged unnecessary cost for the same result in the common case.
   Cost if wrong: if the human wants every match reachable through paging
   regardless of corpus size, search's ranking/paging needs to either raise
   or remove the 1000-candidate ceiling, at the cost of recomputing (or
   otherwise re-deriving) ranking across a larger or unbounded candidate
   set per page.

6. **The maintenance tick's per-step deadline is best-effort for SQL, not a
   hard bound.**
   What: the tick's wall-clock deadline (half the configured cleanup
   interval) is enforced between chunks and for the HTTP embedding call.
   SQL statements themselves are not made context-aware: a lock wait or an
   `incremental_vacuum` call can still run past the deadline, bounded only
   by `busy_timeout` (5 s) per statement — not a guaranteed ≤5 s overrun,
   since a capacity sweep can reacquire the writer for vacuum before its
   own next deadline check. This is harmless for correctness since every
   tick step is idempotent and safe to interrupt or repeat.
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
