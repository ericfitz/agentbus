package bus

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// redeliveryHardCeilingBytes bounds a redelivered batch's own size, independent
// of result_default_kib (which can change between calls and must not shrink
// "the same batch").
const redeliveryHardCeilingBytes = 4 * 1024 * 1024

// maxBatchTokenLen is the fixed length of every batch token: randomToken's
// 24 hex characters plus the ":o0"/":o1" include_own suffix makeBatchToken
// appends. Used as a placeholder when reserving space for a new-selection
// token that hasn't been generated yet (C2) — a redelivery reuses an
// existing token instead, so it never needs the placeholder.
const maxBatchTokenLen = 24 + len(":o1")

// maxNoticesPerReceive bounds how many Expired and Gaps entries (each,
// independently) one receive call reports. Without a bound, a sender with
// enough idle or evicted-behind subscriptions could make the metadata alone
// (channel names up to 128 bytes each, per validateName) approach or exceed
// the 4 MiB hard ceiling, entirely independent of any message (C2). Expiry
// deletes and gap cursor advances apply ONLY to the reported subset; the
// remainder are left untouched (still idle, still gapped) to surface on a
// later call. Subscriptions are always processed in channel-name order (the
// loading query is ORDER BY channel), so which ones get reported first is
// deterministic.
const maxNoticesPerReceive = 256

type ReceiveInput struct {
	Ack         string   `json:"ack,omitempty"`
	Count       int      `json:"count,omitempty"`
	WaitSeconds int      `json:"wait_seconds,omitempty"`
	Channels    []string `json:"channels,omitempty"`
	IncludeOwn  bool     `json:"include_own,omitempty"`
}

type Gap struct {
	Channel string `json:"channel"`
	From    int64  `json:"from"`
	To      int64  `json:"to"`
}

type ReceiveResult struct {
	Messages    []Message `json:"messages"`
	Batch       string    `json:"batch,omitempty"`
	Redelivered bool      `json:"redelivered,omitempty"`
	AckIgnored  bool      `json:"ack_ignored,omitempty"`
	Gaps        []Gap     `json:"gaps,omitempty"`
	Expired     []string  `json:"expired,omitempty"`
	Notice      string    `json:"notice,omitempty"`
	Instruction string    `json:"instruction,omitempty"`
}

func (b *Bus) Subscribe(as, channel, from string) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	if from == "" {
		from = "now"
	}
	if from != "now" && from != "oldest" {
		return errf("validation", false, "from must be now or oldest")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer tx.Rollback()
	// Re-verify ownership on the transaction that is about to write (R3).
	if err := b.auth(tx, as); err != nil {
		return err
	}
	var evicted int64
	if err := tx.QueryRow("SELECT evicted_before_seq FROM channels WHERE name=?", channel).Scan(&evicted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errf("not_found", false, "channel %q does not exist", channel)
		}
		return internal(err)
	}
	var cursor int64
	if from == "oldest" {
		cursor = max(evicted-1, 0)
	} else if err := tx.QueryRow("SELECT coalesce(max(seq),0) FROM messages").Scan(&cursor); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,?,?)", as, channel, cursor, b.nowMs()); err != nil {
		return internal(err)
	}
	return internal(tx.Commit())
}

func (b *Bus) Unsubscribe(as, channel string) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer tx.Rollback()
	// Re-verify ownership on the transaction that is about to write (R3).
	if err := b.auth(tx, as); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=? AND channel=?", as, channel); err != nil {
		return internal(err)
	}
	return internal(tx.Commit())
}

type subRow struct {
	channel      string
	cursor       int64
	pendingToken string
	pendingEnd   int64
}

// A batch token carries the include_own flag it was created with (":o1"/":o0"
// suffix, no new column) so redelivery can replay the ORIGINAL selection
// predicate instead of whatever include_own the redelivering call happens to
// pass. Ack matching still compares the whole token string, unaffected.
func makeBatchToken(includeOwn bool) (string, error) {
	tok, err := randomToken()
	if err != nil {
		return "", err
	}
	if includeOwn {
		return tok + ":o1", nil
	}
	return tok + ":o0", nil
}

func tokenIncludesOwn(token string) bool {
	return strings.HasSuffix(token, ":o1")
}

func (b *Bus) Receive(as string, in ReceiveInput) (ReceiveResult, error) {
	if err := b.auth(b.db, as); err != nil {
		return ReceiveResult{}, err
	}
	if in.Count <= 0 {
		in.Count = b.cfg.ReceiveDefaultCount
	}
	if in.Count > b.cfg.ReceiveMaxCount {
		in.Count = b.cfg.ReceiveMaxCount
	}
	if in.WaitSeconds < 0 {
		return ReceiveResult{}, errf("validation", false, "wait_seconds must not be negative")
	}
	if in.WaitSeconds > b.cfg.ReceiveMaxWaitSeconds {
		in.WaitSeconds = b.cfg.ReceiveMaxWaitSeconds
	}
	// Wall-clock deadline: b.Now may be a test clock that does not advance.
	deadline := time.Now().Add(time.Duration(in.WaitSeconds) * time.Second)
	first := true
	ackIgnored := false
	for {
		res, err := b.receiveOnce(as, in)
		if first {
			ackIgnored = res.AckIgnored
			first = false
		} else {
			// The ack, if any, was only ever applied on the first pass; carry its
			// AckIgnored verdict forward so a later empty poll doesn't lose it.
			res.AckIgnored = ackIgnored
		}
		if err != nil || len(res.Messages) > 0 || len(res.Gaps) > 0 || len(res.Expired) > 0 || in.WaitSeconds == 0 {
			return res, err
		}
		if time.Now().After(deadline) {
			return res, nil
		}
		in.Ack = "" // applied on the first pass only
		time.Sleep(receivePollInterval)
	}
}

func (b *Bus) receiveOnce(as string, in ReceiveInput) (ReceiveResult, error) {
	res := ReceiveResult{Messages: []Message{}}
	now := b.nowMs()
	tx, err := b.db.Begin()
	if err != nil {
		return res, internal(err)
	}
	defer tx.Rollback()

	// Re-verify ownership on every iteration of the long-poll (R3): a
	// process stalled past the 30s heartbeat window may have lost this name
	// to a newer registration between Receive's preflight check and this
	// particular poll, and each poll opens its own transaction here.
	if err := b.auth(tx, as); err != nil {
		return res, err
	}

	// Load every subscription for this sender, not just ones matching the
	// channels filter: a pending batch is redelivered unconditionally (spec
	// step 3), regardless of which channels this call asked about, so a
	// subscription outside the filter must still be seen when it has one.
	rows, err := tx.Query("SELECT channel, cursor_seq, last_activity, pending_token, pending_end_seq FROM subscriptions WHERE sender=? ORDER BY channel", as)
	if err != nil {
		return res, internal(err)
	}
	inFilter := func(ch string) bool {
		if len(in.Channels) == 0 {
			return true
		}
		for _, c := range in.Channels {
			if c == ch {
				return true
			}
		}
		return false
	}
	var subs []subRow
	idle := int64(b.cfg.CursorIdleHours) * 3_600_000
	for rows.Next() {
		var s subRow
		var last int64
		if err := rows.Scan(&s.channel, &s.cursor, &last, &s.pendingToken, &s.pendingEnd); err != nil {
			rows.Close()
			return res, internal(err)
		}
		// Outside the filter with nothing pending: not involved in this call,
		// leave it untouched (no activity touch, no expiry check).
		if !inFilter(s.channel) && s.pendingToken == "" {
			continue
		}
		if last < now-idle {
			// C2: cap how many get reported (and thus reaped, below); past
			// the cap, leave the subscription alone so it can still not be
			// delivered to (it's genuinely idle) without inflating this
			// call's metadata. It surfaces again on a later call.
			if len(res.Expired) < maxNoticesPerReceive {
				res.Expired = append(res.Expired, s.channel)
			}
			continue
		}
		subs = append(subs, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return res, internal(err)
	}
	rows.Close()

	for _, ch := range res.Expired {
		if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=? AND channel=?", as, ch); err != nil {
			return res, internal(err)
		}
	}
	for _, s := range subs {
		if _, err := tx.Exec("UPDATE subscriptions SET last_activity=? WHERE sender=? AND channel=?", now, as, s.channel); err != nil {
			return res, internal(err)
		}
	}

	if in.Ack != "" {
		r, err := tx.Exec("UPDATE subscriptions SET cursor_seq=pending_end_seq, pending_token='', pending_end_seq=0 WHERE sender=? AND pending_token=?", as, in.Ack)
		if err != nil {
			return res, internal(err)
		}
		n, err := r.RowsAffected()
		if err != nil {
			return res, internal(err)
		}
		if n == 0 {
			res.AckIgnored = true
		} else {
			for i := range subs {
				if subs[i].pendingToken == in.Ack {
					subs[i].cursor, subs[i].pendingToken, subs[i].pendingEnd = subs[i].pendingEnd, "", 0
				}
			}
		}
	}

	// Gaps: cursor below the channel's retention boundary. Resume from the
	// oldest survivor and drop a now-unreachable pending batch for that channel.
	gapsDeferred := false
	for i := range subs {
		var evicted int64
		if err := tx.QueryRow("SELECT evicted_before_seq FROM channels WHERE name=?", subs[i].channel).Scan(&evicted); err != nil {
			return res, internal(err)
		}
		if subs[i].cursor < evicted-1 {
			// C2/C3: cap how many gaps get reported (and their cursors
			// advanced) per call; past the cap, leave the cursor where it
			// is so the gap surfaces again on a later call rather than
			// inflating this call's metadata. gapsDeferred then suppresses
			// ALL delivery below (C3): this subscription's survivors past
			// the gap must not be delivered (and its pending state must not
			// be touched) until its gap has actually been reported —
			// otherwise a later ack could advance past the gap unreported.
			if len(res.Gaps) >= maxNoticesPerReceive {
				gapsDeferred = true
				continue
			}
			res.Gaps = append(res.Gaps, Gap{Channel: subs[i].channel, From: subs[i].cursor + 1, To: evicted - 1})
			subs[i].cursor = evicted - 1
			if _, err := tx.Exec("UPDATE subscriptions SET cursor_seq=? WHERE sender=? AND channel=?", subs[i].cursor, as, subs[i].channel); err != nil {
				return res, internal(err)
			}
			if subs[i].pendingToken != "" && subs[i].pendingEnd <= subs[i].cursor {
				subs[i].pendingToken, subs[i].pendingEnd = "", 0
				if _, err := tx.Exec("UPDATE subscriptions SET pending_token='', pending_end_seq=0 WHERE sender=? AND channel=?", as, subs[i].channel); err != nil {
					return res, internal(err)
				}
			}
		}
	}

	// The notice must be known before the reserve computation below (C2), so
	// it's read here rather than after message selection as before.
	if err := tx.QueryRow("SELECT message FROM notices WHERE kind='capacity'").Scan(&res.Notice); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, internal(err)
	}

	var pending, inScope []subRow
	for _, s := range subs {
		if s.pendingToken != "" {
			pending = append(pending, s)
		}
		if inFilter(s.channel) {
			inScope = append(inScope, s)
		}
	}
	// ownClause excludes the sender's own messages unless includeOwn is set.
	ownClause := func(includeOwn bool) string {
		if includeOwn {
			return ""
		}
		return " AND sender<>?"
	}
	// buildBounded reproduces "the same batch": the seq range fixed at
	// creation, with no count re-filtering from the current call (in.Count
	// can differ between the original delivery and this redelivery), and the
	// own filter the caller passes here (its meaning depends on which of
	// creation's or the current call's include_own the caller decodes).
	buildBounded := func(set []subRow, own string) (string, []any) {
		var conds []string
		var a []any
		for _, s := range set {
			conds = append(conds, "(channel=? AND seq>? AND seq<=?)")
			a = append(a, s.channel, s.cursor, s.pendingEnd)
		}
		q := "SELECT " + messageColumns + " FROM messages WHERE (" + strings.Join(conds, " OR ") + ") AND tombstone=0" + own + " ORDER BY seq"
		if own != "" {
			a = append(a, as)
		}
		return q, a
	}
	ownNew := ownClause(in.IncludeOwn)
	buildNew := func(set []subRow) (string, []any) {
		var conds []string
		var a []any
		for _, s := range set {
			conds = append(conds, "(channel=? AND seq>?)")
			a = append(a, s.channel, s.cursor)
		}
		q := "SELECT " + messageColumns + " FROM messages WHERE (" + strings.Join(conds, " OR ") + ") AND tombstone=0" + ownNew + " ORDER BY seq LIMIT ?"
		if ownNew != "" {
			a = append(a, as)
		}
		a = append(a, in.Count)
		return q, a
	}

	// reserve returns the exact serialized size of the result this branch
	// will return, with Messages cleared, so message selection can be
	// trimmed against limit-reserve (C2): gaps, expired-channel names, the
	// notice, and batch/instruction are all variable length and a fixed
	// guess can under-reserve for them (e.g. many idle/evicted
	// subscriptions in one call).
	reserve := func(batch, instruction string, redelivered bool) int {
		r := res
		r.Messages = nil
		r.Batch = batch
		r.Instruction = instruction
		r.Redelivered = redelivered
		return marshalLen(r)
	}

	switch {
	case gapsDeferred:
		// C3: at least one gap notice couldn't be reported this call, so no
		// message delivery or pending-batch write can safely happen this
		// round either — see the comment on gapsDeferred above.
		res.Instruction = "Some gap notices exceeded this call's limit; call receive again to see the rest before message delivery resumes."
	case len(pending) > 0:
		// The redelivery token already exists at its final length, so no
		// placeholder is needed for it, unlike the new-selection case below.
		batch := pending[0].pendingToken
		instruction := fmt.Sprintf("This batch was delivered before and not acknowledged. Pass ack=%q on your next receive to advance past it.", batch)
		limit := redeliveryHardCeilingBytes - reserve(batch, instruction, true)
		if limit <= 0 {
			// C2: the reserve alone exceeds the hard ceiling (e.g. a huge
			// batch of gaps/expired channels). Return metadata only, leaving
			// the pending batch untouched for a later poll, rather than fail.
			break
		}
		// Replay the ORIGINAL include_own the batch was created with, decoded
		// from the shared token (all channels in one pending batch always
		// share one token), not whatever this call's in.IncludeOwn happens to
		// be: the caller cannot change what a batch already means.
		q, a := buildBounded(pending, ownClause(tokenIncludesOwn(batch)))
		msgs, err := queryMessages(tx, q, a)
		if err != nil {
			return res, err
		}
		res.Messages = trimToBytes(msgs, envelopeBytes, limit)
		res.Batch = batch
		res.Redelivered = true
		res.Instruction = instruction
		// Never let an ack of this token skip anything not actually shown here:
		// re-stamp each channel's pending_end_seq to the highest seq actually
		// redelivered (falling back to its cursor, i.e. nothing new, if this
		// round shows it none at all), whether that's lower than before because
		// the byte ceiling cut the tail or because a message in range was
		// tombstoned (superseded) since the original delivery.
		shown := map[string]int64{}
		for _, m := range res.Messages {
			shown[m.Channel] = m.Seq
		}
		for _, s := range pending {
			newEnd := shown[s.channel]
			if newEnd == 0 {
				newEnd = s.cursor
			}
			if newEnd != s.pendingEnd {
				if _, err := tx.Exec("UPDATE subscriptions SET pending_end_seq=? WHERE sender=? AND channel=?", newEnd, as, s.channel); err != nil {
					return res, internal(err)
				}
			}
		}
	case len(inScope) > 0:
		// The real token doesn't exist yet (only generated below, once we
		// know at least one message will be delivered), so reserve against a
		// max-length placeholder of the same shape.
		placeholderBatch := strings.Repeat("x", maxBatchTokenLen)
		instruction := fmt.Sprintf("Pass ack=%q on your next receive to acknowledge this batch.", placeholderBatch)
		limit := min(b.cfg.ResultDefaultKiB*1024, trimHardCeilingBytes) - reserve(placeholderBatch, instruction, false)
		if limit <= 0 {
			// C2: as above, return metadata only rather than fail.
			break
		}
		q, a := buildNew(inScope)
		msgs, err := queryMessages(tx, q, a)
		if err != nil {
			return res, err
		}
		res.Messages = trimToBytes(msgs, envelopeBytes, limit)
		if len(res.Messages) > 0 {
			tok, err := makeBatchToken(in.IncludeOwn)
			if err != nil {
				return res, internal(err)
			}
			res.Batch = tok
			ends := map[string]int64{}
			for _, m := range res.Messages {
				ends[m.Channel] = m.Seq
			}
			for ch, end := range ends {
				if _, err := tx.Exec("UPDATE subscriptions SET pending_token=?, pending_end_seq=? WHERE sender=? AND channel=?", res.Batch, end, as, ch); err != nil {
					return res, internal(err)
				}
			}
			res.Instruction = fmt.Sprintf("Pass ack=%q on your next receive to acknowledge this batch.", res.Batch)
		}
	}

	// Hard-cap safety net (C2): by construction this can never fire.
	// Expired/Gaps are bounded to maxNoticesPerReceive each (~128-byte
	// channel names, per validateName), and every branch above trims
	// messages against limit = (4 MiB or result_default_kib) - the exact
	// reserve for everything else in res, so the largest single legal
	// envelope (max_message_kib ≤ 1024 KiB, enforced by config validation)
	// always has room. If some future change breaks one of those
	// invariants, fail loudly rather than ship a result over the ceiling.
	if n := marshalLen(res); n > trimHardCeilingBytes {
		return res, internal(fmt.Errorf("serialized receive result of %d bytes exceeds the %d byte hard ceiling", n, trimHardCeilingBytes))
	}

	if err := tx.Commit(); err != nil {
		return res, internal(err)
	}
	return res, nil
}

func queryMessages(tx *sql.Tx, q string, args []any) ([]Message, error) {
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	return msgs, internal(err)
}
