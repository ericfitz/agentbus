package bus

import "database/sql"

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
