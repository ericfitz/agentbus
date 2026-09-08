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
	if err := b.auth(as); err != nil {
		return err
	}
	if from == "" {
		from = "now"
	}
	if from != "now" && from != "oldest" {
		return errf("validation", false, "from must be now or oldest")
	}
	var evicted int64
	if err := b.db.QueryRow("SELECT evicted_before_seq FROM channels WHERE name=?", channel).Scan(&evicted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errf("not_found", false, "channel %q does not exist", channel)
		}
		return internal(err)
	}
	var cursor int64
	if from == "oldest" {
		cursor = max(evicted-1, 0)
	} else if err := b.db.QueryRow("SELECT coalesce(max(seq),0) FROM messages").Scan(&cursor); err != nil {
		return internal(err)
	}
	_, err := b.db.Exec("INSERT OR IGNORE INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,?,?)", as, channel, cursor, b.nowMs())
	return internal(err)
}

func (b *Bus) Unsubscribe(as, channel string) error {
	if err := b.auth(as); err != nil {
		return err
	}
	_, err := b.db.Exec("DELETE FROM subscriptions WHERE sender=? AND channel=?", as, channel)
	return internal(err)
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
	if err := b.auth(as); err != nil {
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
			res.Expired = append(res.Expired, s.channel)
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
	for i := range subs {
		var evicted int64
		if err := tx.QueryRow("SELECT evicted_before_seq FROM channels WHERE name=?", subs[i].channel).Scan(&evicted); err != nil {
			return res, internal(err)
		}
		if subs[i].cursor < evicted-1 {
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
	limit := b.cfg.ResultDefaultKiB * 1024

	switch {
	case len(pending) > 0:
		// Replay the ORIGINAL include_own the batch was created with, decoded
		// from the shared token (all channels in one pending batch always
		// share one token), not whatever this call's in.IncludeOwn happens to
		// be: the caller cannot change what a batch already means.
		q, a := buildBounded(pending, ownClause(tokenIncludesOwn(pending[0].pendingToken)))
		msgs, err := queryMessages(tx, q, a)
		if err != nil {
			return res, err
		}
		res.Messages = trimBytes(msgs, redeliveryHardCeilingBytes)
		res.Batch = pending[0].pendingToken
		res.Redelivered = true
		res.Instruction = fmt.Sprintf("This batch was delivered before and not acknowledged. Pass ack=%q on your next receive to advance past it.", res.Batch)
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
		q, a := buildNew(inScope)
		msgs, err := queryMessages(tx, q, a)
		if err != nil {
			return res, err
		}
		res.Messages = trimBytes(msgs, limit)
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
	if err := tx.QueryRow("SELECT message FROM notices WHERE kind='capacity'").Scan(&res.Notice); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, internal(err)
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
