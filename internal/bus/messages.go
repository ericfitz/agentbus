package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

type Ref struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type SendInput struct {
	Channel        string            `json:"channel"`
	Content        string            `json:"content"`
	Type           string            `json:"type,omitempty"`
	ReplyTo        *int64            `json:"reply_to,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Refs           []Ref             `json:"refs,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

type SendResult struct {
	Seq      int64  `json:"seq"`
	MemoryID *int64 `json:"memory_id,omitempty"`
}

type Message struct {
	Seq       int64             `json:"seq"`
	Channel   string            `json:"channel"`
	Sender    string            `json:"sender"`
	Context   string            `json:"context"`
	CreatedAt int64             `json:"created_at"`
	Type      string            `json:"type,omitempty"`
	Content   string            `json:"content"`
	ReplyTo   *int64            `json:"reply_to,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Refs      []Ref             `json:"refs,omitempty"`
	MemoryID  *int64            `json:"memory_id,omitempty"`
	Revision  *int64            `json:"revision,omitempty"`
}

const messageColumns = "seq, channel, sender, context, created_at, type, content, reply_to, metadata, refs, memory_id, revision"

func scanMessages(rows *sql.Rows) ([]Message, error) {
	out := []Message{}
	for rows.Next() {
		var m Message
		var meta, refs sql.NullString
		if err := rows.Scan(&m.Seq, &m.Channel, &m.Sender, &m.Context, &m.CreatedAt, &m.Type, &m.Content, &m.ReplyTo, &meta, &refs, &m.MemoryID, &m.Revision); err != nil {
			return nil, err
		}
		if meta.Valid {
			if err := json.Unmarshal([]byte(meta.String), &m.Metadata); err != nil {
				return nil, err
			}
		}
		if refs.Valid {
			if err := json.Unmarshal([]byte(refs.String), &m.Refs); err != nil {
				return nil, err
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// envelopeBytes measures the serialized size of a Message. Message holds only
// strings, ints, and pointers/maps/slices of those, so json.Marshal cannot fail.
func envelopeBytes(m Message) int {
	j, _ := json.Marshal(m)
	return len(j)
}

var refKinds = map[string]bool{"url": true, "unix_path": true, "windows_path": true}

func (b *Bus) validateSend(as string, in SendInput) (kind string, err error) {
	if in.Channel == "" || in.Content == "" {
		return "", errf("validation", false, "channel and content are required")
	}
	for _, r := range in.Refs {
		if !refKinds[r.Kind] || r.Value == "" {
			return "", errf("validation", false, "ref kind must be url, unix_path, or windows_path with a nonempty value")
		}
	}
	switch err := b.db.QueryRow("SELECT kind FROM channels WHERE name=?", in.Channel).Scan(&kind); {
	case errors.Is(err, sql.ErrNoRows):
		return "", errf("not_found", false, "channel %q does not exist; create it first", in.Channel)
	case err != nil:
		return "", internal(err)
	}
	if in.ReplyTo != nil {
		var n int
		if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE seq=?", *in.ReplyTo).Scan(&n); err != nil {
			return "", internal(err)
		}
		if n == 0 {
			return "", errf("not_found", false, "reply_to %d does not exist", *in.ReplyTo)
		}
	}
	probe := Message{Channel: in.Channel, Sender: as, Context: strings.Repeat("x", 128), CreatedAt: 1 << 62, Type: in.Type, Content: in.Content, ReplyTo: in.ReplyTo, Metadata: in.Metadata, Refs: in.Refs}
	if envelopeBytes(probe) > b.cfg.MaxMessageKiB*1024 {
		return "", errf("validation", false, "envelope exceeds max_message_kib (%d KiB)", b.cfg.MaxMessageKiB)
	}
	return kind, nil
}

// insertMessage writes one row inside tx and returns its seq. For memory channels
// it sets memory_id = seq and revision = 1.
func (b *Bus) insertMessage(tx *sql.Tx, as, context string, in SendInput, kind string) (int64, error) {
	var meta, refs any
	if in.Metadata != nil {
		j, err := json.Marshal(in.Metadata)
		if err != nil {
			return 0, err
		}
		meta = string(j)
	}
	if in.Refs != nil {
		j, err := json.Marshal(in.Refs)
		if err != nil {
			return 0, err
		}
		refs = string(j)
	}
	size := envelopeBytes(Message{Channel: in.Channel, Sender: as, Context: context, Type: in.Type, Content: in.Content, ReplyTo: in.ReplyTo, Metadata: in.Metadata, Refs: in.Refs})
	res, err := tx.Exec("INSERT INTO messages(channel,sender,context,created_at,type,content,reply_to,metadata,refs,bytes) VALUES(?,?,?,?,?,?,?,?,?,?)",
		in.Channel, as, context, b.nowMs(), in.Type, in.Content, in.ReplyTo, meta, refs, size)
	if err != nil {
		return 0, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if kind == "memory" {
		if _, err := tx.Exec("UPDATE messages SET memory_id=seq, revision=1 WHERE seq=?", seq); err != nil {
			return 0, err
		}
	}
	return seq, nil
}

func (b *Bus) senderContext(as string) (string, error) {
	var c string
	if err := b.db.QueryRow("SELECT context FROM sessions WHERE sender=?", as).Scan(&c); err != nil {
		return "", err
	}
	return c, nil
}

// receiptResult unmarshals a stored receipt result back into a SendResult.
func receiptResult(raw json.RawMessage) (SendResult, error) {
	var res SendResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return SendResult{}, internal(err)
	}
	return res, nil
}

// Temporary stubs for hooks that later tasks replace: Task 10 (checkCapacity,
// embedSoon), Task 11 (inspect).
func (b *Bus) inspect(kind, as string, payload any) error { return nil }
func (b *Bus) checkCapacity() error                       { return nil }
func (b *Bus) embedSoon()                                 {}

func (b *Bus) Send(as string, in SendInput) (SendResult, error) {
	if err := b.auth(as); err != nil {
		return SendResult{}, err
	}
	kind, err := b.validateSend(as, in)
	if err != nil {
		return SendResult{}, err
	}
	key := in.IdempotencyKey
	in.IdempotencyKey = ""

	// Cheap read-only replay check before paying for the hook/capacity work below.
	if prev, hit, err := b.checkReceipt(b.db, as, key, in); err != nil {
		return SendResult{}, err
	} else if hit {
		return receiptResult(prev)
	}

	if err := b.inspect("send", as, in); err != nil {
		return SendResult{}, err
	}
	if err := b.checkCapacity(); err != nil {
		return SendResult{}, err
	}

	tx, err := b.db.Begin()
	if err != nil {
		return SendResult{}, internal(err)
	}
	defer tx.Rollback()

	// Re-check inside the transaction: closes the race where two sends with the
	// same key both passed the read-only check above concurrently.
	if prev, hit, err := b.checkReceipt(tx, as, key, in); err != nil {
		return SendResult{}, err
	} else if hit {
		return receiptResult(prev)
	}

	// Charge the rate limit only for a send that has cleared the hook and
	// capacity checks and is about to be accepted; a receipt replay never
	// reaches this line, so replays are free.
	if !b.limits.allow(as, envelopeBytes(Message{Content: in.Content, Metadata: in.Metadata, Refs: in.Refs}), b.Now()) {
		return SendResult{}, errf("rate_limited", true, "send rate limit exceeded for %s", as)
	}

	context, err := b.senderContext(as)
	if err != nil {
		return SendResult{}, internal(err)
	}
	seq, err := b.insertMessage(tx, as, context, in, kind)
	if err != nil {
		return SendResult{}, internal(err)
	}
	res := SendResult{Seq: seq}
	if kind == "memory" {
		res.MemoryID = &seq
	}
	if err := b.storeReceipt(tx, as, key, in, res); err != nil {
		return SendResult{}, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return SendResult{}, internal(err)
	}
	if kind == "memory" {
		b.embedSoon()
	}
	return res, nil
}

func (b *Bus) History(as, channel string, before, after *int64, count int) ([]Message, error) {
	if err := b.auth(as); err != nil {
		return nil, err
	}
	if count <= 0 {
		count = b.cfg.ReceiveDefaultCount
	}
	if count > b.cfg.ReceiveMaxCount {
		count = b.cfg.ReceiveMaxCount
	}
	q := "SELECT " + messageColumns + " FROM messages WHERE channel=? AND tombstone=0"
	args := []any{channel}
	if before != nil {
		q += " AND seq<?"
		args = append(args, *before)
	}
	if after != nil {
		q += " AND seq>?"
		args = append(args, *after)
	}
	order := "ASC"
	if before != nil && after == nil {
		order = "DESC"
	}
	q += " ORDER BY seq " + order + " LIMIT ?"
	args = append(args, count)
	rows, err := b.db.Query(q, args...)
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, internal(err)
	}
	// Trim while still in fetch order, so the rows kept are those nearest the
	// caller's cursor (before/after), not an arbitrary end of the page.
	msgs = trimBytes(msgs, b.cfg.ResultDefaultKiB*1024)
	if order == "DESC" {
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}
	return msgs, nil
}

// trimBytes keeps whole records up to limit bytes, always at least one.
func trimBytes(msgs []Message, limit int) []Message {
	total := 0
	for i, m := range msgs {
		total += envelopeBytes(m)
		if total > limit && i > 0 {
			return msgs[:i]
		}
	}
	return msgs
}
