package bus

import (
	"database/sql"
	"encoding/json"
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
	defer func() { _ = tx.Rollback() }()
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

// channelBytes measures the serialized size of a Channel; it holds only
// strings and ints, so json.Marshal cannot fail.
func channelBytes(c Channel) int {
	j, _ := json.Marshal(c)
	return len(j)
}

func (b *Bus) ListChannels(as string) ([]Channel, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	chans, err := b.listChannels(b.db)
	if err != nil {
		return nil, err
	}
	// listChannels is also StatusReport's source (the local, human-facing
	// status command, not an MCP tool result), so trimming happens here,
	// not inside the shared query.
	return trimToBytes(chans, channelBytes, min(b.cfg.ResultDefaultKiB*1024, trimHardCeilingBytes)), nil
}
