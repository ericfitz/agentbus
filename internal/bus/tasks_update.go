package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// TaskPatch is task_update's input: absent fields are unchanged. Before and
// After are mutually exclusive sibling ids, 0 meaning unset (as in
// TaskCreateInput).
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

// TaskUpdateResult is task_update's result.
type TaskUpdateResult struct {
	Task     Task  `json:"task"`
	Replaced int64 `json:"replaced"` // seq of the revision this one replaced
	Deleted  bool  `json:"deleted,omitempty"`
}

// openBlockers returns the entries of blockedBy that are still live in ts
// and not completed. Unlike fillDerived (which reads BlockedBy off each
// task already in ts), this checks an arbitrary candidate list against ts,
// for the patched task's not-yet-committed BlockedBy.
func openBlockers(ts []Task, blockedBy []int64) []int64 {
	var open []int64
	for _, bid := range blockedBy {
		if blocker := taskByID(ts, bid); blocker != nil && blocker.Status != "completed" {
			open = append(open, bid)
		}
	}
	return open
}

// ownerGuardBlocked reports whether the owner guard (rules 2 and 3: only
// the owner may change an in_progress task, and taking ownership needs the
// owner to be empty) would refuse p against cur, ignoring Force. TaskUpdate
// uses it, after applyPatch has run with Force honored, to tell whether
// Force actually bypassed the guard (which decides the "forced" revision
// type); applyPatch enforces the same two conditions directly, for their
// distinct error messages.
func ownerGuardBlocked(cur Task, p TaskPatch, as string) bool {
	if cur.Status == "in_progress" && cur.Owner != as {
		return true
	}
	return p.Owner != nil && *p.Owner != "" && *p.Owner != cur.Owner && cur.Owner != ""
}

// applyPatch is pure: it returns the patched task or the rule violation.
// Rule order matches the design's "Rules" section: delete-combo, owner
// guard, delete, scalar fields, owner existence, status, lease, links,
// order, no-op. ts includes cur itself, as loadTasks returns every live
// task; ownerKnown reports whether a candidate owner has ever registered.
func applyPatch(ts []Task, cur Task, p TaskPatch, as string, now int64, ownerKnown func(string) (bool, error)) (Task, error) {
	// Rule 1: delete cannot be combined with any other change.
	if p.Delete && (p.Status != nil || p.Owner != nil || p.Subject != nil || p.Description != nil ||
		p.Parent != nil || p.Before != 0 || p.After != 0 || len(p.AddBlockedBy) > 0 ||
		len(p.RemoveBlockedBy) > 0 || p.LeasedUntil != nil || p.Metadata != nil) {
		return Task{}, errf("validation", false, "delete cannot be combined with other changes")
	}

	// Rules 2-3: the owner guard, bypassed by Force.
	if !p.Force {
		if cur.Status == "in_progress" && cur.Owner != as {
			return Task{}, errf("conflict", false, "task %d is in progress, owned by %s", cur.ID, cur.Owner)
		}
		if p.Owner != nil && *p.Owner != "" && *p.Owner != cur.Owner && cur.Owner != "" {
			return Task{}, errf("conflict", false, "task %d is owned by %s", cur.ID, cur.Owner)
		}
	}

	// Rule 4: delete, once the guard clears. Not bypassed by Force.
	if p.Delete {
		var children []int64
		for _, t := range ts {
			if t.Parent == cur.ID {
				children = append(children, t.ID)
			}
		}
		if len(children) > 0 {
			return Task{}, errf("conflict", false, "task %d has subtasks: %v", cur.ID, children)
		}
		return cur, nil
	}

	// Rule 5: apply scalar fields onto a working copy.
	patched := cur
	if p.Subject != nil {
		if err := validateTaskSubject(*p.Subject); err != nil {
			return Task{}, err
		}
		patched.Subject = *p.Subject
	}
	if p.Description != nil {
		patched.Description = *p.Description
	}
	if p.Metadata != nil {
		merged := make(map[string]string, len(patched.Metadata)+len(p.Metadata))
		for k, v := range patched.Metadata {
			merged[k] = v
		}
		for k, v := range p.Metadata {
			if v == "" {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		if len(merged) == 0 {
			merged = nil
		}
		patched.Metadata = merged
	}
	if p.Owner != nil {
		patched.Owner = *p.Owner
	}
	if p.Parent != nil {
		patched.Parent = *p.Parent
	}
	if len(p.RemoveBlockedBy) > 0 || len(p.AddBlockedBy) > 0 {
		removeSet := make(map[int64]bool, len(p.RemoveBlockedBy))
		for _, id := range p.RemoveBlockedBy {
			removeSet[id] = true
		}
		kept := make([]int64, 0, len(patched.BlockedBy))
		for _, id := range patched.BlockedBy {
			if !removeSet[id] {
				kept = append(kept, id)
			}
		}
		patched.BlockedBy = dedupeIDs(append(kept, p.AddBlockedBy...))
	}

	// Rule 6: a new non-empty owner must have registered before.
	if patched.Owner != "" && patched.Owner != cur.Owner {
		known, err := ownerKnown(patched.Owner)
		if err != nil {
			return Task{}, err
		}
		if !known {
			return Task{}, errf("not_found", false, "identity %q has never registered", patched.Owner)
		}
	}

	// Rule 7: status transitions.
	if p.Status != nil {
		ns := *p.Status
		if !isTaskStatus(ns) {
			return Task{}, errf("validation", false, "status must be pending, in_progress, or completed")
		}
		if cur.Status == "completed" && ns == "in_progress" {
			return Task{}, errf("validation", false, "reopen the task (status pending) before starting it")
		}
		switch ns {
		case "pending":
			if p.Owner == nil || *p.Owner == "" {
				patched.Owner = ""
			}
			patched.LeasedUntil = 0
		case "completed":
			if patched.Owner == "" {
				patched.Owner = as
			}
			patched.LeasedUntil = 0
		case "in_progress":
			if patched.Owner == "" {
				return Task{}, errf("validation", false, "an in_progress task needs an owner")
			}
			if open := openBlockers(ts, patched.BlockedBy); len(open) > 0 {
				return Task{}, errf("conflict", false, "task %d is blocked by %v", cur.ID, open)
			}
		}
		patched.Status = ns
	}

	// Rule 8: lease. Validated first, then force-cleared unless the
	// (possibly just-settled) status is in_progress.
	if p.LeasedUntil != nil {
		if *p.LeasedUntil != 0 && *p.LeasedUntil <= now {
			return Task{}, errf("validation", false, "leased_until must be in the future (or 0 to clear)")
		}
		patched.LeasedUntil = *p.LeasedUntil
	}
	if patched.Status != "in_progress" {
		patched.LeasedUntil = 0
	}

	// Rule 9: parent and blocked_by links, checked against a copy whose
	// blocked_by is pruned of ids that no longer exist in ts and were not
	// added by this patch. A blocker deleted since it was set doesn't lock
	// the dependent out of later, unrelated updates (design: "a deleted
	// blocker stops blocking"); patched.BlockedBy itself is left as stored.
	// A blocked_by id this patch is adding must still resolve.
	check := patched
	if len(check.BlockedBy) > 0 {
		added := make(map[int64]bool, len(p.AddBlockedBy))
		for _, id := range p.AddBlockedBy {
			added[id] = true
		}
		live := make([]int64, 0, len(check.BlockedBy))
		for _, id := range check.BlockedBy {
			if added[id] || taskByID(ts, id) != nil {
				live = append(live, id)
			}
		}
		check.BlockedBy = live
	}
	if err := validateTaskLinks(ts, check); err != nil {
		return Task{}, err
	}

	// Rule 10: order, recomputed only when parent or before/after are named.
	if p.Parent != nil || p.Before != 0 || p.After != 0 {
		rank, err := placeRank(ts, cur.ID, patched.Parent, p.Before, p.After)
		if err != nil {
			return Task{}, err
		}
		patched.Rank = rank
	}

	// Rule 11: no-op.
	if reflect.DeepEqual(patched, cur) {
		return cur, nil
	}
	return patched, nil
}

// writeTaskRevision tombstones t's current revision and inserts the next
// one, returning t with seq, Revision, UpdatedAt, and UpdatedBy updated to
// match. Callers with no caller identity (the reclaim fix-up) pass
// as="" and context="".
func (b *Bus) writeTaskRevision(tx *sql.Tx, as, context string, t Task, typ string) (Task, error) {
	doc, err := taskContent(t)
	if err != nil {
		return Task{}, internal(err)
	}
	if _, err := tx.Exec("UPDATE messages SET tombstone=1, tombstone_at=? WHERE seq=?", b.nowMs(), t.seq); err != nil {
		return Task{}, internal(err)
	}
	seq, err := b.insertMessage(tx, as, context, SendInput{Channel: t.Channel, Type: typ, Content: doc}, "ordinary")
	if err != nil {
		return Task{}, internal(err)
	}
	if _, err := tx.Exec("UPDATE messages SET memory_id=?, revision=? WHERE seq=?", t.ID, t.Revision+1, seq); err != nil {
		return Task{}, internal(err)
	}
	t.seq = seq
	t.Revision++
	t.UpdatedAt = b.nowMs()
	t.UpdatedBy = as
	return t, nil
}

// TaskUpdate is the single write path for every task rule: patch, claim,
// and release all funnel through it (TaskClaim and TaskRelease are thin
// wrappers with no checks of their own). Pipeline mirrors EditMemory
// (memories.go): auth and the read-only receipt check come before any
// mutable-state lookup (R2), so a keyed retry replays even after the task
// has been deleted; the live-revision lookup, hook, and capacity check run
// before the write transaction opens; auth, the receipt check, and the
// task's live state are then re-read inside the transaction against
// committed state before the rate limit is charged and anything is
// written.
func (b *Bus) TaskUpdate(as string, p TaskPatch) (TaskUpdateResult, error) {
	if err := b.auth(b.db, as); err != nil {
		return TaskUpdateResult{}, err
	}

	key := p.IdempotencyKey
	p.IdempotencyKey = ""
	if key != "" {
		defer b.lockKey(as, key)()
	}

	if prev, hit, err := b.checkReceipt(b.db, as, key, p); err != nil {
		return TaskUpdateResult{}, err
	} else if hit {
		var r TaskUpdateResult
		if err := json.Unmarshal(prev, &r); err != nil {
			return TaskUpdateResult{}, internal(err)
		}
		return r, nil
	}

	_, _, channel, err := liveRevision(b.db, p.ID)
	if err != nil {
		return TaskUpdateResult{}, err
	}
	if !IsTaskChannel(channel) {
		return TaskUpdateResult{}, errf("not_found", false, "memory %d is not a task", p.ID)
	}

	if err := b.inspect("task_update", as, p); err != nil {
		return TaskUpdateResult{}, err
	}
	if !p.Delete {
		if err := b.checkCapacity(); err != nil {
			return TaskUpdateResult{}, err
		}
	}

	tx, err := b.db.Begin()
	if err != nil {
		return TaskUpdateResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := b.auth(tx, as); err != nil {
		return TaskUpdateResult{}, err
	}
	if prev, hit, err := b.checkReceipt(tx, as, key, p); err != nil {
		return TaskUpdateResult{}, err
	} else if hit {
		var r TaskUpdateResult
		if err := json.Unmarshal(prev, &r); err != nil {
			return TaskUpdateResult{}, internal(err)
		}
		return r, nil
	}

	ts, err := loadTasks(tx, channel)
	if err != nil {
		return TaskUpdateResult{}, err
	}
	cur := taskByID(ts, p.ID)
	if cur == nil {
		return TaskUpdateResult{}, errf("not_found", false, "memory %d is not a task", p.ID)
	}

	ownerKnown := func(o string) (bool, error) {
		var exists int
		err := tx.QueryRow("SELECT 1 FROM channels WHERE name=?", DMChannel(o)).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, internal(err)
		}
		return true, nil
	}
	patched, err := applyPatch(ts, *cur, p, as, b.nowMs(), ownerKnown)
	if err != nil {
		return TaskUpdateResult{}, err
	}

	if p.Delete {
		if !b.limits.allow(as, 0, b.Now()) {
			return TaskUpdateResult{}, errf("rate_limited", true, "send_messages_per_second rate limit exceeded for %s", as)
		}
		if _, err := tx.Exec("UPDATE messages SET tombstone=1, tombstone_at=? WHERE seq=?", b.nowMs(), cur.seq); err != nil {
			return TaskUpdateResult{}, internal(err)
		}
		res := TaskUpdateResult{Task: *cur, Replaced: cur.seq, Deleted: true}
		if err := b.storeReceipt(tx, as, key, p, res); err != nil {
			return TaskUpdateResult{}, internal(err)
		}
		if err := tx.Commit(); err != nil {
			return TaskUpdateResult{}, internal(err)
		}
		return res, nil
	}

	if reflect.DeepEqual(patched, *cur) {
		res := TaskUpdateResult{Task: *cur}
		if err := b.storeReceipt(tx, as, key, p, res); err != nil {
			return TaskUpdateResult{}, internal(err)
		}
		if err := tx.Commit(); err != nil {
			return TaskUpdateResult{}, internal(err)
		}
		return res, nil
	}

	typ := ""
	if p.Force && ownerGuardBlocked(*cur, p, as) {
		typ = "forced"
	}
	content, err := taskContent(patched)
	if err != nil {
		return TaskUpdateResult{}, internal(err)
	}
	context, size, err := b.sendEnvelope(tx, as, SendInput{Channel: channel, Type: typ, Content: content}, true)
	if err != nil {
		return TaskUpdateResult{}, err
	}
	if !b.limits.allow(as, size, b.Now()) {
		return TaskUpdateResult{}, errf("rate_limited", true, "send_messages_per_second rate limit exceeded for %s", as)
	}
	newTask, err := b.writeTaskRevision(tx, as, context, patched, typ)
	if err != nil {
		return TaskUpdateResult{}, err
	}

	ts, err = loadTasks(tx, channel)
	if err != nil {
		return TaskUpdateResult{}, err
	}
	final := taskByID(ts, newTask.ID)
	if final == nil {
		return TaskUpdateResult{}, internal(fmt.Errorf("task %d missing after update", newTask.ID))
	}
	res := TaskUpdateResult{Task: *final, Replaced: cur.seq}
	if err := b.storeReceipt(tx, as, key, p, res); err != nil {
		return TaskUpdateResult{}, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return TaskUpdateResult{}, internal(err)
	}
	return res, nil
}

// TaskClaim is task_update {owner: as, status: "in_progress", leased_until}.
func (b *Bus) TaskClaim(as string, id, leasedUntil int64, key string) (TaskUpdateResult, error) {
	st := "in_progress"
	p := TaskPatch{ID: id, Owner: &as, Status: &st, IdempotencyKey: key}
	if leasedUntil != 0 {
		p.LeasedUntil = &leasedUntil
	}
	return b.TaskUpdate(as, p)
}

// TaskRelease is task_update {owner: "", status: "pending", leased_until: 0}.
func (b *Bus) TaskRelease(as string, id int64, key string) (TaskUpdateResult, error) {
	st, none, zero := "pending", "", int64(0)
	return b.TaskUpdate(as, TaskPatch{ID: id, Owner: &none, Status: &st, LeasedUntil: &zero, IdempotencyKey: key})
}
