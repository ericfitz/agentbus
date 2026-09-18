# Schema migration: drop `messages.bytes`

Date: 2026-09-17. Status: decided by the human (ADR 0006 item 4); this
document records the design for v1.4.0.

## Goal

Drop the `messages.bytes` column. It undercounted the envelope (ADR 0003
R4 moved the real measurement to `envelopeUpperBound`) and nothing read it.
Doing so needs the bus's first schema migration, so the migration machinery
is the real deliverable; the column drop is its first use.

## Human decisions (2026-09-17)

1. Drop the column rather than fix the count.
2. Schema version 2, recorded in `PRAGMA user_version`.
3. The rebuild follows SQLite's recommended shape (create the new table, copy,
   drop the old, rename) in one transaction.
4. Every running old-binary session fails to open the database until
   restarted, and the release notes say so. No compatibility shim.
5. Ships as its own release, v1.4.0.

## Design

- `internal/bus/migrate.go`: `migrations[v]` maps a `user_version` to the
  function that upgrades it to `v+1`. `Open` reads `user_version` before any
  DDL (unchanged, ADR 0003 A6): `0` is stamped with the current version,
  greater than the current version is refused, and anything in between is
  passed to `migrate`, which applies each step in order.
- Each step runs on a dedicated connection with `foreign_keys` off (a table
  rebuild would otherwise cascade-delete `embeddings`), inside one immediate
  write transaction. It re-reads `user_version` under the write lock and
  returns without doing anything if another process already applied the
  step, runs the step, checks `pragma_foreign_key_check`, stamps `v+1`, and
  commits. `foreign_keys` is turned back on before the connection is
  released to the pool.
- The shared schema DDL (`CREATE ... IF NOT EXISTS`) runs after `migrate`,
  as before, and recreates the indexes and FTS triggers a rebuild dropped.
- Step 1 to 2 (`dropMessagesBytes`): drop the two FTS triggers so the copy
  and drop never touch `messages_fts` (external-content, keyed on
  `messages` by name, so it survives the rename untouched); create
  `messages_v2` without `bytes`; copy every row; drop `messages`; rename.
  The `AUTOINCREMENT` counter is carried over explicitly, because `DROP
  TABLE` removes the `sqlite_sequence` row and the copy alone would reset
  the counter to the surviving maximum seq, breaking the monotonic-seq
  guarantee that `Reset` and the embedder rely on.
- `insertMessage` no longer writes `bytes`. No other reader existed.

## Compatibility

An older binary that opens a version 2 file refuses it with the existing
"schema version N is newer than this binary supports" error and leaves the
file untouched. Two v1.4.0 processes starting together serialize on the
write lock; the second sees version 2 inside its transaction and skips.

## Testing

`TestMigrateV1DropsMessagesBytes` builds a real version 1 file (the v1
`messages` DDL is kept verbatim in the test), with a memory row, an
embedding, an FTS entry, and a deleted tail row so the counter exceeds the
maximum seq. After `Open`: version is 2, `bytes` is gone, the row, the
embedding, and the FTS entry survive, the triggers and indexes exist, and
the next send gets the counter's successor. The existing
`TestOpenRejectsNewerSchemaVersion` covers the old-binary path.
