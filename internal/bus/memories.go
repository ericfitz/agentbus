package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
)

type EditInput struct {
	ID             int64             `json:"id"`
	Content        string            `json:"content"`
	Type           string            `json:"type,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Refs           []Ref             `json:"refs,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

type EditResult struct {
	Seq      int64 `json:"seq"`
	MemoryID int64 `json:"memory_id"`
	Revision int64 `json:"revision"`
	Replaced int64 `json:"replaced"`
}

func (b *Bus) GetMemory(as string, id int64) (Message, error) {
	if err := b.auth(as); err != nil {
		return Message{}, err
	}
	rows, err := b.db.Query("SELECT "+messageColumns+" FROM messages WHERE memory_id=? AND tombstone=0", id)
	if err != nil {
		return Message{}, internal(err)
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return Message{}, internal(err)
	}
	if len(msgs) == 0 {
		return Message{}, errf("not_found", false, "memory %d has no live revision", id)
	}
	return msgs[0], nil
}

// liveRevision reads a memory's current live revision (seq, revision, channel).
// It runs against either b.db or a *sql.Tx so callers can read outside a
// transaction first and repeat the read inside one before committing writes.
func liveRevision(q queryRower, id int64) (seq, revision int64, channel string, err error) {
	err = q.QueryRow("SELECT seq, revision, channel FROM messages WHERE memory_id=? AND tombstone=0", id).Scan(&seq, &revision, &channel)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, "", errf("not_found", false, "memory %d has no live revision", id)
	}
	if err != nil {
		return 0, 0, "", internal(err)
	}
	return seq, revision, channel, nil
}

// EditMemory tombstones a memory's current revision and inserts the next one
// in a single transaction. Ordering mirrors Send: the receipt check, hook,
// and capacity check all run before any transaction is opened so a later
// task's external hook and eviction do not run under the writer lock; the
// receipt check and live-revision lookup are then repeated inside the
// transaction against the committed state before charging the rate limit
// and writing.
func (b *Bus) EditMemory(as string, in EditInput) (EditResult, error) {
	if err := b.auth(as); err != nil {
		return EditResult{}, err
	}
	if in.Content == "" {
		return EditResult{}, errf("validation", false, "content is required")
	}

	// Read-only lookup to learn the channel for validateSend; repeated inside
	// the transaction below so the tombstone/insert pair is computed against
	// the committed state.
	_, _, channel, err := liveRevision(b.db, in.ID)
	if err != nil {
		return EditResult{}, err
	}
	send := SendInput{Channel: channel, Content: in.Content, Type: in.Type, Metadata: in.Metadata, Refs: in.Refs}
	if _, err := b.validateSend(as, send); err != nil {
		return EditResult{}, err
	}

	key := in.IdempotencyKey
	in.IdempotencyKey = ""

	// Cheap read-only replay check before paying for the hook/capacity work below.
	if prev, hit, err := b.checkReceipt(b.db, as, key, in); err != nil {
		return EditResult{}, err
	} else if hit {
		var r EditResult
		if err := json.Unmarshal(prev, &r); err != nil {
			return EditResult{}, internal(err)
		}
		return r, nil
	}

	if err := b.inspect("edit_memory", as, in); err != nil {
		return EditResult{}, err
	}
	if err := b.checkCapacity(); err != nil {
		return EditResult{}, err
	}

	tx, err := b.db.Begin()
	if err != nil {
		return EditResult{}, internal(err)
	}
	defer tx.Rollback()

	// Re-check inside the transaction: closes the race where two edits with
	// the same key both passed the read-only check above concurrently.
	if prev, hit, err := b.checkReceipt(tx, as, key, in); err != nil {
		return EditResult{}, err
	} else if hit {
		var r EditResult
		if err := json.Unmarshal(prev, &r); err != nil {
			return EditResult{}, internal(err)
		}
		return r, nil
	}

	// Re-read the live revision against committed state: it may have moved
	// (or been deleted) between the read-only lookup above and this point.
	curSeq, curRev, _, err := liveRevision(tx, in.ID)
	if err != nil {
		return EditResult{}, err
	}

	// Charge the rate limit only for an edit that has cleared the hook and
	// capacity checks and is about to be accepted; a receipt replay never
	// reaches this line, so replays are free.
	if !b.limits.allow(as, envelopeBytes(Message{Content: in.Content, Metadata: in.Metadata, Refs: in.Refs}), b.Now()) {
		return EditResult{}, errf("rate_limited", true, "send rate limit exceeded for %s", as)
	}

	if _, err := tx.Exec("UPDATE messages SET tombstone=1, tombstone_at=? WHERE seq=?", b.nowMs(), curSeq); err != nil {
		return EditResult{}, internal(err)
	}
	context, err := b.senderContext(as)
	if err != nil {
		return EditResult{}, internal(err)
	}
	seq, err := b.insertMessage(tx, as, context, send, "ordinary")
	if err != nil {
		return EditResult{}, internal(err)
	}
	if _, err := tx.Exec("UPDATE messages SET memory_id=?, revision=? WHERE seq=?", in.ID, curRev+1, seq); err != nil {
		return EditResult{}, internal(err)
	}
	res := EditResult{Seq: seq, MemoryID: in.ID, Revision: curRev + 1, Replaced: curSeq}
	if err := b.storeReceipt(tx, as, key, in, res); err != nil {
		return EditResult{}, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return EditResult{}, internal(err)
	}
	b.embedSoon()
	return res, nil
}

// DeleteMemory tombstones a memory's live revision without inserting a new
// one. Ordering mirrors EditMemory: the receipt check and hook run before any
// transaction is opened, then both the receipt check and the live-revision
// lookup are repeated inside the transaction against committed state before
// charging the rate limit and writing. Delete never inserts, so it never
// calls checkCapacity.
func (b *Bus) DeleteMemory(as string, id int64, key string) error {
	if err := b.auth(as); err != nil {
		return err
	}
	payload := map[string]any{"delete": id}

	// Cheap read-only replay check before paying for the hook work below. A
	// stored (empty) result means this delete already committed.
	if _, hit, err := b.checkReceipt(b.db, as, key, payload); err != nil {
		return err
	} else if hit {
		return nil
	}

	if err := b.inspect("delete_memory", as, payload); err != nil {
		return err
	}

	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer tx.Rollback()

	// Re-check inside the transaction: closes the race where two deletes with
	// the same key both passed the read-only check above concurrently.
	if _, hit, err := b.checkReceipt(tx, as, key, payload); err != nil {
		return err
	} else if hit {
		return nil
	}

	// Re-read the live revision against committed state: it may have moved
	// (or already been deleted) between the read-only check above and here.
	seq, _, _, err := liveRevision(tx, id)
	if err != nil {
		return err
	}

	// Charge the rate limit only for a delete that has cleared the hook check
	// and is about to be accepted; a receipt replay never reaches this line,
	// so replays are free. Delete inserts nothing, so it charges one
	// operation and zero bytes.
	if !b.limits.allow(as, 0, b.Now()) {
		return errf("rate_limited", true, "send rate limit exceeded for %s", as)
	}

	r, err := tx.Exec("UPDATE messages SET tombstone=1, tombstone_at=? WHERE seq=? AND tombstone=0", b.nowMs(), seq)
	if err != nil {
		return internal(err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return internal(err)
	}
	if n == 0 {
		return errf("not_found", false, "memory %d has no live revision", id)
	}
	if err := b.storeReceipt(tx, as, key, payload, nil); err != nil {
		return internal(err)
	}
	return internal(tx.Commit())
}
