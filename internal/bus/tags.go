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
		t = strings.ToLower(strings.TrimSpace(t))
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
