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
