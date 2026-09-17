package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// TaskPrefix starts the name of every task-list channel: tasks/<name>.
const TaskPrefix = "tasks/"

// IsTaskChannel reports whether channel is a task list.
func IsTaskChannel(channel string) bool {
	return strings.HasPrefix(channel, TaskPrefix)
}

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

// TaskSummary is one row of a task_list result.
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

// TaskCreateInput is task_create's input.
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

// TaskListInput is task_list's input.
type TaskListInput struct {
	Channel string `json:"channel"`
	Status  string `json:"status,omitempty"`
	Owner   string `json:"owner,omitempty"`
}

// taskDoc is the JSON object stored as a task's memory content: the subset
// of Task that isn't envelope data (id/channel/revision/updated_at/updated_by)
// or derived (blocked/open_blockers).
type taskDoc struct {
	Subject     string            `json:"subject"`
	Description string            `json:"description,omitempty"`
	Status      string            `json:"status"`
	Owner       string            `json:"owner,omitempty"`
	Parent      int64             `json:"parent,omitempty"`
	Rank        string            `json:"rank"`
	BlockedBy   []int64           `json:"blocked_by,omitempty"`
	LeasedUntil int64             `json:"leased_until,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// taskContent returns the JSON document stored as t's memory content.
func taskContent(t Task) (string, error) {
	j, err := json.Marshal(taskDoc{
		Subject: t.Subject, Description: t.Description, Status: t.Status,
		Owner: t.Owner, Parent: t.Parent, Rank: t.Rank, BlockedBy: t.BlockedBy,
		LeasedUntil: t.LeasedUntil, Metadata: t.Metadata,
	})
	if err != nil {
		return "", err
	}
	return string(j), nil
}

// isTaskStatus reports whether s is one of the three task statuses.
func isTaskStatus(s string) bool {
	return s == "pending" || s == "in_progress" || s == "completed"
}

// validateTaskSubject enforces subject 1-256 bytes, no control characters.
func validateTaskSubject(s string) error {
	if len(s) == 0 || len(s) > 256 {
		return errf("validation", false, "subject must be 1-256 bytes")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errf("validation", false, "subject must not contain control characters")
		}
	}
	return nil
}

// dedupeIDs returns ids with duplicates removed, order preserved.
func dedupeIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// loadTasks returns every live task in channel: rows whose content is not a
// JSON object with a non-empty subject and a valid status are skipped (an
// old binary may have written a plain memory there). Derived fields
// (Blocked, OpenBlockers) are filled in, and the result is ordered
// depth-first by the tree (see taskTree). ORDER BY seq makes the scan
// deterministic, which matters for taskTree's cycle fallback.
func loadTasks(q querier, channel string) ([]Task, error) {
	rows, err := q.Query("SELECT seq, memory_id, revision, sender, created_at, content FROM messages WHERE channel=? AND memory_id IS NOT NULL AND tombstone=0 ORDER BY seq", channel)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	var ts []Task
	for rows.Next() {
		var seq, id, rev, createdAt int64
		var sender, content string
		if err := rows.Scan(&seq, &id, &rev, &sender, &createdAt, &content); err != nil {
			return nil, internal(err)
		}
		var d taskDoc
		if err := json.Unmarshal([]byte(content), &d); err != nil || d.Subject == "" || !isTaskStatus(d.Status) {
			continue
		}
		ts = append(ts, Task{
			ID: id, Channel: channel, Revision: rev,
			Subject: d.Subject, Description: d.Description, Status: d.Status,
			Owner: d.Owner, Parent: d.Parent, Rank: d.Rank, BlockedBy: d.BlockedBy,
			LeasedUntil: d.LeasedUntil, Metadata: d.Metadata,
			UpdatedAt: createdAt, UpdatedBy: sender, seq: seq,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	fillDerived(ts)
	order, _ := taskTree(ts)
	return order, nil
}

// fillDerived computes Blocked and OpenBlockers for every task in ts, in
// place: a blocker is open while it is live (in ts) and not completed.
func fillDerived(ts []Task) {
	byID := make(map[int64]*Task, len(ts))
	for i := range ts {
		byID[ts[i].ID] = &ts[i]
	}
	for i := range ts {
		var open []int64
		for _, bid := range ts[i].BlockedBy {
			if blocker, ok := byID[bid]; ok && blocker.Status != "completed" {
				open = append(open, bid)
			}
		}
		ts[i].OpenBlockers = open
		ts[i].Blocked = len(open) > 0
	}
}

// taskByID returns the task in ts with the given id, or nil.
func taskByID(ts []Task, id int64) *Task {
	for i := range ts {
		if ts[i].ID == id {
			return &ts[i]
		}
	}
	return nil
}

// effectiveParent returns parent's effective value for tree purposes: 0
// (root) when parent is not a live task in ts. The design's Hierarchy
// section makes this reachable ("a task whose parent is missing ... is
// treated as a root task"), for example after retention drops the parent.
func effectiveParent(ts []Task, parent int64) int64 {
	if parent != 0 && taskByID(ts, parent) == nil {
		return 0
	}
	return parent
}

// taskTree groups ts by effective parent and walks it depth-first from the
// root, returning both the resulting order (each task followed by its
// children, siblings by rank then id) and each task's depth. A cycle with
// no path from a live root can't be reached by the walk (only possible from
// a corrupt write); its members are emitted last, as extra roots at depth 0.
func taskTree(ts []Task) ([]Task, map[int64]int) {
	children := map[int64][]Task{}
	for _, t := range ts {
		p := effectiveParent(ts, t.Parent)
		children[p] = append(children[p], t)
	}
	for p, sib := range children {
		sort.Slice(sib, func(i, j int) bool {
			if sib[i].Rank != sib[j].Rank {
				return sib[i].Rank < sib[j].Rank
			}
			return sib[i].ID < sib[j].ID
		})
		children[p] = sib
	}
	order := make([]Task, 0, len(ts))
	depth := make(map[int64]int, len(ts))
	visited := make(map[int64]bool, len(ts))
	var walk func(parent int64, d int)
	walk = func(parent int64, d int) {
		for _, t := range children[parent] {
			if visited[t.ID] {
				continue
			}
			visited[t.ID] = true
			depth[t.ID] = d
			order = append(order, t)
			walk(t.ID, d+1)
		}
	}
	walk(0, 0)
	for _, t := range ts {
		if !visited[t.ID] {
			order = append(order, t)
			depth[t.ID] = 0
		}
	}
	return order, depth
}

// followsToSelf reports whether walking from start via next (parent chain
// or blocked_by edges) ever reaches target, guarding against a cycle that
// doesn't involve target itself.
func followsToSelf(ts []Task, start, target int64, next func(Task) []int64) bool {
	seen := map[int64]bool{}
	stack := []int64{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		t := taskByID(ts, id)
		if t == nil {
			continue
		}
		stack = append(stack, next(*t)...)
	}
	return false
}

// validateTaskLinks checks t's parent and blocked_by against the live tasks
// in ts: existence, no self-reference, no cycles, and a cap on blocked_by
// length. Duplicate blocked_by ids are silently skipped rather than
// rejected or double-counted against the cap.
func validateTaskLinks(ts []Task, t Task) error {
	if t.Parent != 0 {
		if t.Parent == t.ID {
			return errf("validation", false, "task %d cannot be its own parent", t.ID)
		}
		if taskByID(ts, t.Parent) == nil {
			return errf("validation", false, "parent %d does not exist", t.Parent)
		}
		if followsToSelf(ts, t.Parent, t.ID, func(x Task) []int64 {
			if x.Parent == 0 {
				return nil
			}
			return []int64{x.Parent}
		}) {
			return errf("validation", false, "parent %d would create a cycle", t.Parent)
		}
	}
	blockers := dedupeIDs(t.BlockedBy)
	if len(blockers) > 64 {
		return errf("validation", false, "blocked_by accepts at most 64 tasks")
	}
	for _, bid := range blockers {
		if bid == t.ID {
			return errf("validation", false, "task %d cannot block itself", t.ID)
		}
		if taskByID(ts, bid) == nil {
			return errf("validation", false, "blocked_by %d does not exist", bid)
		}
		if followsToSelf(ts, bid, t.ID, func(x Task) []int64 { return x.BlockedBy }) {
			return errf("validation", false, "blocked_by %d would create a cycle", bid)
		}
	}
	return nil
}

// rankStep wraps rankBetween, falling back to unbounded-above when lo and hi
// are equal (only possible from a corrupt write): rankBetween's precondition
// is a < b.
func rankStep(lo, hi string) string {
	if hi != "" && lo >= hi {
		hi = ""
	}
	return rankBetween(lo, hi)
}

// placeRank computes the rank for self among the tasks in ts that share
// parent's effective value (self excluded from that sibling set), positioned
// by before/after (sibling task ids, mutually exclusive) or appended after
// the last sibling when neither is set. Comparing by effective parent (see
// effectiveParent) matches an orphaned sibling — one whose stored parent no
// longer exists — which the tree treats as a root.
func placeRank(ts []Task, self, parent, before, after int64) (string, error) {
	if before != 0 && after != 0 {
		return "", errf("validation", false, "before and after are mutually exclusive")
	}
	parent = effectiveParent(ts, parent)
	var sib []Task
	for _, t := range ts {
		if t.ID != self && effectiveParent(ts, t.Parent) == parent {
			sib = append(sib, t)
		}
	}
	sort.Slice(sib, func(i, j int) bool {
		if sib[i].Rank != sib[j].Rank {
			return sib[i].Rank < sib[j].Rank
		}
		return sib[i].ID < sib[j].ID
	})
	named := before
	if after != 0 {
		named = after
	}
	idx := -1
	if named != 0 {
		for i, t := range sib {
			if t.ID == named {
				idx = i
				break
			}
		}
		if idx == -1 {
			return "", errf("validation", false, "task %d is not a sibling under parent %d", named, parent)
		}
	}
	switch {
	case after != 0:
		hi := ""
		if idx+1 < len(sib) {
			hi = sib[idx+1].Rank
		}
		return rankStep(sib[idx].Rank, hi), nil
	case before != 0:
		lo := ""
		if idx > 0 {
			lo = sib[idx-1].Rank
		}
		return rankStep(lo, sib[idx].Rank), nil
	default:
		lo := ""
		if len(sib) > 0 {
			lo = sib[len(sib)-1].Rank
		}
		return rankStep(lo, ""), nil
	}
}

// taskChannelExists returns not_found when channel does not exist. q may be
// b.db (a preflight check) or a *sql.Tx.
func (b *Bus) taskChannelExists(q queryRower, channel string) error {
	var exists int
	err := q.QueryRow("SELECT 1 FROM channels WHERE name=?", channel).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return errf("not_found", false, "channel %q does not exist; create it first", channel)
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

// receiptTaskResult unmarshals a stored receipt result back into a Task.
func receiptTaskResult(raw json.RawMessage) (Task, error) {
	var t Task
	if err := json.Unmarshal(raw, &t); err != nil {
		return Task{}, internal(err)
	}
	return t, nil
}

// TaskCreate adds a task to a task-list channel, positioning it among its
// siblings and validating its parent/blocked_by links. Pipeline mirrors
// Send (messages.go): shape checks, then the read-only receipt check (R2:
// a keyed retry must replay its stored result even if the channel it named
// has since been deleted, rather than failing not_found — the controller's
// ruling on the brief, which had the channel-existence check too early),
// then the mutable channel-existence check (mirrors Send's validateSendRefs
// placement), then preflight checks on b.db, then a write transaction that
// re-verifies ownership and idempotency before computing the rank and
// inserting. Never charges embedSoon: task rows are excluded from the
// embedder (embed.go).
func (b *Bus) TaskCreate(as string, in TaskCreateInput) (Task, error) {
	if err := b.auth(b.db, as); err != nil {
		return Task{}, err
	}
	if !IsTaskChannel(in.Channel) {
		return Task{}, errf("validation", false, "%q is not a task list", in.Channel)
	}
	if err := validateTaskSubject(in.Subject); err != nil {
		return Task{}, err
	}

	key := in.IdempotencyKey
	in.IdempotencyKey = ""
	if key != "" {
		defer b.lockKey(as, key)()
	}
	if prev, hit, err := b.checkReceipt(b.db, as, key, in); err != nil {
		return Task{}, err
	} else if hit {
		return receiptTaskResult(prev)
	}

	if err := b.taskChannelExists(b.db, in.Channel); err != nil {
		return Task{}, err
	}

	blockedBy := dedupeIDs(in.BlockedBy)
	t := Task{Channel: in.Channel, Subject: in.Subject, Description: in.Description, Status: "pending",
		Parent: in.Parent, Rank: "V", BlockedBy: blockedBy, Metadata: in.Metadata}
	content, err := taskContent(t)
	if err != nil {
		return Task{}, internal(err)
	}
	send := SendInput{Channel: in.Channel, Content: content}
	if _, _, err := b.sendEnvelope(b.db, as, send, true); err != nil {
		return Task{}, err
	}

	if err := b.inspect("task_create", as, in); err != nil {
		return Task{}, err
	}
	if err := b.checkCapacity(); err != nil {
		return Task{}, err
	}

	tx, err := b.db.Begin()
	if err != nil {
		return Task{}, internal(err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := b.auth(tx, as); err != nil {
		return Task{}, err
	}
	if prev, hit, err := b.checkReceipt(tx, as, key, in); err != nil {
		return Task{}, err
	} else if hit {
		return receiptTaskResult(prev)
	}

	ts, err := loadTasks(tx, in.Channel)
	if err != nil {
		return Task{}, err
	}
	if err := validateTaskLinks(ts, t); err != nil {
		return Task{}, err
	}
	rank, err := placeRank(ts, 0, in.Parent, in.Before, in.After)
	if err != nil {
		return Task{}, err
	}
	t.Rank = rank
	if send.Content, err = taskContent(t); err != nil {
		return Task{}, internal(err)
	}
	context, size, err := b.sendEnvelope(tx, as, send, true)
	if err != nil {
		return Task{}, err
	}
	if !b.limits.allow(as, size, b.Now()) {
		return Task{}, errf("rate_limited", true, "send_messages_per_second rate limit exceeded for %s", as)
	}
	seq, err := b.insertMessage(tx, as, context, send, "memory")
	if err != nil {
		return Task{}, internal(err)
	}
	ts, err = loadTasks(tx, in.Channel)
	if err != nil {
		return Task{}, err
	}
	created := taskByID(ts, seq)
	if created == nil {
		return Task{}, internal(fmt.Errorf("task %d missing after insert", seq))
	}
	if err := b.storeReceipt(tx, as, key, in, *created); err != nil {
		return Task{}, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return Task{}, internal(err)
	}
	return *created, nil
}

// TaskGet returns id's live task, including derived Blocked/OpenBlockers.
func (b *Bus) TaskGet(as string, id int64) (Task, error) {
	if err := b.auth(b.db, as); err != nil {
		return Task{}, err
	}
	_, _, channel, err := liveRevision(b.db, id)
	if err != nil {
		return Task{}, err
	}
	if !IsTaskChannel(channel) {
		return Task{}, errf("not_found", false, "memory %d is not a task", id)
	}
	ts, err := loadTasks(b.db, channel)
	if err != nil {
		return Task{}, err
	}
	t := taskByID(ts, id)
	if t == nil {
		return Task{}, errf("not_found", false, "memory %d is not a task", id)
	}
	return *t, nil
}

// taskSummaryBytes measures the serialized size of a TaskSummary.
func taskSummaryBytes(s TaskSummary) int {
	j, _ := json.Marshal(s)
	return len(j)
}

// TaskList returns channel's live tasks in depth-first tree order, filtered
// by status/owner when set; filtering keeps the order (design: "Order").
func (b *Bus) TaskList(as string, in TaskListInput) ([]TaskSummary, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	if !IsTaskChannel(in.Channel) {
		return nil, errf("validation", false, "%q is not a task list", in.Channel)
	}
	if err := b.taskChannelExists(b.db, in.Channel); err != nil {
		return nil, err
	}
	if in.Status != "" && !isTaskStatus(in.Status) {
		return nil, errf("validation", false, "status must be pending, in_progress, or completed")
	}
	ts, err := loadTasks(b.db, in.Channel)
	if err != nil {
		return nil, err
	}
	_, depth := taskTree(ts)
	sums := make([]TaskSummary, 0, len(ts))
	for _, t := range ts {
		if in.Status != "" && t.Status != in.Status {
			continue
		}
		if in.Owner != "" && t.Owner != in.Owner {
			continue
		}
		sums = append(sums, TaskSummary{
			ID: t.ID, Subject: t.Subject, Status: t.Status, Owner: t.Owner,
			Parent: t.Parent, Depth: depth[t.ID], OpenBlockers: t.OpenBlockers,
			LeasedUntil: t.LeasedUntil,
		})
	}
	return trimToBytes(sums, taskSummaryBytes, min(b.cfg.ResultDefaultKiB*1024, trimHardCeilingBytes)), nil
}
