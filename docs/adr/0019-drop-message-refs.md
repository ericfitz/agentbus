# ADR 0019: Drop message refs; references live in the text

Status: Accepted 2026-10-06 (design; not yet implemented). Decided by the
user. Tracked in #33.

## Context

Messages and memories carried an optional `refs` list of `{kind, value}`
with kinds `url`, `unix_path` and `windows_path` only. Commits and issues
were rejected, refs were not searchable, and no client displayed them. On
the live bus, 11 of 460 retained messages used them, all memories.

## Human decisions (user, 2026-10-06)

1. Drop `refs` from `send`, `edit_memory` and all payloads. A caller still
   passing it gets a validation error (the generated MCP schema rejects
   unknown properties).
2. A schema migration folds each row's refs into its content as a
   `Refs:` block, one value per line, values verbatim; the column is
   dropped.
3. From now on references are written inline in the text. The skill
   recommends plain paths, URLs as-is, `owner/repo@sha` and `owner/repo#N`.
4. `search` treats a word ending in `*` as a prefix, so a short commit hash
   finds the full one.

Rejected: moving refs into `metadata`, discarding them, a `file://`
convention (percent-encoding breaks search; relative paths cannot be file
URIs), and a `commit:` scheme (the tokenizer splits at `:`, so it adds no
precision).

## Consequences

- Breaking for MCP callers that send `refs`; release notes say so.
- One schema bump; every host and session upgrades together.
- Folded rows are re-embedded.

Spec: `docs/superpowers/specs/2026-10-06-drop-refs-design.md`.
