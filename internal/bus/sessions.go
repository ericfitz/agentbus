package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/ericfitz/agentbus/internal/procs"
)

type Registration struct {
	Sender  string           `json:"as"`
	Resumed bool             `json:"resumed"`
	Pending []PendingChannel `json:"pending"`
	// Subscribed and SubscribeFailed report the persistent channel list the
	// MCP server applied after registering (from .local/agentbus.json). The
	// bus itself never sets them.
	Subscribed      []string          `json:"subscribed"`
	SubscribeFailed map[string]string `json:"subscribe_failed,omitempty"`
	// MemoryChannels lists the persistent memory channels (task lists
	// excepted), which register does not subscribe: memories are searched,
	// not pushed (ADR 0008).
	MemoryChannels []string `json:"memory_channels"`
	// IgnoredChannels lists the persistent-list entries register skipped
	// because they name a task list ("tasks" or "tasks/..."): task lists
	// are no longer a default and are not subscribed from
	// .local/agentbus.json (ADR 0013). register does not rewrite the file;
	// the caller is told to remove these entries itself. Omitted when empty.
	IgnoredChannels []string `json:"ignored_channels,omitempty"`
	// Others lists the other live identities (what discover returns, minus
	// this one), so a session need not call discover to learn it is not alone.
	// Omitted when discovery is disabled.
	Others []Session `json:"others,omitempty"`
	// TagSubscriptions reports the persistent tag sets register applied (.local/agentbus.json).
	TagSubscriptions [][]string `json:"tag_subscriptions,omitempty"`
	// RepoTags lists the repository's own approved tags (.local/agentbus.json
	// "tags"), returned as guidance; send does not enforce them. Omitted when empty.
	RepoTags []string `json:"repo_tags,omitempty"`
	// IgnoredTags lists "tags" entries that are not valid tags. Omitted when empty.
	IgnoredTags []string `json:"ignored_tags,omitempty"`
}

type PendingChannel struct {
	Channel string `json:"channel"`
	Pending int64  `json:"pending"`
}

type Session struct {
	Sender       string `json:"sender"`
	Context      string `json:"context"`
	RegisteredAt int64  `json:"registered_at"`
	// Harness and HarnessVersion are the name and version the session's MCP
	// client sent in the initialize handshake (ADR 0015), refreshed at each
	// register. Omitted when the client sent none.
	Harness        string `json:"harness,omitempty"`
	HarnessVersion string `json:"harness_version,omitempty"`
}

// cleanClientField makes a clientInfo string safe to store and show: control
// characters are dropped and the result is cut to 128 bytes on a rune
// boundary. clientInfo is advisory display text, so it is cleaned, never
// rejected (an odd client must still register).
func cleanClientField(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > 128 {
		s = strings.ToValidUTF8(s[:128], "")
	}
	return s
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

// SetHarness records h, the harness process this process serves, on every
// session it registers from now on (ADR 0017). Only the MCP server calls
// it, once at startup, before any register.
func (b *Bus) SetHarness(h procs.Ref) { b.harness = h }

// IdentityForHarness is the live top-level session registered from harness
// h (ADR 0017), the most recent if there are several, or "" if none.
// Subagent sessions (parent/name) share their parent's harness and are
// skipped. Like SessionLive it needs no registration, so a hook or a wait
// can ask before it knows its identity. Where the platform cannot tell
// start times, both sides are 0 and the pid alone decides.
func (b *Bus) IdentityForHarness(h procs.Ref) (string, error) {
	if h.Pid <= 0 {
		return "", nil
	}
	cutoff, err := b.liveCutoff(b.db)
	if err != nil {
		return "", err
	}
	var name string
	err = b.db.QueryRow("SELECT sender FROM sessions WHERE harness_pid=? AND harness_start=? AND instr(sender,'/')=0 AND heartbeat >= ? ORDER BY registered_at DESC, sender LIMIT 1",
		h.Pid, h.Start, cutoff).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", internal(err)
	}
	return name, nil
}

// SessionHarness is the harness process recorded on name's live session
// (zero for one registered before ADR 0017 or where the platform cannot
// tell); live is false when name has no live session.
func (b *Bus) SessionHarness(name string) (h procs.Ref, live bool, err error) {
	cutoff, err := b.liveCutoff(b.db)
	if err != nil {
		return procs.Ref{}, false, err
	}
	err = b.db.QueryRow("SELECT harness_pid, harness_start FROM sessions WHERE sender=? AND heartbeat >= ?",
		name, cutoff).Scan(&h.Pid, &h.Start)
	if errors.Is(err, sql.ErrNoRows) {
		return procs.Ref{}, false, nil
	}
	if err != nil {
		return procs.Ref{}, false, internal(err)
	}
	return h, true, nil
}

// TUIContext is the context the TUI registers with; register over MCP
// refuses any name a live session with this context holds (ADR 0025
// decision 9).
const TUIContext = "tui"

// LiveTUISession reports whether name is held by a live TUI session of
// another process. MCP register refuses the TUI context to agents, so only
// the TUI writes it.
func (b *Bus) LiveTUISession(name string) (bool, error) {
	cutoff, err := b.liveCutoff(b.db)
	if err != nil {
		return false, err
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM sessions WHERE sender=? AND context=? AND owner<>? AND heartbeat >= ?", name, TUIContext, b.owner, cutoff).Scan(&n); err != nil {
		return false, internal(err)
	}
	return n > 0, nil
}

// Register is RegisterWithClient for an in-process caller with no MCP
// clientInfo (the TUI, tests). It may rename: the refusal in ADR 0025
// decision 8 guards agents sharing one MCP process.
func (b *Bus) Register(name, parent, context string, resume bool) (Registration, error) {
	return b.RegisterWithClient(name, parent, context, resume, true, "", "")
}

// RegisterWithClient registers like Register and records harness and
// harnessVersion (the MCP initialize clientInfo name and version, ADR 0015)
// on the session, replacing whatever an earlier register stored. rename
// lets a top-level register replace the process's live top-level name; without
// it that register is refused (ADR 0025 decision 8).
func (b *Bus) RegisterWithClient(name, parent, context string, resume, rename bool, harness, harnessVersion string) (Registration, error) {
	harness, harnessVersion = cleanClientField(harness), cleanClientField(harnessVersion)
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
	cutoff, err := b.liveCutoff(tx)
	if err != nil {
		return Registration{}, err
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE heartbeat < ?", cutoff); err != nil {
		return Registration{}, internal(err)
	}
	gen, err := resetGeneration(tx)
	if err != nil {
		return Registration{}, err
	}
	base, sep := name, ""
	if parent != "" {
		base, sep = parent+"/"+name, "-"
	}
	// Walk base, base2, base3, ... to the first name that is free or that
	// this process already holds. Reusing our own row makes register
	// idempotent: Claude Code keeps the MCP server across /clear, so a
	// re-register from the same process must not mint a fresh suffix.
	//
	// ponytail: one SELECT per candidate suffix; fine while collisions are
	// rare (a handful of concurrent sessions sharing a base name), would need
	// a single batched lookup if register ever sees deep suffix chains.
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
	// A subagent shares its parent's MCP process, so a parentless register
	// from one would rename the parent; refuse it unless the caller says it
	// means to rename (ADR 0025 decision 8).
	if parent == "" && !rename {
		var held string
		err := tx.QueryRow("SELECT sender FROM sessions WHERE owner=? AND instr(sender,'/')=0 AND sender<>? ORDER BY registered_at DESC LIMIT 1", b.owner, display).Scan(&held)
		if err == nil {
			return Registration{}, errf("validation", false, "this process is registered as %q; a subagent registers with parent=%q, and renaming %q to %q takes rename=true", held, held, held, name)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Registration{}, internal(err)
		}
	}
	if reused {
		// registered_at moves too: IdentityForHarness takes the most recent
		// register from a harness, and re-registering an older name (after
		// a /clear) makes it the current one again (ADR 0017).
		if _, err := tx.Exec("UPDATE sessions SET context=?, heartbeat=?, registered_at=?, harness=?, harness_version=?, harness_pid=?, harness_start=? WHERE sender=?",
			context, now, now, harness, harnessVersion, b.harness.Pid, b.harness.Start, display); err != nil {
			return Registration{}, internal(err)
		}
	} else if _, err := tx.Exec("INSERT INTO sessions(sender,context,owner,heartbeat,registered_at,harness,harness_version,harness_pid,harness_start) VALUES(?,?,?,?,?,?,?,?,?)",
		display, context, b.owner, now, now, harness, harnessVersion, b.harness.Pid, b.harness.Start); err != nil {
		return Registration{}, internal(err)
	}
	// One top-level name per process (ADR 0025 decision 6): a top-level
	// register drops this process's other top-level rows. Subagent rows
	// (parent/name) and every subscription stay, so an old name resumes.
	var dropped []string
	if parent == "" {
		old, err := tx.Query("SELECT sender FROM sessions WHERE owner=? AND instr(sender,'/')=0 AND sender<>?", b.owner, display)
		if err != nil {
			return Registration{}, internal(err)
		}
		for old.Next() {
			var s string
			if err := old.Scan(&s); err != nil {
				_ = old.Close()
				return Registration{}, internal(err)
			}
			dropped = append(dropped, s)
		}
		if err := old.Err(); err != nil {
			_ = old.Close()
			return Registration{}, internal(err)
		}
		_ = old.Close()
		if _, err := tx.Exec("DELETE FROM sessions WHERE owner=? AND instr(sender,'/')=0 AND sender<>?", b.owner, display); err != nil {
			return Registration{}, internal(err)
		}
	}
	if !resume {
		// resume=false means no backlog at all, inbox included (human
		// decision, ADR 0004): drop every subscription, including the inbox
		// row, so ensureInbox below recreates it at the current head rather
		// than leaving an old cursor in place.
		if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=?", display); err != nil {
			return Registration{}, internal(err)
		}
		if _, err := tx.Exec("DELETE FROM tag_subscriptions WHERE sender=?", display); err != nil {
			return Registration{}, internal(err)
		}
	} else {
		// The tick no longer reaps idle subscriptions itself (that would erase
		// the evidence Receive needs to report them as expired), so a resuming
		// registration must drop its own idle-expired ones before listing
		// pending channels, or an expired subscription would show up as pending.
		idle := int64(b.cfg.CursorIdleHours) * 3_600_000
		if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=? AND last_activity < ? AND channel<>?", display, now-idle, DMChannel(display)); err != nil {
			return Registration{}, internal(err)
		}
	}
	// Sets without their tags/ row (idle-swept above) are dead; drop them so a
	// persistent re-apply starts them fresh.
	if _, err := tx.Exec("DELETE FROM tag_subscriptions WHERE sender=? AND NOT EXISTS (SELECT 1 FROM subscriptions WHERE sender=? AND channel=?)", display, display, tagSource); err != nil {
		return Registration{}, internal(err)
	}
	// Count subscriptions before ensureInbox mints this call's own inbox
	// subscription, so a brand-new identity's first register still reports
	// resumed=false even though Pending will go on to list dm/<name>.
	var priorSubs int
	if err := tx.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=?", display).Scan(&priorSubs); err != nil {
		return Registration{}, internal(err)
	}
	// resume=true starts a newly created inbox at 0 (any DM queued before
	// this identity's first inbox-aware register is still delivered);
	// resume=false starts it at the current head, per resume=false's
	// "no backlog" meaning. An existing row is left alone either way
	// (ensureInbox's INSERT OR IGNORE).
	inboxCursor := int64(0)
	if !resume {
		if err := tx.QueryRow("SELECT coalesce(max(seq),0) FROM messages").Scan(&inboxCursor); err != nil {
			return Registration{}, internal(err)
		}
	}
	if err := b.ensureInbox(tx, display, now, inboxCursor); err != nil {
		return Registration{}, err
	}
	rows, err := tx.Query(`SELECT s.channel,
	  (SELECT count(*) FROM messages m WHERE m.channel=s.channel AND m.seq>s.cursor_seq AND m.tombstone=0 AND m.sender<>s.sender),
	  coalesce((SELECT c.kind FROM channels c WHERE c.name=s.channel),'')
	  FROM subscriptions s WHERE s.sender=? ORDER BY s.channel`, display)
	if err != nil {
		return Registration{}, internal(err)
	}
	reg := Registration{Sender: display, Pending: []PendingChannel{}, Subscribed: []string{}}
	for rows.Next() {
		var p PendingChannel
		var kind string
		if err := rows.Scan(&p.Channel, &p.Pending, &kind); err != nil {
			_ = rows.Close()
			return Registration{}, internal(err)
		}
		// A subscription row this identity cannot currently read (e.g. left
		// over from a past observer session) must not surface as pending. The
		// tags/ pseudo row is not a channel either.
		if !b.dmReadable(display, p.Channel) || p.Channel == tagSource {
			continue
		}
		// ADR 0025 decision 7: a leftover memory subscription is not pending.
		// Skipped rather than dropped here: the TUI registers before
		// SetObserver and must keep its memory rows and cursors.
		if b.memoryKindHidden(display, p.Channel, kind) {
			continue
		}
		reg.Pending = append(reg.Pending, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Registration{}, internal(err)
	}
	_ = rows.Close()
	reg.Resumed = reused || priorSubs > 0
	if b.cfg.DiscoveryEnabled {
		live, err := b.liveSessions(tx)
		if err != nil {
			return Registration{}, err
		}
		reg.Others = []Session{}
		for _, s := range live {
			if s.Sender != display {
				reg.Others = append(reg.Others, s)
			}
		}
	}
	// Forget the dropped names before the commit: once it lands, a Heartbeat
	// blocked on the write lock could re-insert a dropped row and still pass
	// restore's remembered check. If the commit fails the rows are still in
	// the database, so they are remembered again.
	forgotten := make([]sessionRow, 0, len(dropped))
	for _, d := range dropped {
		if r, ok := b.rememberedRow(d); ok {
			forgotten = append(forgotten, r)
		}
		b.forget(d)
	}
	if err := tx.Commit(); err != nil {
		for _, r := range forgotten {
			b.remember(r)
		}
		return Registration{}, internal(err)
	}
	if b.afterRegisterCommit != nil {
		b.afterRegisterCommit()
	}
	b.remember(sessionRow{sender: display, context: context, harness: harness, harnessVersion: harnessVersion,
		harnessPid: b.harness.Pid, harnessStart: b.harness.Start, registeredAt: now, generation: gen})
	// A park left by a session that died must not outlive its name (ADR 0025
	// decision 3). A subagent name has a '/', which cannot be parked.
	if parent == "" {
		if err := b.Unpark(display); err != nil {
			b.log.Warn("register: clearing park", "name", display, "err", err)
		}
	}
	// A rename's dropped names go too; none of them is a subagent name.
	for _, d := range dropped {
		if err := b.Unpark(d); err != nil {
			b.log.Warn("register: clearing park", "name", d, "err", err)
		}
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
func (b *Bus) auth(q execQuerier, as string) error {
	if as == "" {
		return errf("validation", false, "as is required: pass the display name returned by register")
	}
	var n int
	if err := q.QueryRow("SELECT count(*) FROM sessions WHERE sender=? AND owner=?", as, b.owner).Scan(&n); err != nil {
		return internal(err)
	}
	if n == 0 && b.restoreOne(q, as) {
		// The row was swept while this process was alive (ADR 0022) and is back.
		n = 1
	}
	if n == 0 {
		return errf("not_registered", false, "%q is not registered in this process; call register", as)
	}
	return nil
}

// Owns reports whether this process holds a live session row for as: the
// ownership check without the error, for callers that must not act on a name
// another process owns (the unpark middleware).
func (b *Bus) Owns(as string) bool { return b.auth(b.db, as) == nil }

// sessionRow is the row a process last wrote for one of its sessions: what
// it needs to put the row back after another process swept it (ADR 0022).
type sessionRow struct {
	sender, context, harness, harnessVersion string
	harnessPid                               int
	harnessStart, registeredAt               int64
	// generation is the reset generation the row was registered under.
	generation int64
}

// resetGeneration is the number Reset bumps; a missing row reads as 0.
func resetGeneration(q queryRower) (int64, error) {
	var g int64
	err := q.QueryRow("SELECT value FROM meta WHERE key='reset_generation'").Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, internal(err)
	}
	return g, nil
}

// execQuerier is what a restore needs of b.db or a *sql.Tx.
type execQuerier interface {
	queryRower
	Exec(query string, args ...any) (sql.Result, error)
}

func (b *Bus) remember(r sessionRow) {
	b.rememberedMu.Lock()
	defer b.rememberedMu.Unlock()
	if b.remembered == nil {
		b.remembered = map[string]sessionRow{}
	}
	b.remembered[r.sender] = r
}

func (b *Bus) forget(name string) {
	b.rememberedMu.Lock()
	defer b.rememberedMu.Unlock()
	delete(b.remembered, name)
}

func (b *Bus) rememberedRow(name string) (sessionRow, bool) {
	b.rememberedMu.Lock()
	defer b.rememberedMu.Unlock()
	r, ok := b.remembered[name]
	return r, ok
}

// restore puts r back if its name is free, with its original registered_at
// and a fresh heartbeat. A name another owner holds is forgotten, never
// overwritten (ADR 0022). It reports whether this process owns the row after.
func (b *Bus) restore(q execQuerier, r sessionRow) (bool, error) {
	// The generation check is part of the INSERT, so a Reset cannot commit
	// between the check and the write and leave a row that outlives it.
	res, err := q.Exec("INSERT INTO sessions(sender,context,owner,heartbeat,registered_at,harness,harness_version,harness_pid,harness_start) SELECT ?,?,?,?,?,?,?,?,? WHERE coalesce((SELECT value FROM meta WHERE key='reset_generation'),0)=? ON CONFLICT(sender) DO NOTHING",
		r.sender, r.context, b.owner, b.nowMs(), r.registeredAt, r.harness, r.harnessVersion, r.harnessPid, r.harnessStart, r.generation)
	if err != nil {
		return false, internal(err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		// EndSessions may have run since the caller read the entry; do not
		// leave a row behind for a session it ended.
		if _, ok := b.rememberedRow(r.sender); !ok {
			_, err := q.Exec("DELETE FROM sessions WHERE sender=? AND owner=?", r.sender, b.owner)
			return false, internal(err)
		}
		return true, nil
	}
	gen, err := resetGeneration(q)
	if err != nil {
		return false, err
	}
	if gen != r.generation {
		// A reset wiped the bus; it meant every live process to register again.
		b.forget(r.sender)
		return false, nil
	}
	var owner string
	if err := q.QueryRow("SELECT owner FROM sessions WHERE sender=?", r.sender).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, internal(err)
	}
	if owner != b.owner {
		b.forget(r.sender)
		return false, nil
	}
	return true, nil
}

// restoreOne is auth's single restore attempt for name; any failure leaves
// the caller to report not_registered.
func (b *Bus) restoreOne(q execQuerier, name string) bool {
	r, ok := b.rememberedRow(name)
	if !ok {
		return false
	}
	ok, err := b.restore(q, r)
	if err != nil {
		b.log.Warn("restore session", "name", name, "err", err)
	}
	return ok
}

// Heartbeat refreshes every session this process owns, then puts back any
// it remembers that a sweep removed while it was alive, e.g. across a sleep
// (ADR 0022). Rows lost to a newer registration are forgotten, so auth fails
// for them afterwards.
func (b *Bus) Heartbeat() error {
	// First, so a wake is recorded before this process's own rows look fresh.
	// A failed write stays pending for the next call; the heartbeat goes on.
	if err := b.noteClock(b.db); err != nil {
		b.log.Warn("record wake", "err", err)
	}
	if _, err := b.db.Exec("UPDATE sessions SET heartbeat=? WHERE owner=?", b.nowMs(), b.owner); err != nil {
		return internal(err)
	}
	b.rememberedMu.Lock()
	rows := make([]sessionRow, 0, len(b.remembered))
	for _, r := range b.remembered {
		rows = append(rows, r)
	}
	b.rememberedMu.Unlock()
	// Try every name: one failure must not leave the others swept.
	var errs []error
	for _, r := range rows {
		if _, err := b.restore(b.db, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// EndSessions deletes every session this process owns, so a process that
// shuts down cleanly frees its names at once instead of holding them until
// the attachment expiry. Subscriptions stay: the identity resumes later. It
// also forgets them, so a later Heartbeat does not restore them.
func (b *Bus) EndSessions() error {
	b.rememberedMu.Lock()
	b.remembered = nil
	b.rememberedMu.Unlock()
	if _, err := b.db.Exec("DELETE FROM sessions WHERE owner=?", b.owner); err != nil {
		return internal(err)
	}
	return nil
}

func (b *Bus) Discover(as string) ([]Session, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	if !b.cfg.DiscoveryEnabled {
		// Code "validation" (not e.g. a dedicated "disabled" code): this is a
		// caller-visible input-shape problem exactly like any other bad
		// argument — discover simply isn't a callable operation while
		// discovery_enabled=false — so it shares validation's retryable=false
		// semantics rather than warranting a new error code.
		return nil, errf("validation", false, "discovery is disabled by configuration")
	}
	sessions, err := b.liveSessions(b.db)
	if err != nil {
		return nil, err
	}
	return trimToBytes(sessions, sessionBytes, min(b.cfg.ResultDefaultKiB*1024, trimHardCeilingBytes)), nil
}
