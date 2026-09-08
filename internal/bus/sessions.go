package bus

import (
	"fmt"
	"strings"
	"unicode"
)

type Registration struct {
	Sender  string           `json:"as"`
	Resumed bool             `json:"resumed"`
	Pending []PendingChannel `json:"pending"`
}

type PendingChannel struct {
	Channel string `json:"channel"`
	Pending int64  `json:"pending"`
}

type Session struct {
	Sender       string `json:"sender"`
	Context      string `json:"context"`
	RegisteredAt int64  `json:"registered_at"`
}

// validateName enforces 1–128 UTF-8 bytes, no control characters, no '/'.
func validateName(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return errf("validation", false, "name must be 1-128 bytes")
	}
	if strings.ContainsRune(name, '/') {
		return errf("validation", false, "name must not contain '/'")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errf("validation", false, "name must not contain control characters")
		}
	}
	return nil
}

func (b *Bus) Register(name, parent, context string, resume bool) (Registration, error) {
	if err := validateName(name); err != nil {
		return Registration{}, err
	}
	now := b.nowMs()
	tx, err := b.db.Begin()
	if err != nil {
		return Registration{}, internal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM sessions WHERE heartbeat < ?", now-attachmentExpiryMs); err != nil {
		return Registration{}, internal(err)
	}
	base, sep := name, ""
	if parent != "" {
		base, sep = parent+"/"+name, "-"
	}
	display := base
	for n := 2; ; n++ {
		var taken int
		if err := tx.QueryRow("SELECT count(*) FROM sessions WHERE sender=?", display).Scan(&taken); err != nil {
			return Registration{}, internal(err)
		}
		if taken == 0 {
			break
		}
		display = fmt.Sprintf("%s%s%d", base, sep, n)
	}
	if _, err := tx.Exec("INSERT INTO sessions(sender,context,owner,heartbeat,registered_at) VALUES(?,?,?,?,?)",
		display, context, b.owner, now, now); err != nil {
		return Registration{}, internal(err)
	}
	if !resume {
		if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=?", display); err != nil {
			return Registration{}, internal(err)
		}
	}
	rows, err := tx.Query(`SELECT s.channel,
	  (SELECT count(*) FROM messages m WHERE m.channel=s.channel AND m.seq>s.cursor_seq AND m.tombstone=0 AND m.sender<>s.sender)
	  FROM subscriptions s WHERE s.sender=? ORDER BY s.channel`, display)
	if err != nil {
		return Registration{}, internal(err)
	}
	reg := Registration{Sender: display, Pending: []PendingChannel{}}
	for rows.Next() {
		var p PendingChannel
		if err := rows.Scan(&p.Channel, &p.Pending); err != nil {
			rows.Close()
			return Registration{}, internal(err)
		}
		reg.Pending = append(reg.Pending, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Registration{}, internal(err)
	}
	rows.Close()
	reg.Resumed = len(reg.Pending) > 0
	if err := tx.Commit(); err != nil {
		return Registration{}, internal(err)
	}
	return reg, nil
}

// auth succeeds only for a live session row registered by this process. It
// runs against either b.db (a cheap preflight check) or a *sql.Tx, so every
// mutating transaction can re-verify ownership immediately after opening:
// a process that stalls past the 30s heartbeat window may have lost its
// name to a newer registration between a preflight check and the write.
func (b *Bus) auth(q queryRower, as string) error {
	if as == "" {
		return errf("validation", false, "as is required: pass the display name returned by register")
	}
	var n int
	if err := q.QueryRow("SELECT count(*) FROM sessions WHERE sender=? AND owner=?", as, b.owner).Scan(&n); err != nil {
		return internal(err)
	}
	if n == 0 {
		return errf("not_registered", false, "%q is not registered in this process; call register", as)
	}
	return nil
}

// Heartbeat refreshes every session this process owns. Rows lost to a newer
// registration are simply not refreshed, so auth fails for them afterwards.
func (b *Bus) Heartbeat() error {
	if _, err := b.db.Exec("UPDATE sessions SET heartbeat=? WHERE owner=?", b.nowMs(), b.owner); err != nil {
		return internal(err)
	}
	return nil
}

func (b *Bus) Discover(as string) ([]Session, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	if !b.cfg.DiscoveryEnabled {
		return nil, errf("validation", false, "discovery is disabled by configuration")
	}
	rows, err := b.db.Query("SELECT sender, context, registered_at FROM sessions WHERE heartbeat >= ? ORDER BY sender", b.nowMs()-attachmentExpiryMs)
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
