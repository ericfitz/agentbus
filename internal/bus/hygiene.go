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
