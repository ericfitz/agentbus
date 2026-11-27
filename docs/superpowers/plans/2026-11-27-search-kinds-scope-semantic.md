# Search kinds, channel scope, semantic over every kind (#52) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `search` can narrow by kind (memories, chat, tasks) and scope to one channel; task rows match only on subject and description; every kind is embedded and ranked by semantic search; the TUI gets `in:` and `channel:` tokens.

**Architecture:** One kind predicate in `searchFilters` (internal/bus/search.go) shared by text, tags-only and semantic search. Schema 13 -> 14 rebuilds the FTS index so task rows index `subject` + JSON `description`. The embed backlog and semantic candidates drop the memory-only restriction; task revisions with unchanged text reuse the previous vector.

**Tech Stack:** Go, modernc.org/sqlite (FTS5, JSON1), Bubble Tea TUI, MCP go-sdk.

**Spec:** docs/superpowers/specs/2026-11-27-search-kinds-scope-semantic-design.md (read it; ADR 0030 and ADR 0032 hold the human decisions).

## Global Constraints

- Kind values exactly `memories`, `chat`, `tasks`; no kinds = all kinds; unknown values are a `validation` error naming every bad value.
- `channel` set => `kinds` ignored silently (never an error).
- Every kind embedded whenever an embedder is configured; no new config key.
- Hit shape unchanged (task hit: `Message` with task JSON `content`, task id in `memory_id`).
- DM visibility rule in `searchFilters` unchanged for every mode and kind.
- Fallback unchanged: no embedder or endpoint failure => text results + `semantic_unavailable=true`.
- Migration must not fail on malformed task JSON (`json_valid` guard).
- Done gate: `make verify` (repo CLAUDE.md). American English. Work lands on `main`.

## Review Focus

1. A task whose `content` is not valid JSON (old or corrupted row): FTS insert/delete triggers and the migration must not error; it indexes subject plus empty description. Test in Task 2.
2. `kinds` with duplicates or mixed case (`["Chat","chat"]`): mixed case is rejected as unknown (values are case-sensitive), duplicates are fine. Test in Task 1.
3. Semantic search by another agent never returns a DM between two others, now that DMs are embedded. Test in Task 3.
4. A task deleted (tombstoned without a new revision) disappears from semantic results and its vector is not copied forward. Test in Task 3.
5. TUI `channel:` naming a DM inbox whose session is not in the sessions pane: rejected with an error, not a hit that cannot open. Test in Task 5.

---

### Task 1: Kinds filter and channel scope in the bus

**Files:**
- Modify: `internal/bus/search.go` (SearchInput, Search, searchFilters)
- Test: `internal/bus/search_test.go`

**Interfaces:**
- Produces: `SearchInput.Kinds []string` (`json:"kinds,omitempty"`, jsonschema `"narrow to these kinds: memories, chat, tasks; default all; ignored when channel is set"`); exported `bus.SearchKinds = []string{"memories","chat","tasks"}` for the TUI parser.

- [ ] **Step 1: Write failing tests** in search_test.go (fixture: memory channel `mem`, ordinary `general`, task list `tasks/w` with one task, DM `dm/Sam` from Ann; every row contains the word `widget`; task description contains `widget`):
  - `TestSearchKindsDefaultAll`: no kinds => hits from `mem`, `general`, `tasks/w`, `dm/Sam` (as Sam).
  - `TestSearchKindsChat`: `Kinds:["chat"]` => only `general` and `dm/Sam` (as Sam); as Bob => only `general`.
  - `TestSearchKindsTasks` / `TestSearchKindsUnion`: `["tasks"]` => only `tasks/w`; `["memories","tasks"]` => `mem` and `tasks/w`.
  - `TestSearchKindsUnknown`: `["chats","x","Chat"]` => validation error whose message contains `"chats"`, `"x"` and `"Chat"`; `["chat","chat"]` => no error.
  - `TestSearchChannelOverridesKinds`: `Channel:"tasks/w", Kinds:["chat"]` => the task hit; `Channel:"mem", Kinds:["chat"]` => the memory hit.
  - `TestSearchKindsWithFilters`: `Kinds:["chat"]` with tags-only (no query, tag set on the general row), and with sender/since filters, returns only matching chat rows.
- [ ] **Step 2: Run** `go test ./internal/bus -run 'TestSearchKinds|TestSearchChannelOverrides'` — expect FAIL (no Kinds field).
- [ ] **Step 3: Implement.** In `Search`, after tag normalization: validate kinds (collect all unknowns, error `unknown kind %q[, %q]; kinds are memories, chat, tasks`), dedupe, clear when `Channel != ""`. In `searchFilters`, when kinds is a non-empty proper subset of the three, append `AND (p1 OR p2 ...)` with the predicates from the spec (tasks: channel prefix; memories: `m.channel IN (SELECT name FROM channels WHERE kind='memory') AND NOT tasks`; chat: `m.channel NOT IN (SELECT name FROM channels WHERE kind='memory')`).
- [ ] **Step 4: Run** `go test ./internal/bus` — PASS.
- [ ] **Step 5: Commit** `feat(bus): search kinds filter; channel scope ignores kinds (#52)`.

### Task 2: Index task rows by subject and description (schema 13 -> 14)

**Files:**
- Modify: `internal/bus/schema.go` (new FTS DDL constant; `schemaVersion = 14`; fresh schema uses the new DDL)
- Modify: `internal/bus/migrate.go` (`13: reindexTaskText` step; `addSubject`/`foldRefs` keep a frozen copy of the v13 DDL)
- Modify: `internal/bus/admin.go:168` only if the chosen FTS form makes `'rebuild'` invalid
- Test: `internal/bus/migrate_test.go`, `internal/bus/search_test.go`

**Interfaces:**
- Produces: `messagesFTSDDLv6` (frozen, used by `addSubject`), `messagesFTSDDL` (current); a single SQL expression `ftsContentExpr(alias)` used by both triggers and the repopulation, yielding `json_extract(content,'$.description')` (or `''` when absent or `json_valid` is false) for task-channel rows and `content` otherwise.

- [ ] **Step 1: Write failing tests:**
  - `TestSearchTaskMatchesDescriptionNotJSON` (search_test.go): task subject `Fix rotation`, description `log the retry`; query `status` => 0 hits; `pending` => 0; `retry` => the task; `rotation` => the task. Still finds after a `TaskUpdate` changing status (the new revision is indexed the same way).
  - `TestSearchTaskInvalidJSONRow` (Review Focus 1): insert a task-channel row with content `not json` directly; insert succeeds, query on its subject finds it, delete via purge does not error.
  - `TestMigrate13To14` (migrate_test.go, pattern of `TestMigrate12To13`): open, create a task, then recreate the v13 FTS (frozen DDL + `'rebuild'`) and set `user_version = 13`; reopen twice; `user_version` 14; `status` no longer matches; description word matches; second open is a no-op.
  - `TestMigrate13To14Leftovers`: a v13 file whose FTS table is already the new form (died before stamping) migrates cleanly with the same results.
- [ ] **Step 2: Run** `go test ./internal/bus -run 'TestSearchTask|TestMigrate13To14'` — FAIL.
- [ ] **Step 3: Implement.** Builder picks explicit repopulation (`INSERT INTO messages_fts(rowid,subject,content) SELECT seq, subject, <expr> FROM messages`) or a contentless table; the delete trigger must pass exactly the inserted values. The step drops triggers and table, recreates, repopulates unconditionally.
- [ ] **Step 4: Run** `go test ./internal/bus` — PASS.
- [ ] **Step 5: Commit** `feat(bus): index task rows by subject and description, schema 14 (#52)`.

### Task 3: Embed and semantically search every kind

**Files:**
- Modify: `internal/bus/embed.go` (`unembeddedFrom`, row text selection, `embedBatch` guards)
- Modify: `internal/bus/search.go` (`semanticSearch` candidate WHERE)
- Modify: `internal/bus/messages.go:415` (embedSoon for every kind), `internal/bus/tasks.go` (TaskCreate), `internal/bus/tasks_update.go` (writeTaskRevision vector reuse; embedSoon after each commit), `internal/bus/tasks_reclaim.go` (embedSoon after commit)
- Modify: `internal/mcpserver/server.go` search handler (touch only memory-channel hits)
- Test: `internal/bus/embed_test.go`, `internal/bus/search_test.go`

**Interfaces:**
- Consumes: Task 1 `Kinds`; Task 2 indexing (independent).
- Produces: `taskEmbedText(subject, content string) string` = `embedText(subject, description-from-JSON)`.

- [ ] **Step 1: Write failing tests** (use `fakeEmbeddings`, embed_test.go:24):
  - `TestEmbedBacklogCoversAllKinds`: chat, DM and task rows get embeddings after one pass; `StatusReport` backlog counts them before and is 0 after; the text sent for the task is `subject + "\n\n" + description` (assert on the fake server's received input), never JSON.
  - `TestSemanticSearchFindsChatAndTasks`: mode `semantic` returns a chat hit and a task hit; `Kinds:["tasks"]` returns only the task.
  - `TestSemanticSearchDMVisibility` (Review Focus 3): DM Ann->Sam embedded; Bob's semantic and both searches never return it.
  - `TestTaskStatusChangeReusesEmbedding`: after embedding, `TaskUpdate` status only => new seq has an embedding immediately, fake server call count unchanged; description change => new seq has none until the next pass, which calls the server.
  - `TestDeletedTaskLeavesSemantic` (Review Focus 4): delete a task => no semantic hit; no live row carries its vector.
  - `TestSendChatTriggersEmbedSoon`: a chat `Send` and a `TaskCreate` start a pass (row embedded without calling the maintenance tick; poll with a bounded deadline).
  - `TestSemanticFallbackForChatKind`: embedder failing, `Kinds:["chat"]`, mode `both` => text hits and `SemanticUnavailable`.
- [ ] **Step 2: Run** `go test ./internal/bus -run 'TestEmbed|TestSemantic|TestTaskStatusChange|TestDeletedTask|TestSendChat'` — FAIL.
- [ ] **Step 3: Implement.** `unembeddedFrom`: live rows, keep `embed_failures` exclusion, select channel too so `embedBatch` picks `taskEmbedText` for task channels (`IsTaskChannel`). Insert guards: `tombstone=0` only. `semanticSearch`: drop `m.memory_id IS NOT NULL`. `writeTaskRevision`: before tombstoning, read the old row's subject/content; if subject and description are equal to the new ones, `INSERT INTO embeddings(seq,model,vector) SELECT ?,model,vector FROM embeddings WHERE seq=?` (old seq). Call `embedSoon()` after every commit that writes a message or task revision. MCP handler: skip hits whose channel satisfies `bus.IsTaskChannel` when collecting ids for `TouchMemoryAccess` (chat hits have no `MemoryID` already); test `TestSearchTaskHitNotTouched` asserts a task hit leaves `memory_access` unchanged and a memory hit updates it.
- [ ] **Step 4: Run** `go test ./internal/bus ./internal/mcpserver` — PASS.
- [ ] **Step 5: Commit** `feat(bus): embed and semantically search chat, DMs and tasks (#52, ADR 0032)`.

### Task 4: MCP tool surface

**Files:**
- Modify: `internal/mcpserver/server.go:522` (search description)
- Test: `internal/mcpserver/server_test.go`

- [ ] **Step 1: Write failing test** `TestSearchToolKinds`: through the MCP client, `kinds:["tasks"]` returns only task hits; `kinds:["bogus"]` returns a tool error containing `bogus`; the tool's input schema lists a `kinds` property; the description contains `kinds` and no longer contains `ranks memories by meaning`.
- [ ] **Step 2: Run** `go test ./internal/mcpserver -run TestSearchToolKinds` — FAIL (description).
- [ ] **Step 3: Implement** the description: semantic ranks messages, memories and tasks by meaning; `kinds` (memories, chat, tasks; default all; ignored when `channel` is set); `channel` searches one channel of any kind.
- [ ] **Step 4: Run** `go test ./internal/mcpserver` — PASS.
- [ ] **Step 5: Commit** `feat(mcp): document search kinds and channel scope (#52)`.

### Task 5: TUI `in:` and `channel:` tokens, scope title, task hits

**Files:**
- Modify: `internal/tui/searchparse.go` (table rows, `searchSpec.Kinds`, `searchSpec.Channel`)
- Modify: `internal/tui/search.go` (pass Kinds/Channel; validate channel; title; task-hit open)
- Test: `internal/tui/searchparse_test.go`, `internal/tui/search_test.go` (or the existing overlay test file)

**Interfaces:**
- Consumes: `bus.SearchKinds`, `SearchInput.Kinds`, `SearchInput.Channel`.

- [ ] **Step 1: Write failing tests:**
  - Parser table rows in `TestParseSearchInput`: `in:chat foo` => Kinds `[chat]`, Query `foo`; `IN:Tasks in:chat` => `[tasks chat]` (values lowercased); `in:` => error mentioning `in:`; `in:chats` => error naming `chats`; `channel:general x` => Channel `general`; `channel:` => error; `channel:a channel:b` => error; `"in:chat"` => literal text; mixed with `tag:deployment`.
  - Overlay: `channel:nope` => error shown, no search run; `channel:dm/<name-not-in-sessions>` => error (Review Focus 5); `channel:` naming a rail channel runs with `SearchInput.Channel` set.
  - Title: `in:chat in:tasks` => title contains `in chat, tasks`; `channel:general` => `in general`; default scope => neither.
  - Task hit: enter on a task hit selects the task list and the task tree cursor is on that task id (`taskIndex`), not a message row.
- [ ] **Step 2: Run** `go test ./internal/tui -run 'TestParseSearchInput|TestSearch'` — FAIL.
- [ ] **Step 3: Implement.** Validation set = rail `m.channels` names plus `bus.DMChannel(n)` for `n` in `sessionNames()`. In `jumpTo`, when `bus.IsTaskChannel(hit.Channel)` and `hit.MemoryID != nil`: after `selectChannel(i)`, call `m.revealTask(hit.Channel, *hit.MemoryID)` and skip `placeCursor`/history load.
- [ ] **Step 4: Run** `go test ./internal/tui` — PASS.
- [ ] **Step 5: Commit** `feat(tui): in: and channel: search tokens; task hits open in the tree (#52)`.

### Task 6: Docs and done gate

**Files:**
- Modify: `README.md` (~line 158 search bullet; semantic config section ~133: chat and DM text goes to the endpoint)
- Modify: `internal/cli/skills/using-agentbus/SKILL.md` (search section: kinds, channel scope, semantic covers chat/tasks)
- Modify: `docs/adr/0030-search-default-kinds-and-semantic-scope.md` Consequences: add "Superseded in part by ADR 0032: chat, DMs and tasks are now embedded."
- Comment on issue #54 (or the release-notes draft it tracks) with the next-release line: semantic search now covers chat, DMs and tasks; their text is sent to the configured embedding endpoint.

- [ ] **Step 1:** Update the docs; every claim checked against the code from Tasks 1-5.
- [ ] **Step 2: Run** `make verify` — all targets pass; paste the summary.
- [ ] **Step 3: Commit** `docs: search kinds, channel scope, semantic over every kind (#52)`.
