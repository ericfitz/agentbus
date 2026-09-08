package bus

import "database/sql"

// Status is the admin view of the whole bus: it does not require a
// registration, unlike Discover/ListChannels.
type Status struct {
	Sessions         []Session `json:"sessions"`
	Channels         []Channel `json:"channels"`
	UsageBytes       int64     `json:"usage_bytes"`
	BudgetBytes      int64     `json:"budget_bytes"`
	Notice           string    `json:"notice,omitempty"`
	EmbeddingBacklog int64     `json:"embedding_backlog"`
	ConfigPath       string    `json:"config_path"`
	DataDirectory    string    `json:"data_directory"`
}

// liveSessions is the shared query behind Discover and StatusReport.
func (b *Bus) liveSessions(db *sql.DB) ([]Session, error) {
	rows, err := db.Query("SELECT sender, context, registered_at FROM sessions WHERE heartbeat >= ? ORDER BY sender", b.nowMs()-attachmentExpiryMs)
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.Sender, &s.Context, &s.RegisteredAt); err != nil {
			return nil, internal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return out, nil
}

// listChannels is the shared query behind ListChannels and StatusReport.
func (b *Bus) listChannels(db *sql.DB) ([]Channel, error) {
	rows, err := db.Query(`SELECT c.name, c.kind,
	  (SELECT count(*) FROM messages m WHERE m.channel=c.name AND m.tombstone=0),
	  (SELECT coalesce(max(seq),0) FROM messages m WHERE m.channel=c.name)
	  FROM channels c ORDER BY c.name`)
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.Name, &c.Kind, &c.Messages, &c.LatestSeq); err != nil {
			return nil, internal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return out, nil
}

// LiveSessionCount is the number of sessions with a current heartbeat,
// usable without a registration (unlike Discover, which requires auth and
// discovery_enabled).
func (b *Bus) LiveSessionCount() (int, error) {
	var n int
	err := b.db.QueryRow("SELECT count(*) FROM sessions WHERE heartbeat >= ?", b.nowMs()-attachmentExpiryMs).Scan(&n)
	if err != nil {
		return 0, internal(err)
	}
	return n, nil
}

// StatusReport is the admin view; it does not require a registration.
func (b *Bus) StatusReport() (Status, error) {
	st := Status{ConfigPath: b.cfg.Path, DataDirectory: b.cfg.DataDirectory, BudgetBytes: b.budget()}
	var err error
	if st.UsageBytes, err = b.Usage(); err != nil {
		return st, err
	}
	if st.Sessions, err = b.liveSessions(b.db); err != nil {
		return st, err
	}
	if st.Channels, err = b.listChannels(b.db); err != nil {
		return st, err
	}
	if err := b.db.QueryRow("SELECT message FROM notices WHERE kind='capacity'").Scan(&st.Notice); err != nil && err != sql.ErrNoRows {
		return st, internal(err)
	}
	model := ""
	if b.embedder != nil {
		model = b.embedder.model
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages m LEFT JOIN embeddings e ON e.seq=m.seq AND e.model=? WHERE m.memory_id IS NOT NULL AND m.tombstone=0 AND e.seq IS NULL", model).Scan(&st.EmbeddingBacklog); err != nil {
		return st, internal(err)
	}
	return st, nil
}

// Reset wipes every table in one transaction. The file is never unlinked, so
// a live process sees its session row gone and fails its next auth with
// not_registered on its own; Reset never touches running processes directly.
func (b *Bus) Reset() error {
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer tx.Rollback()
	for _, t := range []string{"embeddings", "messages", "subscriptions", "sessions", "channels", "receipts", "notices", "sqlite_sequence"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return internal(err)
		}
	}
	if _, err := tx.Exec("INSERT INTO messages_fts(messages_fts) VALUES('rebuild')"); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("UPDATE leases SET owner='', expires_at=0"); err != nil {
		return internal(err)
	}
	if err := tx.Commit(); err != nil {
		return internal(err)
	}
	return nil
}
