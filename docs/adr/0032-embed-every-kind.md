# ADR 0032: embed every message kind whenever an embedding endpoint is configured

Status: Accepted (Eric, 2026-11-27). Human decision, recorded by Claude.

## Context

ADR 0030 kept every channel kind in the default search and asked #52 to
explore extending semantic search beyond memories. Until now only memories
were embedded: chat, DMs and task rows were excluded from the embedding
backlog, so semantic search could not find them and mode `both` found them
by text only.

Measured on a real bus (2026-11-27): about 12 chat and 12 DM messages a
day, 7-day chat retention (about 170 live chat and DM rows), 22 task rows,
572 memories. Messages average 360 to 770 characters; none is under 40, so
a minimum-length filter would skip nothing. On text-embedding-3-small the
added cost is well under a cent a month.

Options considered: A, always embed every kind; B, embed every kind by
default with an `embed_kinds` key to narrow it; C, opt in per kind with
memories as the default; D, split the expansion into its own issue.

## Decision (Eric, 2026-11-27)

A. Whenever an embedding endpoint is configured, agentbus embeds memories,
chat messages, DMs and the current revision of each task (subject plus
description, never the raw task JSON). There is no per-kind config.

## Consequences

- Semantic and both modes rank every kind in one ranking; `kinds` narrows.
- Chat and DM text is sent to the configured embedding endpoint, as memory
  text already is. Release notes and the README say so.
- Embeddings for expired chat rows and superseded task revisions are
  removed with the rows (cascade), and ADR 0018's failure handling covers
  the new kinds. The task-row exclusion from embedding (#31) is removed.
- Vectors count toward `sqlite_budget_mib` (about 6 KB each at 1536
  dimensions, ~1.2 MB for today's chat and task volume against the 2048 MiB
  default). A task revision whose subject and description are unchanged
  reuses the previous embedding instead of calling the endpoint.
- Volume grows with chat traffic; if it ever matters, a narrowing config
  is a later decision.
