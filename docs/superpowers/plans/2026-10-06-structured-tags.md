# Structured Tags and Prefix Patterns Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let tags carry one `:` (`env:prod`), be 32 characters long, and drop `_`; let every tag filter and subscription take a prefix pattern (`env:*`) matched as a byte range on the `message_tags(tag, seq)` index.

**Architecture:** `internal/bus/tags.go` gains the pattern rule (`NormalizeTagPatterns`), the range mapping (`tagRange`, `MatchTag`) and range-based SQL for `history`/`search` (`tagsFilter`) and tag subscriptions (`tagCond`, over new `lo`/`hi` columns added by schema v10). `repoconfig`, the MCP server, the TUI and the skill adopt the two rules; stored tags are never rewritten.

**Tech Stack:** Go 1.2x, `modernc.org/sqlite` (pure Go SQLite), `database/sql`, Bubble Tea TUI, MCP go-sdk. Tests are plain `testing` with the package's existing helpers (`newTestBus`, `reg`, `setupTwo`, `tagSetup`, `sendTagged`, `wantCode`).

**Spec:** `docs/superpowers/specs/2026-10-06-structured-tags-design.md` (approved 2026-10-06, frozen). ADR: `docs/adr/0009-message-tags-and-tag-subscriptions.md`, amendment 2026-10-06.

## Global Constraints

- Stored tag rule: `^[a-z0-9-]+(:[a-z0-9-]+)?$`, 1-32 characters, input lowercased first; cap of 10 per message and deduplication unchanged. Error text: `tag %q must be 1-32 characters of a-z, 0-9 and -, with at most one : between other characters`.
- Pattern rule: an exact tag, or a 1-32 character prefix matching `^[a-z0-9-]+(:[a-z0-9-]*)?$` followed by one trailing `*`. `*` is never valid in a stored tag.
- Matching is the inclusive byte range `[p, p~]` for `p*` and `[t, t]` for an exact tag, with SQL `BETWEEN`, never `LIKE` (BINARY collation keeps `message_tags_tag_seq` usable).
- A subscribed AND set matches when `count(DISTINCT st.tag)` equals the set size; `matched_tags` lists the message's own tags, sorted and deduplicated, never the patterns.
- Stored tags are not rewritten; a stored tag that breaks the new rule stays readable but cannot be named in a filter.
- Schema: this change is schema v10, migration key `migrations[9]` (`addTagRanges`). Issue #33 (drop refs) will be v11. If #33 ships first, renumber to v11 / `migrations[10]` and rename the fixture constants accordingly.
- `tagSubscriptionTagsDDL` is shared with `splitTagSets` (v4 -> v5), so `addTagRanges` must skip a column the file already has (`pragma_table_info`), exactly as `addSessionHarness` does.
- Vocabulary is not enforced by the bus; the using-agentbus skill keys `env:`, `repo:`, `area:`, `kind:` and keeps triage tags flat.
- American English; gofmt; comments in the style of the surrounding code. Search with `rg PATTERN <path>` (always an explicit path), never `grep`.
- Work lands directly on `main` (no PRs). End each commit message with the attribution lines from the session's system reminder.
- Done gate: `make verify` (build, build-all, vet, gofmt, lint, unit tests). There is no CI.

## Review Focus

1. A set mixing `env:*` and `env:prod` where one message tag satisfies both patterns: delivered once, `matched_tags` `[env:prod]`; a message tagged only `env:staging` is not delivered. (Test added in Task 5.)
2. A stored legacy tag containing `_` (written before this release): `history` still returns it in `tags`, while naming it in a filter is a validation error, not a silent no-match. (Test added in Task 3.)
3. An uppercase pattern (`ENV:*`) on `subscribe` and `unsubscribe`: both lowercase it, so unsubscribe with `FAILED, env:*` removes the set subscribed as `ENV:*, failed`. (Tests added in Tasks 5 and 6.)
4. More than 10 patterns on `history` or `search`: a validation error, not a truncated filter. (Test added in Task 3.)
5. The TUI follow prompt typed as `ENV:*, failed` (spaces, uppercase): subscribes `env:*,failed`, the pane is named from the normalized set and shows prefix matches. (Test added in Task 8.)

---

## File Structure

| File | Responsibility in this change |
|---|---|
| `internal/bus/tags.go` | Tag rule, pattern rule, `tagRange`, `MatchTag`, `tagsFilter`, `SubscribeTags`/`UnsubscribeTags` (store `lo`/`hi`), `tagCond`, `matchedTags`. |
| `internal/bus/tags_test.go` | Rule, pattern, match, filter, subscription and `EXPLAIN QUERY PLAN` tests. |
| `internal/bus/messages.go`, `internal/bus/search.go` | `History` and `Search` normalize patterns, not tags. |
| `internal/bus/schema.go` | `schemaVersion = 10`; `lo`/`hi` in `tagSubscriptionTagsDDL`. |
| `internal/bus/migrate.go`, `migrate_test.go` | `migrations[9] = addTagRanges`; v9 fixture tests. |
| `internal/repoconfig/repoconfig.go`, `repoconfig_test.go` | `tag_subscriptions` take patterns; `tags` follow the new rule. |
| `internal/mcpserver/server.go`, `server_test.go` | Pattern normalization in subscribe/unsubscribe output; tool descriptions. |
| `main.go` | `-tags` flag help mentions patterns. |
| `internal/tui/tags.go`, `tags_test.go` | `tagPaneMsgs` uses `MatchTag`; prompt label. |
| `internal/cli/skills/using-agentbus/SKILL.md` | Tag rule, pattern rule, keyed vocabulary. |

Task order matters: Task 1 -> 2 -> 3, Task 4 before Task 5, Tasks 6-9 after Task 5, Task 10 last.

---

### Task 1: New stored-tag rule in `NormalizeTags`

**Files:**
- Modify: `internal/bus/tags.go:10-35`
- Test: `internal/bus/tags_test.go:16-36` (`TestNormalizeTags`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `const maxTagLen = 32`, `var tagRe` (new regexp), `const tagRuleText string`, `NormalizeTags(tags []string) ([]string, error)` (same signature, new rule). Callers that keep using it for stored tags: `Send` (`messages.go:341`), `EditMemory` (`memories.go:133`), `repoconfig.Tags`.

- [ ] **Step 1: Replace `TestNormalizeTags` with the new table**

Replace the whole function at `internal/bus/tags_test.go:16-36` with:

```go
func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{"Release", "bug", "release", "env:prod", "a-1"})
	if err != nil || !slices.Equal(got, []string{"a-1", "bug", "env:prod", "release"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := NormalizeTags(nil); err != nil || got != nil {
		t.Fatalf("no tags: %v %v", got, err)
	}
	// 32 characters in total is the limit (decision 6); the parts have no
	// limit of their own.
	long32 := strings.Repeat("a", 20) + ":" + strings.Repeat("b", 11)
	if got, err := NormalizeTags([]string{long32}); err != nil || len(got) != 1 || got[0] != long32 {
		t.Fatalf("32 characters: %v %v", got, err)
	}
	for _, bad := range [][]string{{""}, {"has space"}, {"x/y"}, {"ünïcode"}, {long32 + "c"}, {" bug "}, {"a_b"}, {":prod"}, {"env:"}, {"env::prod"}, {"a:b:c"}, {"env:*"}, {"*"}} {
		if _, err := NormalizeTags(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	wantCode(t, func() error { _, err := NormalizeTags([]string{"a_b"}); return err }(), "validation")
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "t" + string(rune('a'+i))
	}
	wantCode(t, func() error { _, err := NormalizeTags(eleven); return err }(), "validation")
	if _, err := NormalizeTags(append(eleven[:10], "TA")); err != nil {
		t.Fatalf("duplicates are removed before the limit applies: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd <repo root> && go test ./internal/bus/ -run TestNormalizeTags -count=1`
Expected: FAIL, first line `tag "env:prod" must be 1-20 characters of a-z, 0-9, _ or -` (the old rule rejects the colon).

- [ ] **Step 3: Implement the rule**

In `internal/bus/tags.go`, replace lines 10-35 (from `// maxTags is` through the end of `NormalizeTags`) with:

```go
// maxTags is the per-message (and per-subscription) tag ceiling (ADR 0009).
const maxTags = 10

// maxTagLen caps a stored tag, and a pattern's prefix, at 32 characters in
// total (spec 2026-10-06 decision 6; was 20). The parts around the colon
// have no limit of their own.
const maxTagLen = 32

// tagRe is the stored-tag rule (ADR 0009 amendment 2026-10-06): a-z, 0-9
// and -, with at most one colon between other characters. The colon is an
// ordinary character to the bus; the using-agentbus skill gives it its
// key:value reading. Length is checked apart from the regexp so one message
// can name both limits.
var tagRe = regexp.MustCompile(`^[a-z0-9-]+(:[a-z0-9-]+)?$`)

// tagRuleText is the rule as NormalizeTags states it to the caller.
const tagRuleText = "must be 1-32 characters of a-z, 0-9 and -, with at most one : between other characters"

// NormalizeTags lowercases tags, rejects any that fail the tag rule (one bad
// tag fails the whole call), drops duplicates, sorts, and caps the result at
// maxTags. nil in, nil out. Exported for repoconfig's persistent tag sets.
// Filters take patterns instead: see NormalizeTagPatterns.
func NormalizeTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(t)
		if len(t) > maxTagLen || !tagRe.MatchString(t) {
			return nil, errf("validation", false, "tag %q "+tagRuleText, t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > maxTags {
		return nil, errf("validation", false, "at most %d tags per message", maxTags)
	}
	slices.Sort(out)
	return out, nil
}
```

(The comment "Filters take patterns instead: see NormalizeTagPatterns." refers to Task 2's function; it is fine for the comment to land first.)

- [ ] **Step 4: Run the package tests**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestNormalizeTags|TestSendStoresTags|TestEditMemoryTags' -count=1`
Expected: PASS.

Run: `cd <repo root> && go test ./... -count=1 2>&1 | rg -n 'FAIL|ok' `
Expected: `internal/repoconfig` FAILS (`TestTagsNormalizesDedupesAndReportsBad` expects `repo:tmi` and a 21-character tag to be rejected; Task 6 updates it). Every other package: `ok`. Record the repoconfig failure; do not fix it here.

- [ ] **Step 5: Commit**

```bash
cd <repo root> && git add internal/bus/tags.go internal/bus/tags_test.go && git commit -m "feat(bus): tags take one colon, 32 characters, and drop _ (#20)"
```

---

### Task 2: `NormalizeTagPatterns`, `tagRange`, `MatchTag`

**Files:**
- Modify: `internal/bus/tags.go` (after `NormalizeTags`)
- Test: `internal/bus/tags_test.go` (new tests after `TestNormalizeTags`)

**Interfaces:**
- Consumes: `maxTags`, `maxTagLen`, `tagRe`, `errf` from Task 1 / existing code.
- Produces:
  - `func NormalizeTagPatterns(patterns []string) ([]string, error)`: lowercases, validates (exact tag or `prefix*`), deduplicates, sorts, caps at 10; nil in, nil out. Used by `History`, `Search`, `SubscribeTags`, `UnsubscribeTags`, `repoconfig.TagSubscriptions/AddTagSet/RemoveTagSet`, and the MCP subscribe/unsubscribe output.
  - `func tagRange(pattern string) (lo, hi string)`: `p*` -> `(p, p+"~")`, exact `t` -> `(t, t)`.
  - `func MatchTag(pattern, tag string) bool`: `lo <= tag <= hi`. Used by `matchedTags` and the TUI.

- [ ] **Step 1: Write the failing tests**

Append to `internal/bus/tags_test.go` after `TestNormalizeTags`:

```go
func TestNormalizeTagPatterns(t *testing.T) {
	long32 := strings.Repeat("a", 32)
	got, err := NormalizeTagPatterns([]string{"Failed", "ENV:*", "env*", "env:pr*", "failed", long32 + "*", long32})
	want := []string{long32, long32 + "*", "env*", "env:*", "env:pr*", "failed"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v %v, want %v", got, err, want)
	}
	if got, err := NormalizeTagPatterns(nil); err != nil || got != nil {
		t.Fatalf("no patterns: %v %v", got, err)
	}
	for _, bad := range []string{"*", ":*", "e*v", "env**", "env_*", "*env", "env:*x", "", long32 + "b*", "a_b", "env::*"} {
		wantCode(t, func() error { _, err := NormalizeTagPatterns([]string{bad}); return err }(), "validation")
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "t" + string(rune('a'+i)) + "*"
	}
	wantCode(t, func() error { _, err := NormalizeTagPatterns(eleven); return err }(), "validation")
}

// TestMatchTagAgreesWithSQL: the Go rule and the BETWEEN range the SQL
// paths use give the same answer on every case, including env* matching
// env itself and env:* not matching env.
func TestMatchTagAgreesWithSQL(t *testing.T) {
	b := newTestBus(t)
	cases := []struct {
		pattern, tag string
		want         bool
	}{
		{"env*", "env", true},
		{"env*", "environment", true},
		{"env*", "env:prod", true},
		{"env*", "env-", true},
		{"env*", "env9", true},
		{"env*", "envz", true},
		{"env*", "enw", false},
		{"env:*", "env", false},
		{"env:*", "env:prod", true},
		{"env:*", "env:9", true},
		{"env:*", "env:z-9", true},
		{"env:*", "envy", false},
		{"env:pr*", "env:prod", true},
		{"env:pr*", "env:staging", false},
		{"failed", "failed", true},
		{"failed", "failed-again", false},
		{"fail*", "failed", true},
		{"a*", "b", false},
	}
	for _, c := range cases {
		if got := MatchTag(c.pattern, c.tag); got != c.want {
			t.Errorf("MatchTag(%q, %q) = %v, want %v", c.pattern, c.tag, got, c.want)
		}
		lo, hi := tagRange(c.pattern)
		var n int
		if err := b.db.QueryRow("SELECT ? BETWEEN ? AND ?", c.tag, lo, hi).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 1) != c.want {
			t.Errorf("SQL %q BETWEEN %q AND %q = %d, want %v", c.tag, lo, hi, n, c.want)
		}
	}
	if lo, hi := tagRange("env:*"); lo != "env:" || hi != "env:~" {
		t.Fatalf("tagRange(env:*) = %q %q", lo, hi)
	}
	if lo, hi := tagRange("failed"); lo != "failed" || hi != "failed" {
		t.Fatalf("tagRange(failed) = %q %q", lo, hi)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestNormalizeTagPatterns|TestMatchTagAgreesWithSQL' -count=1`
Expected: build FAIL, `undefined: NormalizeTagPatterns`, `undefined: MatchTag`, `undefined: tagRange`.

- [ ] **Step 3: Implement**

Insert into `internal/bus/tags.go` directly after `NormalizeTags`:

```go
// tagPrefixRe is what may precede a pattern's trailing *: the start of some
// valid tag, so the colon may be last (env:*) but not first or doubled.
var tagPrefixRe = regexp.MustCompile(`^[a-z0-9-]+(:[a-z0-9-]*)?$`)

// NormalizeTagPatterns is NormalizeTags for filters (history, search, tag
// subscriptions, the TUI): each entry is an exact tag, or a 1-32 character
// tag prefix followed by one *. Same lowercasing, deduplication, sort and
// cap. nil in, nil out.
func NormalizeTagPatterns(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.ToLower(p)
		if !validTagPattern(p) {
			return nil, errf("validation", false, "tag pattern %q must be a tag, or a 1-32 character tag prefix followed by one *", p)
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if len(out) > maxTags {
		return nil, errf("validation", false, "at most %d tag patterns", maxTags)
	}
	slices.Sort(out)
	return out, nil
}

func validTagPattern(p string) bool {
	prefix, ok := strings.CutSuffix(p, "*")
	if !ok {
		return len(p) <= maxTagLen && tagRe.MatchString(p)
	}
	return len(prefix) <= maxTagLen && tagPrefixRe.MatchString(prefix)
}

// tagRange maps a pattern to the inclusive byte range of the tags it
// matches: an exact tag to itself, a prefix p* to [p, p~]. Every legal tag
// character (-, 0-9, :, a-z) sorts below ~ (0x7e), so p~ is above every
// tag that starts with p. SQL compares with BETWEEN, never LIKE: LIKE is
// case-insensitive and cannot use message_tags_tag_seq, whose collation is
// BINARY.
func tagRange(pattern string) (lo, hi string) {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return p, p + "~"
	}
	return pattern, pattern
}

// MatchTag is the Go side of the rule tagsFilter and tagCond apply in SQL,
// for matched_tags and the TUI's tag panes. env* matches env itself,
// environment and env:prod; env:* matches only env:<value>.
func MatchTag(pattern, tag string) bool {
	lo, hi := tagRange(pattern)
	return tag >= lo && tag <= hi
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestNormalizeTagPatterns|TestMatchTagAgreesWithSQL' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd <repo root> && git add internal/bus/tags.go internal/bus/tags_test.go && git commit -m "feat(bus): tag patterns, their byte range, and MatchTag (#20)"
```

---

### Task 3: Prefix patterns in `history` and `search`

**Files:**
- Modify: `internal/bus/tags.go` (`tagsFilter`, lines 67-78 before Task 2's insertions)
- Modify: `internal/bus/messages.go:465` (`History`)
- Modify: `internal/bus/search.go:166` (`Search`)
- Test: `internal/bus/tags_test.go`

**Interfaces:**
- Consumes: `NormalizeTagPatterns`, `tagRange` (Task 2).
- Produces: `tagsFilter(alias string, patterns []string) (string, []any)` (same signature; two args per pattern, `BETWEEN`). `History` and `Search` signatures unchanged; their `tags` are now patterns.

- [ ] **Step 1: Write the failing tests**

Append to `internal/bus/tags_test.go`:

```go
func TestHistoryAndSearchMatchTagPatterns(t *testing.T) {
	b, sam, _ := setupTwo(t)
	sendTagged(t, b, sam, "dev", "prod deploy", "deployment", "env:prod")
	sendTagged(t, b, sam, "dev", "staging deploy", "deployment", "env:staging")
	sendTagged(t, b, sam, "dev", "old style", "prod")
	sendTagged(t, b, sam, "dev", "envelope", "envelope")
	sendTagged(t, b, sam, "dev", "plain")
	contents := func(ms []Message) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Content)
		}
		return out
	}
	h, err := b.History(sam, "dev", nil, nil, 10, "env:*")
	if err != nil || !slices.Equal(contents(h), []string{"prod deploy", "staging deploy"}) {
		t.Fatalf("env:* : %v %v", contents(h), err)
	}
	h, err = b.History(sam, "dev", nil, nil, 10, "env*")
	if err != nil || !slices.Equal(contents(h), []string{"prod deploy", "staging deploy", "envelope"}) {
		t.Fatalf("env* : %v %v", contents(h), err)
	}
	h, err = b.History(sam, "dev", nil, nil, 10, "ENV:prod", "prod")
	if err != nil || !slices.Equal(contents(h), []string{"prod deploy", "old style"}) {
		t.Fatalf("mixed exact, lowercased: %v %v", contents(h), err)
	}
	for _, bad := range []string{"env_*", "*", "e*v"} {
		wantCode(t, func() error { _, err := b.History(sam, "dev", nil, nil, 10, bad); return err }(), "validation")
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "t" + string(rune('a'+i)) + "*"
	}
	wantCode(t, func() error { _, err := b.History(sam, "dev", nil, nil, 10, eleven...); return err }(), "validation")

	s, err := b.Search(sam, SearchInput{Query: "deploy", Mode: "text", Tags: []string{"env:st*"}})
	if err != nil || len(s.Hits) != 1 || s.Hits[0].Content != "staging deploy" {
		t.Fatalf("search prefix: %+v %v", s.Hits, err)
	}
	s, err = b.Search(sam, SearchInput{Query: "deploy", Mode: "text", Tags: []string{"env:*", "prod"}})
	if err != nil || len(s.Hits) != 2 {
		t.Fatalf("search mixed: %+v %v", s.Hits, err)
	}
	wantCode(t, func() error {
		_, err := b.Search(sam, SearchInput{Query: "deploy", Mode: "text", Tags: []string{"e*v"}})
		return err
	}(), "validation")
	wantCode(t, func() error {
		_, err := b.Search(sam, SearchInput{Query: "deploy", Mode: "text", Tags: eleven})
		return err
	}(), "validation")
}

// A tag stored under the old rule (one with _) is not rewritten: history
// still returns it, but it can no longer be named in a filter.
func TestLegacyUnderscoreTagStaysReadableButNotFilterable(t *testing.T) {
	b, sam, _ := setupTwo(t)
	r := sendTagged(t, b, sam, "dev", "legacy")
	if _, err := b.db.Exec("INSERT INTO message_tags(seq, tag) VALUES(?, 'a_b')", r.Seq); err != nil {
		t.Fatal(err)
	}
	h, err := b.History(sam, "dev", nil, nil, 10)
	if err != nil || len(h) != 1 || !slices.Equal(h[0].Tags, []string{"a_b"}) {
		t.Fatalf("history keeps the stored tag: %+v %v", h, err)
	}
	wantCode(t, func() error { _, err := b.History(sam, "dev", nil, nil, 10, "a_b"); return err }(), "validation")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestHistoryAndSearchMatchTagPatterns|TestLegacyUnderscoreTag' -count=1`
Expected: FAIL in `TestHistoryAndSearchMatchTagPatterns` with a validation error naming `"env:*"` (History still calls `NormalizeTags`). `TestLegacyUnderscoreTag...` passes already (the stored row is read back as-is); keep it.

- [ ] **Step 3: Implement**

Replace `tagsFilter` in `internal/bus/tags.go`:

```go
// tagsFilter is the any-of clause for History and search: messages
// carrying a tag in the range of at least one pattern (BETWEEN, so the
// comparison is BINARY and can use the (seq, tag) key; see tagRange).
// Empty patterns adds nothing.
func tagsFilter(alias string, patterns []string) (string, []any) {
	if len(patterns) == 0 {
		return "", nil
	}
	conds := make([]string, len(patterns))
	args := make([]any, 0, 2*len(patterns))
	for i, p := range patterns {
		lo, hi := tagRange(p)
		conds[i] = "t.tag BETWEEN ? AND ?"
		args = append(args, lo, hi)
	}
	return " AND EXISTS (SELECT 1 FROM message_tags t WHERE t.seq=" + alias + ".seq AND (" + strings.Join(conds, " OR ") + "))", args
}
```

In `internal/bus/messages.go:465` change `tags, err := NormalizeTags(tags)` to `tags, err := NormalizeTagPatterns(tags)`.

In `internal/bus/search.go:166` change `if in.Tags, err = NormalizeTags(in.Tags); err != nil {` to `if in.Tags, err = NormalizeTagPatterns(in.Tags); err != nil {`.

Do not touch `Send` (`messages.go:341`) or `EditMemory` (`memories.go:133`): stored tags keep `NormalizeTags`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestHistoryAndSearchMatchTagPatterns|TestLegacyUnderscoreTag|TestSendStoresTagsAndHistorySearchFilter' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd <repo root> && git add internal/bus/tags.go internal/bus/tags_test.go internal/bus/messages.go internal/bus/search.go && git commit -m "feat(bus): history and search filter by tag patterns (#20)"
```

---

### Task 4: Schema v10, `addTagRanges`

**Files:**
- Modify: `internal/bus/schema.go:3` (`schemaVersion`), `:5-19` (`tagSubscriptionTagsDDL`)
- Modify: `internal/bus/migrate.go:20-29` (`migrations` map), new `addTagRanges` after `addSessionHarnessProcess`
- Test: `internal/bus/migrate_test.go` (append)

**Interfaces:**
- Consumes: `runMigration`, `migrations`, `schema`, `SQLiteDSN`, `Open` (existing).
- Produces: `tag_subscription_tags.lo TEXT NOT NULL DEFAULT ''`, `.hi TEXT NOT NULL DEFAULT ''`; `schemaVersion = 10`; `func addTagRanges(tx *sql.Tx) error`. Task 5 writes `lo`/`hi` on subscribe and reads them in `tagCond`.

- [ ] **Step 1: Write the failing tests**

Add `"errors"` to the import block of `internal/bus/migrate_test.go` (between `"database/sql"` and `"io"`), then append:

```go
// schemaV9TagSubscriptionTags is tag_subscription_tags as schema v5 through
// v9 created it, before #20 added the pattern range columns.
const schemaV9TagSubscriptionTags = `
CREATE TABLE tag_subscription_tags (
  sender TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  tag TEXT NOT NULL,
  PRIMARY KEY (sender, tags_key, tag),
  FOREIGN KEY (sender, tags_key) REFERENCES tag_subscriptions(sender, tags_key) ON DELETE CASCADE
);`

// shapeV9 writes a v9 database with one subscribed AND set (Kim: a,b) and
// returns the config that opens it.
func shapeV9(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{schema, "DROP TABLE tag_subscription_tags", schemaV9TagSubscriptionTags, "PRAGMA user_version = 9",
		"INSERT INTO tag_subscriptions(sender, tags_key, created_seq) VALUES('Kim','a,b',0)",
		"INSERT INTO tag_subscription_tags(sender, tags_key, tag) VALUES('Kim','a,b','a'),('Kim','a,b','b')",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// tagRanges reads (tag, lo, hi) for sender, ordered by tag.
func tagRanges(t *testing.T, db *sql.DB, sender string) [][3]string {
	t.Helper()
	rows, err := db.Query("SELECT tag, lo, hi FROM tag_subscription_tags WHERE sender=? ORDER BY tag", sender)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out [][3]string
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// TestMigrateV9AddsTagRanges (#20): a v9 file's tag_subscription_tags
// gains lo and hi with every existing row backfilled as an exact tag; a
// second open (user_version already 10) runs nothing, so a range written
// in between (here by hand; Task 5's SubscribeTags does it for real) is
// not reset to lo=hi=tag by a repeated backfill.
func TestMigrateV9AddsTagRanges(t *testing.T) {
	cfg := shapeV9(t)
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v9 database: %v", err)
	}
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	if got := tagRanges(t, b.db, "Kim"); len(got) != 2 || got[0] != [3]string{"a", "a", "a"} || got[1] != [3]string{"b", "b", "b"} {
		t.Fatalf("backfill: %v", got)
	}
	if _, err := b.db.Exec("UPDATE tag_subscription_tags SET lo='env:', hi='env:~' WHERE sender='Kim' AND tag='a'"); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reopen v10 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version after reopen = %d, %v", uv, err)
	}
	if got := tagRanges(t, b.db, "Kim"); len(got) != 2 || got[0] != [3]string{"a", "env:", "env:~"} || got[1] != [3]string{"b", "b", "b"} {
		t.Fatalf("a second open must not re-run the backfill: %v", got)
	}
}

// TestMigrateV9FailureLeavesV9AndRetries (#20): the step is one
// transaction, so a failure after its writes leaves the file at v9 without
// the columns, and the next open runs the step again and succeeds.
func TestMigrateV9FailureLeavesV9AndRetries(t *testing.T) {
	cfg := shapeV9(t)
	orig := migrations[9]
	migrations[9] = func(tx *sql.Tx) error {
		if err := orig(tx); err != nil {
			return err
		}
		return errors.New("injected failure after the step's writes")
	}
	t.Cleanup(func() { migrations[9] = orig })
	if _, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("Open must fail when the migration step fails")
	}
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var uv, has int
	if err := db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != 9 {
		t.Fatalf("user_version after a failed step = %d, want 9, %v", uv, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('tag_subscription_tags') WHERE name IN ('lo','hi')").Scan(&has); err != nil || has != 0 {
		t.Fatalf("columns after a failed step = %d, want 0, %v", has, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrations[9] = orig
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open after the failed step: %v", err)
	}
	defer func() { _ = b.Close() }()
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	if got := tagRanges(t, b.db, "Kim"); len(got) != 2 || got[0] != [3]string{"a", "a", "a"} {
		t.Fatalf("backfill on retry: %v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestMigrateV9' -count=1`
Expected: FAIL. `schemaVersion` is still 9, so `Open` runs no migration: `TestMigrateV9AddsTagRanges` passes the version check and then `tagRanges` fails with `no such column: lo`; `TestMigrateV9FailureLeavesV9AndRetries` fails at `Open must fail when the migration step fails`.

- [ ] **Step 3: Implement**

`internal/bus/schema.go`: set `const schemaVersion = 10` and replace the `tagSubscriptionTagsDDL` block (comment and constant) with:

```go
// tagSubscriptionTagsDDL is shared by schema.go (fresh databases) and
// migrate.go's splitTagSets step (v4 -> v5, #13): one row per tag of each
// AND set, so matching drives from these few rows into message_tags(tag,
// seq) instead of scanning messages. ON DELETE CASCADE means every existing
// DELETE FROM tag_subscriptions (receive.go, sessions.go, UnsubscribeTags)
// cleans this table up without code changes. lo and hi (v10, #20) hold the
// pattern's byte range (tagRange): lo = hi for an exact tag, [p, p~] for a
// prefix p*. They carry a default so addTagRanges can ALTER them onto an
// older file; a v4 file migrating straight through gets them from this DDL
// in splitTagSets, which is why addTagRanges checks before adding.
const tagSubscriptionTagsDDL = `
CREATE TABLE IF NOT EXISTS tag_subscription_tags (
  sender TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  tag TEXT NOT NULL,
  lo TEXT NOT NULL DEFAULT '',
  hi TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (sender, tags_key, tag),
  FOREIGN KEY (sender, tags_key) REFERENCES tag_subscriptions(sender, tags_key) ON DELETE CASCADE
);
`
```

`internal/bus/migrate.go`: add to the `migrations` map after the `8:` entry:

```go
	9: addTagRanges,                            // tag_subscription_tags.lo, .hi (#20, ADR 0009 amendment 2026-10-06)
```

(Re-run gofmt; it aligns the comment column.) Then add after `addSessionHarnessProcess`:

```go
// addTagRanges (schema 9 -> 10, #20) adds the lo and hi columns that hold a
// subscribed pattern's byte range (tagRange) and backfills every existing
// row as an exact tag (lo = hi = tag): before patterns, every subscribed
// tag was one. SQLite needs a default to add a NOT NULL column. Like
// addSessionHarness, it skips a column the file already has: a v4 file
// migrating straight through gets both from tagSubscriptionTagsDDL in
// splitTagSets, with '' in every backfilled row, so the UPDATE always runs.
// A file with no table at all (an older file whose chain skips v5, or a
// test shaping one) has nothing to alter or backfill; the schema DDL that
// runs after migrate creates it.
func addTagRanges(tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='tag_subscription_tags'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	for _, col := range []string{"lo", "hi"} {
		var has int
		if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('tag_subscription_tags') WHERE name=?", col).Scan(&has); err != nil {
			return err
		}
		if has == 0 {
			if _, err := tx.Exec("ALTER TABLE tag_subscription_tags ADD COLUMN " + col + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec("UPDATE tag_subscription_tags SET lo=tag, hi=tag")
	return err
}
```

- [ ] **Step 4: Run the migration tests**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestMigrate' -count=1`
Expected: PASS, including `TestMigrateV2ToLatest` and `TestMigrateV4SplitsTagSets` (the chained cases that exercise the column check).

Run: `cd <repo root> && go test ./internal/bus/ -count=1`
Expected: PASS. `SubscribeTags` still inserts without `lo`/`hi` (they default to `''`) and `tagCond` still joins on `mt.tag=st.tag`, so existing behavior is unchanged until Task 5.

- [ ] **Step 5: Commit**

```bash
cd <repo root> && git add internal/bus/schema.go internal/bus/migrate.go internal/bus/migrate_test.go && git commit -m "feat(bus): schema v10 adds tag_subscription_tags.lo and .hi (#20)"
```

---

### Task 5: Tag subscriptions with patterns

**Files:**
- Modify: `internal/bus/tags.go` (`SubscribeTags`, `UnsubscribeTags`, `tagCond`, `matchedTags`)
- Test: `internal/bus/tags_test.go` (`TestTagCondUsesTagSeqIndex` replaced; new tests)

**Interfaces:**
- Consumes: `NormalizeTagPatterns`, `tagRange`, `MatchTag` (Task 2); `lo`/`hi` columns (Task 4).
- Produces: `SubscribeTags(as string, tags []string) error` and `UnsubscribeTags` take patterns; `tagCond(as string, floor int64) (string, []any)` returns four args `{floor, floor, as, as}`; `matchedTags(sets []tagSet, m Message) []string` returns the message's matching tags. `receive.go:437` and `wait.go:129` need no change.

- [ ] **Step 1: Write the failing tests**

Replace `TestTagCondUsesTagSeqIndex` in `internal/bus/tags_test.go` with:

```go
// Tag matching is a join from the sender's subscribed patterns into
// message_tags(tag, seq): no scan of messages or message_tags (#13). The
// join is an OR of two index-friendly branches that SQLite plans as a
// MULTI-INDEX OR, so the plan shape is the same for every set: an exact
// pattern seeks (tag=? AND seq>?) above the cursor, a prefix range-scans
// its tag range (tag>? AND tag<?) and filters on seq (#20).
func TestTagCondUsesTagSeqIndex(t *testing.T) {
	for _, set := range [][]string{{"a", "b"}, {"env:*", "failed"}} {
		b, _, kim := tagSetup(t)
		if err := b.SubscribeTags(kim, set); err != nil {
			t.Fatal(err)
		}
		q, a := tagCond(kim, 0)
		rows, err := b.db.Query("EXPLAIN QUERY PLAN SELECT seq FROM messages WHERE messages.seq>? AND "+q, append([]any{0}, a...)...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, "message_tags_tag_seq") || strings.Contains(joined, "SCAN messages") || strings.Contains(joined, "SCAN mt") {
			t.Fatalf("%v plan:\n%s", set, joined)
		}
		if !strings.Contains(joined, "(tag=? AND seq>?)") || !strings.Contains(joined, "tag>? AND tag<?") {
			t.Fatalf("%v: want an exact seek above the cursor and a prefix range on the index:\n%s", set, joined)
		}
	}
}
```

Append:

```go
// Spec 2026-10-06 "Subscriptions": ["env:*", "failed"] delivers a message
// tagged env:prod, env:staging, failed once with matched_tags naming the
// message's tags; env:prod alone is not delivered; the uppercase pattern
// is lowercased on subscribe and unsubscribe alike.
func TestTagSubscriptionPrefixPatterns(t *testing.T) {
	b, sam, kim := tagSetup(t)
	wantCode(t, b.SubscribeTags(kim, []string{"env_*"}), "validation")
	wantCode(t, b.SubscribeTags(kim, []string{"*"}), "validation")
	if err := b.SubscribeTags(kim, []string{"ENV:*", "failed"}); err != nil {
		t.Fatal(err)
	}
	if sets, _ := b.TagSubscriptions(kim); len(sets) != 1 || !slices.Equal(sets[0], []string{"env:*", "failed"}) {
		t.Fatalf("sets: %v", sets)
	}
	var lo, hi string
	if err := b.db.QueryRow("SELECT lo, hi FROM tag_subscription_tags WHERE sender=? AND tag='env:*'", kim).Scan(&lo, &hi); err != nil || lo != "env:" || hi != "env:~" {
		t.Fatalf("stored range: %q %q %v", lo, hi, err)
	}
	if err := b.db.QueryRow("SELECT lo, hi FROM tag_subscription_tags WHERE sender=? AND tag='failed'", kim).Scan(&lo, &hi); err != nil || lo != "failed" || hi != "failed" {
		t.Fatalf("exact range: %q %q %v", lo, hi, err)
	}
	sendTagged(t, b, sam, "dev", "both envs", "env:prod", "env:staging", "failed", "other")
	sendTagged(t, b, sam, "dev", "env only", "env:prod")
	sendTagged(t, b, sam, "dev", "flat env", "env", "failed")
	sendTagged(t, b, sam, "dev", "failed only", "failed")
	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r.Messages) != 1 || r.Messages[0].Content != "both envs" {
		t.Fatalf("one delivery for two tags matching one pattern: %+v %v", r.Messages, err)
	}
	if !slices.Equal(r.Messages[0].MatchedTags, []string{"env:prod", "env:staging", "failed"}) {
		t.Fatalf("matched_tags lists the message's tags, not the patterns: %v", r.Messages[0].MatchedTags)
	}
	if err := b.UnsubscribeTags(kim, []string{"FAILED", "env:*"}); err != nil {
		t.Fatal(err)
	}
	if sets, _ := b.TagSubscriptions(kim); len(sets) != 0 {
		t.Fatalf("unsubscribe removes the set: %v", sets)
	}
	sendTagged(t, b, sam, "dev", "after", "env:prod", "failed")
	if r2, _ := b.Receive(kim, ReceiveInput{}); len(r2.Messages) != 0 {
		t.Fatalf("removed set no longer matches: %+v", r2.Messages)
	}
}

// A set whose patterns overlap (env:* and env:prod): one tag may satisfy
// both, and the set still needs every pattern satisfied.
func TestTagSetWithOverlappingPatterns(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if err := b.SubscribeTags(kim, []string{"env:*", "env:prod"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "staging", "env:staging")
	sendTagged(t, b, sam, "dev", "prod", "env:prod")
	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r.Messages) != 1 || r.Messages[0].Content != "prod" {
		t.Fatalf("env:prod satisfies both patterns, env:staging only one: %+v %v", r.Messages, err)
	}
	if !slices.Equal(r.Messages[0].MatchedTags, []string{"env:prod"}) {
		t.Fatalf("matched_tags: %v", r.Messages[0].MatchedTags)
	}
	// wait takes the same path (wait.go) and must agree.
	msgs, err := b.Wait(context.Background(), kim, nil, false, nil, time.Second)
	if err != nil || len(msgs) != 1 || !slices.Equal(msgs[0].MatchedTags, []string{"env:prod"}) {
		t.Fatalf("wait: %+v %v", msgs, err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestTagCondUsesTagSeqIndex|TestTagSubscriptionPrefixPatterns|TestTagSetWithOverlappingPatterns' -count=1`
Expected: FAIL. `TestTagSubscriptionPrefixPatterns` fails at `SubscribeTags(kim, {"ENV:*", "failed"})` with a validation error (`SubscribeTags` still calls `NormalizeTags`); `TestTagCondUsesTagSeqIndex` fails for the prefix set the same way; `TestTagSetWithOverlappingPatterns` likewise.

- [ ] **Step 3: Implement**

In `internal/bus/tags.go`:

(a) `SubscribeTags`: change `tags, err := NormalizeTags(tags)` to `tags, err := NormalizeTagPatterns(tags)`, and replace the insert loop with:

```go
	for _, p := range tags {
		lo, hi := tagRange(p)
		if _, err := tx.Exec("INSERT OR IGNORE INTO tag_subscription_tags(sender,tags_key,tag,lo,hi) VALUES(?,?,?,?,?)", as, tagsKey(tags), p, lo, hi); err != nil {
			return internal(err)
		}
	}
```

Update its doc comment's first sentence to: `// SubscribeTags adds an AND set of 1-10 tag patterns (NormalizeTagPatterns).`

(b) `UnsubscribeTags`: change `tags, err := NormalizeTags(tags)` to `tags, err := NormalizeTagPatterns(tags)`.

(c) `tagSet`'s comment: `// tagSet is one AND set of patterns: a message matches when every pattern matches one of its tags and it was written after the set was added.`

(d) Replace `tagCond` (comment and function) with:

```go
// tagCond is the tag source's predicate over messages: ordinary chat
// channels only (dm/ inboxes are ordinary-kind and excluded by name;
// memory channels and task lists are memory-kind), skipping channels the
// sender is directly subscribed to (those arrive through the channel), and
// satisfying every pattern of at least one set added before the message.
// Once a direct channel subscription ends, that channel's messages above
// the tag cursor become tag-deliverable again (ADR 0009 item 7 applies to
// current direct subscriptions, not past ones). Matching is driven from the
// sender's few subscribed patterns into message_tags(tag, seq) (#13), never
// a scan of messages or message_tags. The join is an OR of two branches
// SQLite plans as a MULTI-INDEX OR: an exact pattern (lo = hi) seeks
// (tag=? AND seq>?) above the floor as before; a prefix (lo < hi)
// range-scans its tag range on the same index and filters on seq, reading
// every tagged message in the range (retention bounds it). BETWEEN-style
// bounds, never LIKE, keep the BINARY index usable (#20). A set matches when
// the message satisfies as many distinct patterns as the set has, so two
// tags under one pattern (env:prod and env:staging under env:*) count once.
// mt.seq's two comparisons (rather than max(ts.created_seq, ?)) are what
// let SQLite seek message_tags_tag_seq instead of scanning it.
func tagCond(as string, floor int64) (string, []any) {
	return `messages.seq IN (
		SELECT mt.seq FROM tag_subscription_tags st
		JOIN tag_subscriptions ts ON ts.sender=st.sender AND ts.tags_key=st.tags_key
		JOIN message_tags mt ON ((st.lo=st.hi AND mt.tag=st.lo AND mt.seq>?) OR (st.lo<st.hi AND mt.tag>=st.lo AND mt.tag<=st.hi AND mt.seq>?)) AND mt.seq>ts.created_seq
		WHERE st.sender=?
		GROUP BY mt.seq, st.tags_key
		HAVING count(DISTINCT st.tag)=(SELECT count(*) FROM tag_subscription_tags c WHERE c.sender=st.sender AND c.tags_key=st.tags_key))
	AND messages.channel IN (SELECT name FROM channels WHERE kind='ordinary' AND name NOT LIKE 'dm/%')
	AND messages.channel NOT IN (SELECT channel FROM subscriptions WHERE sender=?)`, []any{floor, floor, as, as}
}
```

(e) Replace `matchedTags` (comment and function) with:

```go
// matchedTags is the union, sorted and deduplicated, of m's own tags that
// satisfied a set m matches (env:prod, never the pattern env:*). A set
// matches when every pattern in it matches at least one of m's tags.
func matchedTags(sets []tagSet, m Message) []string {
	var out []string
	for _, s := range sets {
		if m.Seq <= s.createdSeq {
			continue
		}
		all := true
		for _, p := range s.tags {
			if !slices.ContainsFunc(m.Tags, func(t string) bool { return MatchTag(p, t) }) {
				all = false
			}
		}
		if !all {
			continue
		}
		for _, t := range m.Tags {
			if slices.ContainsFunc(s.tags, func(p string) bool { return MatchTag(p, t) }) && !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}
```

- [ ] **Step 4: Run the tag and migration tests**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestTag|TestMigrateV9|TestMixedChannelAndTagBatch|TestWaitWakesOnTagMatch|TestSubscribeTagsRefreshesStaleRow|TestUnsubscribeRejectsTagSource' -count=1`
Expected: PASS (including `TestMigrateV9AddsTagRanges` from Task 4, now that `env:*` stores its range).

If `TestTagCondUsesTagSeqIndex` fails on the `(tag=? AND seq>?)` / `tag>? AND tag<?` substrings, print the plan and check it is a `MULTI-INDEX OR` with two `SEARCH mt USING COVERING INDEX message_tags_tag_seq` lines; the plan was verified against SQLite 3.54 with exactly this SQL. Do not fall back to a single `BETWEEN st.lo AND st.hi` join: that loses the exact seek the spec keeps.

Run the benchmark once as a sanity check: `cd <repo root> && go test ./internal/bus/ -run '^$' -bench BenchmarkTagMatchFewMatches -benchtime 3x -count=1`
Expected: completes in the ~1 ms/op range reported in ADR 0009 (not tens of ms).

- [ ] **Step 5: Run the whole bus package**

Run: `cd <repo root> && go test ./internal/bus/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd <repo root> && git add internal/bus/tags.go internal/bus/tags_test.go && git commit -m "feat(bus): tag subscriptions match prefix patterns on the tag index (#20)"
```

---

### Task 6: `repoconfig` patterns and the new tag rule

**Files:**
- Modify: `internal/repoconfig/repoconfig.go:177-233` (`TagSubscriptions`, `Tags`, `AddTagSet`, `RemoveTagSet`)
- Test: `internal/repoconfig/repoconfig_test.go` (`TestTagsNormalizesDedupesAndReportsBad` replaced; new test)

**Interfaces:**
- Consumes: `bus.NormalizeTagPatterns`, `bus.NormalizeTags`.
- Produces: `(*File).TagSubscriptions() (sets [][]string, bad []string)` returns pattern sets; `(*File).Tags()` applies the stored-tag rule; `AddTagSet`/`RemoveTagSet` take patterns. Signatures unchanged; `internal/cli/subscribe.go` and the MCP `persistent` paths keep working.

- [ ] **Step 1: Write the failing tests**

Add `"strings"` to the imports of `internal/repoconfig/repoconfig_test.go`. Replace `TestTagsNormalizesDedupesAndReportsBad` with:

```go
func TestTagsNormalizesDedupesAndReportsBad(t *testing.T) {
	dir := t.TempDir()
	long33 := strings.Repeat("a", 33)
	writeFile(t, dir, `{"identity":"Sam","tags":["TMI","tmi","api-schema","repo:tmi","",7,"a_b","env:*","`+long33+`"]}`)
	f, _ := Load(dir)
	got, bad := f.Tags()
	if !reflect.DeepEqual(got, []string{"tmi", "api-schema", "repo:tmi"}) {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(bad, []string{"", "7", "a_b", "env:*", long33}) {
		t.Fatal(bad)
	}
}
```

Append:

```go
func TestTagSubscriptionsAcceptPatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","tag_subscriptions":[["ENV:*","failed"],["env_*"],["*"],["change","area:*"]]}`)
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sets, bad := f.TagSubscriptions()
	if !reflect.DeepEqual(sets, [][]string{{"env:*", "failed"}, {"area:*", "change"}}) {
		t.Fatalf("sets: %v", sets)
	}
	if !reflect.DeepEqual(bad, []string{"[env_*]", "[*]"}) {
		t.Fatalf("bad: %v", bad)
	}
	if _, err := f.AddTagSet([]string{"repo:tmi*"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.AddTagSet([]string{"e*v"}); err == nil {
		t.Fatal("an invalid pattern must be rejected")
	}
	sets, err = f.RemoveTagSet([]string{"FAILED", "env:*"})
	if err != nil || !reflect.DeepEqual(sets, [][]string{{"area:*", "change"}, {"repo:tmi*"}}) {
		t.Fatalf("remove by normalized pattern set: %v %v", sets, err)
	}
	g, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A write stores only the valid sets (setTagSets), so the two bad
	// entries are gone from the file.
	if sets, bad := g.TagSubscriptions(); !reflect.DeepEqual(sets, [][]string{{"area:*", "change"}, {"repo:tmi*"}}) || len(bad) != 0 {
		t.Fatalf("written file: %v %v", sets, bad)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd <repo root> && go test ./internal/repoconfig/ -run 'TestTagsNormalizesDedupesAndReportsBad|TestTagSubscriptionsAcceptPatterns' -count=1`
Expected: FAIL. `TestTagSubscriptionsAcceptPatterns` reports sets missing the pattern entries (`NormalizeTags` rejects `env:*`); `TestTagsNormalizesDedupesAndReportsBad` passes once Task 1 landed (it is updated here because Task 1 broke the old version; keep it).

- [ ] **Step 3: Implement**

In `internal/repoconfig/repoconfig.go`:

(a) `TagSubscriptions`: change the comment to say patterns and switch the call:

```go
// TagSubscriptions returns the persistent tag sets under "tag_subscriptions"
// (a list of lists of tag patterns, bus.NormalizeTagPatterns), each
// normalized; entries that are not a list of valid patterns are returned in
// bad and omitted. Absent key: none.
func (f *File) TagSubscriptions() (sets [][]string, bad []string) {
	list, _ := f.Raw["tag_subscriptions"].([]any)
	for _, e := range list {
		raw, ok := e.([]any)
		tags := make([]string, 0, len(raw))
		for _, v := range raw {
			s, isStr := v.(string)
			ok = ok && isStr
			tags = append(tags, s)
		}
		norm, err := bus.NormalizeTagPatterns(tags)
		if !ok || err != nil || len(norm) == 0 {
			bad = append(bad, fmt.Sprint(e))
			continue
		}
		sets = append(sets, norm)
	}
	return sets, bad
}
```

(b) `Tags`: no code change (it keeps `bus.NormalizeTags`, the stored-tag rule); extend its comment: `// ... entries that are not a valid tag (bus.NormalizeTags: a pattern with * is not one) are returned in bad and omitted.`

(c) `AddTagSet`:

```go
// AddTagSet appends a normalized pattern set (idempotent), writes, and
// returns the list.
func (f *File) AddTagSet(tags []string) ([][]string, error) {
	norm, err := bus.NormalizeTagPatterns(tags)
	if err != nil || len(norm) == 0 {
		return nil, fmt.Errorf("tags %q: must be 1-10 tags (1-32 characters of a-z, 0-9 and -, with at most one :) or tag prefixes ending in *", tags)
	}
	sets, _ := f.TagSubscriptions()
	if !slices.ContainsFunc(sets, func(s []string) bool { return slices.Equal(s, norm) }) {
		sets = append(sets, norm)
	}
	return sets, f.setTagSets(sets)
}
```

(d) `RemoveTagSet`: change `norm, err := bus.NormalizeTags(tags)` to `norm, err := bus.NormalizeTagPatterns(tags)`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd <repo root> && go test ./internal/repoconfig/ ./internal/cli/ -count=1`
Expected: PASS (the CLI's `TestSubscribeTagsWritesTagSets` exercises `AddTagSet`/`RemoveTagSet`).

- [ ] **Step 5: Commit**

```bash
cd <repo root> && git add internal/repoconfig/repoconfig.go internal/repoconfig/repoconfig_test.go && git commit -m "feat(repoconfig): tag_subscriptions take prefix patterns; tags follow the new rule (#20)"
```

---

### Task 7: MCP tool descriptions and pattern output; CLI flag help

**Files:**
- Modify: `internal/mcpserver/server.go:54`, `:60`, `:76`, `:367`, `:376`, `:406`, `:415`, `:445`, `:453`, `:457`, `:486`
- Modify: `main.go:225`
- Test: `internal/mcpserver/server_test.go` (append)

**Interfaces:**
- Consumes: `bus.NormalizeTagPatterns`.
- Produces: `subscribed_tags` / `unsubscribed_tags` carry the normalized patterns (today `null` for a pattern, since `NormalizeTags` errors and the error is discarded).

- [ ] **Step 1: Write the failing test**

Append to `internal/mcpserver/server_test.go`:

```go
// TestTagDescriptionsStateRulesAndPatterns (#20): send and edit_memory
// state the tag rule; the four filters say a trailing * is a prefix; the
// subscribe and unsubscribe results echo normalized patterns.
func TestTagDescriptionsStateRulesAndPatterns(t *testing.T) {
	cs := testSession(t)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"send": "1-32 characters", "edit_memory": "1-32 characters", "history": "ending in *", "search": "ending in *", "subscribe": "ending in *", "unsubscribe": "pattern"}
	for _, tl := range tools.Tools {
		s, ok := want[tl.Name]
		if !ok {
			continue
		}
		if !strings.Contains(tl.Description, s) {
			t.Fatalf("%s description must contain %q: %s", tl.Name, s, tl.Description)
		}
		if tl.Name == "subscribe" || tl.Name == "history" {
			j, err := json.Marshal(tl.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(j), "env:*") {
				t.Fatalf("%s tags schema must show a pattern: %s", tl.Name, j)
			}
		}
		delete(want, tl.Name)
	}
	if len(want) != 0 {
		t.Fatalf("tools not seen: %v", want)
	}
	call(t, cs, "register", map[string]any{"name": "Sam"})
	out, _ := call(t, cs, "subscribe", map[string]any{"as": "Sam", "tags": []string{"ENV:*", "failed"}})
	if fmt.Sprint(out["subscribed_tags"]) != "[env:* failed]" {
		t.Fatalf("subscribed_tags: %v", out["subscribed_tags"])
	}
	out, _ = call(t, cs, "unsubscribe", map[string]any{"as": "Sam", "tags": []string{"env:*", "failed"}})
	if fmt.Sprint(out["unsubscribed_tags"]) != "[env:* failed]" {
		t.Fatalf("unsubscribed_tags: %v", out["unsubscribed_tags"])
	}
	if _, res := call(t, cs, "history", map[string]any{"as": "Sam", "channel": "general", "tags": []string{"env_*"}}); !res.IsError {
		t.Fatal("history must reject an invalid pattern")
	}
	if _, res := call(t, cs, "send", map[string]any{"as": "Sam", "channel": "general", "content": "x", "tags": []string{"env:*"}}); !res.IsError {
		t.Fatal("send must reject a pattern as a stored tag")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd <repo root> && go test ./internal/mcpserver/ -run TestTagDescriptionsStateRulesAndPatterns -count=1`
Expected: FAIL at `send description must contain "1-32 characters"`.

- [ ] **Step 3: Implement**

In `internal/mcpserver/server.go`:

(a) Line 54 and line 60 (`subscribeIn.Tags`, `unsubscribeIn.Tags`), replace the `jsonschema` text with:
`instead of channel: 1-10 tags or prefix patterns (a tag ending in * matches every tag with that prefix: env:*) that must all match a message for it to be delivered (an AND set); subscribe again with another set for OR`

(b) Line 76 (`historyIn.Tags`): `only messages carrying at least one of these tags; a tag ending in * matches every tag with that prefix (env:*)`

(c) Line 376 and line 415: change `norm, _ := bus.NormalizeTags(in.Tags)` to `norm, _ := bus.NormalizeTagPatterns(in.Tags)`.

(d) `subscribe` description (line 367): replace the last sentence `Or pass tags instead of channel: an AND set of 1-10 tags; matching messages ...` with
`Or pass tags instead of channel: an AND set of 1-10 tags or prefix patterns (a tag ending in * matches every tag with that prefix: env:*, or area:* with change); matching messages from any chat channel you are not already subscribed to arrive through receive with matched_tags (the message's own tags, not your patterns), starting from now.`

(e) `unsubscribe` description (line 406): replace `Or pass tags to drop that tag set.` with `Or pass tags to drop that tag set, naming the same tags or patterns it was subscribed with.`

(f) `send` description (line 445): replace
`tags (up to 10, each 1-20 characters of letters, digits, _ and -; stored lowercase) label the message ... and the environment (prod, staging); a changed interface is change plus its area (api-schema, db-schema, config).`
with
`tags (up to 10, each 1-32 characters of a-z, 0-9 and -, with at most one : between other characters; stored lowercase; never *) label the message so other agents can follow, filter, and triage it without reading it: tag the activity (deployment, release), its outcome (started, succeeded, failed), what needs attention (blocked, needs-human, breaking), and the environment (env:prod, env:staging); a changed interface is change plus its area (area:api-schema, area:db-schema, area:config).`

(g) `history` description (line 453): `Agentbus: read a channel's retained messages by sequence range without touching your cursor. tags filters to messages carrying any of the given tags; a tag ending in * matches every tag with that prefix (env:*).`

(h) `search` description (line 457): replace `thread (a seq), tags (any of).` with `thread (a seq), tags (any of; a tag ending in * matches every tag with that prefix, env:*).`

(i) `edit_memory` description (line 486): replace `tags replaces the memory's tags; omit it to keep them.` with `tags replaces the memory's tags (same rule as send: up to 10, each 1-32 characters of a-z, 0-9 and -, with at most one : between other characters; stored lowercase); omit it to keep them.`

In `main.go:225`: `tags := fs.String("tags", "", "comma-separated tag set to follow instead of a channel; a tag ending in * matches every tag with that prefix (env:*)")`

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd <repo root> && go test ./internal/mcpserver/ -count=1 && go build ./...`
Expected: PASS; build ok.

- [ ] **Step 5: Commit**

```bash
cd <repo root> && git add internal/mcpserver/server.go internal/mcpserver/server_test.go main.go && git commit -m "feat(mcp): tag patterns in filter and subscription tools; new tag rule in descriptions (#20)"
```

---

### Task 8: TUI tag panes and follow prompt match patterns

**Files:**
- Modify: `internal/tui/tags.go:12-21` (comment), `:39-57` (`tagPaneMsgs`), `:72-80` (`tagPrompt`)
- Test: `internal/tui/tags_test.go` (append)

**Interfaces:**
- Consumes: `bus.MatchTag`, `bus.SubscribeTags` (patterns).
- Produces: nothing new; `tagPaneMsgs(ch string) []bus.Message` matches by pattern; pane names stay `tags:` + comma-joined normalized set (`model.go:1180`).

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/tags_test.go`:

```go
// TestTagPromptAcceptsPatternsAndPaneMatchesPrefix (#20): the t prompt
// takes patterns with the usual spaces and case; the pane is named from the
// normalized set and shows every loaded message whose tags satisfy it,
// with chips showing whole tags.
func TestTagPromptAcceptsPatternsAndPaneMatchesPrefix(t *testing.T) {
	if got := splitTags("ENV:*, failed"); !slices.Equal(got, []string{"ENV:*", "failed"}) {
		t.Fatalf("splitTags: %v", got)
	}
	f := newFixture(t)
	f.key("esc")
	f.key("t")
	for _, r := range "ENV:*, failed" {
		f.key(string(r))
	}
	f.key("enter")
	sets, err := f.c.b.TagSubscriptions(f.c.as)
	if err != nil || len(sets) != 1 || !slices.Equal(sets[0], []string{"env:*", "failed"}) {
		t.Fatalf("t prompt subscribes a normalized pattern set: %v %v", sets, err)
	}
	for _, m := range []struct {
		ch, content string
		tags        []string
	}{
		{"dev", "prod failure", []string{"env:prod", "failed"}},
		{"general", "staging failure", []string{"env:staging", "failed"}},
		{"dev", "no env", []string{"failed"}},
		{"dev", "no failed", []string{"env:prod"}},
		{"dev", "flat env", []string{"env", "failed"}},
		{"dev-notes", "memory failure", []string{"env:dev", "failed"}},
	} {
		if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: m.ch, Content: m.content, Tags: m.tags}); err != nil {
			t.Fatal(err)
		}
	}
	f.receive(t)
	f.selectTagPane(t, tagPanePrefix+"env:*,failed")
	s := ansi.Strip(f.m.renderStream())
	for _, want := range []string{"prod failure", "staging failure", "memory failure", "env:prod"} {
		if !strings.Contains(s, want) {
			t.Fatalf("pane lacks %q:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"no env", "no failed", "flat env"} {
		if strings.Contains(s, unwanted) {
			t.Fatalf("pane shows %q, which does not satisfy env:* and failed:\n%s", unwanted, s)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd <repo root> && go test ./internal/tui/ -run TestTagPromptAcceptsPatternsAndPaneMatchesPrefix -count=1`
Expected: FAIL at `pane lacks "prod failure"` (the pane still compares tags exactly; subscription itself succeeds after Task 5).

- [ ] **Step 3: Implement**

In `internal/tui/tags.go`:

(a) Replace the `tagPanePrefix` comment:

```go
// tagPanePrefix names a tag set's rail entry: "tags:" plus the sorted set
// of patterns joined by commas (tags:env:*,failed). A tag may itself carry
// one colon (#20), which is why the prefix is matched only at the start
// and TrimPrefix leaves the set intact. No bus channel name begins with
// this prefix (bus.ChannelNameRule; the list is synthetic anyway).
```

(b) Replace `tagPaneMsgs` with:

```go
// tagPaneMsgs is the loaded messages of every tagPaneSources channel whose
// tags satisfy all of the pane's patterns (bus.MatchTag, so env:* matches
// env:prod), ascending by seq.
func (m *Model) tagPaneMsgs(ch string) []bus.Message {
	set := tagPaneSet(ch)
	var out []bus.Message
	for _, c := range m.tagPaneSources() {
		for _, x := range m.msgs[c] {
			all := true
			for _, p := range set {
				if !slices.ContainsFunc(x.Tags, func(t string) bool { return bus.MatchTag(p, t) }) {
					all = false
				}
			}
			if all {
				out = append(out, x)
			}
		}
	}
	slices.SortFunc(out, func(a, b bus.Message) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}
```

(c) `tagPrompt`: change the label to `"tags <tag|prefix*>[,...]"` and the comment to `// tagPrompt asks for a comma-separated set of tags or prefix patterns (env:*) and subscribes to it; the status refresh that follows adds it to the rail under its normalized name.`

(d) `splitTags`'s comment: replace `does not fail NormalizeTags` with `does not fail NormalizeTagPatterns`.

- [ ] **Step 4: Run the TUI tests**

Run: `cd <repo root> && go test ./internal/tui/ -count=1`
Expected: PASS (`TestTagKeyOpensThePromptOnlyFromTheRail` checks only that the label starts with `tags`).

- [ ] **Step 5: Commit**

```bash
cd <repo root> && git add internal/tui/tags.go internal/tui/tags_test.go && git commit -m "feat(tui): tag panes and the follow prompt take prefix patterns (#20)"
```

---

### Task 9: Skill vocabulary and release-note wording

**Files:**
- Modify: `internal/cli/skills/using-agentbus/SKILL.md:184-245` (Tags section) and `:252-256` (session protocol step 4)

**Interfaces:** none (documentation). `internal/cli/init_test.go` only checks the skill file is installed, not its text.

- [ ] **Step 1: Rewrite the Tags section**

Replace everything from `## Tags` through the line `inactivity; re-\`subscribe\` with \`tags\` or re-\`register\`.` (the end of "Following tags") with:

```markdown
## Tags

Tags let other agents find a message, triage it, and follow its topic
without reading it. `send` and `edit_memory` take `tags`: up to 10 labels,
each 1-32 characters of `a-z`, `0-9` and `-`, with at most one `:` between
other characters, stored lowercase. The bus gives the colon no meaning;
the vocabulary below uses it to key a dimension (`env:prod`, `area:aws`).
`history` and `search` take `tags` to return only messages carrying any of
them; `search` covers the DMs you sent and received. In every filter and
subscription a tag ending in `*` matches every tag with that prefix:
`env:*` matches `env:prod` and `env:staging`; `env*` also matches `env`
itself. `*` is valid only last, after at least one character, and never
in a stored tag. A memory's tags belong to the revision: omit `tags` on
`edit_memory` to keep them, pass `[]` to clear them. Task lists do not
take tags.

### Vocabulary

Reuse these before inventing a tag, and never invent a synonym
(`deployment`, not `deploy`). Dimensions are keyed; triage tags stay
flat. `register` returns the repository's own additions in `repo_tags`.
A repository adds tags by listing them under `tags` in
`.local/agentbus.json` (`"tags": ["repo:tmi", "repo:tmi-ux"]`); entries
that are not valid tags come back in `ignored_tags` for you to fix.

- Activity: `deployment` `release` `migration` `ci` `rollback` `infra`
- Lifecycle: `started` `succeeded` `failed`
- Attention: `blocked` `needs-human` `breaking`
- Change: `change` with `area:api-schema` `area:db-schema` `area:config`
  `area:dependency`
- Coordination: `handoff` `review`
- Environment: `env:prod` `env:staging` `env:dev` `env:local`
- Memory kind: `kind:gotcha` `kind:workaround` `kind:howto`
- Area: `area:aws` `area:terraform` `area:go` `area:node` `area:docker`
  `area:gh` `area:macos`
- Repository: `repo:<name>`, on channels several repositories share

Messages from before this vocabulary carry the flat forms (`prod`, `tmi`,
`aws`), so a search over history names both: `["env:prod", "prod"]`.

### When to tag

- **Lifecycle.** Post when an activity starts and when it ends, both
  tagged with the activity and the environment
  (`["deployment", "started", "env:prod"]`). Tag the second `succeeded`
  or `failed`, and set `reply_to` to the first. Say what and where in the
  subject: `tmi-ux v1.4.2 → prod (www.tmi.dev)`.
- **Attention.** Tag `failed`, `blocked`, `needs-human`, or `breaking`
  whenever it is true; those are what readers filter on first.
- **Changes.** When you change something other agents depend on, tag
  `change` and the area (`area:api-schema`, `area:db-schema`,
  `area:config`, `area:dependency`), plus `breaking` if callers must
  change.
- **Memories.** Tag each memory with its kind (`kind:gotcha`) and its
  area (`area:go`). Before unfamiliar work, `search` memories with the
  area's tag.
- **Handoff and review.** Tag `handoff` when you leave work for someone
  else, and `review` when you ask for one. At session start, check your
  project channel for `handoff`.
- **Less prose.** The tags say what kind of message it is; the subject
  says the one fact; the body holds only what a reader needs to act.

### Following tags

To follow a topic without joining every channel, `subscribe` with `tags`
instead of `channel`: `["deployment"]` delivers every chat message tagged
deployment; `["deployment", "failed"]` only messages carrying both (AND);
`["change", "area:*"]` every change whatever its area. Subscribe twice
for OR. Matches from channels you are not subscribed to arrive through
`receive` with `matched_tags` (the message's own tags, such as
`area:aws`, not your pattern), from now on; `agentbus wait` wakes on
them. Tag subscriptions never cover inboxes, memory channels, or task
lists. `persistent: true` records the set in `.local/agentbus.json`
(`tag_subscriptions: [["change", "area:*"], ["breaking"]]`), which
`register` applies each session: use it for what this repository always
needs to hear about. `unsubscribe` with the same `tags` drops a set.
`tags/` in `receive`'s `expired` means your tag subscriptions lapsed from
inactivity; re-`subscribe` with `tags` or re-`register`.
```

Then in the session protocol, step 4, change `` filtering by the area's tag (`aws`, `go`, ...) when there is one `` to `` filtering by the area's tag (`area:aws`, `area:go`, ...) when there is one ``.

Check nothing of the old rule is left: `cd <repo root> && rg -n -F '20 characters' internal/cli/skills/using-agentbus/SKILL.md; rg -n -F '`_`' internal/cli/skills/using-agentbus/SKILL.md`
Expected: no output from either command.

- [ ] **Step 2: Record the release-note wording (no file)**

Release notes files are written in the `chore: release vX.Y.Z` commit at release time (see `git log -- release/notes-v1.13.0.md`), not with the feature. Do not create a notes file. Add the text below to the final report of this plan's execution so the release author can paste it into `release/notes-<tag>.md`; the schema version it names must match what shipped (v10, or v11 if #33 shipped first):

```markdown
## Structured tags and prefix patterns

A tag may now contain one `:` (`env:prod`, `repo:tmi`, `area:aws`) and be
up to 32 characters. The bus gives the colon no meaning; the using-agentbus
vocabulary keys the dimensions (`env:`, `repo:`, `area:`, `kind:`) and
keeps triage tags flat (`failed`, `blocked`, `breaking`). Run
`agentbus init --global` to install the updated skill.

Every tag filter accepts a prefix pattern: a tag ending in `*` matches
every tag with that prefix. `history` and `search` `tags`, `subscribe` and
`unsubscribe` `tags`, `.local/agentbus.json` `tag_subscriptions`, and the
TUI follow prompt all take `env:*`. A subscription `["change", "area:*"]`
delivers every change whatever its area, once per message; `matched_tags`
names the message's tags (`area:aws`), not the pattern. Matching stays on
the `message_tags(tag, seq)` index (#20, ADR 0009 amendment 2026-10-06).

**Breaking:** `_` is no longer a tag character. `send`, `edit_memory` and
`.local/agentbus.json` `tags` reject it (a repository `tags` entry with
`_` comes back in `ignored_tags`). Stored tags are not rewritten: a message
tagged with `_` stays readable, but that tag can no longer be named in a
filter. A client built against the old rule keeps working unless it sends
`_`.

## Upgrading

This release changes the database schema (v10). Upgrade and restart every
host together: run `brew upgrade agentbus`, then `agentbus init --global`
to install the updated skill, then restart every harness session and
`agentbus tui`. A 1.13.x process refuses a v10 database.
```

- [ ] **Step 3: Check the docs against the code**

Run: `cd <repo root> && rg -n '1-20|_ or -|_ and -' internal/ main.go README.md`
Expected: no output (every statement of the old rule is gone; `internal/bus/embed.go:33` matches `[A-Za-z0-9_]` for shell exports, not tags, and does not match this pattern).

Run: `cd <repo root> && go test ./internal/cli/ -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd <repo root> && git add internal/cli/skills/using-agentbus/SKILL.md && git commit -m "docs(skill): keyed tag vocabulary and prefix patterns (#20)"
```

---

### Task 10: Done gate

**Files:** none new.

- [ ] **Step 1: Run the done gate**

Run: `cd <repo root> && make verify 2>&1 | tail -n 40`
Expected: build, build-all, vet, fmt-check, lint and test all succeed; the last lines are `ok` for every package and no `FAIL`. Fix anything it reports (gofmt alignment of the `migrations` map comment is the likely nit), re-run until clean, and keep the final output for the completion report.

- [ ] **Step 2: Race detector over the bus package**

Run: `cd <repo root> && go test -race ./internal/bus/ -count=1`
Expected: PASS.

- [ ] **Step 3: Confirm the tree is clean and the log is complete**

Run: `cd <repo root> && git status --short && git --no-pager log --oneline -9`
Expected: no uncommitted changes; nine commits from Tasks 1-9 on `main`. Do not push, tag or release; report the `make verify` result and stop.

---

## Self-review

- **Spec coverage.** Tag syntax -> Task 1. Patterns, `NormalizeTagPatterns`, matching table and `MatchTag` -> Task 2. `history`/`search` -> Task 3. Schema v10, second run, interrupted run (two-process case is `runMigration`'s existing re-read, unchanged) -> Task 4. Subscriptions (`lo`/`hi`, `tagCond`, `count(DISTINCT)`, `matched_tags`, `EXPLAIN` for exact and prefix) -> Task 5. `.local/agentbus.json` -> Task 6. MCP descriptions -> Task 7. TUI -> Task 8. Conventions and release -> Task 9. `make verify` -> Task 10. Every item in the spec's Testing list maps to a test above.
- **Placeholders.** None; every step carries its code or exact command.
- **Type consistency.** `NormalizeTagPatterns([]string) ([]string, error)`, `tagRange(string) (lo, hi string)`, `MatchTag(pattern, tag string) bool`, `tagsFilter(alias string, patterns []string) (string, []any)`, `tagCond(as string, floor int64) (string, []any)` with four args, `addTagRanges(*sql.Tx) error` are used with the same names and shapes in every task.
- **Review Focus.** Each of the five lines names its owning task and that task carries the test.
