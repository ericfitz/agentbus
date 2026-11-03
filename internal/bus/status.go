package bus

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// AgentStatus is one stored status revision (issue #43). Named so it does
// not collide with Status, the health report.
type AgentStatus struct {
	Identity string `json:"identity"`
	Revision int64  `json:"revision"`
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Problem  string `json:"problem,omitempty"`
	SetAt    int64  `json:"set_at"`
}

// SettableStatusTags are the tags an agent may set, in display order.
var SettableStatusTags = []string{"idle", "planning", "implementing", "reviewing", "waiting", "blocked", "needs-human"}

// StatusOffline is derived when a status is read; it is never stored.
const StatusOffline = "offline"

const (
	maxStatusRunes  = 200
	errStatusTag    = "tag must be one of idle, planning, implementing, reviewing, waiting, blocked, needs-human (offline is set by the bus)"
	errStatusSubagt = "subagents do not set a status; the top-level session's status covers them"
)

// Authorize reports whether this process holds a live session for as: auth
// on the bus connection, exported for read-only MCP tools.
func (b *Bus) Authorize(as string) error { return b.auth(b.db, as) }

// SetStatus records a status for the session as, which this process must
// hold (MCP path). A subagent name is rejected.
func (b *Bus) SetStatus(as, tag, text, problem string) (AgentStatus, error) {
	return b.writeStatus(as, true, tag, text, problem)
}

// SetStatusFor records a status for name from the CLI, which holds no
// session: name needs a live session row (the SessionLive rule).
func (b *Bus) SetStatusFor(name, tag, text, problem string) (AgentStatus, error) {
	return b.writeStatus(name, false, tag, text, problem)
}

func (b *Bus) writeStatus(name string, owned bool, tag, text, problem string) (AgentStatus, error) {
	tx, err := b.db.Begin()
	if err != nil {
		return AgentStatus{}, internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if owned {
		err = b.auth(tx, name)
	} else {
		err = b.liveInTx(tx, name)
	}
	if err != nil {
		return AgentStatus{}, err
	}
	if strings.Contains(name, "/") {
		return AgentStatus{}, errf("validation", false, "%s", errStatusSubagt)
	}
	tag = strings.TrimSpace(tag)
	text, problem = strings.TrimSpace(text), strings.TrimSpace(problem)
	if !validStatusTag(tag) {
		return AgentStatus{}, errf("validation", false, "%s", errStatusTag)
	}
	if err := validStatusLine("text", text, 1); err != nil {
		return AgentStatus{}, err
	}
	if err := validStatusLine("problem", problem, 0); err != nil {
		return AgentStatus{}, err
	}
	st := AgentStatus{Identity: name, Tag: tag, Text: text, Problem: problem, SetAt: b.nowMs()}
	if err := tx.QueryRow(`INSERT INTO statuses(identity, revision, tag, text, problem, set_at)
SELECT ?, COALESCE(MAX(revision),0)+1, ?, ?, ?, ? FROM statuses WHERE identity=? RETURNING revision`,
		name, tag, text, problem, st.SetAt, name).Scan(&st.Revision); err != nil {
		return AgentStatus{}, internal(err)
	}
	if _, err := tx.Exec("DELETE FROM statuses WHERE identity=? AND revision <= ?", name, st.Revision-int64(b.cfg.StatusHistoryCount)); err != nil {
		return AgentStatus{}, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return AgentStatus{}, internal(err)
	}
	return st, nil
}

// liveInTx is SessionLive on a write transaction, failing not_registered.
func (b *Bus) liveInTx(tx dbOrTx, name string) error {
	cutoff, err := b.liveCutoff(tx)
	if err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM sessions WHERE sender=? AND heartbeat >= ?", name, cutoff).Scan(&n); err != nil {
		return internal(err)
	}
	if n == 0 {
		return errf("not_registered", false, "%q has no live session; register it first", name)
	}
	return nil
}

func validStatusTag(tag string) bool {
	for _, t := range SettableStatusTags {
		if t == tag {
			return true
		}
	}
	return false
}

func validStatusLine(field, s string, min int) error {
	n := utf8.RuneCountInString(s)
	if n < min {
		return errf("validation", false, "%s is required", field)
	}
	if n > maxStatusRunes {
		return errf("validation", false, "%s must be at most %d characters", field, maxStatusRunes)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return errf("validation", false, "%s must be a single line without control characters", field)
	}
	return nil
}

// LatestStatus returns name's newest status, or nil when it has none.
func (b *Bus) LatestStatus(name string) (*AgentStatus, error) {
	var s AgentStatus
	err := b.db.QueryRow("SELECT identity, revision, tag, text, problem, set_at FROM statuses WHERE identity=? ORDER BY revision DESC LIMIT 1", name).
		Scan(&s.Identity, &s.Revision, &s.Tag, &s.Text, &s.Problem, &s.SetAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, internal(err)
	}
	return &s, nil
}

// StatusHistory returns name's kept statuses, oldest first, and whether
// name is live.
func (b *Bus) StatusHistory(name string) ([]AgentStatus, bool, error) {
	rows, err := b.db.Query("SELECT identity, revision, tag, text, problem, set_at FROM statuses WHERE identity=? ORDER BY revision", name)
	if err != nil {
		return nil, false, internal(err)
	}
	defer func() { _ = rows.Close() }()
	out := []AgentStatus{}
	for rows.Next() {
		var s AgentStatus
		if err := rows.Scan(&s.Identity, &s.Revision, &s.Tag, &s.Text, &s.Problem, &s.SetAt); err != nil {
			return nil, false, internal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, internal(err)
	}
	live, err := b.SessionLive(name)
	if err != nil {
		return nil, false, err
	}
	return out, live, nil
}

// latestStatuses returns every identity's newest status.
func latestStatuses(db dbOrTx) (map[string]AgentStatus, error) {
	rows, err := db.Query(`SELECT s.identity, s.revision, s.tag, s.text, s.problem, s.set_at FROM statuses s
JOIN (SELECT identity, MAX(revision) AS r FROM statuses GROUP BY identity) m ON s.identity=m.identity AND s.revision=m.r`)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]AgentStatus{}
	for rows.Next() {
		var s AgentStatus
		if err := rows.Scan(&s.Identity, &s.Revision, &s.Tag, &s.Text, &s.Problem, &s.SetAt); err != nil {
			return nil, internal(err)
		}
		out[s.Identity] = s
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return out, nil
}

// BoardEntry is one row of the status board: an identity's newest status,
// with Live false (and Tag offline) when it has no live session. A live
// identity that never set a status has Revision 0 and an empty Tag.
type BoardEntry struct {
	AgentStatus
	Live bool `json:"live"`
}

// Board returns the live top-level sessions plus the non-live identities
// whose newest status is within status_offline_hours (none when that is 0).
// Subagents are excluded. The result is unsorted.
func (b *Bus) Board() ([]BoardEntry, error) {
	cutoff, err := b.liveCutoff(b.db)
	if err != nil {
		return nil, err
	}
	st, err := latestStatuses(b.db)
	if err != nil {
		return nil, err
	}
	rows, err := b.db.Query("SELECT sender FROM sessions WHERE heartbeat >= ? AND instr(sender,'/')=0", cutoff)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	out := []BoardEntry{}
	live := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, internal(err)
		}
		live[n] = true
		e := BoardEntry{AgentStatus: AgentStatus{Identity: n}, Live: true}
		if s, ok := st[n]; ok {
			e.AgentStatus = s
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	if b.cfg.StatusOfflineHours > 0 {
		oldest := b.nowMs() - int64(b.cfg.StatusOfflineHours)*int64(time.Hour/time.Millisecond)
		for n, s := range st {
			if live[n] || strings.Contains(n, "/") || s.SetAt < oldest {
				continue
			}
			s.Tag = StatusOffline
			out = append(out, BoardEntry{AgentStatus: s})
		}
	}
	return out, nil
}

// ShortAge formats d as <n>m, <n>h or <n>d, the largest whole unit; under a
// minute is 0m.
func ShortAge(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return "0m"
}
