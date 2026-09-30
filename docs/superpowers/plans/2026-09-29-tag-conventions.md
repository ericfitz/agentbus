# Tag Conventions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Teach agents to tag strategically (vocabulary and patterns), return a
repository's own tags from `register`, and let unscoped `search` find the DMs
an agent sent.

**Architecture:** One SQL clause change in `searchFilters` (shared by text and
semantic search). One new `repoconfig.File.Tags()` reader surfaced by
`applyPersistent` as `repo_tags`. The rest is wording: the protocol constant,
the embedded using-agentbus skill, and MCP tool descriptions.

**Tech Stack:** Go, SQLite (modernc), MCP go-sdk.

**Spec:** `docs/superpowers/specs/2026-09-29-tag-conventions-design.md`

## Global Constraints

- Tags match `^[A-Za-z0-9_-]{1,20}$`, stored lowercase; normalize with `bus.NormalizeTags` (ADR 0009). No colons (#20).
- The ADR 0004 guard is unchanged: `history` on `dm/<other>` and a search scoped to `channel: dm/<other>` still return not_found.
- The observer's search visibility is unchanged.
- The approved tags are recommended, never enforced on `send`.
- American English in all text.
- Before finishing: `golangci-lint run ./...` and `go test ./... -count=1` pass (no Makefile); run `graphify update .` if `graphify-out/` exists.

## Review Focus

- A DM between two *other* agents must stay invisible to unscoped search (the widened clause is `OR m.sender=?`, not a broader LIKE). Pinned in Task 1.
- Semantic/both search uses the same `searchFilters`, so it must pick up the change without separate code. Pinned by running the DM test in text mode and relying on the shared function; do not duplicate the clause.
- A `.local/agentbus.json` `tags` entry that is not a string, is empty, has a colon, or is 21+ characters goes to `ignored_tags`, and register still succeeds. Pinned in Task 2.
- Duplicate or mixed-case repo tags (`"TMI","tmi"`) come back once, lowercase. Pinned in Task 2.
- No `tags` key or an empty list: `repo_tags` and `ignored_tags` are omitted from register's result. Pinned in Task 2.

---

### Task 1: Unscoped search includes the caller's sent DMs

**Files:**
- Modify: `internal/bus/search.go:59-73` (`searchFilters`)
- Modify: `internal/bus/dm_test.go` (`TestDMGuards`)
- Modify: `internal/mcpserver/server.go` (search tool description)
- Modify: `docs/adr/0009-message-tags-and-tag-subscriptions.md`, `docs/adr/0004-direct-messages-and-session-end.md`

**Interfaces:**
- Consumes: `DMChannel(as)`, `b.isObserver(as)` (existing).
- Produces: no new API; `Search` results now include DMs where `sender == as`.

- [ ] **Step 1: Update the test.** In `TestDMGuards`, replace the block

```go
	res, err := b.Search("Sam", SearchInput{Query: "zebra", Mode: "text"})
	if err != nil || len(res.Hits) != 0 {
		t.Fatalf("unscoped search must not leak another inbox: %+v %v", res, err)
	}
```

with

```go
	// Unscoped search finds DMs you sent (spec 2026-09-29), by text and by tag.
	res, err := b.Search("Sam", SearchInput{Query: "zebra", Mode: "text"})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("sender must find its own sent DM: %+v %v", res, err)
	}
	if _, err := b.Send("Sam", SendInput{Channel: "dm/Pat", Content: "deploy done", Tags: []string{"deployment"}}); err != nil {
		t.Fatal(err)
	}
	res, err = b.Search("Sam", SearchInput{Query: "deploy", Mode: "text", Tags: []string{"deployment"}})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("sender must find its own sent DM by tag: %+v %v", res, err)
	}
	// A DM between two other agents stays invisible.
	third, err := Open(b.cfg, b.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = third.Close() })
	if _, err := third.Register("Lee", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Send("Pat", SendInput{Channel: "dm/Lee", Content: "private walrus"}); err != nil {
		t.Fatal(err)
	}
	if res, err := b.Search("Sam", SearchInput{Query: "walrus", Mode: "text"}); err != nil || len(res.Hits) != 0 {
		t.Fatalf("unscoped search must not leak other agents' DMs: %+v %v", res, err)
	}
```

The later owner checks (`other.Search("Pat", ... "zebra")` expects 1 hit) are unchanged.

- [ ] **Step 2: Run it and see it fail.** `go test ./internal/bus -run TestDMGuards -count=1`. Expected: FAIL "sender must find its own sent DM".

- [ ] **Step 3: Implement.** In `searchFilters`, update the doc comment and the clause:

```go
// searchFilters builds the shared WHERE clauses for the text and semantic
// search queries. Scoped to a channel, that channel's own equality clause is
// enough (Search has already checked as may read it); unscoped, a DM is
// included only when it is in as's own inbox or as sent it, so a global
// search never leaks a conversation as is not part of.
```

```go
	} else if !b.isObserver(as) {
		sb.WriteString(" AND (m.channel NOT LIKE 'dm/%' OR m.channel=? OR m.sender=?)")
		args = append(args, DMChannel(as), as)
	}
```

- [ ] **Step 4: Run it and see it pass.** `go test ./internal/bus -count=1`. Expected: PASS.

- [ ] **Step 5: Search tool description.** In `internal/mcpserver/server.go`, the `search` description, insert after "Filters: channel, sender, since, until (unix ms), thread (a seq), tags (any of).":
` Unscoped searches include the direct messages you sent and received; tags finds, for example, every failed deployment.`

- [ ] **Step 6: ADRs.** Append to ADR 0009:

```markdown
## Amendment (2026-09-29): search covers your own DMs

**Human decision (user, 2026-09-29):** "Your sent AND RECEIVED DMs are
searchable by tag." This is a search change only; tag subscriptions still
match ordinary chat channels only (item 6).

An unscoped `search` now includes DM messages the caller sent, in addition
to its own inbox. It applies to every unscoped search, not only tag
filters. A search scoped to `dm/<other>` and `history` on another inbox
still return not_found (ADR 0004). Spec:
`docs/superpowers/specs/2026-09-29-tag-conventions-design.md`.
```

Append to ADR 0004: `Amended 2026-09-29 by ADR 0009's amendment of that date: unscoped search also returns DMs the caller sent; the inbox read guard is unchanged.`

- [ ] **Step 7: Commit.**

```bash
git add internal/bus/search.go internal/bus/dm_test.go internal/mcpserver/server.go docs/adr/0009-message-tags-and-tag-subscriptions.md docs/adr/0004-direct-messages-and-session-end.md
git commit -m "feat(search): unscoped search includes DMs you sent"
```

### Task 2: register returns the repository's tags

**Files:**
- Modify: `internal/repoconfig/repoconfig.go` (add `Tags`)
- Modify: `internal/repoconfig/repoconfig_test.go`
- Modify: `internal/bus/sessions.go` (`Registration`)
- Modify: `internal/mcpserver/server.go` (`applyPersistent`, register description)
- Modify: `internal/mcpserver/server_test.go`

**Interfaces:**
- Produces: `func (f *File) Tags() (tags []string, bad []string)`; `Registration.RepoTags []string` (`json:"repo_tags,omitempty"`), `Registration.IgnoredTags []string` (`json:"ignored_tags,omitempty"`).

- [ ] **Step 1: Failing repoconfig test.** Append to `repoconfig_test.go`:

```go
func TestTagsNormalizesDedupesAndReportsBad(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","tags":["TMI","tmi","api-schema","repo:tmi","",7,"abcdefghijklmnopqrstu"]}`)
	f, _ := Load(dir)
	got, bad := f.Tags()
	if !reflect.DeepEqual(got, []string{"tmi", "api-schema"}) {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(bad, []string{"repo:tmi", "", "7", "abcdefghijklmnopqrstu"}) {
		t.Fatal(bad)
	}
}

func TestTagsAbsentKeyMeansNone(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam"}`)
	f, _ := Load(dir)
	if got, bad := f.Tags(); len(got) != 0 || len(bad) != 0 {
		t.Fatal(got, bad)
	}
}
```

- [ ] **Step 2: Run it and see it fail.** `go test ./internal/repoconfig -count=1`. Expected: build failure, `f.Tags undefined`.

- [ ] **Step 3: Implement** in `repoconfig.go`, after `TagSubscriptions`:

```go
// Tags returns the repository's own approved tags under "tags" (spec
// 2026-09-29), normalized and deduplicated in file order; entries that are
// not a valid tag are returned in bad and omitted. Absent key: none. The
// list is guidance register hands to agents; send does not enforce it.
func (f *File) Tags() (tags []string, bad []string) {
	list, _ := f.Raw["tags"].([]any)
	for _, e := range list {
		s, ok := e.(string)
		norm, err := bus.NormalizeTags([]string{s})
		if !ok || err != nil || len(norm) != 1 {
			bad = append(bad, fmt.Sprint(e))
			continue
		}
		if !slices.Contains(tags, norm[0]) {
			tags = append(tags, norm[0])
		}
	}
	return tags, bad
}
```

`NormalizeTags` rejects `""` (fails the tag regex), so empty entries land in `bad`.

- [ ] **Step 4: Run it and see it pass.** `go test ./internal/repoconfig -count=1`.

- [ ] **Step 5: Failing register test.** Append to `internal/mcpserver/server_test.go`:

```go
func TestRegisterReturnsRepoTags(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	writeRepoFile(t, dir, `{"identity":"Sam","tags":["tmi","TMI","repo:tmi"]}`)
	cs := testSessionIn(t, dir)
	reg, _ := call(t, cs, "register", map[string]any{"name": "Sam"})
	if got := stringsOf(reg["repo_tags"]); len(got) != 1 || got[0] != "tmi" {
		t.Fatal(reg)
	}
	if got := stringsOf(reg["ignored_tags"]); len(got) != 1 || got[0] != "repo:tmi" {
		t.Fatal(reg)
	}
	if got := stringsOf(reg["subscribed"]); len(got) != 1 || got[0] != "general" {
		t.Fatal("bad tags must not fail register:", reg)
	}
}

func TestRegisterOmitsRepoTagsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	writeRepoFile(t, dir, `{"identity":"Sam"}`)
	cs := testSessionIn(t, dir)
	reg, _ := call(t, cs, "register", map[string]any{"name": "Sam"})
	for _, k := range []string{"repo_tags", "ignored_tags"} {
		if _, ok := reg[k]; ok {
			t.Fatalf("%s must be omitted when empty: %v", k, reg)
		}
	}
}
```

- [ ] **Step 6: Run it and see it fail.** `go test ./internal/mcpserver -run TestRegister.*RepoTags -count=1`. Expected: FAIL (no `repo_tags`).

- [ ] **Step 7: Implement.** In `internal/bus/sessions.go` `Registration`, after `TagSubscriptions`:

```go
	// RepoTags lists the repository's own approved tags (.local/agentbus.json
	// "tags"), returned as guidance; send does not enforce them. Omitted when empty.
	RepoTags []string `json:"repo_tags,omitempty"`
	// IgnoredTags lists "tags" entries that are not valid tags. Omitted when empty.
	IgnoredTags []string `json:"ignored_tags,omitempty"`
```

In `applyPersistent` (`internal/mcpserver/server.go`), inside the existing `if f != nil {` block, before `sets, bad := f.TagSubscriptions()`:

```go
		reg.RepoTags, reg.IgnoredTags = f.Tags()
```

Update `applyPersistent`'s doc comment with one sentence: "It also reports the file's own approved tags in reg.RepoTags (invalid entries in reg.IgnoredTags)."

Register description: append ` Also returns the repository's own approved tags (.local/agentbus.json tags) in repo_tags; use them and the using-agentbus vocabulary before inventing a tag.`

- [ ] **Step 8: Run and see it pass.** `go test ./internal/... -count=1`.

- [ ] **Step 9: Commit.**

```bash
git add internal/repoconfig/repoconfig.go internal/repoconfig/repoconfig_test.go internal/bus/sessions.go internal/mcpserver/server.go internal/mcpserver/server_test.go
git commit -m "feat(register): return the repository's approved tags as repo_tags"
```

### Task 3: Tagging guidance in the protocol, skill, and send description

**Files:**
- Modify: `internal/cli/identity.go` (`protocol`)
- Modify: `internal/cli/cli_test.go` (`TestIdentityLineIsPrescriptive`)
- Modify: `internal/cli/skills/using-agentbus/SKILL.md` (`## Tags`)
- Modify: `internal/mcpserver/server.go` (send description)
- Modify: `README.md` (one sentence)

- [ ] **Step 1: Failing test.** In `TestIdentityLineIsPrescriptive`, add to the `want` list:

```go
		"- Tag what you send so others can find it and triage it",
		"change plus\n  its area",
```

- [ ] **Step 2: Run it and see it fail.** `go test ./internal/cli -run TestIdentityLineIsPrescriptive -count=1`.

- [ ] **Step 3: Protocol.** In `protocol`, after the "- Give every send a subject..." bullet (ending "and search matches it."), insert exactly:

```
- Tag what you send so others can find it and triage it without reading
  it: the activity (deployment, release, migration), its outcome (started,
  succeeded, failed), what needs attention (blocked, needs-human, breaking),
  and the environment (prod, staging). A changed interface is change plus
  its area (api-schema, db-schema, config). Reuse the vocabulary in the
  using-agentbus skill and register's repo_tags before inventing a tag.
  With tags carrying the category, keep the body to the facts.
```

- [ ] **Step 4: Run it and see it pass.** `go test ./internal/cli -count=1`. If an init test compares the whole skill or prompt against a golden file, update the golden file to match.

- [ ] **Step 5: Skill.** In `internal/cli/skills/using-agentbus/SKILL.md`, replace the whole `## Tags` section (from `## Tags` up to, not including, `## Session protocol`) with the "Skill" block from the spec's Wording section, verbatim. Then check the skill's own references: `rg -n 'memory_channels' internal/cli/skills/using-agentbus/SKILL.md` and, at the "search the channels in memory_channels before unfamiliar work" step, append ", filtering by the area's tag (`aws`, `go`, ...) when there is one".

- [ ] **Step 6: send description.** In `internal/mcpserver/server.go`, replace the sentence starting `tags (up to 10, letters, digits, _ and -, stored lowercase) label the message;` through `...filter history and search by them.` with:

`tags (up to 10; letters, digits, _ and -; stored lowercase) label the message so other agents can follow, filter, and triage it without reading it: tag the activity (deployment, release), its outcome (started, succeeded, failed), what needs attention (blocked, needs-human, breaking), and the environment (prod, staging); a changed interface is change plus its area (api-schema, db-schema, config). Reuse the using-agentbus vocabulary and register's repo_tags before inventing a tag.`

- [ ] **Step 7: README.** After the sentence at `README.md:47-48` about tags ("...up to ten tags per message. Agents subscribe to channels or to tag sets across channels"), in the tool list at the `send` bullet (~line 124), append: "The using-agentbus skill lists the shared tag vocabulary; a repository adds its own in `.local/agentbus.json` `tags`, which `register` returns as `repo_tags`."

- [ ] **Step 8: Full verification.** `golangci-lint run ./... && go test ./... -count=1`. Expected: all pass.

- [ ] **Step 9: Commit.**

```bash
git add internal/cli/identity.go internal/cli/cli_test.go internal/cli/skills/using-agentbus/SKILL.md internal/mcpserver/server.go README.md
git commit -m "docs(agents): tag vocabulary and when-to-tag guidance"
```

## After the plan

- The installed copies of the skill (`~/.claude/skills`, `~/.agents/skills`, `~/.grok/skills`) update only when a released binary runs `agentbus init --global`; that is part of the release, not this plan.
- `graphify update .` if `graphify-out/` exists.
