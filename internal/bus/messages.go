package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
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

// decodeJSONFields unmarshals the metadata/refs JSON columns (NULL as nil)
// into m, shared by scanMessages and search's dedicated row scan.
func decodeJSONFields(m Message, meta, refs *string) (Message, error) {
	if meta != nil {
		if err := json.Unmarshal([]byte(*meta), &m.Metadata); err != nil {
			return m, err
		}
	}
	if refs != nil {
		if err := json.Unmarshal([]byte(*refs), &m.Refs); err != nil {
			return m, err
		}
	}
	return m, nil
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	out := []Message{}
	for rows.Next() {
		var m Message
		var meta, refs *string
		if err := rows.Scan(&m.Seq, &m.Channel, &m.Sender, &m.Context, &m.CreatedAt, &m.Type, &m.Content, &m.ReplyTo, &meta, &refs, &m.MemoryID, &m.Revision); err != nil {
			return nil, err
		}
		m, err := decodeJSONFields(m, meta, refs)
		if err != nil {
			return nil, err
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

// validateRefs checks ref shape only: no mutable-state read. Shared by
// validateSendShape and EditMemory's own pre-receipt shape check.
func validateRefs(refs []Ref) error {
	for _, r := range refs {
		if !refKinds[r.Kind] || r.Value == "" {
			return errf("validation", false, "ref kind must be url, unix_path, or windows_path with a nonempty value")
		}
	}
	return nil
}

// validateSendShape checks in without touching mutable state: required
// fields and ref kinds. It runs before the idempotency receipt lookup (R2)
// so a keyed retry is judged by its own payload, not by a downstream lookup
// that can fail for reasons unrelated to whether this exact payload was
// already accepted (e.g. its channel or reply_to target being evicted since).
func validateSendShape(in SendInput) error {
	if in.Channel == "" || in.Content == "" {
		return errf("validation", false, "channel and content are required")
	}
	return validateRefs(in.Refs)
}

// validateSendRefs checks the mutable state a send depends on: the channel
// must exist (its kind decides memory vs ordinary), and reply_to, if set,
// must name a real message. Kept separate from validateSendShape so it runs
// only after the idempotency receipt lookup (R2).
func (b *Bus) validateSendRefs(in SendInput) (kind string, err error) {
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
	return kind, nil
}

// upperBoundSeqDigits is one more than int64's 19-digit maximum, for margin.
// upperBoundCreatedAtDigits is a Unix millisecond timestamp's width until
// the year 2286.
const (
	upperBoundSeqDigits       = 20
	upperBoundCreatedAtDigits = 13
)

// envelopeUpperBound measures the worst-case wire size of a message before
// it is written. seq and created_at are assigned by SQLite/the clock at
// insert time, so real values aren't known yet; they are sized to their
// maximum digit width instead. memory_id and revision are omitempty pointer
// fields that are only ever set on a memory-channel write (memoryKind),
// so they're sized the same way, but only then. Sender, context, channel,
// type, content, metadata, and refs are all real values already known at
// call time, so no padding is needed for them (R4).
func envelopeUpperBound(sender, context string, in SendInput, memoryKind bool) int {
	m := Message{
		Channel:  in.Channel,
		Sender:   sender,
		Context:  context,
		Type:     in.Type,
		Content:  in.Content,
		ReplyTo:  in.ReplyTo,
		Metadata: in.Metadata,
		Refs:     in.Refs,
	}
	size := envelopeBytes(m)
	// Seq and CreatedAt are always-present int fields; the zero value above
	// already contributes one digit ("0") each, so add the rest of their
	// worst-case width.
	size += (upperBoundSeqDigits - 1) + (upperBoundCreatedAtDigits - 1)
	if memoryKind {
		// memory_id and revision are absent (omitempty) above, so account for
		// the full `"memory_id":<digits>` and `"revision":<digits>` text
		// (field name, quotes, colon, leading comma) plus worst-case width.
		size += len(`,"memory_id":`) + upperBoundSeqDigits
		size += len(`,"revision":`) + upperBoundSeqDigits
	}
	return size
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

// senderContext reads a session's registered context, owner-qualified (R3)
// so it always reflects this process's own registration, never a
// replacement's after a name takeover. It runs against either b.db or a
// *sql.Tx (queryRower); every write path reads it on the write transaction,
// after that transaction's own auth(tx, as) check.
func (b *Bus) senderContext(q queryRower, as string) (string, error) {
	var c string
	if err := q.QueryRow("SELECT context FROM sessions WHERE sender=? AND owner=?", as, b.owner).Scan(&c); err != nil {
		return "", err
	}
	return c, nil
}

// sendEnvelope resolves the sender's owner-qualified context on q and
// validates in's worst-case envelope size against max_message_kib (R4),
// returning the context and size for the caller to reuse (inserting the
// row, charging the rate limit). Called twice per write: once on b.db as a
// preflight, before the inspect hook and checkCapacity run, so an oversized
// input can never trigger either (C1); and again on the write tx as the
// authoritative check, since context could change between the two.
func (b *Bus) sendEnvelope(q queryRower, as string, in SendInput, memoryKind bool) (context string, size int, err error) {
	context, err = b.senderContext(q, as)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, errf("not_registered", false, "%q is not registered in this process; call register", as)
		}
		return "", 0, internal(err)
	}
	size = envelopeUpperBound(as, context, in, memoryKind)
	if size > b.cfg.MaxMessageKiB*1024 {
		return "", 0, errf("validation", false, "envelope exceeds max_message_kib (%d KiB)", b.cfg.MaxMessageKiB)
	}
	return context, size, nil
}

// marshalLen returns the serialized size of v. Used to compute an exact
// reserve for a result's non-record fields (C2) so a byte ceiling covers
// the whole serialized result, not just an approximate guess. Every caller
// passes a value built entirely of strings, ints, bools, and slices of
// those, so json.Marshal cannot fail.
func marshalLen(v any) int {
	j, _ := json.Marshal(v)
	return len(j)
}

// receiptResult unmarshals a stored receipt result back into a SendResult.
func receiptResult(raw json.RawMessage) (SendResult, error) {
	var res SendResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return SendResult{}, internal(err)
	}
	return res, nil
}

func (b *Bus) Send(as string, in SendInput) (SendResult, error) {
	if err := b.auth(b.db, as); err != nil {
		return SendResult{}, err
	}
	if err := validateSendShape(in); err != nil {
		return SendResult{}, err
	}
	key := in.IdempotencyKey
	in.IdempotencyKey = ""

	// Serialize concurrent calls sharing this (sender, key) so two racing
	// retries can't both miss the receipt check below and both run the
	// inspection hook before either commits (I3). Released on every return
	// path through commit.
	if key != "" {
		defer b.lockKey(as, key)()
	}

	// Cheap read-only replay check before any mutable-state lookup (R2): a
	// keyed retry must replay its stored result even if the channel or
	// reply_to it named has since been evicted, rather than failing not_found.
	if prev, hit, err := b.checkReceipt(b.db, as, key, in); err != nil {
		return SendResult{}, err
	} else if hit {
		return receiptResult(prev)
	}

	kind, err := b.validateSendRefs(in)
	if err != nil {
		return SendResult{}, err
	}

	// Preflight envelope-size gate (C1): must run before inspect and
	// checkCapacity so an oversized input can never trigger the hook or
	// inline eviction. Re-checked authoritatively on the write tx below,
	// since context could change between here and there.
	if _, _, err := b.sendEnvelope(b.db, as, in, kind == "memory"); err != nil {
		return SendResult{}, err
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
	defer func() { _ = tx.Rollback() }()

	// Re-verify ownership on the transaction that is about to write (R3): a
	// process stalled past the 30s heartbeat window may have lost this name
	// to a newer registration between the preflight check above and here.
	if err := b.auth(tx, as); err != nil {
		return SendResult{}, err
	}

	// Re-check inside the transaction: closes the race where two sends with the
	// same key both passed the read-only check above concurrently.
	if prev, hit, err := b.checkReceipt(tx, as, key, in); err != nil {
		return SendResult{}, err
	} else if hit {
		return receiptResult(prev)
	}

	// Authoritative envelope-size gate (R4/C1): same check as the preflight
	// above, re-read on the write transaction.
	context, size, err := b.sendEnvelope(tx, as, in, kind == "memory")
	if err != nil {
		return SendResult{}, err
	}

	// Charge the rate limit only for a send that has cleared the hook and
	// capacity checks and is about to be accepted; a receipt replay never
	// reaches this line, so replays are free. Charged with the same size
	// used for the gate above (R4), not a partial one that omits
	// channel/sender/context/type.
	if !b.limits.allow(as, size, b.Now()) {
		return SendResult{}, errf("rate_limited", true, "send rate limit exceeded for %s", as)
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
	if err := b.auth(b.db, as); err != nil {
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
	defer func() { _ = rows.Close() }()
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

// trimFramingBytes is a generous, fixed allowance for one record's own
// array punctuation (comma, whitespace) in the enclosing JSON array.
// trimHardCeilingBytes is the absolute cap on any trimmed result,
// regardless of what limit a caller (or misconfiguration) asks for.
//
// Earlier this package also folded a blind, fixed per-result allowance into
// trimToBytes itself for the enclosing result object's other fields (batch,
// instruction, gaps, notice, next, ...). That is not generous enough: those
// fields are variable length (e.g. one gap or expired-channel entry per
// evicted/idle subscription), so a fixed guess can still under-reserve and
// let a serialized result exceed the hard ceiling (C2). Every caller with
// such fields now computes its own exact reserve — the real serialized size
// of a placeholder result with Messages/Hits cleared — and subtracts it
// from limit before calling trimToBytes (see receiveOnce, Search; History
// returns a bare slice with no sibling fields, so its reserve is zero).
const (
	trimFramingBytes     = 32
	trimHardCeilingBytes = 4 << 20
)

// trimToBytes keeps whole items up to limit bytes as measured by size plus
// the per-record framing allowance, clamped to the 4 MiB hard ceiling.
// Shared by trimBytes (messages) and search's SearchHit paging. limit must
// already have any reserve for the caller's other result fields subtracted
// (see the package comment above).
//
// The first item is always kept even if it alone exceeds the limit: a legal
// max_message_kib record above result_default_kib must still be
// deliverable, or a receiver could never make progress past it. This is
// deliberate, not a bug.
func trimToBytes[T any](items []T, size func(T) int, limit int) []T {
	limit = min(limit, trimHardCeilingBytes)
	total := 0
	for i, it := range items {
		total += size(it) + trimFramingBytes
		if total > limit && i > 0 {
			return items[:i]
		}
	}
	return items
}

// trimBytes keeps whole records up to limit bytes, always at least one.
// History returns a bare []Message with no sibling result fields, so no
// reserve is needed here (C2).
func trimBytes(msgs []Message, limit int) []Message {
	return trimToBytes(msgs, envelopeBytes, limit)
}
