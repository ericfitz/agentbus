# ADR 0031: tag filters on search and history match all tags; tags-only search

Status: Accepted (Eric, 2026-11-27). Human decisions, recorded by Claude.
Amends ADR 0009 item 1 ("`history` and `search` take an any-of `tags`
filter"). Tracked in #51; design in
`docs/superpowers/specs/2026-11-27-search-tags-all-of-design.md`.

## Context

`history` and `search` share one tag filter, which matched a message
carrying any of the given tags. Tag subscriptions are AND sets (ADR 0009
item 3), so the same list of tags meant different things depending on the
tool. The TUI search overlay is to gain `tag:` tokens, and search refused a
query with no words, so "every failed deployment" could not be asked.

## Decisions (Eric, 2026-11-27)

1. A `tags` filter on `search` and `history` matches only messages where
   every pattern matches one of the message's tags. A prefix pattern
   (`env:*`) still matches per pattern. Any-of is dropped, not kept behind
   a second parameter; callers that want any-of run separate searches.
2. A search with at least one tag and no words is allowed, on the MCP tool
   and the TUI. Its hits come newest first; the mode ranks nothing. A
   search with no words and no tags is still refused.

## Consequences

- A caller passing several tags to `search` or `history` gets fewer
  results than before. Tool descriptions, the using-agentbus skill and the
  README say "all of".
- Search, history and tag subscriptions read a tag list the same way.
