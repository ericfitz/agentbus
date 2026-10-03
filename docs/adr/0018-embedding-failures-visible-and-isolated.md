# ADR 0018: Embedding failures are visible, and a rejected memory is skipped

Status: Accepted 2026-10-02. Decided by the user. Supersedes issue #31.

## Context

On another deployment, the TUI showed `embed ok backlog <n>` with a backlog
that never drained, even on the latest build. Two things hid the cause:

- `embed ok` reflected only the TUI's own search queries reaching the
  endpoint. Embedding is done by the agentbus process holding the maintenance
  lease (and by a post-write pass in the sending process), and its failures
  went only to that process's log file.
- `embedBatch` sent up to 64 memories in one request. If the endpoint
  rejected any one of them (for example, one longer than the model's input
  limit), the whole batch failed on every tick, forever. The code noted this
  as a known gap.

The likely causes on that system are a harness-spawned `agentbus mcp`
without the API key (its environment lacks `embedding_api_key_env` and no
`embedding_api_key_file` is set: 401 on every pass), or one rejected memory.
Neither could be confirmed without access, so both are handled.

#31 assumed task-list rows were the stuck backlog; the backlog count has
skipped task-list rows since 8739e54, so that premise does not explain a
current build.

## Human decision (2026-10-02)

**Diagnose and skip bad rows.**

1. **Every embedding pass records its outcome** in `notices` (kind
   `embedding`): an error replaces the notice, success clears it. Status
   (`agentbus status`, the TUI status bar and health view) reports it:
   `embed failing` instead of `ok`, with the error text and when it happened.
   A 401/403 error says whether the process had no key at all and names the
   config keys to check.
2. **A batch the endpoint rejects (400, 413, 422) is retried one memory at
   a time.** A memory rejected on its own is recorded in `embed_failures`
   (seq, model, error) and left out of the backlog; the rest embed. Status
   counts them as `rejected`. Other failures (auth, rate limit, server,
   network) are not one memory's fault: the pass fails, nothing is marked,
   and the next tick retries.

## Consequences

- New table `embed_failures`, created by the schema DDL in the unreleased
  schema v9 (no separate migration step). Rows cascade with their message
  and are dropped when `embedding_model` changes, so a new model retries
  every memory. Editing a memory makes a new revision, which is tried fresh.
  `reset` clears the table.
- The notice holds the last pass's outcome from whichever process ran it. If
  one process has the key and another does not, the notice reflects the
  latest attempt.
- An idle tick still takes no write lock: clearing the notice reads first.
