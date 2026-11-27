# Search and history tags match all of them; TUI tag: syntax (#51)

Status: approved (Eric, 2026-11-27). Short design (small change).

## Intent

A search for `tag:deployment tag:failed timeout` should return only
messages that mention `timeout` and carry both tags. Today a `tags` filter
is any-of, while tag subscriptions are AND sets (ADR 0009 item 3), so the
two disagree; and the TUI search overlay has no way to filter by tag at all
(`tag:foo` is searched as the two words `tag foo`).

## Human decisions (Eric, 2026-11-27), recorded in ADR 0031

1. A `tags` filter requires every pattern to match one of the message's
   tags (AND), on both `search` and `history`; the shared `tagsFilter` path
   changes. Any-of is dropped; run separate searches for it.
2. A search with tags and no words is allowed on both the MCP tool and the
   TUI. Its hits come newest first. No words and no tags stays an error.

## Goals

- Bus: `tags: [foo, bar]` matches a message tagged `foo bar` and excludes
  one tagged only `foo` or only `bar`; a prefix pattern still works per
  pattern (`env:*` AND `failed`). Applies to text, semantic and both modes
  (shared `searchFilters`) and to `History`.
- Bus: `Search` with a blank query (or one with no terms after dropping
  star-only words) and at least one tag returns the filtered messages
  newest first (seq descending), with the same visibility rules, filters,
  `count`, cursor paging and result-size trimming as a ranked search.
- TUI: `tag:<pattern>` tokens anywhere in the input become
  `SearchInput.Tags`; the remaining words are `Query`.

## Non-goals

- No any-of option (no `any_tags` parameter).
- No `tag:` parsing in the MCP `query` string: the tool already has `tags`.
- No filter-only search for other filters (sender, channel, time, thread
  without tags); that stays `query is required`.
- The `in:` / `channel:` tokens are #52; this change only builds the
  parser they will extend.

## Design

**Bus (`internal/bus`).**
- `tagsFilter` emits one `EXISTS (SELECT 1 FROM message_tags t WHERE
  t.seq=<alias>.seq AND t.tag BETWEEN ? AND ?)` per pattern, ANDed. Its doc
  comment says all-of. `History` and `searchFilters` keep calling it.
- `Search` validation order: auth; normalize tags; then if the query has
  no terms (blank, or only star words): with tags, run the tags-only
  listing; without tags, keep today's errors (`query is required` for
  blank, the ADR 0019 message for star-only). A star-only query with tags
  counts as no words (tags-only listing).
- Tags-only listing: `SELECT <messageCols> FROM messages m WHERE
  m.tombstone=0` + `searchFilters` + `ORDER BY m.seq DESC LIMIT
  searchPageMax`, hits with `Score` 0. It goes through the same paging and
  trimming code as ranked results. `mode` is still validated (an unknown
  mode is still an error) but ranks nothing; `semantic_unavailable` is
  never set on a tags-only search, and no embedding call is made.

**MCP (`internal/mcpserver`).** `search`'s `query` is optional when `tags`
is given (schema and description). Descriptions of `search` and `history`
say tags must all match. `Search`'s error for a blank query without tags
names the alternative: `query is required (or give tags)`.

**TUI (`internal/tui`).**
- A shared parser, `parseSearchInput(line) (searchSpec, error)`, splits the
  line on whitespace. A word starting `tag:` (ASCII, case-insensitive
  prefix) contributes its remainder as a tag pattern; a word wrapped in
  double quotes is text (quotes stripped before it is added to the query,
  so `"tag:foo"` searches the words `tag foo`); everything else is text,
  kept in order. Tag patterns go through `bus.NormalizeTagPatterns`
  (lowercase, dedupe, validation); its error is shown in the overlay and
  nothing is searched. `tag:` with an empty remainder is an error. The
  parser is table-driven so #52 adds `in:` / `channel:` as new prefixes.
- `runSearch` sends `Tags` and `Query` from the parser. A line that parses
  to no words and no tags does nothing (as a blank line does today).
- The overlay title shows the active tags after the mode, e.g.
  `search  mode both · tags deployment failed · tab to switch`.
- Help text for `/` mentions `tag:`.

**Docs.** README `search`/`history` lines, the using-agentbus skill
(`internal/cli/skills/using-agentbus/SKILL.md`, line 191 area), and ADR
0009 item 1's "any-of" pointer to ADR 0031.

## Approaches

Only one fits. Serving a tags-only search through `history` was ruled out:
`history` reads one channel, and the point is a cross-channel search. A
separate `all_tags` parameter was declined by Eric (decision 1).

## Tests (acceptance)

- Bus: AND across two tags; exclusion of single-tag messages; prefix
  pattern AND exact tag; History with two tags; each search mode with tags;
  tags-only search newest first, with paging (`next`) and DM visibility;
  tags-only with an unknown mode errors; blank query without tags errors;
  star-only query with tags is tags-only; no embedder call on tags-only.
- TUI parser: `tag:foo tag:bar keyword`, mixed order `keyword tag:foo`,
  duplicate tags, uppercase `TAG:Foo`, quoted `"tag:foo"`, empty `tag:`,
  invalid tag (error), tags-only line. `runSearch` sends the parsed input.
- `make verify` green.

## Left to the builder

Naming of the parser types, exact error strings beyond those above, and
the overlay title layout within the hint above.
