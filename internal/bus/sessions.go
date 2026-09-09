package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
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
	if err := NameRule(name); err != nil {
		return errf("validation", false, "name %v", err)
	}
	return nil
}

// NameRule is the identity name rule shared by Register and the CLI: 1-128
// UTF-8 bytes, no control characters, no '/'. The error is a plain
// sentence without the "name" subject so callers can supply their own.
func NameRule(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return errors.New("must be 1-128 bytes")
	}
	if strings.ContainsRune(name, '/') {
		return errors.New("must not contain '/'")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// validateParent bounds parent to 512 UTF-8 bytes with no control
// characters. Unlike name, parent MAY contain '/': it is itself a display
// name such as "Sam/impl".
func validateParent(parent string) error {
	if len(parent) > 512 {
		return errf("validation", false, "parent must be at most 512 bytes")
	}
	for _, r := range parent {
		if unicode.IsControl(r) {
			return errf("validation", false, "parent must not contain control characters")
		}
	}
	return nil
}

// validateContext bounds context to 1024 UTF-8 bytes with no control
// characters. The spec does not set a bound on context; this cap is a
// controller ruling (M12.2) so a single register call cannot make
// discover/list results exceed the 4 MiB hard ceiling — recorded here as
// human-visible per the review process.
func validateContext(context string) error {
	if len(context) > 1024 {
		return errf("validation", false, "context must be at most 1024 bytes")
	}
	for _, r := range context {
		if unicode.IsControl(r) {
			return errf("validation", false, "context must not contain control characters")
		}
	}
	return nil
}

func (b *Bus) Register(name, parent, context string, resume bool) (Registration, error) {
	if err := validateName(name); err != nil {
		return Registration{}, err
	}
	if err := validateParent(parent); err != nil {
		return Registration{}, err
	}
	if err := validateContext(context); err != nil {
		return Registration{}, err
	}
	now := b.nowMs()
	tx, err := b.db.Begin()
	if err != nil {
		return Registration{}, internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("DELETE FROM sessions WHERE heartbeat < ?", now-attachmentExpiryMs); err != nil {
		return Registration{}, internal(err)
	}
	base, sep := name, ""
	if parent != "" {
		base, sep = parent+"/"+name, "-"
	}
	// Walk base, base2, base3, ... to the first name that is free or that
	// this process already holds. Reusing our own row makes register
	// idempotent: Claude Code keeps the MCP server across /clear, so a
	// re-register from the same process must not mint a fresh suffix.
	display, reused := base, false
	for n := 2; ; n++ {
		var owner string
		err := tx.QueryRow("SELECT owner FROM sessions WHERE sender=?", display).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return Registration{}, internal(err)
		}
		if owner == b.owner {
			reused = true
			break
		}
		display = fmt.Sprintf("%s%s%d", base, sep, n)
	}
	if reused {
		if _, err := tx.Exec("UPDATE sessions SET context=?, heartbeat=? WHERE sender=?", context, now, display); err != nil {
			return Registration{}, internal(err)
		}
	} else if _, err := tx.Exec("INSERT INTO sessions(sender,context,owner,heartbeat,registered_at) VALUES(?,?,?,?,?)",
		display, context, b.owner, now, now); err != nil {
		return Registration{}, internal(err)
	}
	if !resume {
		if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=?", display); err != nil {
			return Registration{}, internal(err)
		}
	} else {
		// The tick no longer reaps idle subscriptions itself (that would erase
		// the evidence Receive needs to report them as expired), so a resuming
		// registration must drop its own idle-expired ones before listing
		// pending channels, or an expired subscription would show up as pending.
		idle := int64(b.cfg.CursorIdleHours) * 3_600_000
		if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=? AND last_activity < ?", display, now-idle); err != nil {
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
			_ = rows.Close()
			return Registration{}, internal(err)
		}
		reg.Pending = append(reg.Pending, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Registration{}, internal(err)
	}
	_ = rows.Close()
	reg.Resumed = reused || len(reg.Pending) > 0
	if err := tx.Commit(); err != nil {
		return Registration{}, internal(err)
	}
	// Pending is bounded by the number of subscriptions, not by anything a
	// caller controls, but a session resuming thousands of subscriptions
	// could still exceed the hard ceiling; trim it to the same whole-record
	// budget as ListChannels/Discover. No separate reserve is subtracted
	// for Sender/Resumed: Sender is bounded by parent (validateParent, <=512
	// bytes) + "/" + name (validateName, <=128 bytes) + a small numeric
	// disambiguation suffix, and Resumed is a bool, both negligible next to
	// the ceiling.
	reg.Pending = trimToBytes(reg.Pending, pendingBytes, min(b.cfg.ResultDefaultKiB*1024, trimHardCeilingBytes))
	return reg, nil
}

// pendingBytes measures the serialized size of a PendingChannel; it holds
// only a string and an int, so json.Marshal cannot fail.
func pendingBytes(p PendingChannel) int {
	j, _ := json.Marshal(p)
	return len(j)
}

// sessionBytes measures the serialized size of a Session; it holds only
// strings and ints, so json.Marshal cannot fail.
func sessionBytes(s Session) int {
	j, _ := json.Marshal(s)
	return len(j)
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
	sessions, err := b.liveSessions(b.db)
	if err != nil {
		return nil, err
	}
	return trimToBytes(sessions, sessionBytes, min(b.cfg.ResultDefaultKiB*1024, trimHardCeilingBytes)), nil
}
