# Drop message refs; references live in the text

Status: Approved 2026-10-06 (design). Tracked in #33. ADR 0019.

## Goal

Remove the `refs` field from messages and memories. References (paths,
URLs, commits, issues) go in the message text, where search already finds
them, and existing refs are folded into their message's text so nothing is
lost.

Why: `refs` accepts only the kinds `url`, `unix_path` and `windows_path`,
so a commit or issue is rejected; it is not searchable; and neither the TUI
nor the CLI shows it. On the live bus on 2026-10-06, 11 of 460 retained
messages carried refs (all current revisions of live memories in
`memory/agentbus`, `memory/tmi` and `memory/hilt`; 3 URLs and 12 paths,
6 of them repository-relative), and a search for
`internal/tui/view.go` found nothing although one memory's refs named it.

## Human decisions (user, 2026-10-06)

1. **Drop `refs`** from `send`, `edit_memory` and every message payload.
2. **Fold stored refs into the content** of their row, one per line under a
   `Refs:` line, values verbatim.
3. **References are written inline from now on.** Agents are not asked to
   write a `Refs:` block; the skill recommends plain, searchable forms.
4. **`search` gets prefix terms:** a word ending in `*` matches by prefix,
   so `e159e8c*` finds a full 40-character hash.

Rejected:

- Moving refs into `metadata.refs` (hidden and unsearchable), or discarding
  them.
- A `file://` convention: percent-encoding breaks search on paths with
  spaces (searching `release notes.md` did not find
  `file:///.../release%20notes.md`), repository-relative paths cannot be
  file URIs, and agents would encode inconsistently.
- A `commit:` scheme: the search tokenizer splits at `:`, so
  `commit:1234567` also matches the plain text "commit 1234567"; prefix
  search gives the useful part without a scheme.

## Human decisions (user, 2026-10-07)

Approved by Eric on 2026-10-07.

5. **The `foldRefs` step also clears the embed failure.** The v11
   `foldRefs` step deletes the folded row's `embed_failures` marker, when
   that table exists, so the row is re-embedded with its new text.

## API removal

- `bus.Ref`, `refKinds`, `validateRefs` and the `Refs` fields of
  `SendInput`, `EditInput` and `Message` are deleted, with their uses in
  `validateSendShape`, `EditMemory`'s pre-receipt check, `insertMessage`,
  `envelopeUpperBound`, `messageCols` and `decodeJSONFields`.
- The MCP tool schemas are generated from these structs with
  `additionalProperties: false`, so a caller that still passes `refs` gets
  `validation: ... unexpected additional properties ["refs"]` (verified
  2026-10-06 with an unknown field on `send`). No placeholder accepts it.
- `receive`, `history`, `search` and `get_memory` results no longer carry
  `refs`.

## Migration

The next free schema version (v10; v11 if the #20 structured-tags change
ships first) gets one Go step, `foldRefs`, run by the existing
`runMigration` (one transaction, `user_version` re-read under the write
lock, foreign keys off).

For each row with `refs IS NOT NULL`:

- **Tombstoned row** (`tombstone = 1`): no fold; the column is simply
  dropped below.
- **`refs` parses as a JSON array of `{kind, value}`:** skip if empty
  (`[]`). Otherwise append to `content`:

  ```
  <content>

  Refs:
  - <value>
  - <value>
  ```

  Values are copied exactly: no URL decoding or encoding, no path
  normalization, no conversion to URIs. A value containing a control
  character (newline, tab, and so on) is written in Go's `strconv.Quote`
  form so one ref stays one line. The `kind` is not written.
- **`refs` does not parse:** append `Refs: <raw text>` (control characters
  quoted the same way) instead of failing; a failed step would leave the
  bus unable to open.
- Chat messages, DMs and memories are all folded the same way; every
  revision row is folded independently.

For each folded row, in order: `INSERT INTO messages_fts(messages_fts,
rowid, subject, content) VALUES('delete', seq, subject, <old content>)`,
`UPDATE messages SET content = <new>`, `INSERT INTO messages_fts(rowid,
subject, content) VALUES(seq, subject, <new>)`, and `DELETE FROM embeddings
WHERE seq = ?` so the embedder recomputes it. (`messages_fts` has insert
and delete triggers only, so the index is maintained by hand here.)

Then `ALTER TABLE messages DROP COLUMN refs` (SQLite 3.35+; modernc
v1.58.0 bundles a newer SQLite; `refs` has no index, trigger or view
depending on it), and `schema.go`'s `messages` DDL loses the column.

Size: the folded text is shorter than the JSON envelope field it replaces
(`,"refs":[{"kind":"url","value":"…"}]` versus `\n\nRefs:\n- …`, before any
JSON escaping), so no row grows past what `max_message_kib` already
admitted. On the live data the largest folded content is 932 bytes.

State:

- Second run: `migrate` starts at the stored `user_version`, so nothing
  runs.
- Interrupted run: the step is one transaction; a failure leaves the
  previous version, refs intact, and the next open retries.
- Two processes opening together: `runMigration`'s re-read makes the
  second skip the step.
- Older binaries refuse the new schema (ADR 0003 A6): every host and
  session upgrades together.

A dry run on a copy of the live database (2026-10-06) folded all 11 rows,
passed `messages_fts` `integrity-check`, and made each ref findable by full
path, file name (`view.go`), URL or fragment (`issues/874`), and version
(`v1.5.1`).

## Prefix search

`ftsQuery` keeps quoting every whitespace-separated term, with one change:
a term ending in `*` with at least one character before it becomes a
quoted prefix phrase, `"e159e8c"*`. FTS5 applies the prefix to the last
token of the phrase, so `commit:e159e8c*` and `ericfitz/agentbus@e159*`
also work. A term that is only `*` (or only `*`s) is dropped. A `*`
anywhere else is part of the quoted term, as today (the tokenizer
discards it). `Search` already rejects a blank query with `validation`;
it rejects a query left with no terms after dropping lone `*`s the same
way, so `textSearch` never builds an empty `MATCH`.

`mode=semantic` and the semantic half of `mode=both` embed the query with
trailing `*` removed from each term.

The `search` tool description adds: "A word ending in `*` matches by
prefix (`e159e8c*` finds the full commit hash)."

Verified on FTS5 with the default tokenizer (2026-10-06): `"e159e8c"*`
matches both a full 40-character hash and the short hash; inline paths and
URLs are found mid-sentence.

## Known limits (documented in the skill where they affect agents)

- A percent-encoded URL is found by its encoded form (`My%20Docs`), not
  the decoded one.
- A bare `#33` matches any `33` token; search `owner/repo#33`.
- Accents are folded (`resume` finds `Résumé`).
- An `edit_memory` replaces the content, so it drops a folded `Refs:` block
  unless the agent keeps it (as an edit without `refs` dropped refs
  before).

## Skill (using-agentbus)

The memory-post line "Include a `refs` entry (file path, commit, issue)
when one exists" becomes:

> Name what the memory is about in the text, where search finds it: a file
> path (absolute, or relative to the repository with the repository
> named), a URL as-is, a commit as `owner/repo@sha`, an issue or PR as
> `owner/repo#N`. To find a commit, search its short hash with a trailing
> `*` (`e159e8c*`); that also matches the full hash.

## Release

Release notes tag the release `breaking`: `send` and `edit_memory` reject
`refs`, payloads no longer carry it, and existing refs appear as a `Refs:`
block at the end of their message.

## Testing

- Migration, on a fixture at the previous schema version, with rows
  carrying: a URL; a path with spaces; a Windows path; a value containing a
  newline; two refs; `[]`; malformed JSON; a chat message; a tombstoned
  memory; a second revision. Assert the exact folded content of each,
  `messages_fts` `integrity-check`, that each folded row is found by
  `search` on its ref, that folded rows have no embedding while untouched
  rows keep theirs, and that `refs` is no longer a column.
- Migration state: reopening runs nothing; a step that fails midway
  (injected) leaves the previous version with refs intact and succeeds on
  the next open.
- `send` and `edit_memory` with `refs` return a `validation` error.
- `ftsQuery` table: `e159e8c*` → `"e159e8c"*`; `*` and `**` dropped;
  `a*b` quoted whole; embedded quotes doubled; `search` with a query of
  only `*` returns `validation`.
- Search end to end: a memory containing a full hash is found by
  `e159e8c*`, in text and both modes; the semantic query embeds `e159e8c`
  without `*`.
- `make verify`.
