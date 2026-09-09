# ADR 0002: v2 design decisions

Status: accepted 2026-09-07. Consolidated design is in
docs/superpowers/specs/2026-09-07-agentbus-local-design-v2.md. Decisions in
[ADR 0001](0001-discussion-decisions.md) stand unless superseded below.

## Human-made decisions

The user explicitly directed or selected the following:

- Simplify as much as possible; remove Redis. Supersedes the ADR 0001
  decisions on a dedicated Redis process, Redis projection and recovery, and
  the Redis budget.
- Drop the daemon. Each MCP adapter opens SQLite directly. Supersedes the
  ADR 0001 decisions on a single bus daemon, Unix-socket JSON-RPC IPC,
  automatic daemon startup, the `daemon` and `stop` commands, prefetch
  notifications, and handoff acknowledgment.
- Identity is declarative: the agent registers on connect, prompted by a
  skill or hook, persisting its chosen name in a local config file; subagents
  register sub-identities referring to the parent. Supersedes the ADR 0001
  decisions binding identity to native harness conversation IDs, the `--agent`
  adapter argument, and duplicate-attachment rejection by native session ID.
- Add semantic memory search.
- Embeddings through an OpenAI-compatible embeddings endpoint configured by
  URL, model, and key file, unset by default (option 1 of three presented).
- `as` is required on every operation except `register`, after the user
  challenged making it optional.
- Cursor acknowledgment is a batch token passed on the next `receive`
  (option 1 of three presented). Supersedes acknowledgment on transport
  handoff.
- The operation is named `receive`, not `recv`.
- Identity persistence is a per-repo `.local/agentbus.json`, not a shared
  identities file.
- `reply_to` is kept.
- The sender column is named `sender` throughout the schema.

## AI-proposed details approved in review

Presented section by section and approved; recorded because they change v1
behavior:

- No generation number for reset; reset wipes tables in one transaction and
  live processes detect it through the ordinary `not_registered` path.
- `seq` is the only message identifier; no UUID column. Memory IDs and
  `reply_to` are sequences.
- `messages` carries `sender` and `context` only; no requested name or parent
  column. `sessions` carries no requested name.
- Capacity usage is measured from SQLite page pragmas, replacing the v1
  logical row-byte accounting.
- Every memory revision is a stream row delivered through cursors; no
  non-persisted change broadcasts. Deletion still inserts no row.
- Maintenance is a lease-guarded opportunistic tick with chunked deletes, a
  bounded vacuum, a capped embedding pass, and a wall-clock deadline.
- Idempotency receipts are keyed by caller-supplied key and retained one hour;
  adapter-side automatic retry is removed.
- Added operations `list_channels` and `history`; own messages excluded from
  receive by default; a `result_default_kib` ceiling; `AGENTBUS_CONFIG` and
  `AGENTBUS_DATA_DIR` environment overrides.
- Tool names carry no prefix; the harness namespaces by server name, and tool
  descriptions open with `Agentbus:`.

## Human-made decision added 2026-09-09

- `register` is idempotent within a process: registering a name the calling
  process already holds (including a suffixed form it was given earlier)
  returns that name with a refreshed context and `resumed: true`, instead
  of minting the next suffix. Suffixes still disambiguate distinct live
  processes. Prompted by a `/clear`-preserved MCP server re-registering as
  `agentbus3`. `subscribe` was already idempotent (a repeat leaves the
  cursor alone).
- Two channels always exist: `general` (ordinary) and `memory` (memory).
  The bus creates them when it opens and again after `reset`; a same-named
  channel of another kind that already exists is left as is. The
  `agentbus identity` hook line names them so a fresh agent has a place to
  chat and remember without knowing to create channels. Nobody is
  subscribed automatically.
