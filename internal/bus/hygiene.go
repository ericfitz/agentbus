package bus

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// notPruned is the AND clause that hides pruned memories from agents; alias
// names the messages row ("m" in search, "messages" elsewhere). A pruned
// memory answers agents exactly as a deleted one.
func notPruned(alias string) string {
	return " AND NOT EXISTS (SELECT 1 FROM memory_access pa WHERE pa.memory_id=" + alias + ".memory_id AND pa.pruned_at IS NOT NULL)"
}

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

// scanMemoryStats reads rows selected with memoryStatsCols and closes them.
func scanMemoryStats(rows *sql.Rows) ([]MemoryStats, error) {
	defer func() { _ = rows.Close() }()
	var out []MemoryStats
	for rows.Next() {
		var s MemoryStats
		if err := rows.Scan(&s.MemoryID, &s.AccessedAt, &s.ServedCount, &s.UsedCount, &s.UsedAt, &s.GraceFrom,
			&s.GraceServedBase, &s.IncorrectAt, &s.IncorrectBy, &s.IncorrectServedBase, &s.PrunedAt, &s.PrunedReason); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// memoryAccessUpsertSQL takes (memory_id, now, now). Create and edit share
// it: a create inserts the row with the grace period starting now; an edit
// restarts the grace period (grace_served_base catches up to served_count),
// clears the INCORRECT flag, and keeps the serve and use counters.
const memoryAccessUpsertSQL = `INSERT INTO memory_access(memory_id, accessed_at, grace_from) VALUES(?,?,?)
  ON CONFLICT(memory_id) DO UPDATE SET accessed_at=excluded.accessed_at, grace_from=excluded.grace_from,
  grace_served_base=served_count, incorrect_at=NULL, incorrect_by=NULL, incorrect_served_base=0`

// FeedbackItem is one verdict on a memory. Verdict is a plain string checked
// in the bus, so one error names bad verdicts and bad ids together.
type FeedbackItem struct {
	MemoryID int64  `json:"memory_id"`
	Verdict  string `json:"verdict" jsonschema:"USED or INCORRECT"`
}

// FeedbackResult echoes the items MemoryFeedback applied, in list order.
type FeedbackResult struct {
	Applied []FeedbackItem `json:"applied"`
}

// FeedbackMaxItems caps one memory_feedback call.
const FeedbackMaxItems = 50

// feedbackReason returns why item cannot take feedback, or "" when it can.
// An unknown id, a deleted or pruned memory, a chat or DM seq and a memory
// without a live memory_access row all read "not found", so a pruned memory
// answers like a deleted one.
func feedbackReason(q queryRower, item FeedbackItem) (string, error) {
	if item.Verdict != "USED" && item.Verdict != "INCORRECT" {
		return fmt.Sprintf("unknown verdict %q (use USED or INCORRECT)", item.Verdict), nil
	}
	_, _, channel, err := liveRevisionAny(q, item.MemoryID)
	if err != nil {
		var be *Error
		if errors.As(err, &be) && be.Code == "not_found" {
			return "not found", nil
		}
		return "", err
	}
	if IsTaskChannel(channel) {
		return "task row", nil
	}
	var pruned sql.NullInt64
	err = q.QueryRow("SELECT pruned_at FROM memory_access WHERE memory_id=?", item.MemoryID).Scan(&pruned)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && pruned.Valid) {
		return "not found", nil
	}
	if err != nil {
		return "", internal(err)
	}
	return "", nil
}

// MemoryFeedback records USED and INCORRECT verdicts on memories the caller
// was served (#53). Nothing is applied unless every item is valid; items
// apply in list order under one timestamp, so a duplicated id counts each
// time. USED counts a use; INCORRECT flags the memory with who, when and the
// serve count at that moment, replacing an earlier flag. One call costs one
// rate-limit operation.
func (b *Bus) MemoryFeedback(as string, items []FeedbackItem) (FeedbackResult, error) {
	if err := b.auth(b.db, as); err != nil {
		return FeedbackResult{}, err
	}
	if len(items) == 0 {
		return FeedbackResult{}, errf("validation", false, "items is required: 1 to %d {memory_id, verdict} pairs", FeedbackMaxItems)
	}
	if len(items) > FeedbackMaxItems {
		return FeedbackResult{}, errf("validation", false, "at most %d items per call, got %d", FeedbackMaxItems, len(items))
	}
	tx, err := b.db.Begin()
	if err != nil {
		return FeedbackResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.auth(tx, as); err != nil {
		return FeedbackResult{}, err
	}
	var bad []string
	for i, item := range items {
		reason, err := feedbackReason(tx, item)
		if err != nil {
			return FeedbackResult{}, err
		}
		if reason != "" {
			bad = append(bad, fmt.Sprintf("item %d (memory %d): %s", i, item.MemoryID, reason))
		}
	}
	if len(bad) > 0 {
		return FeedbackResult{}, errf("validation", false, "no feedback applied: %s", strings.Join(bad, "; "))
	}
	if !b.limits.allow(as, 0, b.Now()) {
		return FeedbackResult{}, errf("rate_limited", true, "send_messages_per_second rate limit exceeded for %s", as)
	}
	now := b.nowMs()
	for _, item := range items {
		if item.Verdict == "USED" {
			_, err = tx.Exec("UPDATE memory_access SET used_count=used_count+1, used_at=?, accessed_at=? WHERE memory_id=?", now, now, item.MemoryID)
		} else {
			_, err = tx.Exec("UPDATE memory_access SET incorrect_at=?, incorrect_by=?, incorrect_served_base=served_count WHERE memory_id=?", now, as, item.MemoryID)
		}
		if err != nil {
			return FeedbackResult{}, internal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return FeedbackResult{}, internal(err)
	}
	return FeedbackResult{Applied: items}, nil
}

// FlaggedMemory is get_memory's result: the message plus the same incorrect
// field search hits carry.
type FlaggedMemory struct {
	Message
	Incorrect bool `json:"incorrect,omitempty"`
}

// GetMemoryFlagged is GetMemory plus one memory_access lookup; a row with
// no memory_access entry (a task) is never flagged.
func (b *Bus) GetMemoryFlagged(as string, id int64) (FlaggedMemory, error) {
	m, err := b.GetMemory(as, id)
	if err != nil {
		return FlaggedMemory{}, err
	}
	var flagged bool
	err = b.db.QueryRow("SELECT incorrect_at IS NOT NULL FROM memory_access WHERE memory_id=?", id).Scan(&flagged)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FlaggedMemory{}, internal(err)
	}
	return FlaggedMemory{Message: m, Incorrect: flagged}, nil
}

// ChannelMemoryStats returns the memory_access row of every live memory on
// channel, pruned ones included, keyed by memory id. A memory with no live
// revision (deleted) or no row (a task) is absent. Human-facing only (TUI
// and CLI): never registered as an MCP tool, so agents never see counts.
func (b *Bus) ChannelMemoryStats(as, channel string) (map[int64]MemoryStats, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	rows, err := b.db.Query("SELECT "+memoryStatsCols+` FROM memory_access a
  JOIN messages m ON m.memory_id=a.memory_id AND m.tombstone=0 WHERE m.channel=?`, channel)
	if err != nil {
		return nil, internal(err)
	}
	list, err := scanMemoryStats(rows)
	if err != nil {
		return nil, internal(err)
	}
	out := make(map[int64]MemoryStats, len(list))
	for _, s := range list {
		out[s.MemoryID] = s
	}
	return out, nil
}

// RestoreMemory clears pruned_at, pruned_reason and the INCORRECT flag (all
// three columns), and restarts the grace period: grace_from=now and
// grace_served_base=served_count. The counters stay. Human-facing only: never
// registered as an MCP tool.
func (b *Bus) RestoreMemory(as string, id int64) error {
	return b.memoryAccessWrite(as, id, `UPDATE memory_access SET pruned_at=NULL, pruned_reason=NULL,
  incorrect_at=NULL, incorrect_by=NULL, incorrect_served_base=0, grace_from=?, grace_served_base=served_count
  WHERE memory_id=? AND pruned_at IS NOT NULL`, []any{b.nowMs(), id},
		fmt.Sprintf("memory %d is not pruned", id))
}

// ClearMemoryIncorrect clears incorrect_at, incorrect_by and
// incorrect_served_base on any live memory, pruned or not; the pruned state
// stays. Human-facing only: never registered as an MCP tool.
func (b *Bus) ClearMemoryIncorrect(as string, id int64) error {
	return b.memoryAccessWrite(as, id, `UPDATE memory_access SET incorrect_at=NULL, incorrect_by=NULL,
  incorrect_served_base=0 WHERE memory_id=? AND incorrect_at IS NOT NULL`, []any{id},
		fmt.Sprintf("memory %d is not flagged incorrect", id))
}

// memoryAccessWrite runs one guarded memory_access UPDATE for a live memory,
// pruned or not, in one transaction. A guard that matches no row means the
// memory is not in the required state: a validation error with notState.
func (b *Bus) memoryAccessWrite(as string, id int64, update string, args []any, notState string) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.auth(tx, as); err != nil {
		return err
	}
	_, _, channel, err := liveRevisionAny(tx, id)
	if err != nil {
		return err
	}
	if IsTaskChannel(channel) {
		return errf("validation", false, "%s is a task list; use task_create, task_update, task_claim, task_release", channel)
	}
	r, err := tx.Exec(update, args...)
	if err != nil {
		return internal(err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return internal(err)
	}
	if n == 0 {
		return errf("validation", false, "%s", notState)
	}
	return internal(tx.Commit())
}

// DeleteMemoryAny deletes a live memory, pruned or not: DeleteMemory's write
// with no idempotency receipt. Human-facing only: never registered as an MCP
// tool.
func (b *Bus) DeleteMemoryAny(as string, id int64) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	if err := b.inspect("delete_memory", as, map[string]any{"delete": id}); err != nil {
		return err
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.auth(tx, as); err != nil {
		return err
	}
	seq, _, channel, err := liveRevisionAny(tx, id)
	if err != nil {
		return err
	}
	if IsTaskChannel(channel) {
		return errf("validation", false, "%s is a task list; use task_create, task_update, task_claim, task_release", channel)
	}
	if !b.limits.allow(as, 0, b.Now()) {
		return errf("rate_limited", true, "send_messages_per_second rate limit exceeded for %s", as)
	}
	ok, err := b.tombstoneMemoryRow(tx, id, seq)
	if err != nil {
		return internal(err)
	}
	if !ok {
		return errf("not_found", false, "memory %d has no live revision", id)
	}
	return internal(tx.Commit())
}
