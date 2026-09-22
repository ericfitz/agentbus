package bus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// prefixKinds maps each channel-name prefix to the kind it implies: a
// project channel is <default>/<repo>, named after the machine-wide default
// it scopes (ADR 0007). dm/ is not here; inboxes are register's, not
// CreateChannel's.
var prefixKinds = map[string]string{"general/": "ordinary", "memory/": "memory", TaskPrefix: "memory"}

// PrefixKind returns the kind a prefixed channel name implies and true, or
// "" and false for an unprefixed name.
func PrefixKind(name string) (string, bool) {
	for p, k := range prefixKinds {
		if strings.HasPrefix(name, p) {
			return k, true
		}
	}
	return "", false
}

// ChannelNameRule is the channel name rule shared by CreateChannel, the CLI's
// persistent subscribe list, and repoconfig: a name is valid if it passes
// NameRule directly, or if it has a general/, memory/, or tasks/ prefix and
// the remainder passes NameRule. It does not decide reserved names (dm,
// tasks) or channel kind; callers apply those separately.
func ChannelNameRule(name string) error {
	for p := range prefixKinds {
		if rest, ok := strings.CutPrefix(name, p); ok {
			return NameRule(rest)
		}
	}
	return NameRule(name)
}

// resolveKind applies the kind rules shared by CreateChannel and
// EnsureChannel: a prefixed name implies its kind (an empty kind takes it, a
// different one is refused); "dm" and "tasks" are reserved; otherwise kind
// must be ordinary or memory.
func resolveKind(name, kind string) (string, error) {
	if name == "tasks" {
		return "", errf("validation", false, "channel name %q is reserved for task lists", name)
	}
	if implied, ok := PrefixKind(name); ok {
		if kind == "" {
			return implied, nil
		}
		if kind != implied {
			return "", errf("validation", false, "channels named %s.../ must have kind %s", name[:strings.Index(name, "/")], implied)
		}
		return kind, nil
	}
	if name == "dm" {
		return "", errf("validation", false, "channel name %q is reserved for direct messages", name)
	}
	if kind != "ordinary" && kind != "memory" {
		return "", errf("validation", false, "kind must be ordinary or memory")
	}
	return kind, nil
}

// validateChannelName wraps ChannelNameRule in the bus's validation-error
// shape, like validateName wraps NameRule for identities.
func validateChannelName(name string) error {
	if err := ChannelNameRule(name); err != nil {
		return errf("validation", false, "name %v", err)
	}
	return nil
}

type Channel struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Messages  int64  `json:"messages"`
	LatestSeq int64  `json:"latest_seq"`
}

// DefaultChannels exist on every bus so agents have somewhere to talk and
// remember before anyone creates a channel: "general" (ordinary), "memory"
// (memory), and the task list "tasks" (memory). Decisions of 2026-09-09
// (ADR 0002) and 2026-09-17 (ADR 0007).
var DefaultChannels = []Channel{{Name: "general", Kind: "ordinary"}, {Name: "memory", Kind: "memory"}, {Name: "tasks", Kind: "memory"}}

// ensureDefaults creates any missing default channel. A same-named channel
// of another kind is left alone (INSERT OR IGNORE), so a user's earlier
// choice wins over the default.
func (b *Bus) ensureDefaults() error {
	for _, c := range DefaultChannels {
		if err := b.EnsureChannel(c.Name, c.Kind); err != nil {
			return err
		}
	}
	return nil
}

// ChannelKind returns the kind of an existing channel, or "" if there is
// no such channel.
func (b *Bus) ChannelKind(name string) (string, error) {
	var kind string
	switch err := b.db.QueryRow("SELECT kind FROM channels WHERE name=?", name).Scan(&kind); {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", internal(err)
	}
	return kind, nil
}

// EnsureChannel creates the channel if it is missing, without a registration
// (it is the CLI's, for `agentbus init`). A same-named channel of another
// kind is left alone, like ensureDefaults.
func (b *Bus) EnsureChannel(name, kind string) error {
	if err := validateChannelName(name); err != nil {
		return err
	}
	if name != "tasks" { // the default list is the one bare "tasks" ensureDefaults may create
		var err error
		if kind, err = resolveKind(name, kind); err != nil {
			return err
		}
	}
	if _, err := b.db.Exec("INSERT OR IGNORE INTO channels(name,kind,created_seq,evicted_before_seq) VALUES(?,?,(SELECT coalesce(max(seq),0) FROM messages),0)", name, kind); err != nil {
		return internal(err)
	}
	return nil
}

func (b *Bus) CreateChannel(as, name, kind string) (Channel, error) {
	if err := b.auth(b.db, as); err != nil {
		return Channel{}, err
	}
	if err := validateChannelName(name); err != nil {
		return Channel{}, err
	}
	kind, err := resolveKind(name, kind)
	if err != nil {
		return Channel{}, err
	}
	tx, err := b.db.Begin()
	if err != nil {
		return Channel{}, internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Re-verify ownership on the transaction that is about to write (R3).
	if err := b.auth(tx, as); err != nil {
		return Channel{}, err
	}
	var existing string
	err = tx.QueryRow("SELECT kind FROM channels WHERE name=?", name).Scan(&existing)
	switch {
	case err == nil && existing != kind:
		return Channel{}, errf("conflict", false, "channel %q exists with kind %s", name, existing)
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		var latest int64
		if err := tx.QueryRow("SELECT coalesce(max(seq),0) FROM messages").Scan(&latest); err != nil {
			return Channel{}, internal(err)
		}
		if _, err := tx.Exec("INSERT INTO channels(name,kind,created_seq,evicted_before_seq) VALUES(?,?,?,0)", name, kind, latest); err != nil {
			return Channel{}, internal(err)
		}
	default:
		return Channel{}, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, internal(err)
	}
	return Channel{Name: name, Kind: kind}, nil
}

// channelBytes measures the serialized size of a Channel; it holds only
// strings and ints, so json.Marshal cannot fail.
func channelBytes(c Channel) int {
	j, _ := json.Marshal(c)
	return len(j)
}

func (b *Bus) ListChannels(as string) ([]Channel, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	chans, err := b.listChannels(b.db)
	if err != nil {
		return nil, err
	}
	// DM inboxes are not ordinary channels: they never appear here (or in
	// discover/create_channel), only in StatusReport for the TUI. listChannels
	// is also StatusReport's source, so the filter happens here, not inside
	// the shared query.
	chans = slices.DeleteFunc(chans, func(c Channel) bool { _, dm := dmOwner(c.Name); return dm })
	// listChannels is also StatusReport's source (the local, human-facing
	// status command, not an MCP tool result), so trimming happens here,
	// not inside the shared query.
	return trimToBytes(chans, channelBytes, min(b.cfg.ResultDefaultKiB*1024, trimHardCeilingBytes)), nil
}

// isDefaultChannel reports whether name is one of DefaultChannels.
func isDefaultChannel(name string) bool {
	for _, c := range DefaultChannels {
		if c.Name == name {
			return true
		}
	}
	return false
}

// DeleteChannel permanently removes a channel and every message in it. It
// needs no registration (it is an admin operation, like Reset). It refuses
// default channels and channels with an active subscriber: a subscription
// whose sender has a live heartbeat, other than except (the caller's own
// session, for the TUI which subscribes to every channel). Stale sessions'
// subscriptions are dropped with the channel. Confirmation is the caller's
// job; the returned Channel reports what was destroyed.
func (b *Bus) DeleteChannel(name, except string) (Channel, error) {
	if _, ok := dmOwner(name); ok {
		return Channel{}, errf("validation", false, "can't delete direct-message channel %q", name)
	}
	if isDefaultChannel(name) {
		return Channel{}, errf("validation", false, "can't delete default channel %q", name)
	}
	tx, err := b.db.Begin()
	if err != nil {
		return Channel{}, internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	c := Channel{Name: name}
	err = tx.QueryRow(`SELECT kind,
	  (SELECT count(*) FROM messages m WHERE m.channel=c.name AND m.tombstone=0),
	  (SELECT coalesce(max(seq),0) FROM messages m WHERE m.channel=c.name)
	  FROM channels c WHERE name=?`, name).Scan(&c.Kind, &c.Messages, &c.LatestSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, errf("not_found", false, "channel %q does not exist", name)
	}
	if err != nil {
		return Channel{}, internal(err)
	}
	rows, err := tx.Query("SELECT s.sender FROM subscriptions s JOIN sessions x ON x.sender=s.sender WHERE s.channel=? AND x.heartbeat>=? AND s.sender<>? ORDER BY s.sender",
		name, b.nowMs()-attachmentExpiryMs, except)
	if err != nil {
		return Channel{}, internal(err)
	}
	var live []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return Channel{}, internal(err)
		}
		live = append(live, s)
	}
	_ = rows.Close()
	if len(live) > 0 {
		return Channel{}, errf("conflict", false, "can't delete channel %q: it has active subscribers (%s)", name, strings.Join(live, ", "))
	}
	// The messages_ad trigger keeps messages_fts in sync; embeddings cascade.
	for _, q := range []string{"DELETE FROM messages WHERE channel=?", "DELETE FROM subscriptions WHERE channel=?", "DELETE FROM channels WHERE name=?"} {
		if _, err := tx.Exec(q, name); err != nil {
			return Channel{}, internal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, internal(err)
	}
	return c, nil
}

// reapEmptyChannels is the tick's step: drop non-default channels holding no
// messages (tombstones included) and no subscription from a live session,
// along with every subscription to a channel that no longer exists (the
// stale sessions' rows on the channels just dropped). A subscription
// without its channel makes the subscriber's every receive fail on the
// eviction-boundary lookup once the session resumes. Nothing is lost, so
// no confirmation is involved.
func (b *Bus) reapEmptyChannels() error {
	names := make([]any, 0, len(DefaultChannels)+1)
	for _, c := range DefaultChannels {
		names = append(names, c.Name)
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM channels WHERE name NOT IN (`+strings.Repeat("?,", len(names)-1)+`?)
	  AND name NOT LIKE 'dm/%'
	  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.channel=channels.name)
	  AND NOT EXISTS (SELECT 1 FROM subscriptions s JOIN sessions x ON x.sender=s.sender WHERE s.channel=channels.name AND x.heartbeat>=?)`,
		append(names, b.nowMs()-attachmentExpiryMs)...); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM subscriptions WHERE channel NOT IN (SELECT name FROM channels)`); err != nil {
		return err
	}
	return tx.Commit()
}

// RenameChannel moves channel from to to, carrying its messages and
// subscriptions, in one transaction. It is the CLI's, for `agentbus init`
// migrating a pre-ADR-0007 project channel (<repo>, <repo>-memory) to its
// prefixed name. from must exist, to must not, and to's implied kind must
// match from's kind. Cursors keep their seqs, so subscribers resume where
// they were.
func (b *Bus) RenameChannel(from, to string) error {
	if err := validateChannelName(to); err != nil {
		return err
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var kind string
	if err := tx.QueryRow("SELECT kind FROM channels WHERE name=?", from).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errf("not_found", false, "channel %q does not exist", from)
		}
		return internal(err)
	}
	if _, err := resolveKind(to, kind); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM channels WHERE name=?", to).Scan(&n); err != nil {
		return internal(err)
	}
	if n > 0 {
		return errf("conflict", false, "channel %q already exists", to)
	}
	for _, q := range []string{"UPDATE channels SET name=? WHERE name=?", "UPDATE messages SET channel=? WHERE channel=?", "UPDATE subscriptions SET channel=? WHERE channel=?"} {
		if _, err := tx.Exec(q, to, from); err != nil {
			return internal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return internal(err)
	}
	return nil
}
