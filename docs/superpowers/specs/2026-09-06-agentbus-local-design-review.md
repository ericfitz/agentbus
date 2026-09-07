# Agentbus local design review

Status: peer architectural review of
[the consolidated design](2026-09-06-agentbus-local-design.md), read against
[the decision record](../../adr/0001-discussion-decisions.md) and
[the approved configuration](../../configuration-proposal.md). Findings are
separated into assumptions that may not hold, choices the AI designer made
without an explicit human decision, gaps, and human decisions worth
reconfirming with their cost visible. Verification results for findings 1 and 2
are recorded at the end.

## Assumptions that may not hold

1. **The harness hands the adapter a native conversation ID.** The session
   model (resume restores cursors, `/clear` starts fresh, duplicate-attachment
   rejection, child binding) rests on this and the spec admits it is unverified.
   Neither Claude Code nor Codex is known to pass a session ID to an MCP server
   at spawn time, and Claude Code keeps MCP servers running across `/clear`, so
   the adapter would not see a restart. The realistic plumbing is a
   SessionStart hook writing the ID somewhere the adapter finds by parent PID,
   which brushes against the "never use PIDs" rule. Registration therefore
   cannot happen at adapter startup; it must be lazy at first tool call. If this
   fails, the fallback (agent-supplied session key) changes the operation
   contract.
2. **Children get their own attachment.** Claude Code subagents share the
   parent's MCP connection. Every subagent call arrives over the same adapter,
   indistinguishable from the parent. "Bind sender from the attachment" cannot
   produce `Sam2-implement804`. Either children are the parent as far as the bus
   is concerned, or child labels are sender-supplied untrusted hints.
3. **Transport handoff means delivery.** A recv with a long wait can outlive the
   harness tool-call timeout. The harness abandons the call, the adapter writes
   to a pipe nobody reads, the write succeeds, and the batch is acknowledged and
   lost. The spec never mentions MCP cancellation (`notifications/cancelled`).
   Treat a cancelled request as a failed handoff and keep the recv wait ceiling
   under the verified harness timeout.
4. **A user-installed `redis-server` has text search.** Stock Redis 7 does not.
   Debian and Fedora replaced Redis with Valkey, which has no search module.
   Redis 8 bundles the query engine, but distro and Homebrew builds must be
   checked. This may turn "user installs Redis" into "user installs Redis Stack
   from a vendor tarball."
5. **Prefetch buys latency.** The daemon is one local process reachable over a
   Unix socket; pull-on-recv costs well under a millisecond. Prefetch is why the
   spec needs in-flight windows, stale-report invalidation, obsolete-memory
   invalidation, and "content already handed off cannot be recalled." The
   decision is human-made, but its premise deserves reconfirmation.

## AI-made choices to push back on

6. **Full rebuild as the response to any uncertainty.** "Uncertain" is never
   defined. A failed `XADD` with a clear error is known-not-applied. Since the
   messages table already has a monotonic sequence, project with explicit
   stream IDs equal to that sequence; re-projection is then idempotent and
   recovery becomes "replay from last known projected sequence," which is not a
   second log. Reserve clear-and-rebuild for a Redis that is empty or carries
   the wrong generation marker.
7. **Memory delivery is a hybrid with an undefined seam.** Creation flows
   through durable cursors, edits are non-persisted broadcasts, but rev2 is a
   new row with its own sequence. If rev2 appears in the stream, subscribers get
   the memory twice; if not, an offline subscriber never learns of the edit.
   Simplest consistent rule: every revision row is a stream entry, and a row
   tombstoned at delivery time is skipped. That removes the
   broadcast-and-invalidate machinery. Deletion still has no durable event under
   the human rule "no replacement row"; flag that residual explicitly.
8. **Who generates the operation ID** is never stated. If the adapter does,
   dedup protects only adapter-to-daemon retries over a persistent local socket,
   and 72 hours of durable receipts that outlive message eviction is heavy for
   that. If the model does, expect ID reuse and confusing conflict errors.
   Standard answer: adapter generates, plus an optional agent-supplied
   idempotency key.
9. **Result sizes were set for the transport, not the consumer.** A 4 MiB recv
   result is roughly a million tokens; the default count of 100 at 64 KiB each
   already exceeds it. Add a small default byte ceiling for recv and search
   results, independent of the frame budget.
10. **Two budgets where one binds.** Redis holds the same retained ordinary
    messages plus live memories that SQLite holds, so at 512 MiB versus 2048 MiB
    the Redis budget is almost always the constraint. Say so, or collapse to one.
11. **Fixed runtime path with no environment override.** Tests require isolated
    data and a dedicated test Redis, but the socket and lock live at a fixed
    per-user path and configuration bans environment overrides. Tests will
    collide with the developer's real daemon. The runtime directory needs an
    override.
12. **Adapter behavior on duplicate-attachment rejection** is unspecified. A
    newcomer with the same native ID is almost always a legitimate restart
    behind a hung predecessor. The adapter should retry until attachment expiry
    before surfacing the error, otherwise the harness marks the server dead.
13. **Hook concurrency** is unspecified. Serial hooks cap throughput at one over
    hook latency; concurrent hooks let two sends from one sender commit out of
    submission order within a channel. Choose and document.

## Gaps

- **No channel listing, no history read, no channel deletion.** Agents must
  know channel names out of band. A new session cannot fetch recent messages on
  a channel unless search accepts empty text. Channels accumulate forever.
- **Own messages.** Nothing says whether a session receives its own sends.
  Default to excluding them.
- **Binary and schema versioning.** An upgraded adapter can attach to an older
  running daemon. Registration needs a version check; SQLite needs a schema
  version.
- **Daemon detachment.** The auto-started daemon must `setsid` and must not
  inherit the adapter's stdout, which is the MCP channel. Orphan Redis after a
  daemon crash needs a generation marker so the next daemon clears rather than
  trusts it.
- **Startup cost.** Every daemon start recomputes SQLite accounting over up to
  2 GiB and rebuilds Redis in full because stop kills Redis. Expect tens of
  seconds of "unavailable" at the first tool call of the day, longer than the
  30-second automatic retry. RDB persistence on the dedicated Redis, or ordering
  the rebuild (memories and subscribed backlogs first), would soften this.
- **Capacity warning cadence.** Broadcast to every channel every cleanup
  interval is 60-second spam.

## Human decisions to reconfirm with the cost visible

- **Redis.** Roughly a third of the spec exists to keep Redis consistent with
  SQLite: projection, recovering state, rebuild pause, dual budgets, eviction in
  two stores, tombstone purge guards, capability probing, child-process
  management. SQLite with FTS5 and an index on channel and sequence does streams
  and text search in the same transaction, and every one of those sections
  disappears. The spec should name the specific Redis feature that earns that
  cost.
- **Last-write-wins with no expected-revision check.** Two agents reading rev1
  and both editing is the classic lost update. If rejection stays out, return
  the overwritten revision ID in the edit result so an agent can notice.
- **Acknowledge on transport handoff.** Fine as a rule, provided finding 3 is
  addressed.

## Suggested order

Verify findings 1 and 2 in real harnesses before any other work, then 3, then
4. Settle findings 6 and 7 before writing an implementation plan.

## Verification of findings 1 and 2

Verified on 2026-09-06 with a stdio probe MCP server that logged its argv,
environment, every JSON-RPC message, and process IDs, plus Claude Code hooks
that logged their input. Harness versions: Claude Code 2.1.263, Codex CLI
0.153.4. Probe sources live in the session scratchpad, not in this repo.

### Claude Code

| Scenario | Observed |
|---|---|
| Fresh start (`-p` and interactive) | MCP server spawned once per Claude process with `CLAUDE_CODE_SESSION_ID` in its environment, matching the SessionStart hook's `session_id`. Parent PID of the server equals the parent PID of every hook invocation. |
| Tool call metadata | `tools/call` `_meta` carries only `claudecode/toolUseId` and `progressToken`. No session or agent identity per call. |
| Subagent (`Agent` tool, general-purpose) | Subagent's tool call arrived on the **same** server process as the main agent's, with no distinguishing field in the MCP request. The PreToolUse hook for that call carried `agent_id`, `agent_type`, and `tool_use_id`; the main agent's PreToolUse carried no `agent_id`. |
| `/clear` (interactive, driven through a pty) | SessionStart hook fired with `source: clear` and a **new** `session_id`. The MCP server process was **not** restarted; its environment still held the old session ID, and the post-clear tool call was indistinguishable from the pre-clear one at the MCP layer. |
| `--resume <id>` | New Claude process, MCP server respawned with `CLAUDE_CODE_SESSION_ID` equal to the resumed ID; SessionStart hook fired with `source: resume`. |

Consequences for the design:

- Finding 1 holds for `/clear` and is refuted for launch and resume: the spawn
  environment is a correct conversation ID at attach time, but it goes stale
  on `/clear` with no signal to the adapter. The only channel that sees the new
  ID is a SessionStart hook. Rendezvous between hook and adapter is by shared
  parent PID (the Claude process), so the "never use PIDs" rule needs a carve-out
  for rendezvous, distinct from identity. Registration must therefore be
  re-evaluated per tool call, not fixed at adapter startup.
- Finding 2 holds fully: children do not get their own attachment. Child
  identity is recoverable only by a PreToolUse hook writing the
  `tool_use_id` to `agent_id` mapping and the adapter joining on
  `claudecode/toolUseId` from `_meta`. Without the hook installed, the bus
  cannot distinguish a child from its parent, and any child label is a
  sender-supplied hint. The `Sam2-implement804` naming needs `agent_id`, and
  no task name is available from the harness.
- Integration for Claude Code therefore requires shipping hook configuration
  alongside the MCP server entry, and the design must state what happens when
  the hook is absent.

### Codex

| Scenario | Observed |
|---|---|
| Fresh start (`codex exec`) | MCP server spawned with **no** harness-specific environment variables. |
| Tool call metadata | Every `tools/call` carries `_meta["x-codex-turn-metadata"]` with `session_id`, `thread_id`, `turn_id`, `thread_source` (`user` or `subagent`), and for children `parent_thread_id`, `forked_from_thread_id`, and `subagent_kind`. Also a top-level `threadId` and `callId`. |
| Subagent (`collaboration.spawn_agent`) | Codex spawned a **second** MCP server process for the child thread. The child's call arrived on that new process with `thread_source: subagent` and its parent thread ID. |

Consequences for the design:

- Codex supplies full identity per call, including parent linkage, with no
  hooks and no environment. Finding 1 is refuted for Codex; finding 2 is refuted
  for Codex because children get their own attachment.
- Codex identity is per-call metadata rather than per-attachment state. An
  adapter that binds identity at registration would be wrong on Codex the
  same way it is wrong on Claude Code after `/clear`.

### Design implications common to both

- Bind identity per tool call from harness-supplied evidence, not once per
  attachment. The attachment is a transport, not an identity.
- The "duplicate live attachment" rule keyed on native session ID cannot be
  evaluated at attach time on Claude Code, and on Codex one session legitimately
  has several attachments (one per subagent thread). The rule needs restating
  in terms of per-call identity, or dropping.
- The child model must accept two shapes: separate attachment with per-call
  metadata (Codex) and shared attachment with hook-derived mapping (Claude
  Code). "A child additionally supplies its native child ID and parent binding"
  is true for Codex and false for Claude Code without hooks.
