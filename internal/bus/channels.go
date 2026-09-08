package bus

import (
	"database/sql"
	"errors"
)

type Channel struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Messages  int64  `json:"messages"`
	LatestSeq int64  `json:"latest_seq"`
}

func (b *Bus) CreateChannel(as, name, kind string) (Channel, error) {
	if err := b.auth(b.db, as); err != nil {
		return Channel{}, err
	}
	if err := validateName(name); err != nil {
		return Channel{}, err
	}
	if kind != "ordinary" && kind != "memory" {
		return Channel{}, errf("validation", false, "kind must be ordinary or memory")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return Channel{}, internal(err)
	}
	defer tx.Rollback()
	// Re-verify ownership on the transaction that is about to write (R3).
	if err := b.auth(tx, as); err != nil {
		return Channel{}, err
	}
	var existing string
	err = tx.QueryRow("SELECT kind FROM channels WHERE name=?", name).Scan(&existing)
	switch {
	case err == nil && existing != kind:
		return Channel{}, errf("conflict", false, "channel %q exists with kind %s", name, existing)
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		var latest int64
		if err := tx.QueryRow("SELECT coalesce(max(seq),0) FROM messages").Scan(&latest); err != nil {
			return Channel{}, internal(err)
		}
		if _, err := tx.Exec("INSERT INTO channels(name,kind,created_seq,evicted_before_seq) VALUES(?,?,?,0)", name, kind, latest); err != nil {
			return Channel{}, internal(err)
		}
	default:
		return Channel{}, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, internal(err)
	}
	return Channel{Name: name, Kind: kind}, nil
}

func (b *Bus) ListChannels(as string) ([]Channel, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	rows, err := b.db.Query(`SELECT c.name, c.kind,
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
