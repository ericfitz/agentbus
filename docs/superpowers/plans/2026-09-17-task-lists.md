# Task Lists Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Shared, ordered, hierarchical work queues on the bus (`tasks/<name>` channels) with atomic claim, owner guard, dependencies, leases, and automatic reclaim of abandoned tasks; six MCP tools and a read-only TUI tree view.

**Architecture:** A task list is a `memory`-kind channel named `tasks/<name>`; a task is a memory whose content is a JSON document; every change is a new revision written through the existing tombstone-and-insert path. All rules live in `Bus.TaskUpdate` (one write transaction); `TaskClaim`/`TaskRelease` are wrappers. No schema change.

**Tech Stack:** Go 1.27, `modernc.org/sqlite`, `github.com/modelcontextprotocol/go-sdk` v1.7.0, Charm bubbletea v1 (TUI). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-17-task-lists-design.md`. Decisions: `docs/adr/0005-task-lists.md`. Executors read both.

## Global Constraints

- No schema change, no `user_version` bump, no new dependencies, no new error codes (`validation`, `conflict`, `not_found`, `not_registered`, `rate_limited`, `internal` only).
- All enforcement lives in `Bus.TaskUpdate` / `Bus.TaskCreate`. `TaskClaim` and `TaskRelease` must contain no checks of their own.
- American English everywhere. Match the comment density and idiom of the surrounding file.
- Tests pass under `CGO_ENABLED=0 go test ./...` and `go test -race ./...`; `golangci-lint run` reports 0 issues; `go vet ./...` clean.
- TUI fixture rules: no `tea.Sequence` in production code; timers only through the package `tick` var; static (non-blinking) cursors on every text input.
- macOS host: use `rg` not grep; no GNU `timeout`; never chain `sleep`; no `git stash`; stage only files you changed (never `git add -A`); do not push.
- Every commit message ends with:
  ```
  Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01JqyxqA7fsf8RZAvkfcYBfA
  ```
- Times are unix milliseconds UTC (`b.nowMs()`); a session is live when `heartbeat >= b.nowMs()-attachmentExpiryMs`.

## File Structure

| File | Responsibility |
|---|---|
| `internal/bus/rank.go` (new) | `rankBetween`: base-62 string midpoint. Pure. |
| `internal/bus/tasks.go` (new) | Types, `IsTaskChannel`, loading a list, tree order, blocked derivation, `TaskCreate`, `TaskGet`, `TaskList`. |
| `internal/bus/tasks_update.go` (new) | `TaskPatch`, `applyPatch` (all rules), `TaskUpdate`, `TaskClaim`, `TaskRelease`. |
| `internal/bus/tasks_reclaim.go` (new) | `abandoned`, `reclaimAbandoned`, tick step. |
| `internal/bus/channels.go`, `messages.go`, `memories.go`, `embed.go`, `maintenance.go` (modify) | `tasks/` name rule, write guards, embedder skip, tick step. |
| `internal/mcpserver/server.go` (modify) | Six tools. |
| `internal/tui/tasks.go` (new), `view.go`, `model.go`, `msgs.go` (modify) | Read-only tree view. |
| `internal/cli/identity.go`, `internal/cli/skills/using-agentbus/SKILL.md`, `internal/cli/subscribe.go`, `README.md`, `docs/install.md` (modify) | Guidance and docs. |

---

### Task 1: Rank keys

**Files:**
- Create: `internal/bus/rank.go`
- Test: `internal/bus/rank_test.go`

**Interfaces:**
- Produces: `func rankBetween(a, b string) string` — returns a key strictly between `a` and `b` in byte order. `a == ""` means "no lower bound", `b == ""` means "no upper bound". Precondition: `a < b` when both are non-empty. Keys use the alphabet `0-9A-Za-z` (ASCII order) and never end in `'0'`.

- [ ] **Step 1: Write the failing test**

```go
package bus

import (
	"math/rand"
	"sort"
	"testing"
)

func TestRankBetweenOrdersStrictly(t *testing.T) {
	cases := [][2]string{{"", ""}, {"", "V"}, {"V", ""}, {"A", "B"}, {"A", "A1"}, {"Az", "B"}, {"", "1"}, {"zz", ""}, {"A", "A01"}}
	for _, c := range cases {
		m := rankBetween(c[0], c[1])
		if m == "" || (c[0] != "" && m <= c[0]) || (c[1] != "" && m >= c[1]) {
			t.Fatalf("rankBetween(%q,%q)=%q is not strictly between", c[0], c[1], m)
		}
		if m[len(m)-1] == '0' {
			t.Fatalf("rankBetween(%q,%q)=%q ends in '0' (nothing sorts between x and x0)", c[0], c[1], m)
		}
	}
}

// Repeated insertion at one spot (the worst case for float or sparse-int
// ranks) and at random spots keeps a strict total order.
func TestRankBetweenSurvivesManyInserts(t *testing.T) {
	keys := []string{rankBetween("", "")}
	for i := 0; i < 500; i++ { // always insert at the front
		keys = append([]string{rankBetween("", keys[0])}, keys...)
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		j := r.Intn(len(keys) + 1)
		lo, hi := "", ""
		if j > 0 {
			lo = keys[j-1]
		}
		if j < len(keys) {
			hi = keys[j]
		}
		k := rankBetween(lo, hi)
		keys = append(keys[:j], append([]string{k}, keys[j:]...)...)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("keys are not sorted")
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] == keys[i-1] {
			t.Fatalf("duplicate key %q", keys[i])
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/bus/ -run TestRankBetween`
Expected: FAIL, `undefined: rankBetween`.

- [ ] **Step 3: Implement**

```go
package bus

import "strings"

// rankDigits is the rank alphabet in ASCII (byte) order, so comparing keys
// as strings compares them as base-62 fractions.
const rankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// rankBetween returns a key strictly between a and b in byte order; ""
// means unbounded on that side. Keys are base-62 fractions ("V" is about
// one half), and a key can always grow by a digit, so a midpoint always
// exists and sibling ranks never need rebalancing. Keys never end in '0':
// nothing sorts between "x" and "x0". Precondition: a < b when both are set.
func rankBetween(a, b string) string {
	var out strings.Builder
	for i := 0; ; i++ {
		lo := 0
		if i < len(a) {
			lo = strings.IndexByte(rankDigits, a[i])
		}
		hi := len(rankDigits)
		if i < len(b) {
			hi = strings.IndexByte(rankDigits, b[i])
		}
		if hi-lo > 1 {
			out.WriteByte(rankDigits[(lo+hi)/2])
			return out.String()
		}
		// Adjacent or equal digits: keep a's digit and go one place deeper.
		// If the digits differed, the result is already below b, so b stops
		// bounding the remaining places.
		out.WriteByte(rankDigits[lo])
		if hi != lo {
			b = ""
		}
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/bus/ -run TestRankBetween -count=1`
Expected: PASS. If the `{"A","A01"}` case fails, the precondition handling for a lower bound shorter than the upper bound is wrong: with `a="A"`, `b="A01"` the digits at place 1 are `lo=0` (a exhausted), `hi=0`; equal, so emit `'0'` and continue; place 2: `lo=0`, `hi=1`; adjacent, emit `'0'`, unbound b; place 3: `lo=0`, `hi=62`, emit `'V'` → `"A00V"`, which is between.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/rank.go internal/bus/rank_test.go
git commit -m "feat(bus): rank keys for ordered task lists"
```

---

### Task 2: Task channels, create, get, list

**Files:**
- Create: `internal/bus/tasks.go`, `internal/bus/tasks_test.go`
- Modify: `internal/bus/channels.go` (`CreateChannel` name validation), `internal/bus/messages.go` (`Send` guard), `internal/bus/memories.go` (`EditMemory`, `DeleteMemory` guards), `internal/bus/embed.go` (batch query)

**Interfaces:**
- Consumes: `rankBetween` (Task 1); existing `b.auth`, `b.lockKey`, `b.checkReceipt`, `b.storeReceipt`, `b.inspect`, `b.checkCapacity`, `b.sendEnvelope`, `b.insertMessage`, `b.limits.allow`, `liveRevision`, `trimToBytes`, `validateName`, `errf`, `internal`.
- Produces:

```go
const TaskPrefix = "tasks/"
func IsTaskChannel(channel string) bool

// Task is a task as returned to callers. The stored JSON document is the
// subset tagged in taskDoc; the rest is envelope data and derived fields.
type Task struct {
	ID           int64             `json:"id"`
	Channel      string            `json:"channel"`
	Revision     int64             `json:"revision"`
	Subject      string            `json:"subject"`
	Description  string            `json:"description,omitempty"`
	Status       string            `json:"status"`
	Owner        string            `json:"owner,omitempty"`
	Parent       int64             `json:"parent,omitempty"`
	Rank         string            `json:"rank"`
	BlockedBy    []int64           `json:"blocked_by,omitempty"`
	LeasedUntil  int64             `json:"leased_until,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Blocked      bool              `json:"blocked"`
	OpenBlockers []int64           `json:"open_blockers,omitempty"`
	UpdatedAt    int64             `json:"updated_at"`
	UpdatedBy    string            `json:"updated_by"`

	seq int64 // live revision's seq; unexported, not serialized
}

type TaskSummary struct {
	ID           int64   `json:"id"`
	Subject      string  `json:"subject"`
	Status       string  `json:"status"`
	Owner        string  `json:"owner,omitempty"`
	Parent       int64   `json:"parent,omitempty"`
	Depth        int     `json:"depth"`
	OpenBlockers []int64 `json:"open_blockers,omitempty"`
	LeasedUntil  int64   `json:"leased_until,omitempty"`
}

type TaskCreateInput struct {
	Channel        string            `json:"channel"`
	Subject        string            `json:"subject"`
	Description    string            `json:"description,omitempty"`
	Parent         int64             `json:"parent,omitempty"`
	Before         int64             `json:"before,omitempty"`
	After          int64             `json:"after,omitempty"`
	BlockedBy      []int64           `json:"blocked_by,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

type TaskListInput struct {
	Channel string `json:"channel"`
	Status  string `json:"status,omitempty"`
	Owner   string `json:"owner,omitempty"`
}

func (b *Bus) TaskCreate(as string, in TaskCreateInput) (Task, error)
func (b *Bus) TaskGet(as string, id int64) (Task, error)
func (b *Bus) TaskList(as string, in TaskListInput) ([]TaskSummary, error)

// Shared with Tasks 3 and 4:
func loadTasks(q querier, channel string) ([]Task, error)      // live tasks, derived fields filled, tree order
func taskByID(ts []Task, id int64) *Task
func taskContent(t Task) (string, error)                        // the stored JSON document
func placeRank(ts []Task, self, parent, before, after int64) (string, error)
func validateTaskLinks(ts []Task, t Task) error                 // parent + blocked_by existence, self, cycles
func (b *Bus) writeTaskRevision(tx *sql.Tx, as, context string, t Task, typ string) (Task, error)
```

`querier` is the existing interface in the package that both `*sql.DB` and `*sql.Tx` satisfy for `Query` (see `liveSessions(db querier)` in `admin.go`); if it lacks `QueryRow`, use it only for `Query`.

**Rules for this task** (spec sections Storage, Tools, Dependencies, Hierarchy, Order):

1. `IsTaskChannel(ch)` is `strings.HasPrefix(ch, TaskPrefix)`.
2. `CreateChannel`: a name with the `tasks/` prefix is valid when the remainder passes `validateName` and `kind == "memory"`; with another kind → `validation` "task lists (tasks/...) must have kind memory". The bare name `tasks` → `validation` "channel name \"tasks\" is reserved for task lists". All other names behave as today (so `a/b` stays invalid). Implement by validating `strings.TrimPrefix(name, TaskPrefix)` when the prefix is present.
3. `Send`, `EditMemory`, `DeleteMemory`: when the target channel is a task channel → `errf("validation", false, "%s is a task list; use task_create, task_update, task_claim, task_release", channel)`. In `Send` check `in.Channel` right after `validateSendShape`; in `EditMemory`/`DeleteMemory` check the channel returned by the first `liveRevision` lookup (in `DeleteMemory` add that lookup's channel to the existing in-tx `liveRevision` call: it already returns the channel as its third value).
4. Embedder: add `AND m.channel NOT LIKE 'tasks/%'` to the batch `SELECT` in `embedBatch`.
5. Stored document (`taskContent`): JSON object with exactly `subject`, `description`, `status`, `owner`, `parent`, `rank`, `blocked_by`, `leased_until`, `metadata` (use a private `taskDoc` struct with `omitempty` on all but `subject`, `status`, `rank`).
6. `loadTasks`: `SELECT seq, memory_id, revision, sender, created_at, content FROM messages WHERE channel=? AND memory_id IS NOT NULL AND tombstone=0`. Rows whose content is not a JSON object with a non-empty `subject` and a valid `status` are skipped (an old binary may have written a plain memory there). Then: a task whose `parent` is not in the set is treated as root (`Parent` left as stored, but placed at depth 0); `OpenBlockers` = ids in `BlockedBy` that are in the set and not `completed`; `Blocked = len(OpenBlockers) > 0`; result sorted in depth-first tree order, siblings by `Rank` then `ID`. Guard the tree walk with a visited set so a corrupt parent loop cannot recurse forever (tasks in a loop are emitted as roots).
7. `placeRank(ts, self, parent, before, after)`: siblings = tasks with that effective parent, excluding `self`, in rank order. `before` and `after` both non-zero → `validation` "before and after are mutually exclusive". A named sibling that is not among those siblings → `validation` "task %d is not a sibling under parent %d". `after=x` → `rankBetween(x.Rank, next.Rank or "")`; `before=x` → `rankBetween(prev.Rank or "", x.Rank)`; neither → `rankBetween(last.Rank or "", "")`. If two neighbors have equal ranks (only possible from a corrupt write), fall back to `rankBetween(lo, "")`.
8. `validateTaskLinks(ts, t)`: `Parent != 0` must be a task in `ts` and `!= t.ID`, and following parents from `t.Parent` must not reach `t.ID`; each `BlockedBy` id must be in `ts` and `!= t.ID`; at most 64; no duplicates (dedupe silently); following `BlockedBy` edges from each blocker must not reach `t.ID`. Each violation → `validation` naming the id.
9. `TaskCreate` pipeline (mirror `Send`): `auth(b.db)`; validate channel is a task channel (`validation`) and exists (`not_found`); subject 1-256 bytes, no control characters (`validation`); `lockKey` + read-only `checkReceipt`; preflight `sendEnvelope` with a provisional document (rank `"V"`); `inspect("task_create", as, in)`; `checkCapacity`; `Begin`; `auth(tx)`; `checkReceipt(tx)`; `loadTasks(tx)`; `validateTaskLinks`; `placeRank(ts, 0, in.Parent, in.Before, in.After)`; authoritative `sendEnvelope`; `limits.allow`; `insertMessage(tx, as, context, SendInput{Channel, Content}, "memory")` (this sets `memory_id=seq, revision=1`); `storeReceipt`; `Commit`. Do **not** call `embedSoon`. Status is `pending`, owner empty. Return the task as `loadTasks` would render it (re-load inside the tx before commit and pick it by id).
10. `TaskGet`: `auth`; `liveRevision(b.db, id)` → channel; not a task channel → `not_found` "memory %d is not a task"; `loadTasks`; return the task by id (`not_found` if its row was skipped as malformed).
11. `TaskList`: `auth`; channel must be a task channel and exist; `loadTasks`; filter by `Status`/`Owner` when set (an invalid status → `validation`); map to `TaskSummary` with `Depth`; `trimToBytes` against `min(b.cfg.ResultDefaultKiB*1024, 4<<20)` the way `ListChannels` does (read `channels.go:103-121` and copy its cap expression exactly).
12. `writeTaskRevision(tx, as, context, t, typ)`: tombstone `t.seq` (`UPDATE messages SET tombstone=1, tombstone_at=? WHERE seq=?`), `insertMessage(tx, as, context, SendInput{Channel: t.Channel, Type: typ, Content: doc}, "ordinary")`, then `UPDATE messages SET memory_id=?, revision=? WHERE seq=?` with `t.ID`, `t.Revision+1`. Returns `t` with `seq`, `Revision`, `UpdatedAt`, `UpdatedBy` updated. (Used by Tasks 3 and 4; write and unit-test it here through a small exported-in-test helper or directly in Task 3's tests.)

- [ ] **Step 1: Write failing tests** in `internal/bus/tasks_test.go`. Use the existing fixtures `newTestBus(t)` and `twoAgents(t)` (`dm_test.go:205`; returns `b` with "Sam" registered and `other` with "Pat"). Add this helper and these tests:

```go
package bus

import (
	"strings"
	"testing"
)

func taskList(t *testing.T, b *Bus) string {
	t.Helper()
	if _, err := b.CreateChannel("Sam", "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	return "tasks/work"
}

func mustCreate(t *testing.T, b *Bus, in TaskCreateInput) Task {
	t.Helper()
	if in.Channel == "" {
		in.Channel = "tasks/work"
	}
	tk, err := b.TaskCreate("Sam", in)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var be *Error
	if !errors.As(err, &be) || be.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
```

(Use whatever the package's error type and code field are actually called: read `errors.go` first and adapt `wantCode`; add `"errors"` to the imports.)

Tests (each a separate `func Test…`):

- `TestTaskChannelNameRules`: `tasks/work`+`memory` ok; `tasks/work`+`ordinary` → validation; `tasks` → validation; `tasks/` → validation; `tasks/a/b` → validation; `x/y` → validation (unchanged).
- `TestTaskCreateGetRoundTrip`: create with description and metadata; `TaskGet` returns the same subject, `pending`, empty owner, non-empty rank, `Revision==1`, `UpdatedBy=="Sam"`, `Blocked==false`.
- `TestTaskCreateValidation`: empty subject, 257-byte subject, subject with `\n` → validation; non-task channel → validation; missing `tasks/none` → not_found; unknown parent → validation; unknown `blocked_by` → validation; `before` and `after` together → validation; `after` naming a task under a different parent → validation.
- `TestTaskListTreeOrderSurvivesInsertsAndDeletes`:

```go
func TestTaskListTreeOrderSurvivesInsertsAndDeletes(t *testing.T) {
	b, _ := twoAgents(t)
	taskList(t, b)
	a := mustCreate(t, b, TaskCreateInput{Subject: "a"})
	c := mustCreate(t, b, TaskCreateInput{Subject: "c"})
	bb := mustCreate(t, b, TaskCreateInput{Subject: "b", After: a.ID})
	a1 := mustCreate(t, b, TaskCreateInput{Subject: "a1", Parent: a.ID})
	a0 := mustCreate(t, b, TaskCreateInput{Subject: "a0", Parent: a.ID, Before: a1.ID})
	order := func() string {
		l, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, x := range l {
			s = append(s, strings.Repeat(">", x.Depth)+x.Subject)
		}
		return strings.Join(s, " ")
	}
	if got := order(); got != "a >a0 >a1 b c" {
		t.Fatalf("order=%q", got)
	}
	_ = bb
	_ = c
	_ = a0
}
```

  (Task 3 extends this test with a delete and a move once `TaskUpdate` exists.)
- `TestTaskBlockedIsDerived`: `x` blocked by `y` → `TaskGet(x)` has `Blocked==true`, `OpenBlockers==[y]`; the list summary carries the same `OpenBlockers`.
- `TestPlainWritesRefusedOnTaskChannels`: `Send` to `tasks/work`, `EditMemory` and `DeleteMemory` of a task id → validation mentioning `task_update`; `GetMemory` of the task id succeeds and its content is JSON containing `"subject"`.
- `TestLoadTasksSkipsForeignRows`: insert a non-JSON memory row directly (`b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,content,bytes,memory_id,revision) VALUES('tasks/work','x','',0,'not json',8,999,1)")`); `TaskList` still succeeds and omits it.
- `TestTaskCreateIdempotentReplay`: same `IdempotencyKey` twice returns the same id and creates one task.
- `TestEmbedderSkipsTaskRows`: with the package's existing embed test server helper (see `embed_test.go` for how tests stand up an embedder), create one task and one memory in `memory`; run `b.embedBatch(ctx)`; assert exactly one row in `embeddings` and that it is the memory's seq.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/bus/ -run 'TestTask|TestPlainWrites|TestLoadTasks|TestEmbedderSkips' -count=1`
Expected: FAIL to compile, `undefined: TaskCreateInput`.

- [ ] **Step 3: Implement** `tasks.go` and the four guards per the rules above.

- [ ] **Step 4: Run the package**

Run: `CGO_ENABLED=0 go test ./internal/bus/ -count=1 && golangci-lint run ./internal/bus/...`
Expected: `ok`, `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/bus/tasks.go internal/bus/tasks_test.go internal/bus/channels.go internal/bus/messages.go internal/bus/memories.go internal/bus/embed.go
git commit -m "feat(bus): task list channels with create, get, and ordered tree list"
```

---

### Task 3: TaskUpdate rules, claim, release

**Files:**
- Create: `internal/bus/tasks_update.go`, `internal/bus/tasks_update_test.go`

**Interfaces:**
- Consumes: everything Task 2 produces.
- Produces:

```go
type TaskPatch struct {
	ID              int64             `json:"task_id"`
	Status          *string           `json:"status,omitempty"`
	Owner           *string           `json:"owner,omitempty"`
	Subject         *string           `json:"subject,omitempty"`
	Description     *string           `json:"description,omitempty"`
	Parent          *int64            `json:"parent,omitempty"`
	Before          int64             `json:"before,omitempty"`
	After           int64             `json:"after,omitempty"`
	AddBlockedBy    []int64           `json:"add_blocked_by,omitempty"`
	RemoveBlockedBy []int64           `json:"remove_blocked_by,omitempty"`
	LeasedUntil     *int64            `json:"leased_until,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Delete          bool              `json:"delete,omitempty"`
	Force           bool              `json:"force,omitempty"`
	IdempotencyKey  string            `json:"idempotency_key,omitempty"`
}

type TaskUpdateResult struct {
	Task     Task  `json:"task"`
	Replaced int64 `json:"replaced"`          // seq of the revision this one replaced
	Deleted  bool  `json:"deleted,omitempty"`
}

func (b *Bus) TaskUpdate(as string, p TaskPatch) (TaskUpdateResult, error)
func (b *Bus) TaskClaim(as string, id, leasedUntil int64, key string) (TaskUpdateResult, error)
func (b *Bus) TaskRelease(as string, id int64, key string) (TaskUpdateResult, error)

// applyPatch is pure: it returns the patched task or the rule violation.
// abandonedNow reports whether cur is abandoned (Task 4 wires the real
// value; until then TaskUpdate passes false).
func applyPatch(ts []Task, cur Task, p TaskPatch, as string, now int64, ownerKnown func(string) (bool, error)) (Task, error)
```

The wrappers are exactly:

```go
func (b *Bus) TaskClaim(as string, id, leasedUntil int64, key string) (TaskUpdateResult, error) {
	st := "in_progress"
	p := TaskPatch{ID: id, Owner: &as, Status: &st, IdempotencyKey: key}
	if leasedUntil != 0 {
		p.LeasedUntil = &leasedUntil
	}
	return b.TaskUpdate(as, p)
}

func (b *Bus) TaskRelease(as string, id int64, key string) (TaskUpdateResult, error) {
	st, none, zero := "pending", "", int64(0)
	return b.TaskUpdate(as, TaskPatch{ID: id, Owner: &none, Status: &st, LeasedUntil: &zero, IdempotencyKey: key})
}
```

**`applyPatch` rules, in this order** (spec sections Status, Ownership guard, Lease, Dependencies, Hierarchy, Order, Errors):

1. `Delete` with any other change field set (anything but `Force`, `IdempotencyKey`) → `validation` "delete cannot be combined with other changes".
2. Owner guard: if `cur.Status == "in_progress" && cur.Owner != as && !p.Force` → `conflict` "task %d is in progress, owned by %s". (Task 4 makes an abandoned task `pending` before this runs, which is how "anyone can release an abandoned task" holds.)
3. Taking ownership: if `p.Owner != nil && *p.Owner != "" && *p.Owner != cur.Owner && cur.Owner != "" && !p.Force` → `conflict` "task %d is owned by %s". (With rule 2, a lost claim race lands here or on rule 2 depending on the winner's status.)
4. Delete: if any task in `ts` has `Parent == cur.ID` → `conflict` "task %d has subtasks: %v" (not bypassed by `Force`). Otherwise return `cur` with a delete marker (the caller tombstones without inserting).
5. Apply scalar fields: `Subject` (1-256 bytes, no control chars), `Description`, `Metadata` (merge; an empty value deletes the key), `Owner`, `Parent`, `AddBlockedBy`/`RemoveBlockedBy` (remove first, then add, dedupe).
6. New non-empty owner differing from `cur.Owner`: `ownerKnown(owner)` must be true, else `not_found` "identity %q has never registered".
7. Status, when `p.Status != nil`: must be one of the three values (`validation`). Transitions: `completed → in_progress` → `validation` "reopen the task (status pending) before starting it". `→ pending` (from any status, including an explicit re-set) clears `Owner` unless the same patch sets a non-empty `Owner` (assignment), and clears `LeasedUntil`. `→ completed`: if `Owner == ""` set `Owner = as`; clear `LeasedUntil`. `→ in_progress`: `Owner` must be non-empty after the patch, else `validation` "an in_progress task needs an owner"; the patched task must not be blocked (compute open blockers against `ts` with the patched `BlockedBy`), else `conflict` "task %d is blocked by %v".
8. Lease: `p.LeasedUntil != nil`: non-zero and `<= now` → `validation` "leased_until must be in the future (or 0 to clear)". After status is settled, if the task is not `in_progress`, force `LeasedUntil = 0`.
9. Links: `validateTaskLinks(ts, patched)`.
10. Order: if `p.Parent != nil` or `Before`/`After` set → `patched.Rank, err = placeRank(ts, cur.ID, patched.Parent, p.Before, p.After)`.
11. If the patched task equals `cur` in every stored field → return it with a no-op marker; `TaskUpdate` then returns the current task without writing a revision or charging the rate limit.

**`TaskUpdate` pipeline** (mirror `EditMemory`, `memories.go:92`): `auth(b.db)`; `lockKey`; read-only `checkReceipt` (payload = the patch with `IdempotencyKey` blanked); `liveRevision(b.db, p.ID)` → channel, must be a task channel else `not_found`; `inspect("task_update", as, p)`; `checkCapacity` (skip for delete); `Begin`; `auth(tx)`; `checkReceipt(tx)`; `loadTasks(tx, channel)`; `cur := taskByID` (`not_found` if gone); `applyPatch` with `ownerKnown = func(o string) (bool, error)` that checks `SELECT 1 FROM channels WHERE name=?` for `DMChannel(o)`; on delete: rate-charge `limits.allow(as, 0, now)`, tombstone `cur.seq`, `storeReceipt`, commit, return `{Task: cur, Replaced: cur.seq, Deleted: true}`; otherwise build the document, authoritative `sendEnvelope(tx, as, SendInput{Channel, Type, Content}, true)`, `limits.allow(as, size, now)`, `writeTaskRevision(tx, as, context, patched, typ)` where `typ` is `"forced"` when `p.Force` actually bypassed rule 2 or 3 and `""` otherwise; re-`loadTasks(tx)` to return derived fields; `storeReceipt`; `Commit`. Rate-limit error text names the limit the way `EditMemory` does after the 2026-09-17 sweep (copy its message).

- [ ] **Step 1: Write failing tests** in `tasks_update_test.go` (reuse `taskList`, `mustCreate`, `wantCode` from Task 2). One `func Test…` per bullet; assert both the error code and that the stored task is unchanged after a refusal (`TaskGet` revision unchanged).

  - `TestClaimSetsOwnerAndStatus`: `TaskClaim("Sam", id, 0, "")` → `in_progress`, owner `Sam`, revision 2.
  - `TestRacingClaimsHaveOneWinner`:

```go
func TestRacingClaimsHaveOneWinner(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	errs := make(chan error, 2)
	go func() { _, err := b.TaskClaim("Sam", tk.ID, 0, ""); errs <- err }()
	go func() { _, err := other.TaskClaim("Pat", tk.ID, 0, ""); errs <- err }()
	var failed int
	for range 2 {
		if err := <-errs; err != nil {
			wantCode(t, err, "conflict")
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("want exactly one loser, got %d", failed)
	}
	got, _ := b.TaskGet("Sam", tk.ID)
	if got.Status != "in_progress" || (got.Owner != "Sam" && got.Owner != "Pat") {
		t.Fatalf("task after race: %+v", got)
	}
}
```

  - `TestClaimAndRawUpdateAreEquivalent`: the patch `{owner: Sam, status: in_progress}` through `TaskUpdate` on a task owned by Pat gives the same `conflict` as `TaskClaim`.
  - `TestOwnerGuard`: Pat updating, completing, moving, or deleting Sam's `in_progress` task → conflict naming Sam; Sam can do each.
  - `TestForceOverridesOwnerGuardAndIsRecorded`: Pat with `Force` reassigns Sam's task to Pat; `MemoryRevisions` shows the newest revision with `Type == "forced"`; a non-bypassing forced update (Sam forcing his own task) has empty type.
  - `TestForceDoesNotBypassBlockingOrChildren`.
  - `TestReleaseClearsOwnerAndLease`.
  - `TestStatusTransitions`: table of (from, patch) → expected status/owner or error code, covering all six rows of the spec table plus assignment (`owner` set on a pending task leaves it pending) and `pending → completed` recording the caller as owner.
  - `TestUnknownOwnerIsNotFound`: `owner: "Nobody"` → not_found; `owner: "Pat"` works.
  - `TestLeaseValidation`: past non-zero → validation; `0` clears; future accepted; completing clears it; a lease on a pending task is cleared.
  - `TestBlockedTaskCannotStart` then complete the blocker → claim succeeds; delete the blocker instead → claim succeeds; reopen a completed blocker after the dependent started → dependent untouched.
  - `TestDependencyCycleRejected`: a→b, b→c, then c blocked_by a → validation; self → validation.
  - `TestParentRules`: unknown parent, self parent, loop (a under b, then b under a) → validation; `parent: 0` moves to root and appends last.
  - `TestDeleteRefusedWithChildren`: conflict listing the child; after reparenting the child to root, delete succeeds and `TaskGet` → not_found.
  - `TestMoveWithBeforeAfter`: extend Task 2's order test: move `c` before `a` → `c a >a0 >a1 b`; delete `b` → `c a >a0 >a1`; create `d` after `c` → `c d a >a0 >a1`.
  - `TestNoopPatchWritesNothing`: patch equal to current state → same revision returned.
  - `TestTaskUpdateIdempotentReplayAfterDelete`: keyed complete, then delete the task, then replay the keyed complete → the stored result, not not_found.
  - `TestDeleteCombinedWithChangesIsValidation`.

- [ ] **Step 2: Run to verify they fail** — `go test ./internal/bus/ -run 'TestClaim|TestRacing|TestOwnerGuard|TestForce|TestRelease|TestStatus|TestUnknownOwner|TestLease|TestBlocked|TestDependency|TestParent|TestDeleteRefused|TestMove|TestNoop|TestTaskUpdate|TestDeleteCombined' -count=1`. Expected: compile failure, `undefined: TaskPatch`.

- [ ] **Step 3: Implement** `tasks_update.go`.

- [ ] **Step 4: Run** `CGO_ENABLED=0 go test ./internal/bus/ -count=1 && go test -race ./internal/bus/ -run 'TestRacing' -count=20 && golangci-lint run ./internal/bus/...`. Expected: `ok`, `ok`, `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/bus/tasks_update.go internal/bus/tasks_update_test.go
git commit -m "feat(bus): task_update rules with claim and release wrappers"
```

---

### Task 4: Abandonment and fix-up

**Files:**
- Create: `internal/bus/tasks_reclaim.go`, `internal/bus/tasks_reclaim_test.go`
- Modify: `internal/bus/tasks.go` (`TaskGet`, `TaskList`), `internal/bus/tasks_update.go` (`TaskUpdate`), `internal/bus/maintenance.go` (`Tick` steps)

**Interfaces:**
- Consumes: `loadTasks`, `writeTaskRevision`, `Task`.
- Produces:

```go
// abandoned reports whether t is in_progress with an expired lease or an
// owner that has no live session.
func (b *Bus) abandoned(q queryRower, t Task, now int64) (bool, error)

// reclaimAbandoned returns every abandoned task among ids (all tasks in the
// channel when ids is nil) to pending, owner and lease cleared, as a
// revision of type "reclaimed" sent by as (empty for the tick). It is not
// rate-charged. Returns how many it reclaimed.
func (b *Bus) reclaimAbandoned(tx *sql.Tx, as, context, channel string, ids []int64) (int, error)
```

**Rules:**

1. `abandoned`: `t.Status == "in_progress" && ((t.LeasedUntil != 0 && t.LeasedUntil <= now) || !live)` where `live` is `SELECT 1 FROM sessions WHERE sender=? AND heartbeat>=?` with `t.Owner`, `now-attachmentExpiryMs`.
2. `TaskUpdate`: inside the tx, after `auth(tx)` and the receipt check and before `applyPatch`, call `reclaimAbandoned(tx, as, context, channel, []int64{p.ID})` and reload the list if it returned > 0. The context comes from `b.senderContext(tx, as)`.
3. `TaskGet` / `TaskList`: load read-only first; if no loaded task (the one task for get; any task for list) is abandoned, return without a write transaction. Otherwise `Begin`, `auth(tx)`, `reclaimAbandoned` (ids = the one task for get, nil for list), `Commit`, and reload. A failed fix-up must not fail the read with anything but `internal`/`not_registered`.
4. Tick: add a step `{"abandoned tasks", …}` after `"stale sessions"` (so sessions that just expired count as dead): for each channel `SELECT name FROM channels WHERE name LIKE 'tasks/%'`, open one tx per channel, `reclaimAbandoned(tx, "", "", channel, nil)`, commit; stop early when `tickCtx.Err() != nil`.
5. Reclaim revisions: `writeTaskRevision(tx, as, context, t, "reclaimed")` with `Status="pending"`, `Owner=""`, `LeasedUntil=0`. No `limits.allow` call, no `inspect`, no receipt.
6. Add the comment `// ponytail: fix-up, list, and cycle checks parse every live task in the channel; add an additive task_index table keyed by memory_id if lists grow past a few thousand tasks.` above `loadTasks`.

- [ ] **Step 1: Write failing tests** in `tasks_reclaim_test.go`. To kill a session, delete its row: `b.db.Exec("DELETE FROM sessions WHERE sender='Pat'")`. To expire a lease, set the bus clock: check how other tests advance time (`rg -n 'b.Now =' internal/bus/*_test.go`) and do the same.

  - `TestClaimOfTaskWithDeadOwnerSucceeds`: Pat claims, Pat's session row is deleted, Sam claims without force → owner Sam; `MemoryRevisions` types, oldest first: `""`, `""` (Pat's claim), `"reclaimed"`, `""` (Sam's claim).
  - `TestClaimOfTaskWithExpiredLeaseSucceeds`: Pat claims with a lease 1 s ahead; advance the clock 2 s; Sam claims → ok. Before the clock advances Sam's claim is `conflict`.
  - `TestGetFixesUpAbandonedTask` and `TestListFixesUpAbandonedTasks`: after Pat's session is deleted, `TaskGet`/`TaskList` return `pending` with empty owner, and the stored revision has type `reclaimed` with `Sender == "Sam"`.
  - `TestReadsDoNotWriteWhenNothingIsAbandoned`: `TaskList` twice leaves `SELECT max(seq) FROM messages` unchanged.
  - `TestTickReclaimsWithEmptySender`: Pat claims; delete Pat's session; `b.Tick(context.Background())`; the newest revision has `Sender == ""` and type `reclaimed`; Sam, subscribed to `tasks/work`, receives it via `Receive` without `IncludeOwn`.
  - `TestReclaimIsNotRateCharged`: configure the smallest rate limit the config allows (see how `receipts_test.go` sets it), exhaust Sam's bucket with sends, then `TaskList` over an abandoned task still reclaims it.
  - `TestAnyoneCanReleaseAbandonedTask`: Pat claims, Pat's session deleted, Sam calls `TaskRelease` → ok, task pending (after fix-up the release is a no-op patch).
  - `TestLiveOwnerWithoutLeaseIsNeverAbandoned`.

- [ ] **Step 2: Run to verify they fail.** `go test ./internal/bus/ -run 'Reclaim|Abandoned|DeadOwner|ExpiredLease|FixesUp|ReadsDoNotWrite' -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=0 go test ./internal/bus/ -count=1 && go test -race ./internal/bus/ -count=1 && golangci-lint run ./internal/bus/...` → `ok`, `ok`, `0 issues.`
- [ ] **Step 5: Commit**

```bash
git add internal/bus/tasks_reclaim.go internal/bus/tasks_reclaim_test.go internal/bus/tasks.go internal/bus/tasks_update.go internal/bus/maintenance.go
git commit -m "feat(bus): reclaim abandoned tasks from update, get, list, and the tick"
```

---

### Task 5: MCP tools

**Files:**
- Modify: `internal/mcpserver/server.go` (tool registration, after `delete_memory`), `internal/mcpserver/server_test.go` (tool-count or tool-list assertions, if any: `rg -n 'delete_memory|ListTools' internal/mcpserver/*_test.go`)
- Test: `internal/mcpserver/integration_more_test.go`

**Interfaces:**
- Consumes: `bus.TaskCreateInput`, `bus.TaskPatch`, `bus.TaskListInput`, `b.TaskCreate/TaskGet/TaskList/TaskUpdate/TaskClaim/TaskRelease`.
- Produces: tools `task_create`, `task_claim`, `task_release`, `task_update`, `task_get`, `task_list`. Input structs follow the file's existing pattern (an `As string \`json:"as,omitempty"\`` field plus the embedded bus input; read `editIn`/`memoryIn` and copy their shape, including how required fields are left optional so the bus produces the JSON error envelope):

```go
type taskCreateIn struct {
	As string `json:"as,omitempty"`
	bus.TaskCreateInput
}
type taskUpdateIn struct {
	As string `json:"as,omitempty"`
	bus.TaskPatch
}
type taskClaimIn struct {
	As             string `json:"as,omitempty"`
	TaskID         int64  `json:"task_id"`
	LeasedUntil    int64  `json:"leased_until,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type taskReleaseIn struct {
	As             string `json:"as,omitempty"`
	TaskID         int64  `json:"task_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type taskGetIn struct {
	As     string `json:"as,omitempty"`
	TaskID int64  `json:"task_id"`
}
type taskListIn struct {
	As string `json:"as,omitempty"`
	bus.TaskListInput
}
```

Descriptions (verbatim):

- `task_create`: "Agentbus: add a task to a task list (a memory channel named tasks/<name>; create one with create_channel kind=memory). New tasks are pending and unowned. parent nests it under an existing task in the same list; before or after (a sibling task id) places it, default last. blocked_by lists task ids in the same list that must complete first."
- `task_claim`: "Agentbus: claim a task: if it has no owner you become its owner and it becomes in_progress. Fails with conflict if someone else owns it or it is blocked. A task whose owner's session has ended, or whose lease ran out, counts as unowned. leased_until (unix ms UTC, optional) sets a lease you must renew with task_update before it passes."
- `task_release`: "Agentbus: release a task: clears the owner and lease and returns it to pending. Do this when you stop working on a task you have not completed."
- `task_update`: "Agentbus: change a task by id; only the fields you pass change. status is pending, in_progress, or completed. While a task is in_progress only its owner may change or delete it; force=true overrides that for anyone and is recorded. owner=\"\" clears the owner, parent=0 moves the task to the top level, leased_until=0 clears the lease (any other value must be in the future). before/after reorder among siblings. delete=true deletes a task that has no subtasks."
- `task_get`: "Agentbus: get one task in full, with blocked and open_blockers derived from its blocked_by tasks."
- `task_list`: "Agentbus: list a task list's tasks in order (each task followed by its subtasks; depth gives the nesting), without descriptions. Optional status and owner filters. Subscribe to the tasks/<name> channel to be told about changes through receive."

Also append to the existing descriptions: `create_channel` + " A task list is a memory channel named tasks/<name>."; `send` + " Task lists (tasks/...) do not accept send; use the task tools."

- [ ] **Step 1: Write the failing integration test** in `integration_more_test.go`, following the file's existing spawn/call helpers (read the first 120 lines for `spawn`, `call`/`callSafe`, and how two processes share one data dir):

  `TestTaskClaimContentionAcrossProcesses`: process A registers "Sam", process B registers "Pat"; A calls `create_channel {name: "tasks/work", kind: "memory"}`, B subscribes to `tasks/work`; A `task_create {channel, subject: "x"}`; both call `task_claim` concurrently; exactly one result is an error envelope with `code == "conflict"`; B's `receive` returns at least the create revision with `channel == "tasks/work"`; `task_list` from either shows one `in_progress` task; `send` to `tasks/work` returns `code == "validation"`.

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/mcpserver/ -run TestTaskClaimContention -count=1` → FAIL (unknown tool).
- [ ] **Step 3: Implement** the six registrations (`return result(b.TaskClaim(in.As, in.TaskID, in.LeasedUntil, in.IdempotencyKey))`, and so on) and fix any tool-list assertions.
- [ ] **Step 4: Run** `CGO_ENABLED=0 go test ./internal/mcpserver/ -count=1 && golangci-lint run ./internal/mcpserver/...` → `ok` (about 30 s), `0 issues.` Run long commands in the background rather than polling.
- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/integration_more_test.go internal/mcpserver/server_test.go
git commit -m "feat(mcp): task_create, task_claim, task_release, task_update, task_get, task_list"
```

---

### Task 6: TUI read-only task tree

**Files:**
- Create: `internal/tui/tasks.go`, `internal/tui/tasks_test.go`
- Modify: `internal/tui/view.go` (`iconTasks`, rail icon choice, `renderStream` dispatch), `internal/tui/model.go` (load on select/receive; disabled keys), `internal/tui/msgs.go` (`tasksMsg`), `internal/tui/help.go` (one line)

**Interfaces:**
- Consumes: `bus.IsTaskChannel`, `(*bus.Bus).TaskList(as string, in bus.TaskListInput) ([]bus.TaskSummary, error)`, `bus.TaskSummary{ID, Subject, Status, Owner, Parent, Depth, OpenBlockers, LeasedUntil}`.
- Produces: `type tasksMsg struct{ ch string; tasks []bus.TaskSummary; err error }`; `func (m *Model) loadTasks(ch string) tea.Cmd`; `func (m Model) renderTasks(ch string) string`; model field `tasks map[string][]bus.TaskSummary`; constant `tasksReadOnlyToast = "task lists are read-only here"`.

**Behavior:**

1. Rail: `iconTasks = "\U0001F4CB️ "` (clipboard); chosen when `bus.IsTaskChannel(c.Name)`, before the `memory` check. Color stays the memory color (no `chanStyle` change).
2. `loadTasks(ch)` returns a `tea.Cmd` that calls `m.c.b.TaskList(m.c.as, bus.TaskListInput{Channel: ch})` and returns a `tasksMsg`. Issue it from `showSelected` when the selected channel is a task channel, and from the received-messages handler whenever any delivered message's channel is a task channel (one cmd per distinct channel per batch). Handle `tasksMsg` in `Update`: store `m.tasks[ch]`, `refreshStream()`; on error show the existing error toast.
3. `renderStream`: at the top, after the `ch == ""` check, `if bus.IsTaskChannel(ch) { return m.renderTasks(ch) }`.
4. `renderTasks`: no tasks → dim "no tasks yet in <ch>". Otherwise one line per summary: `strings.Repeat("  ", min(t.Depth, 6))` + mark + " #<id> " + subject, then dim suffixes: ` @owner` when set; ` ⊘ #12 #15` when `OpenBlockers` is non-empty; ` lease 4m` (`shortDur` of `time.UnixMilli(t.LeasedUntil).Sub(now)`, where `now` is the model's existing clock source, or `lease expired` when not positive) when `LeasedUntil != 0`. Marks: `○` pending, `◐` in_progress, `●` completed, `⊘` replaces `○` when a pending task has open blockers. Completed rows render entirely dim. Truncate each line to the stream width with `lipgloss.NewStyle().MaxWidth(w)`.
5. Disabled on a task channel (when `bus.IsTaskChannel(m.selName())`): `i`, `enter`, `r`, and `m` show `tasksReadOnlyToast` via `m.showToast` and do nothing else; tab cycling skips the stream and compose panes (in `paneKey`, treat both as "nothing to focus" for task channels); `d` still opens the channel-delete confirmation; `s` still toggles subscribe. The TUI starts in compose mode: if the initially selected channel is a task channel, typing must not send: in `updateInsert`, when the selection is a task channel, `enter` shows the toast instead of sending.
6. Help overlay: add the row `{"(task lists)", "tasks/ channels show the task tree; read-only"}` at the end of `helpLines`.

- [ ] **Step 1: Write failing tests** in `tasks_test.go` with the package fixture (`newFixture(t)`, `f.key`, `f.receive`, `f.run`; see `model_test.go:60-160`). The fixture's agent identity and how it sends are in `f.agentSend`; create tasks by calling the fixture's bus handle directly (find the handle the fixture uses for the agent side: `rg -n 'func newFixture' -A30 internal/tui/model_test.go`).

  - `TestTaskChannelRendersTree`: create `tasks/work` with tasks `a`, `a1` (child of a, claimed by the agent), `b` (blocked by a). Select the channel (`f.key("esc")` then `down` until `f.m.selName() == "tasks/work"`), run the returned load cmd through the fixture. Assert `f.m.renderStream()` lines, ANSI stripped with the helper the other view tests use: line 0 contains `○ #` and `a`; line 1 starts with two spaces and contains `◐` and `@`; line 2 contains `⊘` and `#<a's id>`.
  - `TestTaskChannelRailIcon`: `renderRails()` row for `tasks/work` contains `iconTasks`.
  - `TestTaskChannelIsReadOnly`: with the task channel selected, `i`, `enter`, `r`, `m` each leave `f.m.mode == modeNormal` and set `f.m.toast == tasksReadOnlyToast`; `tab` leaves `f.m.pane() == paneChannels`; `d` sets `modeConfirmChannel`.
  - `TestTaskTreeRefreshesOnRevision`: after the first render, the agent completes `a` through the bus; `f.receive(t)`; the rendered tree shows `●` for `a` and `b` is no longer `⊘`.
  - `TestTaskIndentIsCapped`: a chain 8 deep renders its deepest row with 12 leading spaces.

- [ ] **Step 2: Run to verify they fail** — `go test ./internal/tui/ -run 'TestTask' -count=1 -timeout 60s` → FAIL (`undefined: tasksReadOnlyToast`).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=0 go test ./internal/tui/ -count=1 -timeout 120s && go test -race ./internal/tui/ -count=1 -timeout 300s && golangci-lint run ./internal/tui/...` → `ok`, `ok`, `0 issues.` Always pass `-timeout`: a fixture test that loops on a key that no longer moves focus hangs forever otherwise.
- [ ] **Step 5: Commit**

```bash
git add internal/tui/tasks.go internal/tui/tasks_test.go internal/tui/view.go internal/tui/model.go internal/tui/msgs.go internal/tui/help.go
git commit -m "feat(tui): read-only task tree for tasks/ channels"
```

---

### Task 7: Guidance, persistent subscribe, docs

**Files:**
- Modify: `internal/cli/identity.go` (the `protocol` constant), `internal/cli/skills/using-agentbus/SKILL.md`, `internal/cli/subscribe.go` + `internal/cli/subscribe_test.go` (accept `tasks/<name>`), `internal/repoconfig/repoconfig.go` if it validates channel names (`rg -n 'NameRule|dm/' internal/cli/subscribe.go internal/repoconfig/repoconfig.go`), `internal/mcpserver/server.go` only if `subscribe persistent=true` rejects the name, `README.md`, `docs/install.md`
- Test: `internal/cli/subscribe_test.go`, `internal/cli/cli_test.go` (any test that pins the protocol text: `rg -n 'protocol' internal/cli/*_test.go`)

**Interfaces:**
- Consumes: `bus.TaskPrefix`, `bus.NameRule`.
- Produces: `agentbus subscribe tasks/<name>` and MCP `subscribe {persistent: true}` accept a task list name (prefix stripped, remainder checked with `bus.NameRule`); `dm/` stays rejected.

- [ ] **Step 1: Write the failing test** in `subscribe_test.go`, next to the existing test that rejects `dm/x` (commit 7a0359d added it; find it with `rg -n 'dm/' internal/cli/subscribe_test.go`): `agentbus subscribe tasks/work` exits 0 and `.local/agentbus.json` lists `tasks/work`; `tasks/` and `tasks/a/b` are rejected with the same message shape the file uses for invalid names.
- [ ] **Step 2: Run to verify it fails** — `go test ./internal/cli/ -run Subscribe -count=1` → FAIL.
- [ ] **Step 3: Implement** the name check (one shared helper in the file that already validates; do not duplicate the rule in two packages).
- [ ] **Step 4: Protocol text.** In `internal/cli/identity.go`, add one bullet to `protocol`, after the memory-channel bullet:

```
- For work shared between agents, use a task list: a memory channel named
  tasks/<repo>. Claim a task with task_claim before working on it, and
  complete it (task_update status=completed) or task_release it when you stop.
```

  Update any test that pins the protocol text.
- [ ] **Step 5: Skill.** In `internal/cli/skills/using-agentbus/SKILL.md`, add after the "Direct messages" section:

```markdown
## Task lists

A task list is a memory channel named `tasks/<name>`; by convention a
repository's list is `tasks/<repo>`. Create it with `create_channel`
(`kind: memory`) and `subscribe` to it to hear changes through `receive`.
Use a list when more than one agent could pick up the work, or the work
must survive your session; use chat for everything else.

- `task_create` adds a task. `parent` nests it; `before` / `after` place it
  among its siblings; `blocked_by` names tasks that must complete first.
- `task_claim` before you start. `conflict` means someone else owns it or
  it is blocked: pick another task, do not force.
- When you stop: `task_update` with `status: completed`, or `task_release`.
  Never leave a task claimed that you are not working on.
- If your session ends, your tasks return to `pending` on their own. Set
  `leased_until` only on a task you will keep renewing; an expired lease
  hands your task to the next claimer.
- `force: true` overrides another owner and is recorded. Use it only when
  the user tells you to.
- `send`, `edit_memory`, and `delete_memory` are refused on task lists.
```

  Add one row to the "What to post, and where" table: `| Work that any of several agents could take | \`tasks/<repo>\` | A task per unit of work; claim before starting |`.
- [ ] **Step 6: Docs.** `README.md`: add the six tools wherever the MCP tools are listed and one paragraph "Task lists" summarizing the spec's Goal section in three sentences. `docs/install.md`: in the TUI paragraph, add "A `tasks/` channel shows its task tree (read-only): subtasks indented, `○ ◐ ● ⊘` for pending, in progress, completed, blocked."
- [ ] **Step 7: Run** `CGO_ENABLED=0 go test ./... -count=1 && golangci-lint run` → all `ok`, `0 issues.`
- [ ] **Step 8: Commit**

```bash
git add internal/cli internal/repoconfig README.md docs/install.md
git commit -m "feat(cli): task list guidance in the protocol, skill, and docs; persistent subscribe accepts tasks/"
```

---

### Task 8 (controller): final review and release v1.3.0

Not for an implementer subagent.

- [ ] Whole-branch review (Fable) of `main..feat/task-lists`, with attention to: every rule reachable only through `TaskUpdate`; no path writes to a `tasks/` channel without the owner guard (grep every `insertMessage` and `tombstone=1` call site); reads never fail because a fix-up failed; the tick step honors `tickCtx`.
- [ ] `go test -race -count=1 ./...`, `CGO_ENABLED=0 go test -count=1 ./...`, `golangci-lint run`, `go vet ./...`.
- [ ] Merge to `main`; update `PROGRESS.md` (deferred-minors sweep, TUI rail/tab fix, per-row ack fix, task lists), `HANDOFF.md`, and close #2 in the release commit message.
- [ ] Release v1.3.0 following the steps the `release/` directory and the v1.2.0 commits (`e6cd42f`, `b6b6890`) show: version bump, tag, GitHub release, tap formula.

## Self-review notes

Spec coverage: Storage → Task 2; Tools → Tasks 2, 3, 5; Abandonment → Task 4; Status, guard, lease, dependencies, hierarchy, order, errors → Task 3; ceiling comment → Task 4 rule 6; TUI → Task 6; guidance → Task 7; testing section → each task's test list plus Task 5's integration scenario. Out-of-scope items have no task.
