package bus

import (
	"database/sql"
	"regexp"
	"slices"
	"strings"
)

// maxTags is the per-message (and per-subscription) tag ceiling (ADR 0009).
const maxTags = 10

var tagRe = regexp.MustCompile(`^[a-z0-9_-]{1,20}$`)

// NormalizeTags lowercases tags, rejects any that fail the tag rule (one bad
// tag fails the whole call), drops duplicates, sorts, and caps the result at
// maxTags. nil in, nil out. Exported for repoconfig's persistent tag sets.
func NormalizeTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(t)
		if !tagRe.MatchString(t) {
			return nil, errf("validation", false, "tag %q must be 1-20 characters of a-z, 0-9, _ or -", t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > maxTags {
		return nil, errf("validation", false, "at most %d tags per message", maxTags)
	}
	slices.Sort(out)
	return out, nil
}

// insertTags stores a message's (already normalized) tags.
func insertTags(tx *sql.Tx, seq int64, tags []string) error {
	for _, t := range tags {
		if _, err := tx.Exec("INSERT INTO message_tags(seq, tag) VALUES(?,?)", seq, t); err != nil {
			return err
		}
	}
	return nil
}

// tagsOf reads a stored message's tags, sorted; nil when it has none.
func tagsOf(q querier, seq int64) ([]string, error) {
	rows, err := q.Query("SELECT tag FROM message_tags WHERE seq=? ORDER BY tag", seq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// tagsFilter is the any-of tags clause for History and search: messages
// carrying at least one of tags. Empty tags adds nothing.
func tagsFilter(alias string, tags []string) (string, []any) {
	if len(tags) == 0 {
		return "", nil
	}
	args := make([]any, len(tags))
	for i, t := range tags {
		args[i] = t
	}
	return " AND EXISTS (SELECT 1 FROM message_tags t WHERE t.seq=" + alias + ".seq AND t.tag IN (" + strings.Repeat("?,", len(tags)-1) + "?))", args
}

// tagSource is the pseudo-subscription row that carries the cursor, pending
// batch, and idle expiry shared by all of a sender's tag sets (ADR 0009
// item 5). No channel can bear this name: ChannelNameRule rejects the
// trailing slash, and it is neither a dm/ inbox nor a task list.
const tagSource = "tags/"

// tagSet is one AND set: a message matches when it carries every tag and
// was written after the set was added.
type tagSet struct {
	tags       []string
	createdSeq int64
}

func tagsKey(tags []string) string { return strings.Join(tags, ",") }

// SubscribeTags adds an AND set of 1-10 tags. The first set also creates the
// shared tags/ row at the current head, so nothing before it is delivered
// (like Subscribe from now); a set is idempotent.
func (b *Bus) SubscribeTags(as string, tags []string) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	tags, err := NormalizeTags(tags)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return errf("validation", false, "tags must name at least one tag")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.auth(tx, as); err != nil {
		return err
	}
	var head int64
	if err := tx.QueryRow("SELECT coalesce(max(seq),0) FROM messages").Scan(&head); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO tag_subscriptions(sender,tags_key,created_seq) VALUES(?,?,?)", as, tagsKey(tags), head); err != nil {
		return internal(err)
	}
	for _, t := range tags {
		if _, err := tx.Exec("INSERT OR IGNORE INTO tag_subscription_tags(sender,tags_key,tag) VALUES(?,?,?)", as, tagsKey(tags), t); err != nil {
			return internal(err)
		}
	}
	// Upsert, not INSERT OR IGNORE: a tags/ row can be older than
	// cursor_idle_hours (its owner has other live subscriptions keeping the
	// identity around), and adding a set must count as activity or the row
	// expires on the next receive and takes the just-added set with it.
	if _, err := tx.Exec("INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,?,?) ON CONFLICT(sender,channel) DO UPDATE SET last_activity=excluded.last_activity", as, tagSource, head, b.nowMs()); err != nil {
		return internal(err)
	}
	return internal(tx.Commit())
}

// UnsubscribeTags removes one set; removing the last set drops the tags/
// row too, so an idle identity with no sets holds no cursor.
func (b *Bus) UnsubscribeTags(as string, tags []string) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	tags, err := NormalizeTags(tags)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return errf("validation", false, "tags must name at least one tag")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.auth(tx, as); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM tag_subscriptions WHERE sender=? AND tags_key=?", as, tagsKey(tags)); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("DELETE FROM subscriptions WHERE sender=? AND channel=? AND NOT EXISTS (SELECT 1 FROM tag_subscriptions WHERE sender=?)", as, tagSource, as); err != nil {
		return internal(err)
	}
	return internal(tx.Commit())
}

// TagSubscriptions lists as's sets, each sorted, in key order.
func (b *Bus) TagSubscriptions(as string) ([][]string, error) {
	if err := b.auth(b.db, as); err != nil {
		return nil, err
	}
	sets, err := b.tagSets(b.db, as)
	if err != nil {
		return nil, err
	}
	out := make([][]string, len(sets))
	for i, s := range sets {
		out[i] = s.tags
	}
	return out, nil
}

func (b *Bus) tagSets(q querier, as string) ([]tagSet, error) {
	rows, err := q.Query("SELECT tags_key, created_seq FROM tag_subscriptions WHERE sender=? ORDER BY tags_key", as)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []tagSet
	for rows.Next() {
		var key string
		var s tagSet
		if err := rows.Scan(&key, &s.createdSeq); err != nil {
			return nil, internal(err)
		}
		s.tags = strings.Split(key, ",")
		out = append(out, s)
	}
	return out, internal(rows.Err())
}

// tagCond is the tag source's predicate over messages: ordinary chat
// channels only (dm/ inboxes are ordinary-kind and excluded by name;
// memory channels and task lists are memory-kind), skipping channels the
// sender is directly subscribed to (those arrive through the channel), and
// carrying every tag of at least one set added before the message. Once a
// direct channel subscription ends, that channel's messages above the tag
// cursor become tag-deliverable again (ADR 0009 item 7 applies to current
// direct subscriptions, not past ones). Matching is driven from the
// sender's few subscribed tags into message_tags(tag, seq) above floor
// (#13), so only tagged messages above the cursor are read, never a scan
// of messages or message_tags; a message matches a set when it carries as
// many of the set's tags as the set has. mt.seq's two comparisons (rather
// than max(ts.created_seq, ?)) are what let SQLite range-scan
// message_tags_tag_seq instead of scanning it.
func tagCond(as string, floor int64) (string, []any) {
	return `messages.seq IN (
		SELECT mt.seq FROM tag_subscription_tags st
		JOIN tag_subscriptions ts ON ts.sender=st.sender AND ts.tags_key=st.tags_key
		JOIN message_tags mt ON mt.tag=st.tag AND mt.seq>? AND mt.seq>ts.created_seq
		WHERE st.sender=?
		GROUP BY mt.seq, st.tags_key
		HAVING count(*)=(SELECT count(*) FROM tag_subscription_tags c WHERE c.sender=st.sender AND c.tags_key=st.tags_key))
	AND messages.channel IN (SELECT name FROM channels WHERE kind='ordinary' AND name NOT LIKE 'dm/%')
	AND messages.channel NOT IN (SELECT channel FROM subscriptions WHERE sender=?)`, []any{floor, as, as}
}

// matchedTags is the union of the sets m satisfies, sorted.
func matchedTags(sets []tagSet, m Message) []string {
	var out []string
	for _, s := range sets {
		if m.Seq <= s.createdSeq {
			continue
		}
		all := true
		for _, t := range s.tags {
			if !slices.Contains(m.Tags, t) {
				all = false
			}
		}
		if all {
			for _, t := range s.tags {
				if !slices.Contains(out, t) {
					out = append(out, t)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}
