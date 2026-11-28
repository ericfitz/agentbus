# Memory Hygiene Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Count how often each memory is served through MCP, let agents report `USED` or `INCORRECT` with one `memory_feedback` call, soft-prune memories that are noise or left incorrect (hidden from agents, kept for the human), and give the human TUI actions and a CLI export (#53).

**Architecture:** All per-memory state lives in `memory_access` (schema 14 -> 15), keyed by `memory_id`. Agent-facing reads exclude pruned memories through one SQL fragment (`notPruned`) placed in `liveRevision`, `GetMemory`, `searchFilters` and `History`; the TUI reads through separate `SearchAll` / `HistoryAll` methods that no MCP tool calls. The maintenance tick gains a "memory hygiene" step after "memory expiry"; the 1-year hard delete is untouched. New code that is not a change to an existing function goes in `internal/bus/hygiene.go`.

**Tech Stack:** Go (pure, `CGO_ENABLED=0`), SQLite through `modernc.org/sqlite` (FTS5, `_txlock=immediate`), MCP go-sdk (input schema inferred from struct tags), Bubble Tea TUI.

**Spec:** `docs/superpowers/specs/2026-11-27-memory-hygiene-design.md` (frozen; a gap is raised as a design change, never decided in a task).

## Global Constraints

- Work lands on `main`, no PRs. Done gate: `make verify` (build, vet, vet-cross, gofmt, lint, unit tests); show its output before claiming done.
- Per-task gate before each commit: `go vet ./...` (compiles every package's tests), `go test` on every package the task touched, `make fmt-check`, `make lint`.
- Shell is zsh: unquoted `$VAR` is not word-split; build lists as arrays. Always give `rg` an explicit path (`rg PATTERN internal`). No GNU `timeout`/`sha256sum`.
- Pure Go only: no cgo, no new dependency.
- American English in code, comments and docs. Wrap docs as the surrounding file does.
- Schema: `schemaVersion` 15; new columns exactly as the spec's table (`served_count`, `used_count`, `used_at`, `grace_from`, `grace_served_base`, `incorrect_at`, `incorrect_by`, `incorrect_served_base`, `pruned_at`, `pruned_reason`).
- Config keys and defaults: `memory_noise_grace_hours` 720, `memory_noise_serves` 20, `memory_incorrect_prune_hours` 336, `memory_incorrect_prune_serves` 5. Hours range 0 to 87600; serves range 0 to 1000000. Either key of a rule at 0 disables that rule.
- Verdicts are the case-sensitive strings `USED` and `INCORRECT`. `pruned_reason` is `noise` or `incorrect`.
- A pruned memory answers agents exactly as a deleted one: `not_found`, message `memory %d has no live revision`. No agent-visible text, field or error may differ between the two.
- Agents never see counts; search hits and the `get_memory` result carry only `incorrect: true` (omitted when false).
- The include-pruned switch is never a JSON field and never reachable from an MCP input struct.
- Task rows and task channels are unchanged: no `memory_access` row, no feedback, no counters, no pruning.
- Tests that jump `b.Now` call `b.Heartbeat()` before `b.Tick` (the stale-session sweep would end the test's session otherwise), as `internal/bus/maintenance_test.go:627-633` does.
- Tests outside package `bus` reach `memory_access` through a second connection (`bus.SQLiteDSN` + `sql.Open("sqlite", dsn)`), the convention at `internal/mcpserver/server_test.go:886-897`; `internal/mcpserver` already has `openDirectDB(t, dir)`.
- Install goldens are regenerated only with `go test ./internal/cli -run TestInstallGolden -update-golden`, then re-run without the flag.

## Review Focus

1. A file that reaches step 14 already holding the new columns (a pre-v7 file whose step 6 ran the new `memoryAccessDDL`, or a fresh-DDL file re-stamped to an older version, as existing migration tests do) must migrate without a duplicate-column error and still get `grace_from` backfilled (Task 1: `TestMigrate14To15Leftovers`, plus the `grace_from` assertion added to `TestMigrateV6AddsMemoryAccessAndDropsTaskSubscriptions`).
2. A tags-only search and a channel-scoped search (paths the spec does not name) must not return a pruned memory (Task 3: `TestPrunedHiddenFromAgentReads`).
3. `task_get`, `task_update`, `task_claim` and `task_release` given a pruned memory's id must answer exactly as for a deleted memory's id, not "is not a task" (Task 3: `TestPrunedRefusedLikeDeleted` and the MCP leak test).
4. Setting only one of a rule's two keys to 0 must switch that whole rule off and leave the other rule running (Task 6: `TestHygieneZeroDisables`).
5. A memory the human restores must not be pruned again by the next tick, whether it was pruned for noise (stale `grace_from`) or as incorrect (flag still set); and a memory restored with 20 or more old serves must not be pruned when its fresh grace ends with no new serves (Task 7: `TestRestoreMemory`, `TestRestoredMemoryNeedsNewServes`).

---

### Task 1: Schema 15, migration, and the create and edit write paths

**Files:**
- Create: `internal/bus/hygiene.go`, `internal/bus/hygiene_test.go`
- Modify: `internal/bus/schema.go:3` (`schemaVersion`), `internal/bus/schema.go:75-85` (`memoryAccessDDL` and its comment)
- Modify: `internal/bus/migrate.go:23-37` (register step 14), new `addMemoryHygiene`
- Modify: `internal/bus/messages.go:233`, `internal/bus/memories.go:278` (both upserts)
- Test: `internal/bus/migrate_test.go`, `internal/bus/hygiene_test.go`

**Interfaces:**
- Produces (in `hygiene.go`):

```go
// MemoryStats is one memory_access row. Nullable columns are pointers
// without omitempty, so JSON carries an explicit null.
type MemoryStats struct {
	MemoryID            int64   `json:"memory_id"`
	AccessedAt          int64   `json:"accessed_at"`
	ServedCount         int64   `json:"served_count"`
	UsedCount           int64   `json:"used_count"`
	UsedAt              *int64  `json:"used_at"`
	GraceFrom           int64   `json:"grace_from"`
	GraceServedBase     int64   `json:"grace_served_base"`
	IncorrectAt         *int64  `json:"incorrect_at"`
	IncorrectBy         *string `json:"incorrect_by"`
	IncorrectServedBase int64   `json:"incorrect_served_base"`
	PrunedAt            *int64  `json:"pruned_at"`
	PrunedReason        *string `json:"pruned_reason"`
}

// memoryStatsCols lists every memory_access column qualified by alias a.
const memoryStatsCols = "a.memory_id, a.accessed_at, a.served_count, a.used_count, a.used_at, a.grace_from, a.grace_served_base, a.incorrect_at, a.incorrect_by, a.incorrect_served_base, a.pruned_at, a.pruned_reason"

func scanMemoryStats(rows *sql.Rows) ([]MemoryStats, error)

// memoryAccessUpsertSQL takes (memory_id, now, now). Create and edit share it.
const memoryAccessUpsertSQL = `INSERT INTO memory_access(memory_id, accessed_at, grace_from) VALUES(?,?,?)
  ON CONFLICT(memory_id) DO UPDATE SET accessed_at=excluded.accessed_at, grace_from=excluded.grace_from,
  grace_served_base=served_count, incorrect_at=NULL, incorrect_by=NULL, incorrect_served_base=0`
```

- Produces (test helpers in `hygiene_test.go`, reused by Tasks 2 to 7):

```go
func memBus(t *testing.T) (b *Bus, sam, kim string)             // newTestBus; reg Sam, Kim; CreateChannel(sam, "mem", "memory")
func remember(t *testing.T, b *Bus, as, content string) int64   // Send to "mem"; returns the memory id
func stats(t *testing.T, b *Bus, id int64) MemoryStats          // reads the row with memoryStatsCols; t.Fatal when missing
func setAccess(t *testing.T, b *Bus, id int64, set string, args ...any) // UPDATE memory_access SET <set> WHERE memory_id=id
func clockAt(b *Bus, t0 time.Time, d time.Duration)             // b.Now = func() time.Time { return t0.Add(d) }
```

- [ ] **Step 1: Write the failing migration tests** in `migrate_test.go`. Shape a v14 file as `TestMigrateV6AddsMemoryAccessAndDropsTaskSubscriptions` shapes v6: `Open`, then `DROP TABLE memory_access`, create the historical two-column form from a test-only const `memoryAccessDDLv14` (`memory_id INTEGER PRIMARY KEY, accessed_at INTEGER NOT NULL`), insert rows `(1, 111)` and `(2, 222)`, `PRAGMA user_version = 14`, close, reopen.

```go
// TestMigrate14To15
//   user_version == 15; pragma_table_info('memory_access') has 12 columns
//   rows keep accessed_at 111 and 222; served_count == 0; used_count == 0;
//   grace_served_base == 0; incorrect_served_base == 0; used_at, incorrect_at, incorrect_by,
//   pruned_at, pruned_reason are NULL
//   before <= grace_from <= after (time.Now ms around the reopen), both rows
// TestMigrate14To15SecondOpenIsNoop
//   close, reopen again: every column of both rows is byte-identical
// TestMigrate14To15Leftovers
//   a fresh-DDL file (all 12 columns) with rows (1, grace_from=777, served_count=9) and
//   (2, grace_from=0, served_count=4), stamped 14: Open succeeds; row 1 keeps 777 and
//   grace_served_base 0; row 2 gets the migration time and grace_served_base == 4
// TestMigrate14To15WithoutTable
//   memory_access dropped, stamped 14: Open succeeds (the schema DDL
//   recreates the table), 12 columns
```

Also add to the existing `TestMigrateV6AddsMemoryAccessAndDropsTaskSubscriptions`: every backfilled row has `grace_from` between `before` and `after`.

- [ ] **Step 2: Run** `go test ./internal/bus/ -run 'TestMigrate14To15|TestMigrateV6AddsMemoryAccess'`. Expected: FAIL (user_version 14, missing columns).

- [ ] **Step 3: Implement the schema change.** `schemaVersion = 15`. `memoryAccessDDL` gains the ten columns with the spec's types and defaults. `migrations[14] = addMemoryHygiene`.

`func addMemoryHygiene(tx *sql.Tx) error` (schema 14 -> 15, #53): return nil when the table is absent (as `addTagRanges` does); add each column only when `pragma_table_info('memory_access')` lacks it (as `addSessionHarness` does); then `UPDATE memory_access SET grace_from=?, grace_served_base=served_count WHERE grace_from=0` with `time.Now().UnixMilli()` (migrate runs before the Bus clock exists, same as `addMemoryAccessAndDropTaskSubscriptions`).

- [ ] **Step 4: Run** the Step 2 command. Expected: PASS. Run `go test ./internal/bus/ -run TestMigrate`. Expected: PASS (every older chain still ends at 15).

- [ ] **Step 5: Write the failing write-path tests** in `hygiene_test.go`.

```go
// TestCreateSetsGraceFromAndZeroCounters
//   clockAt(b, t0, 0); id := remember(...); s := stats(t, b, id)
//   s.ServedCount == 0 && s.UsedCount == 0 && s.GraceServedBase == 0 && s.IncorrectServedBase == 0
//   s.GraceFrom == t0.UnixMilli() && s.AccessedAt == t0.UnixMilli()
//   s.UsedAt == nil && s.IncorrectAt == nil && s.IncorrectBy == nil && s.PrunedAt == nil && s.PrunedReason == nil
// TestEditRestartsGraceClearsFlagKeepsCounters
//   setAccess(t, b, id, "served_count=7, used_count=2, used_at=5, incorrect_at=9, incorrect_by='Kim', incorrect_served_base=4")
//   clockAt(b, t0, time.Hour); EditMemory(sam, EditInput{ID: id, Content: "v2"})
//   s.ServedCount == 7 && s.UsedCount == 2 && *s.UsedAt == 5
//   s.IncorrectAt == nil && s.IncorrectBy == nil && s.IncorrectServedBase == 0
//   s.GraceFrom == t0.Add(time.Hour).UnixMilli() && s.AccessedAt == s.GraceFrom && s.GraceServedBase == 7
```

- [ ] **Step 6: Run** `go test ./internal/bus/ -run 'TestCreateSetsGraceFrom|TestEditRestartsGrace'`. Expected: FAIL (`MemoryStats` undefined, then `grace_from` 0).

- [ ] **Step 7: Implement** `MemoryStats`, `memoryStatsCols`, `scanMemoryStats` and `memoryAccessUpsertSQL` in `hygiene.go`; replace the two inline upserts (`messages.go:233`, `memories.go:278`) with `tx.Exec(memoryAccessUpsertSQL, id, now, now)` using one `now := b.nowMs()` per call.

- [ ] **Step 8: Run** `go vet ./... && go test ./internal/bus/ && make fmt-check lint`. Expected: PASS, no findings.

- [ ] **Step 9: Commit**

```bash
git add internal/bus/hygiene.go internal/bus/hygiene_test.go internal/bus/schema.go internal/bus/migrate.go internal/bus/migrate_test.go internal/bus/messages.go internal/bus/memories.go
git commit -m "feat(memory): schema 15 adds serve, use, flag and prune columns to memory_access (#53)"
```

---

### Task 2: Serve counter

**Files:**
- Modify: `internal/bus/memories.go:421-441` (`TouchMemoryAccess` and its comment)
- Test: `internal/bus/hygiene_test.go`, `internal/bus/memories_test.go` (`TestBusReadPathsNeverTouchMemoryAccess`), `internal/mcpserver/server_test.go`, `internal/tui/memories_test.go`

**Interfaces:**
- Consumes: `stats`, `setAccess`, `memBus`, `remember` (Task 1).
- Produces: `TouchMemoryAccess(ids []int64) error` keeps its signature and call sites (`internal/mcpserver/server.go:535,545`); it now also adds 1 to `served_count` once per distinct id and skips pruned rows. Test helper in `internal/tui/memories_test.go`: `func rawDB(t *testing.T, cfg config.Config) *sql.DB` (second connection, closed in `t.Cleanup`).

- [ ] **Step 1: Write the failing tests.**

```go
// internal/bus/hygiene_test.go
// TestTouchCountsOncePerDistinctID
//   TouchMemoryAccess([]int64{id, id, id}); stats(...).ServedCount == 1
//   TouchMemoryAccess([]int64{id});         stats(...).ServedCount == 2
// TestTouchSkipsPrunedMemory
//   setAccess(t, b, id, "pruned_at=1, pruned_reason='noise', accessed_at=50, served_count=3")
//   TouchMemoryAccess([]int64{id}); s.ServedCount == 3 && s.AccessedAt == 50

// internal/bus/memories_test.go, added to TestBusReadPathsNeverTouchMemoryAccess:
//   after GetMemory, Search, History and MemoryRevisions: served_count == 0

// internal/mcpserver/server_test.go
// TestSearchAndGetMemoryToolsCountServes (own bus + server + openDirectDB, as the test above it)
//   two memories a, b both matching "widget"
//   search query=widget            -> served a == 1, b == 1
//   search query=widget count=1    -> the returned hit's count +1, the other unchanged
//   search query=widget channel=mem-> both +1
//   get_memory id=a                -> a +1
//   history channel=mem            -> both unchanged
//   get_memory on an unknown id    -> error, nothing changes

// internal/tui/memories_test.go
// TestTUIReadsDoNotCountServes
//   onMemory(t); run a search for "Release" through the overlay; f.key(".")
//   SELECT served_count FROM memory_access WHERE memory_id=? == 0 (rawDB)
```

- [ ] **Step 2: Run** `go test ./internal/bus/ ./internal/mcpserver/ ./internal/tui/ -run 'TestTouch|TestBusReadPathsNeverTouch|TestSearchAndGetMemoryToolsCountServes|TestTUIReadsDoNotCountServes'`. Expected: the bus and mcpserver count tests FAIL (count stays 0); the TUI test passes already and stays as the regression guard.

- [ ] **Step 3: Implement.** In `TouchMemoryAccess`: deduplicate `ids` preserving order, then one statement `UPDATE memory_access SET accessed_at=?, served_count=served_count+1 WHERE pruned_at IS NULL AND memory_id IN (...)`. Update the doc comment (serves, once per call, pruned rows skipped).

- [ ] **Step 4: Run** the Step 2 command, then `go vet ./... && go test ./internal/bus/ ./internal/mcpserver/ ./internal/tui/ && make fmt-check lint`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/memories.go internal/bus/hygiene_test.go internal/bus/memories_test.go internal/mcpserver/server_test.go internal/tui/memories_test.go
git commit -m "feat(memory): count MCP serves per memory, once per call (#53)"
```

---

### Task 3: Agent reads exclude pruned memories; leak test

**Files:**
- Modify: `internal/bus/hygiene.go` (`notPruned`)
- Modify: `internal/bus/memories.go:31-48` (`GetMemory`), `:73-85` (`liveRevision`, new `liveRevisionAny`)
- Modify: `internal/bus/search.go:12-24` (`SearchInput`), `:161-193` (`searchFilters`), `:261` (`Search`, new `SearchAll`)
- Modify: `internal/bus/messages.go:419` (`History`, new `HistoryAll`)
- Modify: `internal/tui/model.go:1471` (`History` -> `HistoryAll`), `internal/tui/search.go:141` (`Search` -> `SearchAll`)
- Create: `internal/mcpserver/pruned_leak_test.go`
- Test: `internal/bus/hygiene_test.go`, `internal/tui/memories_test.go`

**Interfaces:**
- Consumes: Task 1 helpers.
- Produces:

```go
// notPruned is the AND clause that hides pruned memories from agents; alias
// names the messages row ("m" in search, "messages" elsewhere).
func notPruned(alias string) string
// " AND NOT EXISTS (SELECT 1 FROM memory_access pa WHERE pa.memory_id=<alias>.memory_id AND pa.pruned_at IS NOT NULL)"

func liveRevision(q queryRower, id int64) (seq, revision int64, channel string, err error)    // now excludes pruned
func liveRevisionAny(q queryRower, id int64) (seq, revision int64, channel string, err error) // includes pruned; same not_found text

func (b *Bus) Search(as string, in SearchInput) (SearchResult, error)    // agents: excludes pruned
func (b *Bus) SearchAll(as string, in SearchInput) (SearchResult, error) // human TUI only: includes pruned
func (b *Bus) History(as, channel string, before, after *int64, count int, tags ...string) ([]Message, error)    // excludes pruned
func (b *Bus) HistoryAll(as, channel string, before, after *int64, count int, tags ...string) ([]Message, error) // includes pruned
```

`SearchInput` gains the unexported field `includePruned bool` (no tag; set only by `SearchAll`, forced false by `Search`). `searchFilters` appends `notPruned("m")` unless it is set, which covers `textSearch`, `tagsOnlySearch` and both `semanticSearch` queries. `MemoryRevisions` is unchanged (TUI-only, includes pruned). Test helper: `func pruneRaw(t *testing.T, b *Bus, id int64, reason string)` (`setAccess` with `pruned_at=b.nowMs()`).

- [ ] **Step 1: Write the failing bus tests** in `hygiene_test.go`. Each uses a pruned memory `p` and a deleted control `d` (`DeleteMemory`), both containing the word `zebracorn` and tag `kind:gotcha`; `sameErr(a, b error, ida, idb int64)` compares code and message after replacing each id with `N`.

```go
// TestPrunedHiddenFromAgentReads
//   GetMemory(sam, p): code not_found, message "memory <p> has no live revision"
//   Search{Query: "zebracorn"}: no hit with MemoryID p (mode text)
//   Search{Tags: ["kind:gotcha"]}: no hit p            (tags-only path)
//   Search{Query: "zebracorn", Channel: "mem"}: no hit p
//   History(sam, "mem", nil, nil, 0): no row p
//   SearchAll (same three inputs) and HistoryAll: p present
//   MemoryRevisions(sam, p): one revision
//   liveRevision(b.db, p): not_found; liveRevisionAny(b.db, p): p's live seq, revision 1, channel "mem"
//   (this call also keeps liveRevisionAny used until Task 4, so the unused lint stays clean)
// TestPrunedSemanticSearchExcluded (newEmbedBus + fakeEmbeddings, drainEmbed)
//   modes "semantic" and "both": Search has no hit p; SearchAll has it
// TestPrunedRefusedLikeDeleted
//   sameErr for each pair: EditMemory, DeleteMemory, TaskGet, TaskUpdate,
//   TaskClaim, TaskRelease called with p and with d
//   after the calls: stats(t, b, p) unchanged, p still has its live revision
```

- [ ] **Step 2: Run** `go test ./internal/bus/ -run TestPruned`. Expected: FAIL (`SearchAll`, `HistoryAll`, `pruneRaw` undefined; then p leaks).

- [ ] **Step 3: Implement** `notPruned`, the `liveRevision` / `liveRevisionAny` pair (one shared body), the `GetMemory` clause, the `SearchInput` field with `Search` / `SearchAll`, and `History` / `HistoryAll` over one unexported body. Do not widen `messageCols` or `scanMessages`.

- [ ] **Step 4: Run** `go test ./internal/bus/`. Expected: PASS.

- [ ] **Step 5: Write the MCP leak test** `TestPrunedMemoryNeverLeaksThroughAnyTool` in `pruned_leak_test.go`. Build bus, server and client as `TestSearchAndGetMemoryToolsUpdateAccessedAt` does; register `Sam`; create `mem` (memory) and `tasks/work`; send memories `p` and `d` containing `zebracorn`; delete `d` with `delete_memory`; prune `p` through `openDirectDB`. A `map[string]func(t *testing.T)` holds one case per tool name; the test iterates `cs.ListTools` and fails with `no leak case for tool <name>` for any tool without one.

```go
// id-taking tools: the error text for p equals the error text for d once
// both ids are replaced by N:
//   get_memory, edit_memory, delete_memory, task_get, task_update, task_claim, task_release
// content tools: the result text contains neither "zebracorn" nor p's id as memory_id:
//   search (query; tags-only; channel=mem; mode=text), history (channel=mem), receive
// every other tool (register, create_channel, list_channels, subscribe,
// unsubscribe, send, discover, task_create, task_list, set_status,
// status_history): one valid call; the result text does not contain "zebracorn"
```

- [ ] **Step 6: Run** `go test ./internal/mcpserver/ -run TestPrunedMemoryNeverLeaks`. Expected: PASS (the bus change is in place; the test is the guard). Confirm it guards: temporarily drop `notPruned("m")` from `searchFilters`, see it FAIL on `search`, restore.

- [ ] **Step 7: Switch the TUI call sites** (`model.go:1471`, `search.go:141`) and add `TestTUIShowsPrunedMemoryInChannelAndSearch` to `internal/tui/memories_test.go`: `onMemory`, prune the cursor memory through `rawDB`, reload with `f.run(f.m.loadHistory("dev-notes", nil))`: `f.streamHas(t, "Release v3")`; a search for `Release` has a hit with that `MemoryID`.

- [ ] **Step 8: Run** `go vet ./... && go test ./internal/bus/ ./internal/mcpserver/ ./internal/tui/ && make fmt-check lint`. Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/bus/hygiene.go internal/bus/hygiene_test.go internal/bus/memories.go internal/bus/search.go internal/bus/messages.go internal/tui/model.go internal/tui/search.go internal/tui/memories_test.go internal/mcpserver/pruned_leak_test.go
git commit -m "feat(memory): pruned memories are hidden from every agent read; leak test (#53)"
```

---

### Task 4: `memory_feedback`

**Files:**
- Modify: `internal/bus/hygiene.go` (`MemoryFeedback`)
- Modify: `internal/mcpserver/server.go` (input struct near `:92`, tool after `delete_memory` at `:555`)
- Test: `internal/bus/hygiene_test.go`, `internal/mcpserver/server_test.go` (`:80` tool list, new tool test), `internal/mcpserver/pruned_leak_test.go`

**Interfaces:**
- Consumes: `liveRevisionAny`, `pruneRaw` (Task 3); `stats`, `setAccess` (Task 1).
- Produces:

```go
type FeedbackItem struct {
	MemoryID int64  `json:"memory_id"`
	Verdict  string `json:"verdict" jsonschema:"USED or INCORRECT"`
}
type FeedbackResult struct {
	Applied []FeedbackItem `json:"applied"`
}
const FeedbackMaxItems = 50

func (b *Bus) MemoryFeedback(as string, items []FeedbackItem) (FeedbackResult, error)
```

  `Verdict` stays a plain string validated in the bus (like `channelIn.Kind`), so one error names bad verdicts and bad ids together. Every failure is code `validation`, not retryable:
  - no items: `items is required: 1 to 50 {memory_id, verdict} pairs`
  - over the cap: `at most 50 items per call, got %d`
  - bad items: `no feedback applied: ` followed by one `item %d (memory %d): <reason>` per bad item (0-based index, list order, joined with `; `); `<reason>` is `unknown verdict %q (use USED or INCORRECT)`, `not found`, or `task row`. The verdict is checked first; an item gets one reason. `not found` covers an unknown id, a deleted memory, a pruned memory, a chat or DM seq, and a memory with no `memory_access` row.
- Order inside the call: `auth(b.db)`, count checks, `Begin`, `auth(tx)`, validate every item on the tx, charge `b.limits.allow(as, 0, b.Now())` once (error `rate_limited` with the text `DeleteMemory` uses), apply in list order with one shared `now := b.nowMs()`, commit. No inspection hook, no idempotency key.
- Effects: `USED` sets `used_count=used_count+1, used_at=now, accessed_at=now`. `INCORRECT` sets `incorrect_at=now, incorrect_by=as, incorrect_served_base=served_count`.
- MCP input struct and tool `memory_feedback`:

```go
type feedbackIn struct {
	As    string             `json:"as,omitempty"`
	Items []bus.FeedbackItem `json:"items" jsonschema:"1 to 50 pairs of memory_id and verdict (USED or INCORRECT)"`
}
```

  Description: `Agentbus: report on memories you were served, in one call. items is 1 to 50 {memory_id, verdict} pairs. USED: you relied on the memory and it was right. INCORRECT: it is wrong and you do not know the right answer; when you do know it, correct the memory with edit_memory instead, which also clears the flag. Nothing is applied unless every item is valid; the error names each bad item. Task ids are not accepted.`

- [ ] **Step 1: Write the failing bus tests** in `hygiene_test.go`.

```go
// TestFeedbackUsed (Sam is the memory's author)
//   clockAt(b, t0, time.Hour); MemoryFeedback(sam, {id, "USED"})
//   s.UsedCount == 1 && *s.UsedAt == now && s.AccessedAt == now; res.Applied has the one item
// TestFeedbackIncorrectAndReflag
//   setAccess served_count=3; MemoryFeedback(kim, {id, "INCORRECT"}) at t0+1h
//     *s.IncorrectAt == t0+1h && *s.IncorrectBy == "Kim" && s.IncorrectServedBase == 3
//   setAccess served_count=5; MemoryFeedback(sam, {id, "INCORRECT"}) at t0+2h
//     *s.IncorrectAt == t0+2h && *s.IncorrectBy == "Sam" && s.IncorrectServedBase == 5
// TestFeedbackDuplicateIdsApplyInOrder
//   [{id,USED},{id,INCORRECT},{id,USED}] -> s.UsedCount == 2 && *s.UsedAt == *s.IncorrectAt
// TestFeedbackAllOrNothing
//   items: {good,USED} {deleted,USED} {taskID,INCORRECT} {pruned,USED} {good,"used"} {chatSeq,USED}
//   wantCode(t, err, "validation"); the message contains, in this order:
//     "item 1 (memory <deleted>): not found", "item 2 (memory <taskID>): task row",
//     "item 3 (memory <pruned>): not found", `item 4 (memory <good>): unknown verdict "used"`,
//     "item 5 (memory <chatSeq>): not found"
//   and does not contain "item 0"; stats(good).UsedCount == 0
//   the pruned item's reason text is identical to the deleted item's
// TestFeedbackBounds
//   nil and empty -> validation; 51 items -> validation, nothing applied; 50 items -> ok
// TestFeedbackEditClearsFlag
//   INCORRECT, then EditMemory -> s.IncorrectAt == nil; s.UsedCount unchanged
// TestFeedbackRateLimited
//   exhaust the limiter the way internal/bus/receipts_test.go does -> code rate_limited, nothing applied
```

- [ ] **Step 2: Run** `go test ./internal/bus/ -run TestFeedback`. Expected: FAIL (`MemoryFeedback` undefined).

- [ ] **Step 3: Implement `MemoryFeedback`** in `hygiene.go` as specified above.

- [ ] **Step 4: Run** the Step 2 command. Expected: PASS.

- [ ] **Step 5: Write the failing MCP tests.** Add `"memory_feedback": true` to the `want` map in `TestToolsRegisteredWithPrefixDescriptions`. Add `TestMemoryFeedbackTool`: two memories; a call with `USED` and `INCORRECT` returns `applied` of length 2; a call with verdict `MAYBE` returns `IsError` with text containing `validation` and `unknown verdict`; an empty `items` returns a validation error. Add the `memory_feedback` case to the leak test: `{p, USED}` and `{d, USED}` produce the same error text once ids are replaced by `N`.

- [ ] **Step 6: Run** `go test ./internal/mcpserver/ -run 'TestToolsRegistered|TestMemoryFeedbackTool|TestPrunedMemoryNeverLeaks'`. Expected: `TestToolsRegisteredWithPrefixDescriptions` FAILS with `missing tools: map[memory_feedback:true]` and `TestMemoryFeedbackTool` FAILS on an unknown tool; the leak test still passes (its new case is not reached until the tool is listed).

- [ ] **Step 7: Register the tool** in `server.go` with `feedbackIn` and the description above: `return result(b.MemoryFeedback(in.As, in.Items))`.

- [ ] **Step 8: Run** `go vet ./... && go test ./internal/bus/ ./internal/mcpserver/ && make fmt-check lint`. Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/bus/hygiene.go internal/bus/hygiene_test.go internal/mcpserver/server.go internal/mcpserver/server_test.go internal/mcpserver/pruned_leak_test.go
git commit -m "feat(memory): memory_feedback tool records USED and INCORRECT (#53)"
```

---

### Task 5: Search marks flagged hits and ranks them last; `get_memory` carries the flag

**Files:**
- Modify: `internal/bus/search.go:71-74` (`SearchHit`), the shared search body after the mode switch and before `page := ranked[offset:]`
- Modify: `internal/bus/hygiene.go` (`FlaggedMemory`, `GetMemoryFlagged`), `internal/mcpserver/server.go:541-550` (the `get_memory` handler)
- Test: `internal/bus/search_test.go`, `internal/bus/hygiene_test.go`, `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: `MemoryFeedback` (Task 4), `SearchAll`, `pruneRaw` (Task 3).
- Produces:

```go
type SearchHit struct {
	Message
	Score float64 `json:"score"`
	// Incorrect marks a memory flagged INCORRECT and not yet corrected.
	Incorrect bool `json:"incorrect,omitempty"`
	// Pruned is true only on SearchAll hits; it never reaches JSON.
	Pruned bool `json:"-"`
}

// markHits sets Incorrect and Pruned from memory_access (one query for the
// flagged and pruned ids) and moves flagged hits after every unflagged one,
// keeping the existing order inside each group (sort.SliceStable).
func (b *Bus) markHits(hits []SearchHit) ([]SearchHit, error)

// FlaggedMemory is get_memory's result: the message plus the same incorrect
// field search hits carry (spec item 16).
type FlaggedMemory struct {
	Message
	Incorrect bool `json:"incorrect,omitempty"`
}

// GetMemoryFlagged is GetMemory plus one memory_access lookup; a row with
// no memory_access entry (a task) is never flagged.
func (b *Bus) GetMemoryFlagged(as string, id int64) (FlaggedMemory, error)
```

  `GetMemory` keeps its signature. The `get_memory` handler calls `GetMemoryFlagged` and still calls `TouchMemoryAccess` on success, as today.

  `markHits` runs on the full ranked list of every mode (text, semantic, both after fusion, tags-only), before the cursor offset and the count cut. Task-channel hits are never marked.

- [ ] **Step 1: Write the failing tests.**

```go
// internal/bus/search_test.go (memories a, b, c on "mem"; a repeats the query word so it scores best)
// TestSearchFlaggedHitsRankLast
//   MemoryFeedback(kim, {a, INCORRECT}); Search{Query: "widget", Mode: "text"}
//   hit order by MemoryID: unflagged in score order, then a; only a has Incorrect
//   json.Marshal(unflagged hit) has no "incorrect" key
//   json.Marshal(any hit) contains none of "served_count", "used_count", "pruned"
// TestSearchFlaggedOrderAmongFlagged
//   flag a and b: after c, a still precedes b (normal score order)
// TestSearchFlaggedLastBeforeLimit
//   Count: 1 -> the one hit is unflagged and res.Next != ""; following Next reaches a last
// TestSearchFlaggedLastTagsOnly
//   Search{Tags: [...]}: the newest memory is flagged -> it is the last hit
// TestSearchFlaggedLastBothMode (newEmbedBus + fakeEmbeddings)
//   Mode "both": the flagged hit is last after fusion
// TestSearchAllMarksPruned
//   pruneRaw(c); SearchAll: hit c has Pruned == true; Search: no hit c
// TestEditedMemoryLosesFlagInSearch
//   EditMemory(a): the new revision's hit has Incorrect == false

// internal/bus/hygiene_test.go
// TestGetMemoryFlagged
//   unflagged: Incorrect == false; json.Marshal(result) equals json.Marshal of GetMemory's Message (no "incorrect" key)
//   after MemoryFeedback(kim, {id, INCORRECT}): Incorrect == true; the JSON has "incorrect":true and no count field
//   after EditMemory: Incorrect == false
//   pruned: not_found, "memory <id> has no live revision"

// internal/mcpserver/server_test.go
// TestSearchToolCarriesIncorrect
//   after memory_feedback INCORRECT: the search result's hit has "incorrect": true;
//   an unflagged hit's map has no "incorrect" key
// TestGetMemoryToolCarriesIncorrect
//   get_memory on a flagged memory: result["incorrect"] == true; on an unflagged one the key is absent;
//   served_count still rises by 1 per call (raw connection)
```

- [ ] **Step 2: Run** `go test ./internal/bus/ ./internal/mcpserver/ -run 'TestSearchFlagged|TestSearchAllMarksPruned|TestEditedMemoryLosesFlag|TestGetMemoryFlagged|TestSearchToolCarriesIncorrect|TestGetMemoryToolCarriesIncorrect'`. Expected: FAIL (fields and `GetMemoryFlagged` undefined).

- [ ] **Step 3: Implement** the two fields and `markHits`, and call it in the shared search body; implement `FlaggedMemory` and `GetMemoryFlagged` and switch the `get_memory` handler to it.

- [ ] **Step 4: Run** `go vet ./... && go test ./internal/bus/ ./internal/mcpserver/ ./internal/tui/ && make fmt-check lint`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/search.go internal/bus/search_test.go internal/bus/hygiene.go internal/bus/hygiene_test.go internal/mcpserver/server.go internal/mcpserver/server_test.go
git commit -m "feat(search): flagged memories carry incorrect on search and get_memory, and rank last (#53)"
```

---

### Task 6: Config keys, the memory hygiene tick step, health view

**Files:**
- Modify: `internal/config/config.go:26-32` (fields and comment), `:76-80` (defaults), `:349-354` (bounds)
- Modify: `internal/bus/maintenance.go:92-98` (new step after "memory expiry"), new `memoryHygiene` and `pruneChunk`
- Modify: `internal/tui/health.go:153`
- Test: `internal/config/config_test.go`, `internal/bus/maintenance_test.go`, `internal/tui/health_test.go` (`:15` height, `:22` wanted strings)

**Interfaces:**
- Consumes: Task 1 helpers; `SearchAll` (Task 3).
- Produces:

```go
// internal/config/config.go, after MemoryExpiryHours
MemoryNoiseGraceHours      int `json:"memory_noise_grace_hours"`      // default 720, 0..87600
MemoryNoiseServes          int `json:"memory_noise_serves"`           // default 20, 0..1000000
MemoryIncorrectPruneHours  int `json:"memory_incorrect_prune_hours"`  // default 336, 0..87600
MemoryIncorrectPruneServes int `json:"memory_incorrect_prune_serves"` // default 5, 0..1000000

// internal/bus/maintenance.go
func (b *Bus) memoryHygiene(ctx context.Context, now int64) error
func (b *Bus) pruneChunk(now int64, reason, cond string, args ...any) (int, error)
```

  Tick step name: `"memory hygiene"`, directly after `"memory expiry"`. `memoryHygiene` runs the incorrect rule to completion (`loopChunks`), then the noise rule, so a row matching both gets reason `incorrect`; it skips a rule when either of its keys is 0. `pruneChunk` is one statement: `UPDATE memory_access SET pruned_at=?, pruned_reason=? WHERE memory_id IN (SELECT memory_id FROM memory_access WHERE pruned_at IS NULL AND <cond> LIMIT ?)` with `cleanupChunk`, returning rows affected, and logs `memories pruned` with `reason` and `count` when it pruned any. Conditions, verbatim from the spec:
  - incorrect: `incorrect_at IS NOT NULL AND incorrect_at < ? AND served_count - incorrect_served_base > ? AND (used_at IS NULL OR used_at < incorrect_at)` with `now - hours*3_600_000` and the serves key
  - noise: `used_count = 0 AND served_count - grace_served_base >= ? AND grace_from < ?` with the serves key and `now - hours*3_600_000` (serves since the grace start, spec item 15)
- Health lines after `memory expiry`: `noise prune 720 h · 20 serves` and `incorrect prune 336 h · 5 serves` (labels dim, like the lines above them).

- [ ] **Step 1: Write the failing config tests** in `config_test.go`: `TestHygieneDefaults` (720, 20, 336, 5); `TestHygieneOverrideAcceptsZero` (all four at 0 load); `TestRejectsOutOfRangeHygieneKeys` (each key at -1, each hours key at 87601, each serves key at 1000001: the error names the key).

- [ ] **Step 2: Run** `go test ./internal/config/ -run 'Hygiene'`. Expected: FAIL (fields undefined).

- [ ] **Step 3: Add** the four fields, defaults and bounds; extend the comment above `TaskIdleHours`. Run Step 2 again. Expected: PASS.

- [ ] **Step 4: Write the failing tick tests** in `maintenance_test.go`. Each creates memories with `remember` at `t0`, shapes rows with `setAccess`, jumps with `clockAt`, calls `b.Heartbeat()` then `b.Tick(context.Background())`. `H = time.Hour`.

```go
// TestNoisePrune (defaults)
//   served_count=20, used_count=0, created at t0
//   tick at t0+720H   -> PrunedAt == nil            (grace_from < now-720h is strict)
//   tick at t0+721H   -> *PrunedAt == now, *PrunedReason == "noise", AccessedAt unchanged
//   then: GetMemory -> not_found; SearchAll{Query: <its word>} still finds it
//   (FTS row kept); count(*) FROM messages WHERE memory_id=? AND tombstone=0 == 1
// TestNoiseNeedsServesAndZeroUsed
//   served 19, used 0, t0+721H            -> not pruned
//   served 500, used 1, t0+721H           -> not pruned
// TestNoiseCountsServesSinceGraceStart
//   served 34, grace_served_base 15 (19 since), t0+721H -> not pruned
//   served 35, grace_served_base 15 (20 since), t0+721H -> pruned, reason "noise"
// TestIncorrectPrune (flag at F = t0+1H via setAccess: incorrect_at=F, incorrect_by='Kim', incorrect_served_base=10)
//   served 16, used_at NULL, now F+337H   -> pruned, reason "incorrect"
//   served 15, now F+337H                 -> not pruned ("more than 5")
//   served 16, now F+336H                 -> not pruned
//   served 16, used_at == F, F+337H       -> not pruned (same millisecond counts as used since)
//   served 16, used_at == F-1, F+337H     -> pruned
//   served 16, used_at == F+1, F+337H     -> not pruned
//   served 10 (no serve since the flag), now F+5000H -> not pruned
// TestBothRulesMatchReasonIncorrect
//   a row matching both conditions -> *PrunedReason == "incorrect"
// TestHygieneZeroDisables (four subtests, one key at 0 each; one noise-eligible and one incorrect-eligible row)
//   MemoryNoiseGraceHours=0 or MemoryNoiseServes=0              -> noise row survives, incorrect row pruned
//   MemoryIncorrectPruneHours=0 or MemoryIncorrectPruneServes=0 -> incorrect row survives, noise row pruned
// TestHygieneSecondTickIsNoop
//   prune at t0+721H; tick again at t0+722H -> PrunedAt and PrunedReason unchanged
// TestHygienePrunesMoreThanOneChunk
//   1001 eligible memory_access rows inserted by SQL (ids 100001.., served_count=20,
//   grace_from=1) -> one tick leaves count(*) WHERE pruned_at IS NULL AND memory_id>100000 == 0
// TestPrunedMemoryStillExpiresAtOneYear
//   pruned at t0+721H; tick at t0+8761H -> its memory_access row is gone and its message row has tombstone=1
// TestEditRestartsNoiseGrace
//   served 20; EditMemory at E = t0+719H; tick at t0+721H -> not pruned
//   tick at E+721H with no new serve -> not pruned (the edit set grace_served_base to 20)
//   20 TouchMemoryAccess calls, then tick at E+721H+1H -> pruned, reason "noise"
```

- [ ] **Step 5: Run** `go test ./internal/bus/ -run 'TestNoise|TestIncorrectPrune|TestBothRules|TestHygiene|TestPrunedMemoryStillExpires|TestEditRestartsNoiseGrace'`. Expected: FAIL (nothing prunes).

- [ ] **Step 6: Implement** `pruneChunk`, `memoryHygiene` and the tick step.

- [ ] **Step 7: Run** `go test ./internal/bus/`. Expected: PASS.

- [ ] **Step 8: Health view.** In `health_test.go` raise the fixture height at `:15` from 77 to 83 (two summary lines plus four config JSON lines) and add `"noise prune 720 h · 20 serves"`, `"incorrect prune 336 h · 5 serves"` to the wanted strings at `:22`; run `go test ./internal/tui/ -run TestHealth` (FAIL); add the two lines in `health.go`; run again (PASS). If another health test scrolls by a fixed count, adjust it by the same six lines.

- [ ] **Step 9: Run** `go vet ./... && go test ./internal/config/ ./internal/bus/ ./internal/tui/ ./internal/mcpserver/ && make fmt-check lint`. Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/bus/maintenance.go internal/bus/maintenance_test.go internal/tui/health.go internal/tui/health_test.go
git commit -m "feat(memory): tick soft-prunes noise and uncorrected incorrect memories (#53)"
```

---

### Task 7: Bus methods for the human: stats, restore, clear flag, delete

**Files:**
- Modify: `internal/bus/hygiene.go`
- Test: `internal/bus/hygiene_test.go`

**Interfaces:**
- Consumes: `liveRevisionAny` (Task 3), `tombstoneMemoryRow` (`internal/bus/memories.go:400`), `b.inspect` (`internal/bus/inspect.go:43`), `MemoryStats`, `memoryStatsCols`, `scanMemoryStats` (Task 1), the tick (Task 6).
- Produces (none of these is ever registered as an MCP tool; say so in each doc comment):

```go
// ChannelMemoryStats returns the memory_access row of every live memory on channel, pruned ones included, keyed by memory id.
func (b *Bus) ChannelMemoryStats(as, channel string) (map[int64]MemoryStats, error)
// RestoreMemory clears pruned_at, pruned_reason and the INCORRECT flag (all three columns) and sets grace_from=now and grace_served_base=served_count; counters stay.
func (b *Bus) RestoreMemory(as string, id int64) error
// ClearMemoryIncorrect clears incorrect_at, incorrect_by, incorrect_served_base on any live memory, pruned or not; pruned state stays.
func (b *Bus) ClearMemoryIncorrect(as string, id int64) error
// DeleteMemoryAny deletes a live memory, pruned or not: DeleteMemory's write with no idempotency receipt.
func (b *Bus) DeleteMemoryAny(as string, id int64) error
```

  Errors: no live revision -> `not_found`, `memory %d has no live revision`; a task channel -> `validation` with the text `DeleteMemory` uses; `RestoreMemory` on an unpruned memory -> `validation`, `memory %d is not pruned`; `ClearMemoryIncorrect` on an unflagged memory -> `validation`, `memory %d is not flagged incorrect`. Each method: `auth(b.db)`, one transaction, `auth(tx)`, `liveRevisionAny(tx, id)`, a guarded `UPDATE` checked by `RowsAffected`. `DeleteMemoryAny` also runs `b.inspect("delete_memory", as, map[string]any{"delete": id})` before the transaction (TUI sends already run the hook through `Send`) and charges the rate limit like `DeleteMemory`.

- [ ] **Step 1: Write the failing tests.**

```go
// TestChannelMemoryStatsIncludesPruned
//   two memories on "mem", one pruned, one deleted, plus a task: the map has exactly the two live ids;
//   the pruned one has PrunedAt != nil; a memory on another channel is absent
// TestRestoreMemory (two subtests: pruned "noise" with stale grace_from; pruned "incorrect" with the flag set)
//   setAccess served_count=30, used_count=0, and the pruned and flag columns
//   RestoreMemory(sam, id) at t0+1000H:
//     s.PrunedAt == nil && s.PrunedReason == nil && s.IncorrectAt == nil && s.IncorrectBy == nil
//     s.IncorrectServedBase == 0 && s.GraceFrom == now && s.ServedCount == 30 && s.GraceServedBase == 30
//   GetMemory(sam, id) succeeds
//   b.Heartbeat(); b.Tick(ctx) at the same instant and at +1H -> still not pruned
//   RestoreMemory again -> validation "memory <id> is not pruned"; stats unchanged
// TestRestoredMemoryNeedsNewServes (spec item 15)
//   pruned "noise" with served_count=30, used_count=0; RestoreMemory at R = t0+1000H
//   b.Heartbeat(); b.Tick at R+721H with no new serve -> PrunedAt == nil
//   19 TouchMemoryAccess calls; tick at R+722H -> PrunedAt == nil
//   1 more TouchMemoryAccess (20 since the restore); tick at R+723H -> pruned, reason "noise"
// TestClearMemoryIncorrect
//   flagged -> the three columns cleared; counters, grace_from and grace_served_base unchanged
//   flagged and pruned -> flag cleared, PrunedAt still set
//   unflagged -> validation "memory <id> is not flagged incorrect"
// TestDeleteMemoryAny
//   on a pruned memory: tombstone=1, no memory_access row, b.inspectCalls rose by 1
//   on an unpruned memory: same
//   again on the same id -> not_found; on a task id -> validation
//   with a refusing hook (hookScript, internal/bus/inspect_test.go) -> refused, the memory is still live
```

- [ ] **Step 2: Run** `go test ./internal/bus/ -run 'TestChannelMemoryStatsIncludesPruned|TestRestore|TestClearMemoryIncorrect|TestDeleteMemoryAny'`. Expected: FAIL (methods undefined).

- [ ] **Step 3: Implement** the four methods in `hygiene.go`.

- [ ] **Step 4: Run** `go vet ./... && go test ./internal/bus/ && make fmt-check lint`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/hygiene.go internal/bus/hygiene_test.go
git commit -m "feat(memory): bus methods to read stats and restore, unflag or delete any memory (#53)"
```

---

### Task 8: TUI: counts, flag and pruned state; restore, clear flag, delete

**Files:**
- Modify: `internal/tui/msgs.go` (`memStatsMsg`)
- Modify: `internal/tui/memories.go` (stats load, label, three actions, confirm)
- Modify: `internal/tui/model.go:22-30` (mode), `:62` (`Model` fields), `:204-224` (message dispatch, status tick), `:361-374` (mode dispatch), `:458-656` (`updateNormal` keys)
- Modify: `internal/tui/view.go:895-912` (label and dim in `renderStream`), `:985` (confirm line)
- Modify: `internal/tui/search.go:207-230` (hit line), `internal/tui/help.go:29-60`
- Test: `internal/tui/memories_test.go`, `internal/tui/search_test.go`, `internal/tui/help_test.go`

**Interfaces:**
- Consumes: `bus.MemoryStats` (type) and `(*Bus).ChannelMemoryStats`, `RestoreMemory`, `ClearMemoryIncorrect`, `DeleteMemoryAny` (Task 7); `SearchHit.Incorrect`, `SearchHit.Pruned` (Task 5); `HistoryAll`, `SearchAll`, `rawDB` (Tasks 2 and 3).
- Produces:

```go
// msgs.go
memStatsMsg struct {
	channel string
	stats   map[int64]bus.MemoryStats
	err     error
}

// model.go
modeConfirmMemory // x on a memory row; y deletes, anything else cancels (added after modeAssign)
// Model fields
memStats   map[string]map[int64]bus.MemoryStats // by channel, then memory id; each memStatsMsg replaces its channel's map
confirmMem struct{ id int64; ch, title string }

// memories.go
func (m *Model) loadMemStats(ch string) tea.Cmd // ChannelMemoryStats in a tea.Cmd; nil unless ch is a memory-kind, non-task channel in m.channels
func memLabel(rev string, st bus.MemoryStats, ok bool) string
func (m *Model) cursorMemory() (bus.Message, bool) // stream pane, cursor row with MemoryID, memory-kind non-task channel, not a tag pane
func (m *Model) restoreMemory() tea.Cmd
func (m *Model) clearIncorrect() tea.Cmd
func (m *Model) updateConfirmMemory(msg tea.Msg) tea.Cmd
func (m Model) confirmMemoryLine() string
```

- `memLabel` joins with ` · `: `rev` when non-empty, then `served N`, `used N`, then `incorrect by <IncorrectBy> <stamp(IncorrectAt)>` when flagged, then `pruned (<reason>)` when pruned; with `ok == false` it returns `rev` unchanged. `renderStream` passes it to `header` in place of the bare revision label (the version-browsing label `rN · M kept` goes through it too).
- Stats refresh: `loadMemStats(ch)` is issued when a `historyMsg` for `ch` succeeds, on every `statusTickMsg` for `m.selName()` (the 5 s tick; counts and prune state change with no new message), and after each of the three actions. A failed load shows `memory stats: <error>` as a toast and keeps the old values.
- A pruned row renders its header and text in the dim style (text color when selected, like `rowDim`).
- Keys, normal mode, only when `cursorMemory()` holds; otherwise they do nothing:
  - `R`: `RestoreMemory`; on an unpruned memory, toast `memory is not pruned`; success hint `restored memory #<id>`.
  - `f`: `ClearMemoryIncorrect`; unflagged, toast `memory is not flagged incorrect`; success hint `cleared the incorrect flag on memory #<id>`.
  - `x`: `m.mode = modeConfirmMemory`; the compose row shows, in the error style, `delete memory #<id> "<subject or first line, cut to 40 columns>" PERMANENTLY? no undo · y yes  any other key no`. `y` calls `DeleteMemoryAny`, removes every row of that memory from `m.msgs[ch]`, refreshes the stream, hint `deleted memory #<id>`; any other key cancels. Errors show as `delete: <error>` toasts.
- Help rows after the `, / .` row: `{"R", "memory: restore the pruned cursor memory (agents see it again; its noise grace restarts)"}`, `{"f", "memory: clear the cursor memory's incorrect flag"}`, `{"x", "memory: delete the cursor memory, pruned or not (asks first)"}`.
- Search overlay: a hit with `Pruned` renders its whole line dim with ` pruned` appended; a hit with `Incorrect` gets ` incorrect` in the warn style.

- [ ] **Step 1: Write the failing tests** in `memories_test.go` (fixture `onMemory`; `f.ab` is Sam's bus; `rawDB(t, f.c.cfg)` shapes rows).

```go
// TestMemoryHeaderShowsCounts
//   f.streamHas(t, "served 0 · used 0")
//   raw: served_count=12, used_count=2; f.run(f.m.loadMemStats("dev-notes")) -> "r3 · served 12 · used 2"
//   raw: served_count=13; f.send(statusTickMsg{}) with no explicit loadMemStats -> "served 13 · used 2"
// TestMemoryHeaderShowsIncorrect
//   f.ab.MemoryFeedback(f.sam, {id, "INCORRECT"}); reload stats -> "incorrect by Sam"
// TestPrunedMemoryShownDimmedAndLabeled
//   raw prune (noise); reload history and stats -> "pruned (noise)"; with a color profile forced
//   (as the styled tests in this file do) the row's text line carries the dim color, an unpruned row's does not
// TestRestoreKey
//   pruned: f.key("R") -> f.ab.GetMemory(f.sam, id) succeeds; the stream no longer has "pruned"
//   unpruned: f.key("R") -> toast "memory is not pruned"; stats unchanged
// TestClearFlagKey
//   flagged: f.key("f") -> raw incorrect_at IS NULL; the stream no longer has "incorrect"
// TestDeleteMemoryKeyConfirms
//   f.key("x") -> f.m.mode == modeConfirmMemory && strings.Contains(f.m.View(), "PERMANENTLY")
//   f.key("n") -> modeNormal; GetMemory succeeds
//   f.key("x"); f.key("y") -> GetMemory is not_found; no row of that memory in f.m.rows("dev-notes")
//   the same on a pruned memory: its message row has tombstone=1
// TestMemoryKeysIgnoredOffMemoryRows
//   on a chat row in "dev" and with no cursor: x, R, f leave f.m.mode == modeNormal and change nothing
```

In `search_test.go`: `TestSearchOverlayMarksPrunedAndIncorrect` (one pruned, one flagged memory: the overlay view has ` pruned` on one hit line and ` incorrect` on the other). In `help_test.go`: `TestHelpListsMemoryActions` (the help lines contain the three rows' keys and texts).

- [ ] **Step 2: Run** `go test ./internal/tui/ -run 'TestMemoryHeader|TestPrunedMemoryShown|TestRestoreKey|TestClearFlagKey|TestDeleteMemoryKey|TestMemoryKeysIgnored|TestSearchOverlayMarks|TestHelpListsMemory'`. Expected: FAIL (undefined names).

- [ ] **Step 3: Implement** the message, fields, `loadMemStats` and its three triggers, `memLabel`, the dim rendering.

- [ ] **Step 4: Implement** `cursorMemory`, the three keys in `updateNormal`, `modeConfirmMemory` with its dispatch at `model.go:361` and compose-row render at `view.go:985`, the help rows, and the search overlay marks.

- [ ] **Step 5: Run** `go vet ./... && go test ./internal/tui/ && make fmt-check lint`. Expected: PASS. An existing test that pins a memory row's header text changes only by the added ` · served N · used N` label: update its expected string, never drop the label to keep it passing.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/msgs.go internal/tui/memories.go internal/tui/model.go internal/tui/view.go internal/tui/search.go internal/tui/help.go internal/tui/memories_test.go internal/tui/search_test.go internal/tui/help_test.go
git commit -m "feat(tui): show memory counts, flag and pruned state; restore, unflag and delete (#53)"
```

---

### Task 9: CLI export

**Files:**
- Modify: `internal/bus/hygiene.go` (`ExportMemories`)
- Create: `internal/cli/exportcmd.go`, `internal/cli/exportcmd_test.go`
- Modify: `main.go:34` (`usage`), new `case "export-memories"` beside `case "statuses"` (`:382`)
- Test: `internal/bus/hygiene_test.go`, `internal/cli/exportcmd_test.go`, `main_test.go`

**Interfaces:**
- Consumes: `MemoryStats`, `memoryStatsCols` (Task 1).
- Produces:

```go
// internal/bus/hygiene.go
type MemoryExport struct {
	Channel   string   `json:"channel"`
	Revision  int64    `json:"revision"`
	Sender    string   `json:"sender"`
	CreatedAt int64    `json:"created_at"`
	Subject   string   `json:"subject"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"` // never nil: [] when the memory has none
	MemoryStats
}
// ExportMemories returns every live memory that has a memory_access row (so
// no task rows), ordered by memory id, pruned ones included; prunedOnly
// keeps only pruned ones. Read-only: no auth, no counter or timestamp
// changes (like Board, which agentbus statuses calls).
func (b *Bus) ExportMemories(prunedOnly bool) ([]MemoryExport, error)

// internal/cli/exportcmd.go
// ExportMemories is `agentbus export-memories`: one JSON object per line.
func ExportMemories(cfg config.Config, prunedOnly bool, out io.Writer) error
```

- Command: `agentbus export-memories [-config <path>] [-pruned]`; flags only (`parseFlags`); exit 2 on a flag or config error, as `statuses` does. Add `export-memories` to the `usage` string after `statuses`.

- [ ] **Step 1: Write the failing tests.**

```go
// internal/bus/hygiene_test.go
// TestExportMemories
//   live a (tags kind:gotcha, subject "S"), live b (pruned noise, edited once), deleted c, a task, a chat message
//   ExportMemories(false): ids [a, b]; a.Tags == ["kind:gotcha"], a.Subject == "S", b.Revision == 2, b.Tags is [] not nil,
//     *b.PrunedReason == "noise"; ExportMemories(true): ids [b]
//   stats(a) and stats(b) are identical before and after both calls

// internal/cli/exportcmd_test.go (statusEnv(t) for the config; a peer bus seeds two memories)
// TestExportMemoriesWritesOneObjectPerLine
//   two lines; each decodes into map[string]any holding all 19 keys: channel, revision, sender, created_at,
//   subject, content, tags, memory_id, accessed_at, served_count, used_count, used_at, grace_from,
//   grace_served_base, incorrect_at, incorrect_by, incorrect_served_base, pruned_at, pruned_reason
//   "used_at" is present with value nil on a never-used memory
// TestExportMemoriesPrunedOnly
//   one of the two pruned through a raw connection: -pruned output is that one line

// main_test.go
// TestExportMemoriesRejectsPositional
//   execBin(t, "export-memories", "extra").code == 2
```

- [ ] **Step 2: Run** `go test ./internal/bus/ ./internal/cli/ . -run 'TestExportMemories'`. Expected: FAIL (undefined).

- [ ] **Step 3: Implement** `(*Bus).ExportMemories` (one query joining `messages m` and `memory_access a` with `m.tombstone=0`, plus the tags subquery `messageCols` uses), `cli.ExportMemories` (open the bus as `Statuses` does at `internal/cli/statuscmd.go:83`, `json.NewEncoder(out).Encode` per memory), and the `main.go` case and usage.

- [ ] **Step 4: Run** `go vet ./... && go test ./internal/bus/ ./internal/cli/ . && make fmt-check lint`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/hygiene.go internal/bus/hygiene_test.go internal/cli/exportcmd.go internal/cli/exportcmd_test.go main.go main_test.go
git commit -m "feat(cli): agentbus export-memories writes memories with their counters as JSON lines (#53)"
```

---

### Task 10: Agent guidance, docs, ADR, goldens, full gate

**Files:**
- Modify: `internal/mcpserver/server.go:386` (`register`), `:522` (`search`), `:541` (`get_memory`), `:551` (`edit_memory`) descriptions
- Modify: `internal/cli/identity.go:185-239` (`protocol`)
- Modify: `internal/cli/skills/using-agentbus/SKILL.md` (`:105-107` expiry sentence, new "Memory feedback" section, the mistakes table at `:444`)
- Modify: `README.md:52-56`, `:172`, the "From the shell" list
- Modify: `docs/install.md:344-351` (config keys), the "Operating" TUI key reference (`:650-656`) and command list
- Create: `docs/adr/0033-memory-hygiene.md`; modify `docs/adr/0013-per-effort-task-channels-and-expiry.md` (pointer line)
- Modify: `internal/cli/testdata/install_golden/*.txt` (regenerated)
- Test: `internal/cli/agent_text_test.go`, `internal/cli/cli_test.go:122` (`TestIdentityLineIsPrescriptive`), `internal/mcpserver/agent_text_test.go`

**Interfaces:**
- Consumes: every earlier task's names (`memory_feedback`, `incorrect`, the four config keys, `export-memories`, the TUI keys `R`, `f`, `x`).
- Produces: no code interface. Exact agent-facing copy:
  - `protocol`, a new bullet directly after the "Search the memory channels" bullet:
    ```
    - After relying on a memory, report it with memory_feedback (verdict USED).
      When a memory is wrong, correct it with edit_memory; flag it INCORRECT
      only when you do not know the right answer. Send one memory_feedback call
      for all the memories a task used.
    ```
  - `register` description, appended: ` After relying on a memory call memory_feedback with USED; correct a wrong memory with edit_memory, or flag it INCORRECT when you do not know the right answer.`
  - `search` description, appended: ` A hit on a memory flagged INCORRECT carries incorrect=true and ranks after every unflagged hit; verify it before relying on it. Memories retired by maintenance (served often but never reported USED, or flagged INCORRECT and left uncorrected) do not appear. Report the memories you relied on with memory_feedback.`
  - `get_memory` description, appended: ` If you rely on it, report USED with memory_feedback; if it is wrong, correct it with edit_memory or flag it INCORRECT. A memory flagged INCORRECT comes back with incorrect=true.`
  - `edit_memory` description, appended: ` Editing clears an INCORRECT flag.`

- [ ] **Step 1: Write the failing text tests.**

```go
// internal/cli/agent_text_test.go
// TestAgentTextMemoryFeedback
//   normWS(protocol) contains "After relying on a memory, report it with memory_feedback (verdict USED)."
//     and "flag it INCORRECT only when you do not know the right answer"
//   normWS(string(skillMD)) contains "## Memory feedback", "memory_feedback", "`USED`", "`INCORRECT`",
//     "`edit_memory`", "memory_noise_serves", "memory_incorrect_prune_hours"
// internal/cli/cli_test.go, TestIdentityLineIsPrescriptive: add the want
//   "- After relying on a memory, report it with memory_feedback (verdict USED)."
// internal/mcpserver/agent_text_test.go
// TestMemoryToolDescriptionsExplainFeedback
//   search mentions "incorrect=true" and "memory_feedback"; get_memory mentions "incorrect=true"; get_memory and register mention "memory_feedback";
//   edit_memory mentions "clears an INCORRECT flag"; no description contains "served_count" or "used_count"
```

- [ ] **Step 2: Run** `go test ./internal/cli/ ./internal/mcpserver/ -run 'TestAgentTextMemoryFeedback|TestIdentityLineIsPrescriptive|TestMemoryToolDescriptions'`. Expected: FAIL.

- [ ] **Step 3: Edit the agent-facing text.** Apply the copy above. In `SKILL.md`: replace the sentence at `:105-107` with one stating that a memory not touched by `get_memory`, a `search` hit or a `USED` report for `memory_expiry_hours` is deleted, and that a memory served `memory_noise_serves` times with no `USED` after `memory_noise_grace_hours`, or flagged `INCORRECT` and left uncorrected for `memory_incorrect_prune_hours` while still being served, stops being returned to agents. Add a `## Memory feedback` section: when to send `USED`; correct with `edit_memory` first, `INCORRECT` only when the right answer is unknown; one batched call (1 to 50 pairs, all or nothing); what `incorrect: true` on a hit means; `USED` from the author counts. Add a mistakes-table row: relying on a memory and not reporting it -> send `memory_feedback` `USED` when the task is done. Keep every existing guarded phrase (run the whole `internal/cli` package).

- [ ] **Step 4: Run** the Step 2 command, then `go test ./internal/cli/ ./internal/mcpserver/`. Expected: only `TestInstallGolden` FAILS (skill and prompt hashes changed).

- [ ] **Step 5: Regenerate the goldens.**

```bash
go test ./internal/cli -run TestInstallGolden -update-golden
go test ./internal/cli -run TestInstallGolden
git --no-pager diff --stat internal/cli/testdata/install_golden
```

Expected: second command PASS; the diff touches only the hash columns of `SKILL.md`, `.codex/prompts/agentbus.md` and `.grok/commands/agentbus.md` lines.

- [ ] **Step 6: Human docs.** `README.md`: the "Harness-independent memory" paragraph gains two sentences (agents report `USED` or `INCORRECT` with `memory_feedback`; memories that are only noise, or wrong and uncorrected, are hidden from agents and kept for the human, who can restore or delete them in the TUI); `:172` lists `memory_feedback` with the memory tools; "From the shell" gains `agentbus export-memories [-pruned]`. `docs/install.md`: a paragraph after `:351` documenting the four keys with defaults, ranges, the two rules and "either key at 0 disables the rule"; the TUI key reference gains the header label (`served N · used N`, `incorrect by <who> <when>`, `pruned (<reason>)`, dimmed rows) and the keys `R`, `f`, `x`; the command list gains `export-memories` with its field list. Check each sentence against the code from Tasks 1 to 9, not against this plan.

- [ ] **Step 7: ADR.** Write `docs/adr/0033-memory-hygiene.md` in the form of `docs/adr/0032-embed-every-kind.md`: status line `Accepted (Eric, 2026-11-27). Human decision, recorded by Claude.`; context (ADR 0013 items 6 and 7, the gap); a "Decision (Eric, 2026-11-27)" section quoting human decisions 1 to 6 from the spec verbatim, then the approved proposals 7 to 14 labeled as proposals Eric approved, then spec items 15 to 17 under their own heading labeled as the orchestrator's calls during planning, not human decisions; consequences (schema 15, all sessions restart; pruned memories still hard-deleted by the 1-year rule; agents cannot see or revive a pruned memory). State that it amends ADR 0013 items 6 and 7. Add `Amended by ADR 0033 (items 6 and 7: serving, feedback, soft pruning).` to ADR 0013, placed and worded like the "Superseded in part" line in ADR 0030. Search the new and changed docs for private hostnames, IPs and account names before committing: `rg -n '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|\.local\b|@' docs/adr/0033-memory-hygiene.md`.

- [ ] **Step 8: Full gate.** Run `make verify`. Expected: ends with `verify: OK`. Paste the tail of its output in the task report.

- [ ] **Step 9: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/agent_text_test.go internal/cli/identity.go internal/cli/agent_text_test.go internal/cli/cli_test.go internal/cli/skills/using-agentbus/SKILL.md internal/cli/testdata/install_golden README.md docs/install.md docs/adr/0033-memory-hygiene.md docs/adr/0013-per-effort-task-channels-and-expiry.md
git commit -m "docs(memory): feedback guidance for agents, hygiene docs, ADR 0033, goldens (#53)"
```

---

## Spec gaps found while planning

Decided by the orchestrator on 2026-11-27 and recorded in the spec as "orchestrator's call during planning" (items 15 to 17); none is a human decision.

Resolved, and in the tasks above:

- Gap 3: the noise rule counts serves since the grace start (`grace_served_base`, spec item 15; Tasks 1, 6 and 7).
- Gap 5: `get_memory` returns `incorrect: true` on a flagged memory (spec item 16; Tasks 5 and 10).

Accepted, follow-up issue (no task changes them):

- Gap 1: `list_channels` reports `messages` as a count of live rows (`internal/bus/admin.go:72`), so a pruned memory still counts while a deleted one does not.
- Gap 2: `send` with `reply_to` set to a pruned memory's seq succeeds for as long as the row exists (`validateSendRefs` only checks the row exists), while a deleted memory's seq stops working once the tombstone purge removes it after `tombstone_min_hours`.
- Gap 4: `memory_feedback`, restore and clear-flag do not run the inspection hook; only the TUI delete does.
- Gap 6: text search ranks only the best 1000 matches (`searchPageMax`) before the flagged-last reorder, so the reorder applies inside that ceiling.
