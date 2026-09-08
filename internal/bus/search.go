package bus

import (
	"strconv"
	"strings"
)

type SearchInput struct {
	Query   string `json:"query"`
	Mode    string `json:"mode,omitempty"`
	Channel string `json:"channel,omitempty"`
	Sender  string `json:"sender,omitempty"`
	Since   *int64 `json:"since,omitempty"`
	Until   *int64 `json:"until,omitempty"`
	Thread  *int64 `json:"thread,omitempty"`
	Count   int    `json:"count,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
}

type SearchHit struct {
	Message
	Score float64 `json:"score"`
}

type SearchResult struct {
	Hits                []SearchHit `json:"hits"`
	Next                string      `json:"next,omitempty"`
	SemanticUnavailable bool        `json:"semantic_unavailable,omitempty"`
}

const (
	searchPageDefault = 100
	searchPageMax     = 1000
)

// qualifiedColumns prefixes every message column with an alias, for joins
// where messages_fts also has a column named content.
func qualifiedColumns(alias string) string {
	cols := strings.Split(messageColumns, ", ")
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

// ftsQuery turns free text into an FTS5 query of quoted terms (implicit AND),
// so user punctuation cannot produce a syntax error.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.Fields(q) {
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " ")
}

func searchFilters(in SearchInput) (string, []any) {
	var sb strings.Builder
	var args []any
	if in.Channel != "" {
		sb.WriteString(" AND m.channel=?")
		args = append(args, in.Channel)
	}
	if in.Sender != "" {
		sb.WriteString(" AND m.sender=?")
		args = append(args, in.Sender)
	}
	if in.Since != nil {
		sb.WriteString(" AND m.created_at>=?")
		args = append(args, *in.Since)
	}
	if in.Until != nil {
		sb.WriteString(" AND m.created_at<=?")
		args = append(args, *in.Until)
	}
	if in.Thread != nil {
		sb.WriteString(" AND (m.seq=? OR m.reply_to=?)")
		args = append(args, *in.Thread, *in.Thread)
	}
	return sb.String(), args
}

// textSearch runs the FTS5 query joined back to messages (excluding
// tombstones) and returns hits ranked by bm25, best match first.
func (b *Bus) textSearch(in SearchInput, limit int) ([]SearchHit, error) {
	fq := ftsQuery(in.Query)
	if fq == "" {
		return []SearchHit{}, nil
	}
	filters, fargs := searchFilters(in)
	q := "SELECT " + qualifiedColumns("m") + ", bm25(messages_fts) FROM messages_fts JOIN messages m ON m.seq=messages_fts.rowid WHERE messages_fts MATCH ? AND m.tombstone=0" + filters + " ORDER BY bm25(messages_fts) LIMIT ?"
	args := append([]any{fq}, fargs...)
	args = append(args, limit)
	rows, err := b.db.Query(q, args...)
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	hits := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		var meta, refs *string
		var rank float64
		if err := rows.Scan(&h.Seq, &h.Channel, &h.Sender, &h.Context, &h.CreatedAt, &h.Type, &h.Content, &h.ReplyTo, &meta, &refs, &h.MemoryID, &h.Revision, &rank); err != nil {
			return nil, internal(err)
		}
		m, err := decodeJSONFields(h.Message, meta, refs)
		if err != nil {
			return nil, internal(err)
		}
		h.Message = m
		h.Score = -rank
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return hits, nil
}

func (b *Bus) Search(as string, in SearchInput) (SearchResult, error) {
	if err := b.auth(as); err != nil {
		return SearchResult{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return SearchResult{}, errf("validation", false, "query is required")
	}
	if in.Count <= 0 {
		in.Count = searchPageDefault
	}
	if in.Count > searchPageMax {
		in.Count = searchPageMax
	}
	offset := 0
	if in.Cursor != "" {
		o, err := strconv.Atoi(in.Cursor)
		if err != nil || o < 0 {
			return SearchResult{}, errf("validation", false, "invalid cursor %q", in.Cursor)
		}
		offset = o
	}
	if in.Mode == "" {
		in.Mode = "text"
		if b.embedder != nil {
			in.Mode = "both"
		}
	}
	res := SearchResult{}
	var ranked []SearchHit
	var err error
	switch in.Mode {
	case "text":
		ranked, err = b.textSearch(in, searchPageMax)
	case "semantic", "both":
		ranked, res.SemanticUnavailable, err = b.rankedSearch(in)
	default:
		return SearchResult{}, errf("validation", false, "mode must be text, semantic, or both")
	}
	if err != nil {
		return SearchResult{}, err
	}
	if offset > len(ranked) {
		offset = len(ranked)
	}
	page := ranked[offset:]
	if len(page) > in.Count {
		page = page[:in.Count]
	}
	page = trimToBytes(page, func(h SearchHit) int { return envelopeBytes(h.Message) }, b.cfg.ResultDefaultKiB*1024)
	if offset+len(page) < len(ranked) {
		res.Next = strconv.Itoa(offset + len(page))
	}
	res.Hits = page
	return res, nil
}

// rankedSearch is completed in Task 9. Until then semantic modes fall back to
// text results with SemanticUnavailable set.
func (b *Bus) rankedSearch(in SearchInput) ([]SearchHit, bool, error) {
	hits, err := b.textSearch(in, searchPageMax)
	return hits, true, err
}
