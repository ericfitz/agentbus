# ADR 0006: Deferred-minor rulings

Status: Accepted 2026-09-17. The six items below were the last entries of
`docs/superpowers/plans/2026-09-08-agentbus-v2-deferred-minors.md` still
needing a human decision. All six were decided by the user at the end of the
v1.3.0 session on 2026-09-17. Items 1, 2, 3, 5, and 6 shipped in v1.3.1;
item 4 is a schema change and ships on its own in v1.4.0.

## Human decisions

1. **`AGENTBUS_DATA_DIR` is resolved once at config load.** A leading `~/`
   expands to the home directory and a relative path is made absolute
   against the working directory, so every process started from the same
   repository agrees on the data directory. Previously the value was used
   verbatim.
2. **`go.mod` declares `go 1.27`, not `go 1.27.1`.** The patch pin bought
   nothing and forced toolchain downloads.
3. **The owner token stays 12 random bytes.** No code change; ADR 0003
   ruling 16 records the size as final.
4. **Drop the `messages.bytes` column.** It undercounted the envelope and
   nothing read it. This is the first schema migration: schema version 2 via
   `user_version`, a `channels`-style table rebuild in one transaction, and
   the migration machinery itself. Every running old-binary session fails to
   open the database until restarted; the v1.4.0 release notes say so.
   Ships as its own release with a spec.
5. **`Send` re-runs the channel and `reply_to` lookups inside the write
   transaction** (the preflight stays). A send that races a channel delete
   now returns `not_found` instead of writing into a deleted channel,
   matching what `TaskCreate` already did.
6. **`result()` in mcpserver keeps its behavior.** A comment states that a
   non-bus error reaching it is an SDK argument error and anything else must
   be wrapped as `internal` before return.
