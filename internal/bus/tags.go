package bus

import (
	"database/sql"
	"regexp"
	"slices"
	"strings"
)

// maxTags is the per-message (and per-subscription) tag ceiling (ADR 0009).
const maxTags = 10

// maxTagLen caps a stored tag, and a pattern's prefix, at 32 characters in
// total (spec 2026-10-06 decision 6; was 20). The parts around the colon
// have no limit of their own.
const maxTagLen = 32

// tagRe is the stored-tag rule (ADR 0009 amendment 2026-10-06): a-z, 0-9
// and -, with at most one colon between other characters. The colon is an
// ordinary character to the bus; the using-agentbus skill gives it its
// key:value reading. Length is checked apart from the regexp so one message
// can name both limits.
var tagRe = regexp.MustCompile(`^[a-z0-9-]+(:[a-z0-9-]+)?$`)

// tagRuleText is the rule as NormalizeTags states it to the caller.
const tagRuleText = "tag %q must be 1-32 characters of a-z, 0-9 and -, with at most one : between other characters"

// validTag reports whether t (already lowercased) is a storable tag.
func validTag(t string) bool { return len(t) <= maxTagLen && tagRe.MatchString(t) }

// normalizeTagList is the shared body of NormalizeTags and the pattern
// normalizer: lowercase, reject any entry valid rejects (one bad entry fails
// the whole call; badFmt takes the lowercased entry as its one %q), drop
// duplicates, cap at maxTags (capFmt takes maxTags as its one %d), sort.
// nil in, nil out.
func normalizeTagList(tags []string, valid func(string) bool, badFmt, capFmt string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(t)
		if !valid(t) {
			return nil, errf("validation", false, badFmt, t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > maxTags {
		return nil, errf("validation", false, capFmt, maxTags)
	}
	slices.Sort(out)
	return out, nil
}

// NormalizeTags lowercases tags, rejects any that fail the tag rule (one bad
// tag fails the whole call), drops duplicates, sorts, and caps the result at
// maxTags. nil in, nil out. Exported for repoconfig's persistent tag sets.
// Filters take patterns instead: see NormalizeTagPatterns.
func NormalizeTags(tags []string) ([]string, error) {
	return normalizeTagList(tags, validTag, tagRuleText, "at most %d tags per message")
}

// tagPrefixRe is what may precede a pattern's trailing *: the start of some
// valid tag, so the colon may be last (env:*) but not first or doubled.
var tagPrefixRe = regexp.MustCompile(`^[a-z0-9-]+(:[a-z0-9-]*)?$`)

// NormalizeTagPatterns is NormalizeTags for filters (history, search, tag
// subscriptions, the TUI): each entry is an exact tag, or a 1-32 character
// tag prefix followed by one *. Same lowercasing, deduplication, sort and
// cap. nil in, nil out.
func NormalizeTagPatterns(patterns []string) ([]string, error) {
	return normalizeTagList(patterns, validTagPattern,
		"tag pattern %q must be a tag, or a 1-32 character tag prefix followed by one *",
		"at most %d tag patterns")
}

func validTagPattern(p string) bool {
	prefix, ok := strings.CutSuffix(p, "*")
	if !ok {
		return validTag(p)
	}
	return len(prefix) <= maxTagLen && tagPrefixRe.MatchString(prefix)
}

// tagRange maps a pattern to the inclusive byte range of the tags it
// matches: an exact tag to itself, a prefix p* to [p, p~]. Every legal tag
// character (-, 0-9, :, a-z) sorts below ~ (0x7e), so p~ is above every
// tag that starts with p. SQL compares with BETWEEN, never LIKE: LIKE is
// case-insensitive and cannot use message_tags_tag_seq, whose collation is
// BINARY.
func tagRange(pattern string) (lo, hi string) {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return p, p + "~"
	}
	return pattern, pattern
}

// MatchTag is the Go side of the rule tagsFilter and tagCond apply in SQL,
// for matched_tags and the TUI's tag panes. env* matches env itself,
// environment and env:prod; env:* matches only env:<value>.
func MatchTag(pattern, tag string) bool {
	lo, hi := tagRange(pattern)
	return tag >= lo && tag <= hi
}

// MatchTags reports whether every pattern matches at least one of tags: the
// AND-set rule, for matched_tags and the TUI's tag panes.
func MatchTags(patterns, tags []string) bool {
	return !slices.ContainsFunc(patterns, func(p string) bool {
		return !slices.ContainsFunc(tags, func(t string) bool { return MatchTag(p, t) })
	})
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

// tagsFilter is the any-of clause for History and search: messages
// carrying a tag in the range of at least one pattern (BETWEEN, so the
// comparison is BINARY and can use the (seq, tag) key; see tagRange).
// Empty patterns adds nothing.
func tagsFilter(alias string, patterns []string) (string, []any) {
	if len(patterns) == 0 {
		return "", nil
	}
	conds := make([]string, len(patterns))
	args := make([]any, 0, 2*len(patterns))
	for i, p := range patterns {
		lo, hi := tagRange(p)
		conds[i] = "t.tag BETWEEN ? AND ?"
		args = append(args, lo, hi)
	}
	return " AND EXISTS (SELECT 1 FROM message_tags t WHERE t.seq=" + alias + ".seq AND (" + strings.Join(conds, " OR ") + "))", args
}

// tagSource is the pseudo-subscription row that carries the cursor, pending
// batch, and idle expiry shared by all of a sender's tag sets (ADR 0009
// item 5). No channel can bear this name: ChannelNameRule rejects the
// trailing slash, and it is neither a dm/ inbox nor a task list.
const tagSource = "tags/"

// tagSet is one AND set of patterns: a message matches when every pattern
// matches one of its tags and it was written after the set was added.
type tagSet struct {
	tags       []string
	createdSeq int64
}

func tagsKey(tags []string) string { return strings.Join(tags, ",") }

// SubscribeTags adds an AND set of 1-10 tag patterns (NormalizeTagPatterns). The first set also creates the
// shared tags/ row at the current head, so nothing before it is delivered
// (like Subscribe from now); a set is idempotent.
func (b *Bus) SubscribeTags(as string, tags []string) error {
	if err := b.auth(b.db, as); err != nil {
		return err
	}
	tags, err := NormalizeTagPatterns(tags)
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
	for _, p := range tags {
		lo, hi := tagRange(p)
		if _, err := tx.Exec("INSERT OR IGNORE INTO tag_subscription_tags(sender,tags_key,tag,lo,hi) VALUES(?,?,?,?,?)", as, tagsKey(tags), p, lo, hi); err != nil {
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
	tags, err := NormalizeTagPatterns(tags)
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
// satisfying every pattern of at least one set added before the message.
// Once a direct channel subscription ends, that channel's messages above
// the tag cursor become tag-deliverable again (ADR 0009 item 7 applies to
// current direct subscriptions, not past ones). Matching is driven from the
// sender's few subscribed patterns into message_tags(tag, seq) (#13), never
// a scan of messages or message_tags. The join is an OR of two branches
// SQLite plans as a MULTI-INDEX OR: an exact pattern (lo = hi) seeks
// (tag=? AND seq>?) above the floor as before; a prefix (lo < hi)
// range-scans its tag range on the same index and filters on seq, reading
// every tagged message in the range (retention bounds it). BETWEEN, never
// LIKE, keeps the BINARY index usable (#20). A set matches when the message
// satisfies as many distinct patterns as the set has, so two tags under one
// pattern (env:prod and env:staging under env:*) count once. mt.seq's two
// comparisons (rather than max(ts.created_seq, ?)) are what let SQLite seek
// message_tags_tag_seq instead of scanning it.
func tagCond(as string, floor int64) (string, []any) {
	return `messages.seq IN (
		SELECT mt.seq FROM tag_subscription_tags st
		JOIN tag_subscriptions ts ON ts.sender=st.sender AND ts.tags_key=st.tags_key
		JOIN message_tags mt ON ((st.lo=st.hi AND mt.tag=st.lo AND mt.seq>?) OR (st.lo<st.hi AND mt.tag BETWEEN st.lo AND st.hi AND mt.seq>?)) AND mt.seq>ts.created_seq
		WHERE st.sender=?
		GROUP BY mt.seq, st.tags_key
		HAVING count(DISTINCT st.tag)=(SELECT count(*) FROM tag_subscription_tags c WHERE c.sender=st.sender AND c.tags_key=st.tags_key))
	AND messages.channel IN (SELECT name FROM channels WHERE kind='ordinary' AND name NOT LIKE 'dm/%')
	AND messages.channel NOT IN (SELECT channel FROM subscriptions WHERE sender=?)`, []any{floor, floor, as, as}
}

// matchedTags is the union, sorted and deduplicated, of m's own tags that
// satisfied a set m matches (env:prod, never the pattern env:*). A set
// matches when every pattern in it matches at least one of m's tags.
func matchedTags(sets []tagSet, m Message) []string {
	var out []string
	for _, s := range sets {
		if m.Seq <= s.createdSeq || !MatchTags(s.tags, m.Tags) {
			continue
		}
		for _, t := range m.Tags {
			if slices.ContainsFunc(s.tags, func(p string) bool { return MatchTag(p, t) }) && !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}
