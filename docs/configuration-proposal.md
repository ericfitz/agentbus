# Approved configuration

Status: approved by the user on 2026-09-06, including defaults, bounds, fixed
limits, configuration mechanics, and logical SQLite capacity accounting.
This document keeps its original review filename to preserve existing links.

## Configuration mechanism

- One JSON file, defaulting to `~/.config/agentbus/config.json` on macOS and Linux.
  No file means use defaults. No generated file is required.
- `--config <path>` selects a different file. An automatically launched daemon
  receives the same absolute configuration path as its launching adapter.
- Precedence: built-in defaults, then the selected file. No environment-variable
  overrides or general-purpose per-setting CLI overrides initially.
- Reject unknown keys, wrong types, out-of-range values, and invalid combinations
  with the key and required correction. Never silently clamp values.
- Resolve a leading `~/` against the OS user's home directory and relative paths
  against the selected configuration file's directory.
- Configuration is read at process startup; no live reload initially. Apply changes
  with `agentbus stop`, then `agentbus daemon`, or let the next adapter operation
  start the daemon. Existing adapters reload configuration before reattaching.
- A running daemon is authoritative. An adapter using another config path or
  changed configuration reports a configuration mismatch and restart instructions
  rather than silently using different settings. One daemon per OS user.
- The daemon's local endpoint and singleton lock live in a fixed per-user runtime
  location independent of the data directory. Socket paths must fit both target
  platforms. Exact platform path selection is an implementation verification item.
- Identity selection is an adapter argument (`--agent <persistent-name>`), not a
  global daemon setting. Session and child IDs are supplied by harness integration.
- `status` reports effective settings and accounting. `reset` requires explicit
  confirmation, closes active attachments, resets SQLite and Redis, and leaves
  configuration intact. Reset removes identity/session/cursor state as well as
  messages; subsequent registration creates fresh bus state.

## User-configurable values

Units below are part of each setting's name. Integers only except hook timeout,
which permits fractional seconds. Bounds are validation limits, not promises
that every allowed budget is appropriate for every host.

| Setting | Default | Minimum | Maximum / allowed values | Meaning |
|---|---|---|---|---|
| `data_directory` | `~/.local/share/agentbus` | Nonempty path | Writable local directory | SQLite and managed Redis working data; changing it does not migrate old data |
| `redis_executable` | `redis-server` | Nonempty name/path | Executable with required capabilities | Resolved through PATH or an explicit path |
| `redis_budget_mib` | 512 | 64 | 65536 | Global Redis dataset budget |
| `sqlite_budget_mib` | 2048 | 64 | 1048576 | Global accounted SQLite data budget |
| `message_retention_hours` | 168 | 1 | 8760 | Maximum ordinary-message age |
| `cleanup_free_percent` | 25 | 5 | 90 | Free-capacity target after capacity cleanup |
| `cleanup_interval_seconds` | 60 | 5 | 3600 | Periodic age and tombstone cleanup |
| `cursor_idle_hours` | 72 | 1 | 720 | Explicit-subscription-activity expiry; default already approved |
| `tombstone_min_hours` | 72 | 72 | 720 | Minimum tombstone age; 72-hour minimum already approved; purge safety conditions still apply |
| `dedup_retention_hours` | 72 | 1 | 720 | Receipt retention after first accepted operation; retries do not extend it |
| `max_message_kib` | 64 | 1 | 1024 | Complete serialized message envelope limit, including metadata/references |
| `send_messages_per_second` | 100 | 1 | 10000 | Per-session accepted-operation rate; memory mutations count |
| `send_kib_per_second` | 1024 | 64 | 65536 | Per-session serialized mutation-byte rate |
| `prefetch_messages` | 256 | 1 | 4096 | Per-adapter queued delivery count |
| `prefetch_mib` | 4 | 1 | 64 | Per-adapter queued serialized delivery bytes |
| `recv_default_count` | 100 | 1 | 1000 | Default count when recv omits it |
| `recv_max_count` | 1000 | 1 | 10000 | Maximum accepted requested count; byte limit can reduce actual result size |
| `recv_max_wait_seconds` | 60 | 0 | 300 | Maximum requested wait; omitted wait is zero |
| `discovery_enabled` | true | N/A | true / false | Enables agent directory discovery; senders on shared channels remain visible |
| `inspection_command` | `[]` (disabled) | Empty or executable argv | At most 64 entries, 32 KiB encoded total | Executable followed by literal arguments; no shell interpolation |
| `inspection_timeout_seconds` | 5 | 0.1 | 60 | Deadline for one hook invocation |
| `log_level` | `info` | N/A | `debug`, `info`, `warn`, `error` | Diagnostic verbosity; no payload logging |

## Accounting and interactions

- Redis budget measures Redis-reported dataset allocation, including indexes; it
  is not a process-RSS ceiling. Redis must not independently evict arbitrary keys;
  Agentbus performs the agreed FIFO cleanup.
- SQLite budget accounts retained row data, including ordinary messages, all memory
  revisions/tombstones, dedup receipts, and metadata. It is a logical data budget,
  not a hard filesystem quota: database pages, indexes, and WAL can consume more
  disk. Deletion makes pages reusable; normal checkpointing/reclamation prevents
  treating reusable space as live data. The user approved this distinction.
- Ordinary-message age expiry and capacity eviction remove the corresponding
  history from SQLite and Redis, recording enough cursor boundary information to
  report gaps. Redis pressure must not cause evicted messages to return on rebuild.
- Memory revisions and protected tombstones follow their already approved special
  rules. If no ordinary cleanup can free space, use the agreed transient warning;
  do not silently accept a write that could not be committed.
- `recv_default_count <= recv_max_count`; prefetch byte capacity must hold at least
  one maximum-sized envelope. Receive count is a ceiling, not a batch-fill target.
- Message-count and byte rates use a one-second burst, with byte burst capacity at
  least one maximum envelope. Both limits must pass. Retries of a durably accepted
  operation do not count as new accepted mutations.
- Dedup receipts survive message eviction until their own expiry. An unchanged
  retry can return original acceptance even when the message has since been
  removed; acceptance does not promise present availability or delivery.
- Inspection applies to new mutations; a recognized accepted retry returns its
  stored result without re-running the hook.

## Fixed implementation limits, not user settings

These have a single approved value rather than a configurable min/max. Add knobs
later only if actual use demonstrates a need.

| Behavior | Fixed value |
|---|---|
| Heartbeat interval / attachment expiry | 10 seconds / 30 seconds |
| Cursor acknowledgment flush | 20 ms or 100 pending handoff reports, whichever comes first |
| Orderly shutdown grace | 5 seconds; uncommitted acknowledgments may redeliver |
| Automatic retry elapsed limit | 30 seconds, with bounded backoff; stop on explicit denial or validation failure |
| Receive/search serialized result budget | 4 MiB; whole records only, fewer results when needed |
| Internal JSON-RPC frame ceiling | 8 MiB; MCP framing limits must accommodate equivalent valid results |
| Search page default / maximum | 100 / 1000, also subject to result-byte budget |
| Hook output ceiling | 64 KiB each for stdout/stderr; exceeding it fails the invocation |
| Channel/persistent-name length | 1–128 UTF-8 bytes; no control characters |
| Diagnostic log rotation | 4 files of 16 MiB each; rejected message payloads are not logged |

Heartbeat expiry releases attachment ownership, not durable subscription state.
Acknowledgments only cover handed-off messages regardless of batching. Hook
reasons remain agent-visible but are bounded by the hook-output limit.

## Capacity warning instructions

The warning identifies which budget is exhausted and the actual configuration
path. It gives two professional recovery paths:

1. Increase the named budget in that file, then run `agentbus stop` and
   `agentbus daemon` with the same `--config` selection when applicable.
2. If the user intends to discard all bus data, run `agentbus reset` with the same
   configuration selection and confirm the explicitly described data loss, then
   start the daemon again.

These are approved command semantics; commands are not implemented yet. The
warning is non-persisted and uses connected adapters as already agreed.

## Remaining verification before implementation

Verify supported Redis version/search capabilities and Go library choices; the
Codex/Claude session-ID and lifecycle-to-MCP binding; cross-platform runtime-path
and singleton handling; SDK handoff behavior; and physical SQLite maintenance.
These are technical verification items, not grounds to silently change approved
behavior. Report any required architectural change for explicit review.
