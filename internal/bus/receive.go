package bus

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

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
	if in.WaitSeconds < 0 || in.WaitSeconds > b.cfg.ReceiveMaxWaitSeconds {
		return ReceiveResult{}, errf("validation", false, "wait_seconds must be between 0 and %d", b.cfg.ReceiveMaxWaitSeconds)
	}
	// Wall-clock deadline: b.Now may be a test clock that does not advance.
	deadline := time.Now().Add(time.Duration(in.WaitSeconds) * time.Second)
	for {
		res, err := b.receiveOnce(as, in)
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

	q := "SELECT channel, cursor_seq, last_activity, pending_token, pending_end_seq FROM subscriptions WHERE sender=?"
	args := []any{as}
	if len(in.Channels) > 0 {
		q += " AND channel IN (?" + strings.Repeat(",?", len(in.Channels)-1) + ")"
		for _, c := range in.Channels {
			args = append(args, c)
		}
	}
	rows, err := tx.Query(q+" ORDER BY channel", args...)
	if err != nil {
		return res, internal(err)
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

	var pending []subRow
	for _, s := range subs {
		if s.pendingToken != "" {
			pending = append(pending, s)
		}
	}
	own := ""
	if !in.IncludeOwn {
		own = " AND sender<>?"
	}
	build := func(set []subRow, bounded bool) (string, []any) {
		var conds []string
		var a []any
		for _, s := range set {
			if bounded {
				conds = append(conds, "(channel=? AND seq>? AND seq<=?)")
				a = append(a, s.channel, s.cursor, s.pendingEnd)
			} else {
				conds = append(conds, "(channel=? AND seq>?)")
				a = append(a, s.channel, s.cursor)
			}
		}
		q := "SELECT " + messageColumns + " FROM messages WHERE (" + strings.Join(conds, " OR ") + ") AND tombstone=0" + own + " ORDER BY seq LIMIT ?"
		if own != "" {
			a = append(a, as)
		}
		a = append(a, in.Count)
		return q, a
	}
	limit := b.cfg.ResultDefaultKiB * 1024

	switch {
	case len(pending) > 0:
		q, a := build(pending, true)
		msgs, err := queryMessages(tx, q, a)
		if err != nil {
			return res, err
		}
		res.Messages = trimBytes(msgs, limit)
		res.Batch = pending[0].pendingToken
		res.Redelivered = true
		res.Instruction = fmt.Sprintf("This batch was delivered before and not acknowledged. Pass ack=%q on your next receive to advance past it.", res.Batch)
	case len(subs) > 0:
		q, a := build(subs, false)
		msgs, err := queryMessages(tx, q, a)
		if err != nil {
			return res, err
		}
		res.Messages = trimBytes(msgs, limit)
		if len(res.Messages) > 0 {
			tok, err := randomToken()
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
