# Message Subjects Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every message can carry an optional one-line `subject`; agents send, read, search and edit it, and the TUI shows it (or the content's first line) as a collapsed row that `→` opens.

**Architecture:** Schema v6 adds `messages.subject` and rebuilds `messages_fts` over `(subject, content)`; the migration backfills task rows from their JSON document. `bus.Send` / `bus.EditMemory` normalize the field and `insertMessage` stores it, so tasks (which already have `subject`) write it too. `messageCols` selects it, so every read path returns it with one change. The TUI collapses every message row to a header plus one line and layers the existing `→`/`←` keys: body first, then replies.

**Tech Stack:** Go, SQLite (modernc, FTS5), Bubble Tea v1 / Lip Gloss v1, `charmbracelet/x/ansi`.

**Spec:** `docs/superpowers/specs/2026-09-24-message-subjects-design.md` (approved 2026-09-24). Human decisions: `docs/adr/0010-message-subjects.md`. Do not change any of those decisions.

## Global Constraints

- American spelling ("color").
- Field name is `subject` everywhere (ADR 0010 decision 1). Optional; no backfill for non-task rows (decision 2). Stored in `messages.subject`, schema v6 (decision 3). No TUI compose input for it (decision 6).
- Subject rules (spec "Validation"): trim surrounding whitespace, empty means no subject; a `\n` or `\r` is an error; more than 200 runes is an error. The error code is `validation`, the code the tag checks use (human decision 2026-09-24) and its message contains the word `subject`.
- A refused subject writes nothing (spec "Errors").
- `task_create` / `task_update` keep their API and their own 1-256 byte subject rule; `normalizeSubject` never runs on task subjects.
- Embeddings embed `subject + "\n\n" + content` when there is a subject, else `content`. Existing embeddings are not recomputed.
- TUI fixture rules: no `tea.Sequence` in production code, timers only via the package `tick` var, static cursors on every text input.
- Existing glyphs only: `▶` (`markSel`) when anything is hidden, `▼` (`markOpen`) when the body is open.
- Never run a dev build against `~/.local/share/agentbus`; use `AGENTBUS_DATA_DIR=~/.agentbus-dev`.
- No new dependencies. Use `rg`, not `grep`. macOS has no GNU `timeout`; if a command needs one, `perl -e 'alarm 120; exec @ARGV' go test ./...`.
- The gate, run at the end of every task: `gofmt -l . && golangci-lint run ./... && go build ./... && go test ./...` — all four clean (gofmt prints nothing). Then one commit per task on `feat/message-subjects`. Do not push, tag, or run `release/release.sh`.
- 1.8.0 ships schema v6 (Task 1). 1.7.x binaries refuse a migrated bus; the release notes (Task 6) say so.

## Review Focus

1. **A message with no subject whose first line is wider than the pane.** The row shows the line cut with `…`, carries `▶`, and `→` opens the full text (Task 5, `TestCutFirstLineOpensAsBody`).
2. **A subject that is only whitespace.** It is stored as no subject and the row shows the content's first line; nothing is refused (Task 2, `TestSubjectValidation`).
3. **An `edit_memory` that omits `subject` on a memory that has one, then one on a memory that has none.** The first keeps the subject; the second leaves it empty (Task 3, `TestEditMemorySubjectKeepChangeClear`).
4. **A task list holding a row whose content is not JSON.** The v6 backfill skips it instead of aborting the migration and leaving the bus unopenable (Task 1, `TestMigrateV5AddsSubject` has a `not json` row on the task channel).
5. **A v1 file (with the old `bytes` column) opened by 1.8.0.** It walks every step to v6, gets the column, and FTS still finds the old content (Task 1 keeps `TestMigrateV1DropsMessagesBytes` green with the v5-shaped FTS it now needs, and asserts the column).

---

### Task 1: Schema v6 — `messages.subject`, FTS over subject and content, task backfill

**Files:**
- Modify: `internal/bus/schema.go` (`schemaVersion`, messages DDL, shared FTS DDL)
- Modify: `internal/bus/migrate.go` (`migrations[5]`, `addSubject`)
- Test: `internal/bus/migrate_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: column `messages.subject TEXT NOT NULL DEFAULT ''`; `messages_fts(subject, content)` with triggers `messages_ai` / `messages_ad` carrying both; const `messagesFTSDDL` (schema.go). Task 2 relies on the column and on FTS indexing `subject`.

- [ ] **Step 1: Add the v5-shaped FTS helper and the failing v5→v6 test.** In `internal/bus/migrate_test.go`, add after `schemaV1Messages`:

```go
// schemaV5FTS is messages_fts and its two triggers as shipped through
// v1.7.0 (content only). Schema version 6 rebuilds them over subject and
// content, so a test shaping an older file replaces the current ones with
// these first.
const schemaV5FTS = `
DROP TRIGGER IF EXISTS messages_ai;
DROP TRIGGER IF EXISTS messages_ad;
DROP TABLE IF EXISTS messages_fts;
CREATE VIRTUAL TABLE messages_fts USING fts5(content, content='messages', content_rowid='seq');
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.seq, new.content);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.seq, old.content);
END;`
```

And at the end of the file:

```go
// TestMigrateV5AddsSubject (ADR 0010): a v5 file gains messages.subject,
// task rows are backfilled from their JSON document (a non-JSON row on a
// task channel is skipped, not fatal), messages_fts is rebuilt over subject
// and content so it still finds old content and now finds subjects, and
// user_version is 6.
func TestMigrateV5AddsSubject(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Shape a v5 database: content-only FTS and triggers, no subject
	// column, one task row, one malformed task-channel row, one chat row.
	for _, s := range []string{
		schemaV5FTS,
		"ALTER TABLE messages DROP COLUMN subject",
		"INSERT INTO channels(name,kind,created_seq) VALUES('tasks/repo','memory',0)",
		`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision) VALUES('tasks/repo','sam','r',1,'','{"subject":"Ship v6","status":"pending","rank":"V"}',1,1)`,
		"INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision) VALUES('tasks/repo','sam','r',2,'','not json',2,1)",
		"INSERT INTO messages(channel,sender,context,created_at,type,content) VALUES('general','sam','r',3,'','hello world')",
		"PRAGMA user_version = 5",
	} {
		if _, err := b.db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v5 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	var subjects []string
	rows, err := b.db.Query("SELECT subject FROM messages ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		subjects = append(subjects, s)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(subjects, "|") != "Ship v6||" {
		t.Fatalf("backfilled subjects %q, want task row only", subjects)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'hello'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts lost old content after rebuild: %d %v", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'subject:ship'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts must index the backfilled subject: %d %v", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('messages_ai','messages_ad','messages_fts')").Scan(&n); err != nil || n != 3 {
		t.Fatalf("fts objects after migration: %d %v", n, err)
	}
	// The recreated insert trigger carries both columns: a new row with a
	// subject is found by a word that appears only there. Task 2 wires
	// SendInput.Subject; here the row is written directly.
	if _, err := b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,type,subject,content) VALUES('general','sam','r',4,'','Zebra alert','nothing here')"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'zebra'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("insert trigger must index subject: %d %v", n, err)
	}
}
```

- [ ] **Step 2: Run it and confirm it fails.** `go test ./internal/bus -run TestMigrateV5AddsSubject -count=1` fails at `ALTER TABLE messages DROP COLUMN subject` ("no such column") because the column does not exist yet.

- [ ] **Step 3: Schema.** In `internal/bus/schema.go`:

```go
const schemaVersion = 6
```

Add after `tagSubscriptionTagsDDL`:

```go
// messagesFTSDDL is shared by schema.go (fresh databases) and migrate.go's
// addSubject step (v5 -> v6, ADR 0010): the full-text index over subject
// and content, external-content on messages, kept in step by two triggers.
// No update trigger: a message row is never rewritten after insert (an
// edit inserts a new revision row).
const messagesFTSDDL = `
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(subject, content, content='messages', content_rowid='seq');
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, subject, content) VALUES (new.seq, new.subject, new.content);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, subject, content) VALUES ('delete', old.seq, old.subject, old.content);
END;
`
```

In `schema`, the messages table gains a last column (last, so a fresh file and an `ALTER TABLE`d one have the same column order):

```sql
  tombstone INTEGER NOT NULL DEFAULT 0,
  tombstone_at INTEGER,
  subject TEXT NOT NULL DEFAULT ''
);
```

and the three FTS statements (`CREATE VIRTUAL TABLE ... messages_fts ...` through the end of `messages_ad`) are replaced by splitting the const: end the first string after `CREATE INDEX IF NOT EXISTS messages_created_at ON messages(created_at);`, then `` + messagesFTSDDL + ` ``, then continue with `CREATE TABLE IF NOT EXISTS embeddings (`.

- [ ] **Step 4: Migration.** In `internal/bus/migrate.go`, the map:

```go
var migrations = map[int]func(tx *sql.Tx) error{
	1: dropMessagesBytes,
	2: addTables,    // message_tags (ADR 0009)
	3: addTables,    // tag_subscriptions (ADR 0009)
	4: splitTagSets, // tag_subscription_tags (#13)
	5: addSubject,   // messages.subject, FTS over subject and content (ADR 0010)
}
```

Add after `splitTagSets`:

```go
// addSubject (schema 5 -> 6, ADR 0010) adds messages.subject, backfills it
// on task-list rows from the task document's subject (the same channel
// predicate as IsTaskChannel), and rebuilds messages_fts and its triggers
// over subject and content; the rebuild indexes the backfilled subjects.
// The column check keeps the step safe on a file that already has the
// column (a test shaping an older version from a fresh file). json_valid
// guards the backfill: json_extract raises on malformed text, and an error
// here would leave the bus unopenable.
func addSubject(tx *sql.Tx) error {
	var has int
	if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('messages') WHERE name='subject'").Scan(&has); err != nil {
		return err
	}
	if has == 0 {
		if _, err := tx.Exec("ALTER TABLE messages ADD COLUMN subject TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	for _, s := range []string{
		`UPDATE messages SET subject = COALESCE(json_extract(content, '$.subject'), '')
		  WHERE (channel = 'tasks' OR channel LIKE 'tasks/%') AND json_valid(content)`,
		"DROP TRIGGER IF EXISTS messages_ai",
		"DROP TRIGGER IF EXISTS messages_ad",
		"DROP TABLE IF EXISTS messages_fts",
		messagesFTSDDL,
		"INSERT INTO messages_fts(messages_fts) VALUES('rebuild')",
	} {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
```

(`tx.Exec` on the multi-statement `messagesFTSDDL` works with modernc sqlite; verified.)

- [ ] **Step 5: Keep the older migration tests honest.** In `TestMigrateV1DropsMessagesBytes`, the shared `schema` now creates v6 triggers that read `new.subject`, which the v1 table lacks; insert `schemaV5FTS` right after `schema` in the statement list:

```go
	for _, s := range []string{schemaV1Messages, schema, schemaV5FTS, "PRAGMA user_version = 1",
```

and add, after the `bytes` check in that test:

```go
	if !strings.Contains(cols, "subject") {
		t.Fatalf("subject column missing after the full chain: %s", cols)
	}
```

Rename `TestMigrateV2To5` to `TestMigrateV2ToLatest`, change its comment's first line to `// TestMigrateV2ToLatest (#13, ADR 0010): a database shaped like 1.6.0 -- user_version 2,`, and add at its end (after the receive check) a send with a subject that is found by search once Task 2 lands — for now assert the column:

```go
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM pragma_table_info('messages') WHERE name='subject'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("subject column after the chain: %d %v", n, err)
	}
```

(That test's fresh file already has `subject`, so the chain exercises `addSubject`'s column check.)

- [ ] **Step 6: Run the migration tests, then the gate.** `go test ./internal/bus -run 'TestMigrate|TestOpen|TestReset' -count=1` passes, then the full gate from Global Constraints. `Reset` (admin.go) still rebuilds FTS with `'rebuild'`, unchanged.

- [ ] **Step 7: Commit.**

```bash
git add internal/bus/schema.go internal/bus/migrate.go internal/bus/migrate_test.go
git commit -m "feat(bus): schema v6 adds messages.subject and rebuilds FTS over it

ALTER TABLE adds the column, task rows are backfilled from their JSON
document, and messages_fts and its triggers are recreated over
(subject, content) then rebuilt. ADR 0010."
```

---

### Task 2: `subject` on send, receive, history, search, memories; validation; embeddings

**Files:**
- Modify: `internal/bus/messages.go` (`SendInput`, `Message`, `messageCols`, `scanMessages`, `envelopeUpperBound`, `insertMessage`, `Send`, new `normalizeSubject`)
- Modify: `internal/bus/search.go:120` (`textSearch` row scan)
- Modify: `internal/bus/embed.go:195-210` (`embedBatch` query and texts), new `embedText`
- Test: `internal/bus/subject_test.go` (new), `internal/bus/embed_test.go`, `internal/bus/migrate_test.go` (one assertion)

**Interfaces:**
- Consumes: `messages.subject` column (Task 1).
- Produces: `SendInput.Subject string` (`json:"subject,omitempty"`), `Message.Subject string` (`json:"subject,omitempty"`), `func normalizeSubject(s string) (string, error)`, `const maxSubjectRunes = 200`, `func embedText(subject, content string) string`. `insertMessage` stores `in.Subject` verbatim; Task 3 passes task subjects through it.

- [ ] **Step 1: Write the failing tests.** Create `internal/bus/subject_test.go`:

```go
package bus

import (
	"encoding/json"
	"strings"
	"testing"
)

// A subject round-trips through send, receive, history, search, get_memory
// and memory_revisions; a reply does not inherit its parent's; the wire
// form carries it only when set (ADR 0010).
func TestSubjectRoundTrip(t *testing.T) {
	b, sam, kim := setupTwo(t)
	r, err := b.Send(sam, SendInput{Channel: "dev", Subject: "  Deploy plan  ", Content: "step one\nstep two"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(got.Messages) != 1 || got.Messages[0].Subject != "Deploy plan" {
		t.Fatalf("receive: %+v %v", got, err)
	}
	h, err := b.History(sam, "dev", nil, nil, 10)
	if err != nil || len(h) != 1 || h[0].Subject != "Deploy plan" {
		t.Fatalf("history: %+v %v", h, err)
	}
	s, err := b.Search(sam, SearchInput{Query: "step", Mode: "text"})
	if err != nil || len(s.Hits) != 1 || s.Hits[0].Subject != "Deploy plan" {
		t.Fatalf("search: %+v %v", s, err)
	}
	rep, err := b.Send(sam, SendInput{Channel: "dev", Content: "ack", ReplyTo: &r.Seq})
	if err != nil {
		t.Fatal(err)
	}
	h, _ = b.History(sam, "dev", nil, nil, 10)
	if len(h) != 2 || h[1].Seq != rep.Seq || h[1].Subject != "" {
		t.Fatalf("a reply must not inherit the subject: %+v", h)
	}
	if j, _ := json.Marshal(h[1]); strings.Contains(string(j), `"subject"`) {
		t.Fatalf("no subject must be omitted on the wire: %s", j)
	}
	if _, err := b.CreateChannel(sam, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	c, err := b.Send(sam, SendInput{Channel: "mem", Subject: "Fixture rule", Content: "no tea.Sequence"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.GetMemory(sam, *c.MemoryID)
	if err != nil || m.Subject != "Fixture rule" {
		t.Fatalf("get_memory: %+v %v", m, err)
	}
	if j, _ := json.Marshal(m); !strings.Contains(string(j), `"subject":"Fixture rule"`) {
		t.Fatalf("subject on the wire: %s", j)
	}
	revs, err := b.MemoryRevisions(sam, *c.MemoryID)
	if err != nil || len(revs) != 1 || revs[0].Subject != "Fixture rule" {
		t.Fatalf("memory_revisions: %+v %v", revs, err)
	}
}

// A line break or more than 200 runes is refused before anything is
// written, with code validation naming the field; whitespace is trimmed
// and a blank subject is stored as none.
func TestSubjectValidation(t *testing.T) {
	b, sam, _ := setupTwo(t)
	for _, bad := range []string{"two\nlines", "line\rbreak", strings.Repeat("é", 201)} {
		_, err := b.Send(sam, SendInput{Channel: "dev", Subject: bad, Content: "x"})
		wantCode(t, err, "validation")
		if !strings.Contains(err.Error(), "subject") {
			t.Fatalf("error must name the field: %v", err)
		}
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: strings.Repeat("é", 200), Content: "x"}); err != nil {
		t.Fatalf("200 runes is the limit: %v", err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: "   ", Content: "first\nsecond"}); err != nil {
		t.Fatal(err)
	}
	h, err := b.History(sam, "dev", nil, nil, 10)
	if err != nil || len(h) != 2 {
		t.Fatalf("refused sends must write nothing: %d %v", len(h), err)
	}
	if h[1].Subject != "" {
		t.Fatalf("blank subject must store as none, got %q", h[1].Subject)
	}
}

// FTS finds a word that appears only in the subject.
func TestSearchFindsWordOnlyInSubject(t *testing.T) {
	b, sam, _ := setupTwo(t)
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: "Zebra sighting", Content: "in the north field"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "nothing to see"}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Search(sam, SearchInput{Query: "zebra", Mode: "text"})
	if err != nil || len(r.Hits) != 1 || r.Hits[0].Subject != "Zebra sighting" {
		t.Fatalf("%+v %v", r, err)
	}
}
```

Add to `internal/bus/embed_test.go` (imports: add `"slices"`, `"sort"`, `"sync"` to the existing list):

```go
// A memory with a subject embeds subject and content together (ADR 0010);
// one without embeds the content alone.
func TestEmbedBatchEmbedsSubjectWithContent(t *testing.T) {
	var mu sync.Mutex
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		inputs = append(inputs, req.Input...)
		mu.Unlock()
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i := range req.Input {
			data[i] = item{Index: i, Embedding: []float64{1, 0, 0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "model": req.Model})
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	if _, err := b.Send(sam, SendInput{Channel: "mem", Subject: "Roses", Content: "roses are red"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "mem", Content: "violets are blue"}); err != nil {
		t.Fatal(err)
	}
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// Background passes started by each Send may overlap and re-embed a
	// row; only the set of texts matters here.
	sort.Strings(inputs)
	inputs = slices.Compact(inputs)
	if want := []string{"Roses\n\nroses are red", "violets are blue"}; !slices.Equal(inputs, want) {
		t.Fatalf("embedded texts %q, want %q", inputs, want)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/bus -run 'TestSubject|TestSearchFindsWordOnlyInSubject|TestEmbedBatchEmbedsSubjectWithContent' -count=1` fails to compile (`unknown field Subject`).

- [ ] **Step 3: Types and columns.** In `internal/bus/messages.go`, add `"unicode/utf8"` to the imports and:

```go
type SendInput struct {
	Channel        string            `json:"channel"`
	Subject        string            `json:"subject,omitempty" jsonschema:"optional one-line summary (at most 200 characters) shown as the message's title; without one, readers see the first line of content"`
	Content        string            `json:"content"`
	Type           string            `json:"type,omitempty"`
	ReplyTo        *int64            `json:"reply_to,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Refs           []Ref             `json:"refs,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}
```

In `Message`, add after `Type`:

```go
	// Subject is the message's one-line title; empty when it has none.
	Subject string `json:"subject,omitempty"`
```

`messageCols`:

```go
	cols := strings.Split("seq, channel, sender, context, created_at, type, subject, content, reply_to, metadata, refs, memory_id, revision", ", ")
```

`scanMessages`'s Scan:

```go
		if err := rows.Scan(&m.Seq, &m.Channel, &m.Sender, &m.Context, &m.CreatedAt, &m.Type, &m.Subject, &m.Content, &m.ReplyTo, &meta, &refs, &m.MemoryID, &m.Revision, &tags); err != nil {
```

`envelopeUpperBound`'s literal gains `Subject: in.Subject,` after `Type: in.Type,`.

`insertMessage`'s insert:

```go
	res, err := tx.Exec("INSERT INTO messages(channel,sender,context,created_at,type,subject,content,reply_to,metadata,refs) VALUES(?,?,?,?,?,?,?,?,?,?)",
		in.Channel, as, context, b.nowMs(), in.Type, in.Subject, in.Content, in.ReplyTo, meta, refs)
```

Add after `validateSendShape`:

```go
// maxSubjectRunes bounds a message subject (ADR 0010).
const maxSubjectRunes = 200

// normalizeSubject trims s; an empty result means no subject. A line break
// or more than maxSubjectRunes characters is a validation error naming the
// field, like the tag checks. Task subjects never come through here: task
// documents keep their own rule (validateTaskSubject).
func normalizeSubject(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\r\n") {
		return "", errf("validation", false, "subject must be a single line")
	}
	if utf8.RuneCountInString(s) > maxSubjectRunes {
		return "", errf("validation", false, "subject must be at most %d characters", maxSubjectRunes)
	}
	return s, nil
}
```

In `Send`, replace the tags normalization lines with:

```go
	// Normalize before the receipt lookup so a keyed retry with the same
	// subject or tags in another form fingerprints identically.
	var err error
	if in.Subject, err = normalizeSubject(in.Subject); err != nil {
		return SendResult{}, err
	}
	if in.Tags, err = NormalizeTags(in.Tags); err != nil {
		return SendResult{}, err
	}
```

- [ ] **Step 4: Search's own row scan.** In `internal/bus/search.go` `textSearch`:

```go
		if err := rows.Scan(&h.Seq, &h.Channel, &h.Sender, &h.Context, &h.CreatedAt, &h.Type, &h.Subject, &h.Content, &h.ReplyTo, &meta, &refs, &h.MemoryID, &h.Revision, &tags, &rank); err != nil {
```

- [ ] **Step 5: Embeddings.** In `internal/bus/embed.go` `embedBatch`, the query and loop become:

```go
	rows, err := b.db.Query(`SELECT m.seq, m.subject, m.content FROM messages m LEFT JOIN embeddings e ON e.seq=m.seq
	  WHERE m.memory_id IS NOT NULL AND m.tombstone=0 AND e.seq IS NULL AND m.channel <> 'tasks' AND m.channel NOT LIKE 'tasks/%' ORDER BY m.seq LIMIT ?`, embedBatchSize)
	if err != nil {
		return 0, internal(err)
	}
	defer func() { _ = rows.Close() }()
	var seqs []int64
	var texts []string
	for rows.Next() {
		var s int64
		var subject, c string
		if err := rows.Scan(&s, &subject, &c); err != nil {
			return 0, internal(err)
		}
		seqs = append(seqs, s)
		texts = append(texts, embedText(subject, c))
	}
```

Add before `embedBatch`:

```go
// embedText is what a memory revision embeds: subject and content together
// when it has a subject, else the content alone (ADR 0010).
func embedText(subject, content string) string {
	if subject == "" {
		return content
	}
	return subject + "\n\n" + content
}
```

- [ ] **Step 6: Close the loop in the chain migration test.** In `TestMigrateV2ToLatest` (migrate_test.go), replace the column-count assertion added in Task 1 with a real send:

```go
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: "Zebra alert", Content: "after the chain"}); err != nil {
		t.Fatal(err)
	}
	if s, err := b.Search(sam, SearchInput{Query: "zebra", Mode: "text"}); err != nil || len(s.Hits) != 1 || s.Hits[0].Subject != "Zebra alert" {
		t.Fatalf("subject after the v2 chain: %+v %v", s, err)
	}
```

- [ ] **Step 7: Run the new tests, then the gate.** `go test ./internal/bus -count=1` (the whole package: `receive`, `wait`, `dm`, `tags` and `tasks` tests all go through `messageCols`), then the full gate.

- [ ] **Step 8: Commit.**

```bash
git add internal/bus/messages.go internal/bus/search.go internal/bus/embed.go internal/bus/subject_test.go internal/bus/embed_test.go internal/bus/migrate_test.go
git commit -m "feat(bus): send takes subject; every read returns it; FTS and embeddings cover it

Trimmed, one line, at most 200 runes, refused with validation before any
write. messageCols selects it so receive, history, search, get_memory and
memory_revisions carry subject (omitted when empty). Memories embed
subject + content. ADR 0010."
```

---

### Task 3: `edit_memory` subject (keep / change / clear) and task revision rows

**Files:**
- Modify: `internal/bus/memories.go` (`EditInput`, `EditMemory`, new `editSubject`)
- Modify: `internal/bus/tasks.go:482` (`TaskCreate`'s `send`)
- Modify: `internal/bus/tasks_update.go:292,446,497` (`writeTaskRevision`, the forced-delete and update envelopes)
- Test: `internal/bus/subject_test.go`

**Interfaces:**
- Consumes: `normalizeSubject`, `SendInput.Subject`, `Message.Subject` (Task 2); `queryRower` (memories.go); `TaskPatch.Subject *string` (exists).
- Produces: `EditInput.Subject *string` (`json:"subject,omitempty"`): nil keeps the live revision's subject, a pointer to `""` clears it. Task rows store `Task.Subject` in `messages.subject`.

- [ ] **Step 1: Write the failing tests.** Append to `internal/bus/subject_test.go`:

```go
// edit_memory keeps the subject when omitted, replaces it when set, clears
// it on "", and refuses a bad one before writing; a memory with no subject
// stays without one unless the edit sets it.
func TestEditMemorySubjectKeepChangeClear(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	c, err := b.Send(sam, SendInput{Channel: "mem", Subject: "Rule", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	id := *c.MemoryID
	subjectIs := func(want string) {
		t.Helper()
		m, err := b.GetMemory(sam, id)
		if err != nil || m.Subject != want {
			t.Fatalf("subject %q, want %q (%v)", m.Subject, want, err)
		}
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	subjectIs("Rule")
	revised := " Rule, revised "
	if _, err := b.EditMemory(sam, EditInput{ID: id, Subject: &revised, Content: "v3"}); err != nil {
		t.Fatal(err)
	}
	subjectIs("Rule, revised")
	empty := ""
	if _, err := b.EditMemory(sam, EditInput{ID: id, Subject: &empty, Content: "v4"}); err != nil {
		t.Fatal(err)
	}
	subjectIs("")
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v5"}); err != nil {
		t.Fatal(err)
	}
	subjectIs("")
	bad := "a\nb"
	_, err = b.EditMemory(sam, EditInput{ID: id, Subject: &bad, Content: "v6"})
	wantCode(t, err, "validation")
	revs, err := b.MemoryRevisions(sam, id)
	if err != nil || len(revs) != 5 {
		t.Fatalf("refused edit must write nothing: %d %v", len(revs), err)
	}
	if revs[0].Subject != "Rule" || revs[1].Subject != "Rule" || revs[2].Subject != "Rule, revised" || revs[3].Subject != "" {
		t.Fatalf("each revision keeps its own subject: %+v", revs)
	}
}

// task_create and task_update keep their API; every revision row they
// write carries the task's subject, so it is searchable and the TUI's
// revision browser sees it.
func TestTaskRevisionsCarrySubject(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "tasks/repo", "memory"); err != nil {
		t.Fatal(err)
	}
	tk := mustCreate(t, b, TaskCreateInput{Channel: "tasks/repo", Subject: "Ship v6"})
	renamed := "Ship v6 today"
	if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Subject: &renamed}); err != nil {
		t.Fatal(err)
	}
	revs, err := b.MemoryRevisions(sam, tk.ID)
	if err != nil || len(revs) != 2 || revs[0].Subject != "Ship v6" || revs[1].Subject != "Ship v6 today" {
		t.Fatalf("%+v %v", revs, err)
	}
	r, err := b.Search(sam, SearchInput{Query: "today", Mode: "text"})
	if err != nil || len(r.Hits) != 1 || r.Hits[0].Seq != revs[1].Seq {
		t.Fatalf("task subject is searchable: %+v %v", r, err)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/bus -run 'TestEditMemorySubjectKeepChangeClear|TestTaskRevisionsCarrySubject' -count=1`: the first fails to compile (`unknown field Subject` in `EditInput`); after adding the field alone, the second fails with an empty `Subject` on the revisions.

- [ ] **Step 3: EditMemory.** In `internal/bus/memories.go`:

```go
type EditInput struct {
	ID int64 `json:"id"`
	// Subject is nil to keep the current revision's subject; a pointer to
	// "" clears it (the same shape as TaskPatch.Subject).
	Subject  *string           `json:"subject,omitempty" jsonschema:"one-line summary shown as the memory's title; omit to keep the current one, pass an empty string to clear it"`
	Content  string            `json:"content"`
	Type     string            `json:"type,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Refs     []Ref             `json:"refs,omitempty"`
	// Tags is nil to keep the current revision's tags; an empty list clears
	// them.
	Tags           []string `json:"tags,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}
```

Add after `liveRevision`:

```go
// editSubject resolves the subject the next revision stores: the edit's own
// when set, else the live revision's (seq), so an edit that omits it keeps
// it. Runs on b.db for the preflight and again on the write tx against the
// committed live revision, like the tags.
func editSubject(q queryRower, in EditInput, seq int64) (string, error) {
	if in.Subject != nil {
		return *in.Subject, nil
	}
	var s string
	if err := q.QueryRow("SELECT subject FROM messages WHERE seq=?", seq).Scan(&s); err != nil {
		return "", internal(err)
	}
	return s, nil
}
```

In `EditMemory`, after the `validateRefs` check and before the tags block:

```go
	if in.Subject != nil {
		s, err := normalizeSubject(*in.Subject)
		if err != nil {
			return EditResult{}, err
		}
		in.Subject = &s
	}
```

Replace the `send := SendInput{...}` line with:

```go
	subject, err := editSubject(b.db, in, liveSeq)
	if err != nil {
		return EditResult{}, err
	}
	send := SendInput{Channel: channel, Subject: subject, Content: in.Content, Type: in.Type, Metadata: in.Metadata, Refs: in.Refs, Tags: tags}
```

And inside the transaction, right after `send.Tags = tags`:

```go
	if send.Subject, err = editSubject(tx, in, curSeq); err != nil {
		return EditResult{}, err
	}
```

- [ ] **Step 4: Task rows.** `internal/bus/tasks.go` `TaskCreate`:

```go
	send := SendInput{Channel: in.Channel, Subject: in.Subject, Content: content}
```

`internal/bus/tasks_update.go`:

```go
	seq, err := b.insertMessage(tx, as, context, SendInput{Channel: t.Channel, Type: typ, Subject: t.Subject, Content: doc}, "ordinary")
```

and the two envelope estimates that feed the size gate, so they count the subject the row will carry:

```go
			fctx, fsize, err := b.sendEnvelope(tx, as, SendInput{Channel: channel, Type: "forced", Subject: cur.Subject, Content: doc}, true)
```

```go
	context, size, err := b.sendEnvelope(tx, as, SendInput{Channel: channel, Type: typ, Subject: patched.Subject, Content: content}, true)
```

The reclaim path (`tasks_reclaim.go`) calls `writeTaskRevision`, so it is covered. The pre-transaction preflight at `tasks_update.go:368` also counts the subject column (human decision 2026-09-24), so a patch whose subject pushes the row over the limit fails there, before `inspect` and `checkCapacity`:

```go
	if _, _, err := b.sendEnvelope(b.db, as, SendInput{Channel: channel, Subject: subj, Content: string(preflight)}, true); err != nil {
```

Add a test in `internal/bus/subject_test.go` that pins it: a `task_update` whose patch subject plus description is sized so the document alone fits but the document plus the subject column does not is refused with the size error, and the task's revision count is unchanged.

- [ ] **Step 5: Run the tests, then the gate.** `go test ./internal/bus -count=1`, then the full gate.

- [ ] **Step 6: Commit.**

```bash
git add internal/bus/memories.go internal/bus/tasks.go internal/bus/tasks_update.go internal/bus/subject_test.go
git commit -m "feat(bus): edit_memory takes subject; task revisions store theirs

EditInput.Subject is nil to keep, \"\" to clear. task_create and
task_update keep their API but write the task's subject into the row.
ADR 0010."
```

---

### Task 4: MCP descriptions, protocol text, skill, README

**Files:**
- Modify: `internal/mcpserver/server.go:424,444` (`send` and `edit_memory` descriptions)
- Modify: `internal/cli/identity.go:64-94` (`protocol`)
- Modify: `internal/cli/skills/using-agentbus/SKILL.md` (new "Subjects" section before "## Tags")
- Modify: `README.md:59-60` (`send` bullet)
- Test: `internal/mcpserver/server_test.go`, `internal/cli/cli_test.go`, `internal/cli/init_test.go`

**Interfaces:**
- Consumes: `SendInput.Subject` and `EditInput.Subject` struct tags (Tasks 2-3) — the go-sdk derives the tool schemas from `sendIn` / `editIn`, which embed them, so the schemas already list `subject`; this task pins that with tests and updates the prose.
- Produces: nothing code-level. The skill text changes, so the release notes (Task 6) tell users to run `agentbus init --global`.

- [ ] **Step 1: Write the failing tests.** In `internal/mcpserver/server_test.go` add:

```go
// TestSendAndEditMemoryCarrySubject (ADR 0010): both tool schemas list
// subject (it reaches them through bus.SendInput / bus.EditInput), both
// descriptions explain it, and a subject sent over MCP comes back on
// history.
func TestSendAndEditMemoryCarrySubject(t *testing.T) {
	cs := testSession(t)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tl := range tools.Tools {
		if tl.Name != "send" && tl.Name != "edit_memory" {
			continue
		}
		seen++
		j, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(j), `"subject"`) {
			t.Fatalf("%s schema lacks subject: %s", tl.Name, j)
		}
		if !strings.Contains(tl.Description, "subject") {
			t.Fatalf("%s description must explain subject: %s", tl.Name, tl.Description)
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d of send and edit_memory", seen)
	}
	call(t, cs, "register", map[string]any{"name": "Sam"})
	call(t, cs, "send", map[string]any{"as": "Sam", "channel": "general", "subject": "Deploy plan", "content": "step one"})
	_, res := call(t, cs, "history", map[string]any{"as": "Sam", "channel": "general"})
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"subject":"Deploy plan"`) {
		t.Fatalf("history over MCP lacks the subject: %s", text)
	}
	_, bad := call(t, cs, "send", map[string]any{"as": "Sam", "channel": "general", "subject": "two\nlines", "content": "x"})
	if !bad.IsError || !strings.Contains(bad.Content[0].(*mcp.TextContent).Text, "subject") {
		t.Fatalf("a bad subject is refused naming the field: %+v", bad)
	}
}
```

In `internal/cli/cli_test.go` `TestIdentityLineIsPrescriptive`, add to the `want` list:

```go
		"- Give every send a subject",
```

In `internal/cli/init_test.go` at the skill check (line 117), require the skill to mention the field:

```go
		if err != nil || !strings.HasPrefix(string(skill), "---\nname: using-agentbus\n") || !strings.Contains(string(skill), "memory/<repo>") || !strings.Contains(string(skill), "`subject`") {
```

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/mcpserver -run TestSendAndEditMemoryCarrySubject -count=1` fails on the description check; `go test ./internal/cli -run 'TestIdentityLineIsPrescriptive|TestInit' -count=1` fails on the missing text.

- [ ] **Step 3: Tool descriptions.** In `internal/mcpserver/server.go`, the `send` description ends with the tags sentence; append one more sentence so it reads:

```
... other agents can subscribe to tags and filter history and search by them. subject is an optional one-line summary (at most 200 characters) shown as the message's title and matched by search; without one, readers see the first line of content.
```

The `edit_memory` description becomes:

```
Agentbus: replace a memory's content as a new revision. Last committed write wins; the result names the revision you replaced. tags replaces the memory's tags; omit it to keep them. subject replaces the memory's subject; omit it to keep it, pass an empty string to clear it.
```

- [ ] **Step 4: Protocol text.** In `internal/cli/identity.go` `protocol`, add this bullet after the direct-message bullet (the one ending `its seq.`):

```
- Give every send a subject: a one-line summary of the message. The TUI
  shows it as the message's title, and search matches it.
```

- [ ] **Step 5: Skill and README.** In `internal/cli/skills/using-agentbus/SKILL.md`, insert before `## Tags`:

```markdown
## Subjects

`send` takes `subject`: a one-line summary of at most 200 characters that
the TUI shows as the message's title and that `search` matches. Set it on
every message; a message without one is shown by its first line. Every
payload carries `subject` when it is set. `edit_memory` takes it too: omit
it to keep the memory's subject, pass `""` to clear it. Tasks already have
one.

```

In `README.md`, the `send` bullet becomes:

```markdown
- `send` — post a message, or, on a memory channel, create a memory;
  `subject` is a one-line title the TUI shows and search matches; `tags`
  (up to 10 short lowercase labels) let others follow it across channels.
```

- [ ] **Step 6: Run the tests, then the gate.** `go test ./internal/mcpserver ./internal/cli -count=1`, then the full gate.

- [ ] **Step 7: Commit.**

```bash
git add internal/mcpserver/server.go internal/mcpserver/server_test.go internal/cli/identity.go internal/cli/cli_test.go internal/cli/init_test.go internal/cli/skills/using-agentbus/SKILL.md README.md
git commit -m "docs: send and edit_memory describe subject; protocol and skill ask for one

The tool schemas already list subject through bus.SendInput and
bus.EditInput; this pins that with a test. The skill text changed, so
1.8.0 needs agentbus init --global. ADR 0010."
```

---

### Task 5: TUI — collapsed rows, layered `→`/`←`, memory versions, search rows

**Files:**
- Modify: `internal/tui/model.go:80-82,133,468-478` (`bodyOpen` field and init; `right`/`left` keys)
- Modify: `internal/tui/thread.go:177-183` (replace `expandCursor` with `openCursor`; add `closeCursor`, `rowAvail`)
- Modify: `internal/tui/view.go:364-455` (`renderStream`; new `rowLine`)
- Modify: `internal/tui/search.go:150` (`viewSearch` hit line)
- Modify: `internal/tui/help.go:37` (`→ / ←` line)
- Modify: `internal/tui/view_test.go:190-215` (`TestReplyIndentAppliesToWrappedLines`)
- Test: `internal/tui/body_test.go` (new)

**Interfaces:**
- Consumes: `bus.Message.Subject`, `bus.SendInput.Subject`, `bus.EditInput.Subject` (Tasks 2-3); existing `row`, `rows`, `cursorRow`, `setExpanded`, `collapseCursor`, `m.mem.version`, `markSel`/`markOpen`, `ansi.Truncate`.
- Produces: `Model.bodyOpen map[int64]bool`; `func rowLine(x bus.Message, avail int) (line string, hasBody bool)`; `func (m *Model) rowAvail(depth int) int`; `func (m *Model) openCursor()`; `func (m *Model) closeCursor()`. `expandCursor` is removed (its only caller was the `right` key).

Behavior (spec "TUI display"): every message pane (channel, DM inbox, merged DM pane, tag pane) shows each row as header + one line: the subject, else the content's first line, cut to the row width with `…`. A row *has a body* when a subject is set and the content isn't empty, or there's no subject and the content has a second line or its first line was cut. `→` opens the body if closed and present, else shows direct replies; `←` hides the replies (whole subtree) if any are shown, else closes the body; `space` toggles replies only. `▶` when anything is hidden (body or replies), `▼` when the body is open (or replies are shown and nothing is hidden). Open body = subject line (only when set) + full content, wrapped as today. New arrivals, own sends included, start collapsed. Version stepping keeps `bodyOpen`. The task pane is unchanged.

- [ ] **Step 1: Write the failing tests.** Create `internal/tui/body_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

// sendSubject posts content with a subject from Sam.
func (f *fixture) sendSubject(t *testing.T, ch, subject, content string) bus.SendResult {
	t.Helper()
	r, err := f.ab.Send(f.sam, bus.SendInput{Channel: ch, Subject: subject, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// stream is the rendered stream without escapes.
func (f *fixture) stream() string { return ansi.Strip(f.m.renderStream()) }

// onChannel selects ch, loads it through history (no peeked replies), and
// puts the stream cursor on seq.
func (f *fixture) onChannel(t *testing.T, ch string, seq int64) {
	t.Helper()
	f.key("esc")
	for i, c := range f.m.channels {
		if c.Name == ch {
			f.run(f.m.selectChannel(i))
		}
	}
	f.run(f.m.loadHistory(ch, nil))
	f.m.placeCursor(seq)
	if r, ok := f.m.cursorRow(); !ok || r.msg.Seq != seq {
		t.Fatalf("cursor not on %d: %+v", seq, r)
	}
}

// A collapsed row shows the subject, or the first line when there's none,
// and hides the rest of the content behind ▶.
func TestCollapsedRowShowsSubjectElseFirstLine(t *testing.T) {
	f := newFixture(t)
	f.sendSubject(t, "dev", "Deploy plan", "step one\nstep two")
	f.agentSend(t, "dev", "first line\nsecond line")
	f.receive(t)
	s := f.stream()
	for _, want := range []string{"Deploy plan", "first line"} {
		if !strings.Contains(s, want) {
			t.Fatalf("row line %q missing:\n%s", want, s)
		}
	}
	for _, hidden := range []string{"step one", "second line"} {
		if strings.Contains(s, hidden) {
			t.Fatalf("body %q must stay hidden:\n%s", hidden, s)
		}
	}
	if strings.Count(s, markSel) != 2 {
		t.Fatalf("both rows have a body to open:\n%s", s)
	}
}

// → opens the body, then the direct replies; ← hides the replies, then the
// body; space toggles the replies only.
func TestRightOpensBodyThenRepliesLeftClosesRepliesThenBody(t *testing.T) {
	f := newFixture(t)
	a := f.sendSubject(t, "dev", "Deploy plan", "step one")
	f.agentReply(t, "dev", a.Seq, "A1")
	f.onChannel(t, "dev", a.Seq)
	if got := contents(f.m.rows("dev")); !eq(got, []string{"step one"}) || strings.Contains(f.stream(), "step one") {
		t.Fatalf("starts collapsed: rows %v\n%s", got, f.stream())
	}
	f.key("right")
	if s := f.stream(); !strings.Contains(s, "step one") || !eq(contents(f.m.rows("dev")), []string{"step one"}) || !strings.Contains(s, markSel) {
		t.Fatalf("first → opens the body only; replies still hidden behind ▶:\n%s", s)
	}
	f.key("right")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"step one", ">A1"}) || !strings.Contains(f.stream(), markOpen) {
		t.Fatalf("second → shows the replies: %v\n%s", got, f.stream())
	}
	f.key("left")
	if s := f.stream(); !eq(contents(f.m.rows("dev")), []string{"step one"}) || !strings.Contains(s, "step one") {
		t.Fatalf("first ← hides the replies and keeps the body open:\n%s", s)
	}
	f.key("left")
	if s := f.stream(); strings.Contains(s, "step one") || !strings.Contains(s, "Deploy plan") || !strings.Contains(s, markSel) {
		t.Fatalf("second ← closes the body:\n%s", s)
	}
	f.key(" ")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"step one", ">A1"}) || strings.Contains(f.stream(), "step one") {
		t.Fatalf("space toggles the replies only: %v\n%s", got, f.stream())
	}
}

// A one-line message with no subject that fits the width has no body: no
// marker, and → has nothing to open.
func TestOneLineMessageWithoutSubjectHasNoBody(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", "hello")
	f.onChannel(t, "dev", a.Seq)
	if s := f.stream(); strings.Contains(s, markSel) || strings.Contains(s, markOpen) {
		t.Fatalf("nothing to expand:\n%s", s)
	}
	f.key("right")
	if f.m.bodyOpen[a.Seq] {
		t.Fatal("→ must not open an empty body")
	}
}

// A first line wider than the pane is cut with … and counts as a body: →
// shows the full text (Review Focus 1).
func TestCutFirstLineOpensAsBody(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", strings.Repeat("word ", 30))
	f.m.width = 60
	f.m.layout()
	f.onChannel(t, "dev", a.Seq)
	if s := f.stream(); !strings.Contains(s, "…") || !strings.Contains(s, markSel) {
		t.Fatalf("a cut first line is openable:\n%s", s)
	}
	f.key("right")
	if s := f.stream(); strings.Count(s, "word") != 30 || !strings.Contains(s, markOpen) {
		t.Fatalf("the open body shows the whole text:\n%s", s)
	}
}

// Stepping memory versions shows each version's subject and, with the body
// open, its content; the body stays open across steps.
func TestBodyStaysOpenAcrossMemoryVersions(t *testing.T) {
	f := newFixture(t)
	first := f.sendSubject(t, "dev-notes", "Release v1", "notes one")
	v2 := "Release v2"
	if _, err := f.ab.EditMemory(f.sam, bus.EditInput{ID: *first.MemoryID, Subject: &v2, Content: "notes two"}); err != nil {
		t.Fatal(err)
	}
	f.key("esc")
	for i, c := range f.m.channels {
		if c.Name == "dev-notes" {
			f.run(f.m.selectChannel(i))
		}
	}
	f.run(f.m.loadHistory("dev-notes", nil))
	for _, r := range f.m.rows("dev-notes") {
		if r.msg.MemoryID != nil && *r.msg.MemoryID == *first.MemoryID {
			f.m.placeCursor(r.msg.Seq)
		}
	}
	f.streamHas(t, "Release v2")
	if strings.Contains(f.stream(), "notes two") {
		t.Fatal("starts collapsed")
	}
	f.key("right")
	f.streamHas(t, "Release v2", "notes two")
	f.key(".")
	f.streamHas(t, "Release v1", "notes one", "r1 of 2")
	if s := f.stream(); strings.Contains(s, "notes two") || strings.Contains(s, "Release v2") {
		t.Fatalf("the selected version only:\n%s", s)
	}
	f.key(",")
	f.streamHas(t, "Release v2", "notes two", "r2 of 2")
}

// The merged DM pane and tag panes collapse rows too.
func TestDMAndTagPanesCollapseRows(t *testing.T) {
	f := newFixture(t)
	if err := f.c.b.SubscribeTags(f.c.as, []string{"bug"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dm/" + f.c.as, Subject: "Question", Content: "line one\nline two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dev", Subject: "Bug report", Content: "it broke\nbadly", Tags: []string{"bug"}}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	f.selectSessionNamed(t, "Sam")
	if s := f.stream(); !strings.Contains(s, "Question") || strings.Contains(s, "line two") {
		t.Fatalf("DM pane collapses:\n%s", s)
	}
	f.selectTagPane(t, tagPanePrefix+"bug")
	if s := f.stream(); !strings.Contains(s, "Bug report") || strings.Contains(s, "badly") {
		t.Fatalf("tag pane collapses:\n%s", s)
	}
}

// Search hit rows show the subject when there is one.
func TestSearchRowsShowTheSubject(t *testing.T) {
	f := newFixture(t)
	f.sendSubject(t, "dev", "Deploy plan", "the release script")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	v := ansi.Strip(f.m.View())
	if !strings.Contains(v, "Deploy plan") || strings.Contains(v, "the release script") {
		t.Fatalf("hit row shows the subject, not the snippet:\n%s", v)
	}
}
```

Update `TestReplyIndentAppliesToWrappedLines` in `internal/tui/view_test.go`: the long reply is now a collapsed row until its body is opened, so after `f.key("right")` on the root (which shows the reply, since "A" has no body) move onto the reply and open it:

```go
	f.key("shift+tab") // compose -> stream directly
	f.key("up")
	f.key("right") // A has no body: shows the reply
	f.key("down")
	f.key("right") // the reply's cut first line is its body: open it
```

(The rest of that test is unchanged.)

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/tui -run 'TestCollapsedRow|TestRightOpens|TestOneLine|TestCutFirstLine|TestBodyStays|TestDMAndTag|TestSearchRowsShow|TestReplyIndent' -count=1` fails to compile (`m.bodyOpen` undefined).

- [ ] **Step 3: Model state and keys.** In `internal/tui/model.go`, add to `Model` after `peek`:

```go
	bodyOpen   map[int64]bool  // message seq -> its body (subject line and full content) is shown
```

and in `New`, after `peek:         map[int64]int64{},`:

```go
		bodyOpen:     map[int64]bool{},
```

The `right` / `left` cases in `updateNormal` become:

```go
	case "right":
		if bus.IsTaskChannel(m.selName()) {
			return m.expandTask()
		}
		m.openCursor()
	case "left":
		if bus.IsTaskChannel(m.selName()) {
			m.collapseTask()
		} else {
			m.closeCursor()
		}
```

Update the comment above the `down` case so its last sentence reads: `right opens the cursor message's body, then its direct replies; left hides its whole subtree, then its body.`

- [ ] **Step 4: Row line and body helpers.** In `internal/tui/view.go`, add after `header`:

```go
// rowLine is a collapsed row's one text line: the subject, else the
// content's first line, cut to avail columns with an ellipsis. hasBody
// reports whether the content holds anything that line doesn't show: any
// content under a subject, a second line, or a first line that was cut.
func rowLine(x bus.Message, avail int) (line string, hasBody bool) {
	if x.Subject != "" {
		return ansi.Truncate(x.Subject, avail, "…"), x.Content != ""
	}
	first, rest, _ := strings.Cut(x.Content, "\n")
	line = ansi.Truncate(first, avail, "…")
	return line, rest != "" || line != first
}
```

In `internal/tui/thread.go`, replace `expandCursor` with:

```go
// rowAvail is the width left for a row's text after its tree prefix: two
// columns per depth level plus the two-column marker.
func (m *Model) rowAvail(depth int) int {
	return max(m.stream.Width, 20) - 2*depth - 2
}

// openCursor (→) opens the cursor row's body when it's closed and the row
// has one; otherwise it shows the row's direct replies.
func (m *Model) openCursor() {
	r, ok := m.cursorRow()
	if !ok {
		return
	}
	if _, hasBody := rowLine(r.msg, m.rowAvail(r.depth)); hasBody && !m.bodyOpen[r.msg.Seq] {
		m.bodyOpen[r.msg.Seq] = true
		m.refreshStream()
		m.scrollCursorIntoView()
		return
	}
	m.setExpanded(r, true)
}

// closeCursor (←) hides the replies under the cursor row (the whole
// subtree) when any are shown; otherwise it closes the row's body.
func (m *Model) closeCursor() {
	r, ok := m.cursorRow()
	if !ok {
		return
	}
	if r.open {
		m.collapseCursor()
		return
	}
	delete(m.bodyOpen, r.msg.Seq)
	m.refreshStream()
	m.scrollCursorIntoView()
}
```

- [ ] **Step 5: Render.** In `internal/tui/view.go` `renderStream`, replace the block from `// The tree prefix (indent plus expand/collapse marker) is applied` through `line := m.header(x, stampStyle, w-pw) + "\n" + x.Content` with:

```go
		avail := m.rowAvail(r.depth)
		label := ""
		if x.MemoryID != nil && x.Revision != nil && *x.Revision > 1 {
			label = "r" + itoa(*x.Revision)
		}
		if v, ok := m.mem.version(x); ok {
			x.Sender, x.CreatedAt, x.Subject, x.Content = v.Sender, v.CreatedAt, v.Subject, v.Content
			label = "r" + strconv.Itoa(m.mem.idx+1) + " of " + strconv.Itoa(len(m.mem.revs))
		}
		// The row line is the subject or the first line, cut to fit; the
		// body (what that line leaves out) shows only once opened with →,
		// as the subject line (when set) and the full content.
		text, hasBody := rowLine(x, avail)
		bodyOpen := hasBody && m.bodyOpen[x.Seq]
		if bodyOpen {
			text = x.Content
			if x.Subject != "" {
				text = x.Subject + "\n" + x.Content
			}
		}
		// The tree prefix (indent plus expand/collapse marker) is applied
		// after wrapping so every wrapped line sits at the row's depth.
		// ▶ means something is hidden (body or replies); ▼ means the body
		// is open, or the replies are shown with nothing left hidden.
		prefix := strings.Repeat("  ", r.depth)
		switch {
		case r.hidden > 0 || (hasBody && !bodyOpen):
			prefix += rowDim.Render(markSel + " ")
		case r.open || bodyOpen:
			prefix += rowDim.Render(markOpen + " ")
		default:
			prefix += "  "
		}
		pw := lipgloss.Width(prefix)
		line := m.header(x, stampStyle, avail) + "\n" + text
```

(The replaced range already held the `label` and `m.mem.version` lines; they now run before the marker switch so the version's subject feeds `rowLine`. `pw` is still what the wrap and indent below use, and equals `w - avail`.) Update the doc comment above `renderStream` so its last sentence reads: `Each message is a header line (timestamp, sender → recipient, tag chips) followed by its row line (subject or first line) or, when opened, its body, at the row's depth.`

- [ ] **Step 6: Search overlay and help.** In `internal/tui/search.go` `viewSearch`:

```go
		first := hit.Subject
		if first == "" {
			first = strings.SplitN(hit.Content, "\n", 2)[0]
		}
```

In `internal/tui/help.go` `helpLines`:

```go
		{"→ / ←", "open the cursor message's body, then its replies / hide its replies, then its body"},
```

- [ ] **Step 7: Run the TUI tests, then the gate.** `go test ./internal/tui -count=1` (every existing thread, DM, tag, memory, search and view test must pass unchanged apart from the one edit in Step 1), then the full gate. `golangci-lint` would flag a leftover `expandCursor`; it must be gone.

- [ ] **Step 8: Commit.**

```bash
git add internal/tui/model.go internal/tui/thread.go internal/tui/view.go internal/tui/search.go internal/tui/help.go internal/tui/view_test.go internal/tui/body_test.go
git commit -m "feat(tui): message rows collapse to a subject or first line; → opens the body, then replies

Every message pane (channels, DM inboxes, the merged DM pane, tag panes)
shows header + one cut line; a row has a body when a subject is set or
the content runs past what the line shows. ← hides replies, then the
body; space toggles replies only. Version stepping shows each version's
subject and keeps the body open. Search rows show the subject. ADR 0010."
```

---

### Task 6: Release notes for 1.8.0

**Files:**
- Create: `release/notes-v1.8.0.md`
- Do NOT modify `internal/mcpserver/server.go` (`Version`). Human decision 2026-09-24: the bump to 1.8.0 happens in the release step, not on this branch. Local test builds set the version with ldflags only: `go build -ldflags "-X github.com/ericfitz/agentbus/internal/mcpserver.Version=1.7.1-dev" -o dist/agentbus-dev .`

**Interfaces:**
- Consumes: everything above landed on `feat/message-subjects`.
- Produces: the notes file `release/release.sh` picks up (`release/notes-<tag>.md` replaces the generated notes). Tagging and running `release/release.sh` are not part of this plan.

- [ ] **Step 1: Write the notes.** Create `release/notes-v1.8.0.md`:

```markdown
## Message subjects

- `send` takes `subject`: an optional one-line summary (at most 200
  characters) shown as the message's title. A line break or a longer
  subject is refused with `validation`.
- Every payload carries `subject` when it is set: `receive`, `history`,
  `search` hits, `get_memory` and `memory_revisions`. A reply does not
  inherit its parent's subject.
- `edit_memory` takes `subject`: omit it to keep the memory's subject,
  pass `""` to clear it.
- Text search matches subjects, and memories embed subject and content
  together (existing embeddings are not recomputed).
- Task rows carry the task's subject in the same column; `task_create`
  and `task_update` are unchanged. ADR 0010.

## TUI

- Message rows collapse to the header plus one line: the subject, or the
  content's first line cut to the pane width. This applies to channels,
  DM inboxes, the merged DM pane and tag panes; the task pane is
  unchanged.
- `→` opens the cursor message's body (subject line and full content),
  then its direct replies; `←` hides its replies, then its body. `space`
  still toggles the replies only. `▶` marks anything hidden, `▼` an open
  body.
- Stepping memory versions (`.`/`>` and `,`/`<`) shows each version's
  subject and keeps the body open.
- Search hit rows show the subject when there is one.
- Compose is unchanged: messages sent from the TUI have no subject and
  show their first line.

## Upgrading

This release moves the database to **schema v6**. The first 1.8.0 process
to open the live bus migrates it (adds `messages.subject`, backfills task
rows, and rebuilds the full-text index once), and 1.7.x binaries then
refuse it.

1. `brew upgrade agentbus`
2. `agentbus init --global` (the skill text changed: it now asks agents to
   set `subject` on every send)
3. Restart every harness session and the TUI together, so no 1.7.x process
   keeps running against the upgraded database.

Existing non-task messages keep no subject and show their first line.
```

- [ ] **Step 2: Gate, then commit.** Run the full gate, then:

```bash
git add release/notes-v1.8.0.md
git commit -m "docs: release notes for v1.8.0"
```

The version bump to 1.8.0, tagging, `release/release.sh v1.8.0`, the tap update and `PROGRESS.md` stay with the controller and the user.

---

## Self-review notes

- Spec coverage: schema v6 and migration (Task 1); validation, payloads, `SendInput.Subject`, no reply inheritance, embeddings (Task 2); `EditInput.Subject`, task rows (Task 3); MCP schemas and descriptions, protocol text, skill (Task 4); every TUI section — row line, has-a-body rule, `bodyOpen`, keys, markers, opened body, memory versions, search overlay, compose unchanged, task pane unchanged (Task 5); rollout (Task 6). Spec "Testing" items each map to a named test above.
- Human decisions 2026-09-24: error code is `validation` (the spec now says so); the `json_valid` guard on the task backfill is approved; no version change on this branch (Task 6).
- Names used across tasks: `normalizeSubject`, `maxSubjectRunes`, `embedText`, `editSubject`, `messagesFTSDDL`, `schemaV5FTS`, `rowLine`, `rowAvail`, `openCursor`, `closeCursor`, `bodyOpen` — each defined in one task and referenced by name in later ones.
