# ADR 0030: search keeps all channel kinds by default; explore semantic search beyond memories

Status: Accepted (Eric, 2026-11-27). Human decisions, recorded by Claude.

## Context

Issue #52 adds a `kinds` filter (memories, chat, tasks) and single-channel
scoping to `search` in the MCP server and the TUI. The first draft of #52
proposed making `memories` the default kind. Today an unscoped text search
already returns chat, the caller's DMs and task rows; only the semantic half
is limited to memories, because only memories are embedded.

## Decisions (Eric, 2026-11-27)

1. Memories are not the default search kind. With no `kinds` given, search
   covers every kind, as text search does today; `kinds` only narrows it.
2. Explore extending semantic search (embeddings) to chat and task channels
   instead of narrowing the default. #52 carries the exploration: what to
   embed, volume and cost, lifecycle, ranking and fallback. A later ADR
   records the outcome.

## Consequences

- No behavior change to an unscoped search; the tool description and skill
  need no "default changed" warning.
- Until chat and tasks are embedded, semantic and both modes return text-only
  results for those kinds and say so in the result.
