package bus

import (
	"strings"
	"time"
)

// Wait blocks until at least one undelivered message is available for as on
// its subscribed channels (all of them when channels is empty), then returns
// those messages. It is read-only: cursors, pending batches, activity, and
// sessions are untouched, so a following Receive returns the same messages.
// It does not require as to be registered by this process, only subscribed.
// match, when non-nil, decides which messages count; messages it rejects are
// skipped for the rest of the call. timeout <= 0 waits forever. A timeout
// returns no messages and no error.
func (b *Bus) Wait(as string, channels []string, includeOwn bool, match func(Message) bool, timeout time.Duration) ([]Message, error) {
	if as == "" {
		return nil, errf("validation", false, "as is required")
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	skipBelow := map[string]int64{} // channel -> highest seq already rejected by match
	for {
		msgs, err := b.peek(as, channels, includeOwn, skipBelow)
		if err != nil {
			return nil, err
		}
		var out []Message
		for _, m := range msgs {
			if match == nil || match(m) {
				out = append(out, m)
			} else if m.Seq > skipBelow[m.Channel] {
				skipBelow[m.Channel] = m.Seq
			}
		}
		if len(out) > 0 {
			return out, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, nil
		}
		time.Sleep(waitPollInterval)
	}
}

// peek selects, without side effects, the messages Receive would deliver
// next for as: everything past each subscription's cursor. skipBelow raises
// the floor per channel for messages a caller has already rejected.
func (b *Bus) peek(as string, channels []string, includeOwn bool, skipBelow map[string]int64) ([]Message, error) {
	rows, err := b.db.Query("SELECT channel, cursor_seq FROM subscriptions WHERE sender=? ORDER BY channel", as)
	if err != nil {
		return nil, internal(err)
	}
	var conds []string
	var args []any
	for rows.Next() {
		var ch string
		var cursor int64
		if err := rows.Scan(&ch, &cursor); err != nil {
			_ = rows.Close()
			return nil, internal(err)
		}
		// Same guard as receive's delivery loop: a leftover observer-only row
		// (or a CLI `agentbus wait` process, never an observer) must not peek
		// another identity's inbox.
		if !b.dmReadable(as, ch) {
			continue
		}
		if len(channels) > 0 && !contains(channels, ch) {
			continue
		}
		conds = append(conds, "(channel=? AND seq>?)")
		args = append(args, ch, max(cursor, skipBelow[ch]))
	}
	if err := rows.Close(); err != nil {
		return nil, internal(err)
	}
	if len(conds) == 0 {
		return nil, errf("not_found", false, "%q has no matching subscriptions; register first", as)
	}
	q := "SELECT " + messageCols("messages") + " FROM messages WHERE (" + strings.Join(conds, " OR ") + ") AND tombstone=0"
	if !includeOwn {
		q += " AND sender<>?"
		args = append(args, as)
	}
	q += " ORDER BY seq LIMIT ?"
	args = append(args, b.cfg.ReceiveMaxCount)
	return queryMessages(b.db, q, args)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
