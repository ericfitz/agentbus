# Search and history tags match all; TUI tag: syntax Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `tags` filters on `search` and `history` require every pattern (AND); a search with tags and no words lists matches newest first; the TUI search overlay parses `tag:<pattern>` tokens (#51).

**Architecture:** `tagsFilter` (`internal/bus/tags.go`) is shared by `History` and `searchFilters`, so ANDing it there changes both. `Search` gains a tags-only branch that reuses `searchFilters` and the existing paging/trimming. The TUI gets a small table-driven input parser (`internal/tui/searchparse.go`) that #52 will extend with `in:` / `channel:`.

**Tech Stack:** Go (pure, `modernc.org/sqlite` FTS5), Bubble Tea TUI, MCP go-sdk (schema inferred from struct tags).

**Spec:** `docs/superpowers/specs/2026-11-27-search-tags-all-of-design.md`; decisions in `docs/adr/0031-tag-filters-match-all.md`.

## Global Constraints

- Work lands on `main`; done gate `make verify` (build, vet, vet-cross, gofmt, lint, unit tests).
- Tag patterns are validated only through `bus.NormalizeTagPatterns` (lowercase, dedupe, sort, at most 10, `prefix*`).
- No any-of option anywhere; no `tag:` parsing in the MCP `query` string.
- Filter-only searches other than tags-only stay refused.
- Error string for a blank query without tags: `query is required (or give tags)`.
- American English; wrap docs as the surrounding file does.

## Review Focus

1. Tags-only search in `semantic`/`both` mode with an embedder configured must not call the embedder and must not set `semantic_unavailable` (Task 1 test).
2. Tags-only search must not return another agent's DM or a tombstoned (deleted) message (Task 1 test).
3. Tags-only search must still honor `channel`, `sender` and `thread` filters, and page with `next` (Task 1 test).
4. TUI `tag:env:*` (a prefix pattern containing `:`) parses to `env:*`, not an error (Task 3 test).
5. TUI: after an invalid-tag error, fixing the line and pressing enter runs the search instead of trying to open a hit (Task 3 test).

---

### Task 1: Bus — AND tag filter and tags-only search

**Files:**
- Modify: `internal/bus/tags.go:144-160` (`tagsFilter`)
- Modify: `internal/bus/search.go` (`SearchInput.Query` tag, `Search`, new `tagsOnlySearch`)
- Test: `internal/bus/tags_test.go`, `internal/bus/search_test.go`

**Interfaces:**
- Produces: `SearchInput.Query` tagged `json:"query,omitempty"` (makes it optional in the MCP schema, Task 2). `Search(as, SearchInput{Tags: [...]} )` with no terms returns hits newest first with `Score` 0. Error `validation` "query is required (or give tags)" for a blank query without tags.

- [ ] **Step 1: Update existing any-of expectations to AND** in `tags_test.go`:
  - `TestSendStoresTagsAndHistorySearchFilter`: `History(sam,"dev",nil,nil,10,"docs","release")` now returns 0 messages; add `History(..., "bug","release")` returns exactly `["one"]` (message "one" carries both).
  - `TestHistoryAndSearchMatchTagPatterns`: `History(..., "ENV:prod","prod")` returns none; add `History(..., "deployment","env:*")` → `["prod deploy","staging deploy"]` and `History(..., "deployment","env:p*")` → `["prod deploy"]`; `Search{Query:"deploy",Tags:["env:*","prod"]}` → 0 hits; `Search{Query:"deploy",Tags:["env:*","deployment"]}` → 2 hits.
- [ ] **Step 2: New test `TestSearchTagsMustAllMatchInEveryMode`** (`search_test.go`, use the embed test server pattern from `embed_test.go` so `semantic` and `both` run): messages tagged `foo bar`, `foo`, `bar`, all containing "widget"; for each mode `text`, `semantic`, `both`, `Search{Query:"widget",Tags:["foo","bar"]}` returns only the `foo bar` message.
- [ ] **Step 3: New test `TestSearchTagsOnly`** (`search_test.go`), asserting:
  - `Search{Tags:["deployment","failed"]}` (Query "") returns the matching messages newest first (seq descending), every `Score == 0`.
  - `Query:"*"` with the same tags returns the same hits.
  - `Count:1` returns one hit and a non-empty `Next`; the next page returns the second.
  - With `Channel`, `Sender` or `Thread` set, only matching messages come back.
  - A matching message in another agent's DM inbox is absent; a deleted (tombstoned) matching message is absent.
  - `Mode:"bogus"` with tags and no query → `validation`.
  - `Search{}` (no query, no tags) → `validation` whose message contains `query is required (or give tags)`; `Query:"*"` without tags keeps the ADR 0019 error.
- [ ] **Step 4: New test `TestSearchTagsOnlyNeverEmbeds`** modeled on `TestSearchStarOnlyQueryNeverEmbeds`: embedder server fails the test on any call; for `semantic` and `both`, `Search{Tags:["foo"]}` succeeds and `SemanticUnavailable` is false; call count 0.
- [ ] **Step 5: Run** `go test ./internal/bus -run 'Tag|Search'` — expect the new and updated cases to FAIL.
- [ ] **Step 6: Implement.** `tagsFilter`: one `EXISTS (SELECT 1 FROM message_tags t WHERE t.seq=<alias>.seq AND t.tag BETWEEN ? AND ?)` per pattern, joined with ` AND `; doc comment says all-of. `Search`: normalize tags before the query checks; when `searchTerms(in.Query)` is empty: if tags are present, validate `mode` (empty, text, semantic, both) and call `tagsOnlySearch(as, in, searchPageMax) ([]SearchHit, error)` (`SELECT messageCols("m") FROM messages m WHERE m.tombstone=0` + `searchFilters` + `ORDER BY m.seq DESC LIMIT ?`), then fall through to the existing paging/trimming; if no tags, a blank query returns the new error and a star-only one keeps the ADR 0019 error. Change `Query`'s tag to `json:"query,omitempty"`.
- [ ] **Step 7: Run** `go test ./internal/bus` — PASS.
- [ ] **Step 8: Commit** `feat(bus): tag filters match all patterns; tags-only search (#51)`.

### Task 2: MCP tool text, docs

**Files:**
- Modify: `internal/mcpserver/server.go:518,522` (history and search descriptions)
- Modify: `README.md:158-160`, `internal/cli/skills/using-agentbus/SKILL.md:191`, `docs/adr/0009-message-tags-and-tag-subscriptions.md:23`
- Test: `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: Task 1's optional `query` and tags-only behavior.

- [ ] **Step 1: Test `TestSearchToolTagsOnly`** (`server_test.go`, using the existing `call` helper): send two messages tagged `deployment failed` and one tagged `deployment`; `call(t, cs, "search", {"as":…, "tags":["deployment","failed"]})` (no `query`) returns 2 hits, newest first; the tool's input schema (from `cs.ListTools`) does not list `query` in `required`.
- [ ] **Step 2: Run** `go test ./internal/mcpserver -run TestSearchToolTagsOnly` — PASS for hits already (Task 1); the schema assertion confirms the tag change. If it fails, fix before continuing.
- [ ] **Step 3: Text.** `history`: "tags filters to messages carrying all of the given tags; a tag ending in * …". `search`: replace "tags (any of; …)" with "tags (all must match; …)" and add "query may be omitted when tags are given: the matches come back newest first". README `search`/`history` lines: say all tags must match and that `search` takes tags alone. Skill line 191: "carrying every one of". ADR 0009 item 1: change "an any-of `tags` filter" to "a `tags` filter (all of them must match since ADR 0031; any-of before)".
- [ ] **Step 4: Verify** `rg -n -i 'any of|any-of' README.md internal/mcpserver/server.go internal/cli/skills/using-agentbus/SKILL.md` prints no tag-filter mention; `go test ./internal/mcpserver ./internal/cli` PASS.
- [ ] **Step 5: Commit** `docs: search and history tags match all; tags-only search (#51)`.

### Task 3: TUI tag: parsing

**Files:**
- Create: `internal/tui/searchparse.go`
- Modify: `internal/tui/search.go` (`searchState`, `runSearch`, `viewSearch` title), `internal/tui/help.go:51`
- Test: `internal/tui/searchparse_test.go`, `internal/tui/search_test.go`

**Interfaces:**
- Produces: `type searchSpec struct { Query string; Tags []string }`; `func parseSearchInput(line string) (searchSpec, error)`. Prefix handling is a table (`map` or slice of `{prefix string; apply func(*searchSpec, string)}`) so #52 adds `in:` / `channel:` rows.

- [ ] **Step 1: Table test `TestParseSearchInput`** (`searchparse_test.go`), cases (input → Query, Tags, error):
  - `tag:foo tag:bar keyword` → `keyword`, `[bar foo]`
  - `keyword tag:foo` → `keyword`, `[foo]`
  - `tag:foo tag:FOO x` → `x`, `[foo]` (dedupe, lowercase)
  - `TAG:Foo x` → `x`, `[foo]`
  - `tag:env:* deploy` → `deploy`, `[env:*]`
  - `"tag:foo" bar` → `tag:foo bar`, no tags
  - `tag:deployment tag:failed` → ``, `[deployment failed]`
  - `tag: x` → error mentioning `tag:` needs a tag
  - `tag:bad_tag x` → error from `NormalizeTagPatterns`
  - `a  b` (two spaces) → `a b`
- [ ] **Step 2: Run** `go test ./internal/tui -run TestParseSearchInput` — FAIL (undefined).
- [ ] **Step 3: Implement `parseSearchInput`** in `searchparse.go`: `strings.Fields`; a word with a case-insensitive `tag:` prefix contributes its remainder (empty → error `tag: needs a tag, e.g. tag:deployment`); a word of length ≥ 2 wrapped in `"` is text with the quotes stripped; other words are text in order; tags go through `bus.NormalizeTagPatterns`; Query is the text words joined by one space.
- [ ] **Step 4: Overlay tests** in `search_test.go`:
  - `TestSearchTagTokensFilterHits`: agent sends "release one" tagged `deployment failed`, "release two" tagged `deployment`; typing `tag:deployment tag:failed release` + enter → 1 hit ("release one"); view title contains `tags deployment failed`.
  - `TestSearchTagOnlyLine`: `tag:deployment` + enter → both hits, newest first.
  - `TestSearchInvalidTagShowsErrorThenReruns`: `tag:bad_tag x` + enter → `search.err` set, `ran` true, no hits, view shows the error; edit to `tag:deployment release` + enter → hits returned (not a jump).
- [ ] **Step 5: Implement.** `runSearch`: parse the trimmed line; on error set `m.search.err`, `m.search.ran = true`, clear hits, `m.search.query = q`, return nil; with no words and no tags return nil; otherwise send `SearchInput{Query: spec.Query, Tags: spec.Tags, Mode, Count}` and store `spec.Tags` in a new `searchState.tags []string` for the title. Title: `"search  " + dim("mode "+mode+tagsPart+" · tab to switch")` with `tagsPart = " · tags " + strings.Join(tags, " ")` when non-empty. Help line for `/`: `search (tag:<tag> filters; tab cycles text / semantic / both)`.
- [ ] **Step 6: Run** `go test ./internal/tui` — PASS.
- [ ] **Step 7: Done gate** `make verify` → `verify: OK`.
- [ ] **Step 8: Commit** `feat(tui): tag: tokens in the search overlay (#51)`.
