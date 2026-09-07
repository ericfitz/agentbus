# Agentbus local design v2

Status: design approved section by section in discussion on 2026-09-07;
awaiting written-spec review. Supersedes
[the v1 design](2026-09-06-agentbus-local-design.md) and the v1
[configuration specification](../../configuration-proposal.md). Human-made
decisions for v2 are recorded in [ADR 0002](../../adr/0002-v2-decisions.md);
v1 decisions in [ADR 0001](../../adr/0001-discussion-decisions.md) stand unless
ADR 0002 supersedes them. The changes were motivated by
[the v1 review](2026-09-06-agentbus-local-design-review.md) and its harness
verification results.

## Purpose and scope

Agentbus lets coding agents belonging to one OS user on one macOS or Linux
machine exchange channel messages and maintain mutable shared memories, with
text and semantic search over memories. SQLite is the only store. There is no
daemon: every MCP adapter process opens the same database file.

Out of scope: cross-machine transport, cross-user authentication or
authorization, native Windows runtime, human monitoring UI, channel-type
conversion. Windows paths remain valid reference values.

## What changed from v1

- Redis removed. Streams are indexed SQLite queries; text search is FTS5.
- Daemon removed. Adapters open SQLite directly in WAL mode. No socket, no
  JSON-RPC framing, no singleton lock, no auto-start, no prefetch, no handoff
  reports, no projection or recovery state.
- Identity is declarative. The agent calls `register`; every other operation
  carries `as`. Native harness conversation IDs are not used.
- Cursor acknowledgment is a batch token passed on the next `receive`.
- Semantic search over memories through an OpenAI-compatible embeddings
  endpoint, brute-force ranked in Go.
- Every memory revision is a stream row, so edits reach cursors durably.
- Added `list_channels`, `history`, own-message exclusion, a result byte
  ceiling sized for a context window, and environment overrides for tests.
- `recv` is `receive` everywhere.

## Components and process model

One Go executable, `agentbus`, with subcommands:

| Command | Purpose |
|---|---|
| `mcp` | stdio MCP server spawned by the harness |
| `status` | Print live sessions, channels, usage against budget, current notice, embedding backlog, config path |
| `reset` | Wipe all bus data after confirmation; warn about live sessions first |
| `identity` | Print the one-line registration prompt for the current directory (see Install convention) |

Every `mcp` process opens the database in WAL mode with `busy_timeout` set and
`auto_vacuum = incremental`. All state lives in that file. The harness spawns
one `mcp` process per MCP connection: Claude Code one per conversation, Codex
one per thread including subagent threads. The bus does not distinguish these.

Background work is opportunistic. Each `mcp` process runs a maintenance tick
every `cleanup_interval_seconds` and takes a lease before doing it, so one
process works at a time. Long-poll receive queries SQLite every 250 ms until
its wait expires. The inspection hook and the embedding client run in whichever
process needs them; embedding never runs inside a write path.

Logs go to a rotating file in the data directory, never to stdout or stderr:
stdout is the MCP channel and stderr is captured by the harness.

### Reset

`reset` wipes every table inside one transaction and never unlinks the file.
A live process notices on its own: its session row is gone, so its next
heartbeat touches zero rows and its next operation fails the `as` lookup with
`not_registered`, the same error as never having registered. Re-registering
recovers. `reset` warns about live sessions before asking for confirmation.

## Identity and registration

`register` is the only operation without `as`.

| Input | Meaning |
|---|---|
| `name` | Required. 1–128 UTF-8 bytes, no control characters, no `/` |
| `parent` | Optional display name of the parent identity |
| `context` | Optional repository hint; defaults to the basename of the adapter's working directory |
| `resume` | Default true. False drops the display name's existing subscriptions first |

Output: the display name to pass as `as`, whether the identity was resumed or
fresh, and for a resumed identity each subscribed channel with its pending
message count.

**Display-name allocation.** The requested name is granted if no live session
holds it, otherwise the first free numeric suffix: Sam, Sam2, Sam3. A child's
display name is the parent's display name, `/`, and the child's requested name,
with the same suffix rule: `Sam/implement804`, `Sam/implement804-2`. Historical
messages keep the display name at send time.

**Live sessions.** Each `mcp` process generates a random owner token at
startup. Registration inserts a `sessions` row keyed by display name with the
owner token and a heartbeat. The process refreshes the heartbeat every 10
seconds. Refresh and every `as` lookup are conditioned on the owner token, so a
process that stalled past 30 seconds and lost its name to a newer registration
gets `not_registered` rather than acting under a name it no longer owns.
Maintenance deletes rows with stale heartbeats.

**Continuity.** Subscriptions and cursors are keyed by display name, not by
session or harness conversation. Registering a name nobody holds resumes that
name's unexpired subscriptions. Child names usually carry a task suffix, so
children are fresh by construction under the same rule. `/clear`, a fresh
launch, and a resume all look the same to the bus: the agent registers, and
`resume` says whether to continue.

**`as` on every call.** Required, never defaulted. A missing `as` is a
validation error the model fixes on its next call. This keeps the adapter
stateless, so Claude Code (children share the adapter) and Codex (one adapter
per child) run the same code path, and a forgotten `as` is loud rather than a
silent misattribution. The value is the display name `register` returned; there
is no opaque token because there is nothing to protect within one OS user.

**Directory.** `discover` lists live sessions with display name, context, and
registration time when `discovery_enabled` is set. Message rows always carry
the sender regardless.

### Install convention, outside the bus

A per-repo, git-ignored `.local/agentbus.json` found by walking up from the
working directory holds one field, `identity`. `agentbus identity` reads it and
prints one line such as `Agentbus: call register with name Sam`; with no file it
suggests the repository directory's basename. The documented install is a
Claude Code SessionStart hook running `agentbus identity`, which fires on
startup, `/clear`, and resume, and an AGENTS.md line or hook equivalent for
Codex. The bus reads that file in no other path. The harness server entry is
named `agentbus`, which is the namespace the harness prefixes onto tool names;
tool names carry no prefix of their own, and every tool description opens with
`Agentbus:` for disambiguation.

## Operations

Every operation except `register` takes `as`. Tool names are illustrative; the
SDK's conventions may adjust them without changing semantics.

| Operation | Input and behavior |
|---|---|
| `register` | See above |
| `create_channel` | Name, kind `ordinary` or `memory`. Idempotent on matching kind; `conflict` otherwise |
| `list_channels` | All channels with kind, message count, and latest sequence |
| `subscribe` | Channel, `from` as `now` (default) or `oldest`. Creates or keeps the cursor for this sender |
| `unsubscribe` | Channel. Drops the cursor and any pending batch |
| `send` | Channel, content, optional type, `reply_to`, metadata, references, `idempotency_key`. On a memory channel creates a memory. Returns `seq` |
| `receive` | Optional `ack`, `count`, `wait_seconds`, `channels`, `include_own` (default false). Returns messages, a batch token, and notices |
| `history` | Channel, `before` or `after` sequence, count. Reads without touching cursors |
| `search` | Query, `mode` `text`, `semantic`, or `both` (default `both` when embeddings are configured, else `text`), optional channel, sender, time range, thread, count, continuation token |
| `get_memory` | Memory ID. Current live revision or `not_found` |
| `edit_memory` | Memory ID, replacement content and envelope fields, `idempotency_key`. Last committed write wins. Returns the new revision and the revision it replaced |
| `delete_memory` | Memory ID, `idempotency_key`. Tombstones the current revision |
| `discover` | Live sessions, when discovery is enabled |

**Result budgets.** `result_default_kib` (64) is the per-result byte ceiling
sized for a context window; 4 MiB is the fixed hard ceiling. Results contain
whole records and stop early to fit. Count is a maximum, not a batch-fill
target.

**Identifiers.** `seq` is the only message identifier. A memory's ID is the
`seq` of its first revision. `reply_to` is a `seq`.

**Idempotency.** `idempotency_key` is optional, scoped to the sender, and
stored in `receipts` for `receipt_retention_minutes` with the original result.
A repeated key with the same payload returns the original result without a new
row, hook run, or rate charge; a repeated key with a different payload is
`conflict`. There is no adapter-side automatic retry: the adapter writes SQLite
directly, and the only lost-response path is between adapter and harness, where
the model retries with its key.

**Errors.** Each failure carries a code, message, and retryable flag:
`validation`, `not_registered`, `not_found`, `conflict`, `rate_limited`,
`inspection_rejected` with the hook's reason, `inspection_unavailable`,
`capacity_exhausted`, `internal`. Cursor expiry and retention gaps are
notices inside a successful `receive` result, not errors.

**Rate limits.** Per sender, `send_messages_per_second` and
`send_kib_per_second` with a one-second burst, enforced in the process handling
the call. Memory mutations count. Receipt replays do not.

## Storage schema

One SQLite file, WAL mode.

| Table | Columns and purpose |
|---|---|
| `channels` | `name` PK, `kind`, `created_seq`, `evicted_before_seq` (retention boundary for gap reports) |
| `sessions` | `sender` PK, `context`, `owner`, `heartbeat`, `registered_at`. Live sessions only |
| `subscriptions` | (`sender`, `channel`) PK, `cursor_seq`, `last_activity`, `pending_token`, `pending_end_seq` |
| `messages` | `seq` INTEGER PRIMARY KEY AUTOINCREMENT, `channel`, `sender`, `context`, `created_at`, `type`, `content`, `reply_to`, `metadata` JSON, `refs` JSON, `memory_id`, `revision`, `tombstone`, `tombstone_at`, `bytes`. One row per ordinary message and per memory revision |
| `messages_fts` | FTS5 external-content table over `content`, maintained by triggers. Queries join `messages` to exclude tombstones |
| `embeddings` | `seq` PK referencing `messages`, `model`, `vector` BLOB of normalized float32. Live memory revisions only |
| `receipts` | (`sender`, `key`) PK, `fingerprint`, `result` JSON, `expires_at` |
| `leases` | `name` PK, `owner`, `expires_at`. One row, `maintenance` |
| `notices` | `kind` PK, `message`, `set_at`. At most one row, `capacity` |

Indexes: (`channel`, `seq`); (`memory_id`, `tombstone`); `tombstone_at`;
`created_at`; `sessions.heartbeat`; `receipts.expires_at`.

`seq` is the single global order. Per-channel order is `seq` filtered by
channel; receive across channels merges by `seq`, so cross-channel order is
deterministic though not a promised property.

**Envelope.** A message row carries channel, sender, context, server
timestamp, type, content, optional `reply_to`, optional key/value metadata, and
structured references of kind URL, Unix path, or Windows path whose values are
preserved verbatim and never dereferenced. The complete serialized envelope is
limited to `max_message_kib`. The bus sets sender, context, and timestamp;
callers cannot.

**Capacity accounting.** One budget, `sqlite_budget_mib`. Usage is
`page_count` minus `freelist_count`, times `page_size`, from SQLite pragmas: the
space live data occupies, exact, with no counter maintained in write
transactions. This replaces the v1 logical row-byte accounting, which existed
to reconcile two stores.

**Memory revisions.** Editing memory 123 is one transaction: mark the current
revision tombstoned with a timestamp, insert the next revision row. Deletion
marks without inserting. Purge removes tombstoned rows older than
`tombstone_min_hours`; their FTS and embedding rows go with them.

## Receiving and acknowledgment

1. Validate `as` against `sessions` with the owner check. Touch
   `last_activity` on the subscriptions involved; this is the only thing that
   renews the idle clock.
2. If `ack` is present, every subscription of this sender whose
   `pending_token` matches advances `cursor_seq` to `pending_end_seq` and clears
   the pending pair. A token matching nothing is reported as `ack_ignored`.
3. If a pending batch exists and no ack was given, deliver the same batch
   again: the surviving messages between the cursor and `pending_end_seq`. The
   result carries `redelivered: true` and a one-line instruction naming the
   token to acknowledge.
4. Otherwise select messages across the sender's subscriptions (or the
   `channels` filter) with `seq` above each cursor, merged by `seq`, excluding
   the sender's own messages unless `include_own`, skipping rows tombstoned at
   delivery time. Stop at `count` or the result byte ceiling, whole records
   only. Each contributing subscription gets `pending_end_seq` set to its
   highest delivered `seq`, all sharing one new token returned in the result.
5. If nothing is available and `wait_seconds` is positive, poll every 250 ms
   until the deadline.

**Bounds on a pending batch.** It is at most one `count` of messages. Idle
expiry deletes the subscription row and the pending pair with it. Retention
evicts the messages, and redelivery reports the evicted part as a gap. So an
absence of days yields at most one redelivered batch with a gap notice, and an
absence past `cursor_idle_hours` yields an `expired` notice and a fresh start.

**Gaps.** If a subscription's cursor is below the channel's
`evicted_before_seq`, the result carries a gap notice naming the channel and
the evicted range, and delivery resumes from the oldest surviving message. A
gap says the messages are gone, not that they were delivered.

**Expiry.** A subscription whose `last_activity` is older than
`cursor_idle_hours` is deleted and the result carries an `expired` notice for
that channel; receive succeeds for the others. Resubscribing starts at `now`
unless `from: oldest`.

**Memory channels.** Every revision row is a stream row, so subscribers see
creations and edits in sequence. A revision superseded before delivery is
skipped and the current one arrives at its own position, so nobody receives
stale content or the same memory twice. Deletion inserts no row, so a
subscriber that has passed the last revision learns of deletion only through
`get_memory`. This is a known limit of the "no replacement row on delete"
rule.

**Notices.** Every receive result includes the current row of `notices`, if
any. Maintenance writes the capacity warning there and clears it when ordinary
cleanup can free space again. Nothing is queued, dropped, or fanned out.

**Harness timeouts.** `receive_max_wait_seconds` must stay below the harness
MCP tool-call timeout; the verified ceiling is recorded under Testing and
verification. A
receive whose response is lost is redelivered on the next call because its
batch is pending.

## Memories and semantic search

**Lifecycle.** A send on a memory channel creates a memory whose ID is that
row's `seq`. `edit_memory` tombstones the current revision and inserts the
next in one transaction; `delete_memory` tombstones without inserting;
`get_memory` returns the live revision. Concurrent edits serialize; the last
committed write wins, and the edit result names the revision it replaced so a
caller can notice it overwrote something it never saw. Editing a deleted memory
returns `not_found`.

**Embedding configuration.** `embedding_endpoint` (an OpenAI-compatible
`/v1/embeddings` URL), `embedding_model`, and `embedding_api_key_file`, all
unset by default. The key file is read by the program and never logged; it may
be a bare key or a one-line `export NAME='value'` file. With the endpoint unset,
search is text only and nothing leaves the machine. Ollama, LM Studio, OpenAI,
Voyage, and Gemini all serve this request shape.

**Pipeline.** Live memory revisions with no `embeddings` row for the
configured model are embedded by the maintenance tick, up to 64 texts per
request with a 30-second timeout and a fixed number of batches per tick. A
process that commits a memory mutation also runs one best-effort pass
immediately in the background, so a new memory is normally searchable within a
second. Failures are logged and retried on later ticks and never block a write.
Vectors are normalized at store time. Rows embedded under another model are
deleted and re-embedded when the configured model changes.

**Semantic search.** Only memory channels are embedded; ordinary messages are
text only. A semantic query embeds the query synchronously with a 10-second
timeout, applies the SQL filters to select candidate live revisions, loads
their vectors, and ranks by dot product in Go. Brute force is deliberate: at
single-user volumes it is milliseconds. The upgrade path, if memory count
reaches the tens of thousands, is a vector extension. If the endpoint is
unreachable, search falls back to text and the result carries
`semantic_unavailable`.

**Combined mode.** `both` merges text and semantic rankings by reciprocal rank
fusion. Results carry `seq`, channel, sender, content, revision, and score.
Continuation is an offset into a ranking recomputed per page.

**Retention.** Memories and their embeddings are never age- or
capacity-evicted.

## Maintenance, capacity, inspection

**Tick.** Every `cleanup_interval_seconds`, each `mcp` process attempts the
lease with one conditional update:

```sql
UPDATE leases SET owner = ?, expires_at = ?
WHERE name = 'maintenance' AND (expires_at < ? OR owner = ?)
```

One changed row means this process runs the tick, in order: delete sessions
with stale heartbeats; delete expired receipts; delete subscriptions idle past
`cursor_idle_hours`; delete ordinary messages older than
`message_retention_hours`, raising each channel's `evicted_before_seq`; purge
tombstoned revisions older than `tombstone_min_hours`; capacity eviction if
over budget; `incremental_vacuum`; the embedding pass.

The lease bounds duplicated effort, not correctness: every step is idempotent
and runs in short transactions. Constraints on the tick, all fixed:

- Every cleanup statement deletes at most 1000 rows per transaction, releasing
  the writer lock between chunks.
- `incremental_vacuum` reclaims a fixed page count per tick.
- The embedding pass is capped at a fixed number of batches per tick.
- The tick stops at a wall-clock deadline of half the interval and resumes at
  the next tick. The lease expires after two intervals.

**Capacity.** When usage exceeds `sqlite_budget_mib`, ordinary messages are
evicted globally oldest-first until usage is below the budget less
`cleanup_free_percent`, updating channel boundaries. Send checks usage before
writing and runs eviction inline if needed. If eviction cannot get under budget
because live memories and protected tombstones fill it, the write fails with
`capacity_exhausted` and maintenance writes the notice naming the budget and
config path with two recovery paths: raise the budget, or `agentbus reset`.
Live memories are never evicted automatically.

**Inspection hook.** Configured argv executed directly, no shell. Input: JSON
with operation kind, validated payload, and sender. Output: one JSON decision
with optional reason. `inspection_timeout_seconds` deadline, 64 KiB stdout and
stderr caps. No hook means allow. Explicit denial returns `inspection_rejected`
with the reason in the original response; timeout, crash, malformed or
oversized output returns `inspection_unavailable`. A recognized receipt replay
does not re-run the hook. The hook runs in the process handling the send:
serial within a process, concurrent across processes. Commit order is whatever
SQLite serializes, so two sends from different senders may commit in the
opposite order to their hook completion. Log operation, sender, and bounded
reason, never the rejected payload.

## Configuration

One JSON file, default `~/.config/agentbus/config.json`, selected by
`--config` or `AGENTBUS_CONFIG`. `AGENTBUS_DATA_DIR` overrides
`data_directory`; these two environment variables exist primarily so tests can
isolate. Precedence: built-in defaults, then the file, then the two
environment variables. Unknown keys, wrong types, out-of-range values, and
invalid combinations are rejected with the key and required correction; values
are never clamped. `~/` resolves to the home directory; relative paths resolve
against the config file's directory. Each `mcp` process reads config at
startup, so a change takes effect as harnesses restart.

| Setting | Default | Min | Max / allowed | Meaning |
|---|---|---|---|---|
| `data_directory` | `~/.local/share/agentbus` | nonempty | writable directory | Database and logs |
| `sqlite_budget_mib` | 2048 | 64 | 1048576 | Capacity budget on live pages |
| `message_retention_hours` | 168 | 1 | 8760 | Maximum ordinary-message age |
| `cleanup_free_percent` | 25 | 5 | 90 | Free target after capacity eviction |
| `cleanup_interval_seconds` | 60 | 5 | 3600 | Maintenance tick interval |
| `cursor_idle_hours` | 72 | 1 | 720 | Subscription idle expiry |
| `tombstone_min_hours` | 72 | 72 | 720 | Minimum tombstone age before purge |
| `receipt_retention_minutes` | 60 | 1 | 1440 | Idempotency receipt lifetime |
| `max_message_kib` | 64 | 1 | 1024 | Complete serialized envelope limit |
| `send_messages_per_second` | 100 | 1 | 10000 | Per-sender accepted-operation rate |
| `send_kib_per_second` | 1024 | 64 | 65536 | Per-sender mutation-byte rate |
| `receive_default_count` | 100 | 1 | 1000 | Count when `receive` omits it |
| `receive_max_count` | 1000 | 1 | 10000 | Maximum requested count |
| `receive_max_wait_seconds` | 60 | 0 | 240 | Maximum requested wait; verified harness ceiling is 300 seconds |
| `result_default_kib` | 64 | 1 | 4096 | Per-result byte ceiling |
| `discovery_enabled` | true | | true / false | Enables `discover` |
| `inspection_command` | `[]` | empty or argv | 64 entries, 32 KiB | Hook executable and literal arguments |
| `inspection_timeout_seconds` | 5 | 0.1 | 60 | Hook deadline |
| `embedding_endpoint` | unset | | URL | OpenAI-compatible embeddings endpoint |
| `embedding_model` | unset | | nonempty | Model name sent with each request |
| `embedding_api_key_file` | unset | | readable file | Key file; bare key or `export` line |
| `log_level` | `info` | | `debug`, `info`, `warn`, `error` | Diagnostic verbosity; no payload logging |

`receive_default_count <= receive_max_count`. `embedding_model` is required
when `embedding_endpoint` is set. Rate limits use a one-second burst with byte
burst at least one maximum envelope.

Fixed limits, not settings: heartbeat every 10 seconds, expiry at 30; receive
poll interval 250 ms; embedding batch 64, request timeout 30 s, query timeout
10 s; 4 MiB hard result ceiling; search page 100, maximum 1000; hook output
64 KiB per stream; names 1–128 UTF-8 bytes without control characters or `/`;
1000 rows per cleanup transaction; tick deadline half the interval; lease
expiry two intervals; log rotation four 16 MiB files.

## Testing and verification

**Unit checks.** Input validation and name rules, display-name suffix
allocation, receipt fingerprinting across key order, config bounds and
rejection messages, cosine ranking and reciprocal rank fusion, key-file
parsing.

**Integration tests against real SQLite.** Tests set `AGENTBUS_DATA_DIR` to a
temporary directory and spawn real `agentbus mcp` processes, driving them over
stdio with a minimal MCP client in the test package. No mocks of the database
or transport. Required scenarios:

- Concurrent registration of the same name yields a suffix; a stalled process
  loses its name and gets `not_registered`.
- Register with and without resume; child naming and collision suffixes.
- Receive, ack, redeliver: redelivery until acknowledged, `ack_ignored` on a
  stale token, `redelivered` flagged, cross-channel order by `seq`.
- Gaps after age and capacity eviction with chunked deletes under concurrent
  sends; cursor expiry notice; `from: oldest`.
- Memory edit delivered once with current content, superseded revision
  skipped, delete then `not_found`, tombstone purge after the minimum age.
- Idempotency replay, conflicting replay, receipt expiry.
- Capacity eviction to target; `capacity_exhausted` when memories fill the
  budget; notice set and cleared.
- Hook allow, deny with reason, timeout, oversized output, malformed output.
- Embedding pipeline against a fake HTTP endpoint: backlog drained by the
  tick, immediate pass after a write, model change re-embeds, endpoint failure
  yields `semantic_unavailable` with text fallback.
- Lease contention between processes, tick deadline, reset while a process is
  live.

**Harness facts already verified** (2026-09-06, Claude Code 2.1.263, Codex
0.153.4): both harnesses namespace tools as `mcp__<server>__<tool>`; Claude
Code keeps one adapter across `/clear` and shares it with subagents; Codex
spawns one adapter per thread; the Claude Code SessionStart hook fires on
startup, `/clear`, and resume.

**Entry criteria, verified 2026-09-07** (Claude Code 2.1.263, Codex 0.153.4,
Go 1.27.1):

1. Hook context injection. A Claude Code SessionStart hook that echoes
   `Agentbus: call register with name Zebra` was visible to the model on
   startup and on `--resume`, and a hook that echoes a different name when
   `source` is `clear` was visible after `/clear` in an interactive session.
   Codex supports the same `hooks.json` format at `$CODEX_HOME/hooks.json`
   with a `SessionStart` matcher of `startup|resume|clear`, and the injected
   line was visible to the model. Codex requires hook trust to be recorded in
   its config (or `--dangerously-bypass-hook-trust`), which the install notes
   must mention.
2. Tool-call timeouts. Claude Code's default MCP tool timeout is 300 seconds
   (`MCP_TOOL_TIMEOUT` environment variable, or a per-server `timeout` field in
   the MCP config; progress notifications do not extend it). Codex completed a
   200-second tool call under its defaults and exposes a per-server
   `tool_timeout_sec`. Both harnesses returned a 200-second probe result
   intact with no cancellation. `receive_max_wait_seconds` therefore keeps a
   default of 60 with a maximum of 240, under the lowest verified ceiling.
3. Libraries. `modernc.org/sqlite` v1.58.0 creates an FTS5 external-content
   table, matches on it, and opens WAL mode with `CGO_ENABLED=0`, and reports
   `page_count`, `freelist_count`, and `page_size`. The official
   `github.com/modelcontextprotocol/go-sdk` resolves at v1.7.0. These two
   modules plus the standard library are the dependency set.

Implementation must use the verified versions or newer and re-run the FTS5 and
WAL check when the driver is upgraded.
