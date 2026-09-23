package bus

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

type SearchInput struct {
	Query   string   `json:"query"`
	Mode    string   `json:"mode,omitempty"`
	Channel string   `json:"channel,omitempty"`
	Sender  string   `json:"sender,omitempty"`
	Since   *int64   `json:"since,omitempty"`
	Until   *int64   `json:"until,omitempty"`
	Thread  *int64   `json:"thread,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Count   int      `json:"count,omitempty"`
	Cursor  string   `json:"cursor,omitempty"`
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

// searchHitBytes measures a SearchHit's own serialized size, including its
// score field (unlike envelopeBytes, sized for a bare Message). SearchHit
// holds only strings, ints, a float, and pointers/maps/slices of those, so
// json.Marshal cannot fail.
func searchHitBytes(h SearchHit) int {
	j, _ := json.Marshal(h)
	return len(j)
}

const (
	searchPageDefault = 100
	searchPageMax     = 1000
)

// ftsQuery turns free text into an FTS5 query of quoted terms (implicit AND),
// so user punctuation cannot produce a syntax error.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.Fields(q) {
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " ")
}

// searchFilters builds the shared WHERE clauses for the text and semantic
// search queries. Scoped to a channel, that channel's own equality clause is
// enough (Search has already checked as may read it); unscoped, a DM inbox
// that isn't as's own is excluded so a global search never leaks another
// identity's messages.
func (b *Bus) searchFilters(as string, in SearchInput) (string, []any) {
	var sb strings.Builder
	var args []any
	if in.Channel != "" {
		sb.WriteString(" AND m.channel=?")
		args = append(args, in.Channel)
	} else if !b.isObserver(as) {
		sb.WriteString(" AND (m.channel NOT LIKE 'dm/%' OR m.channel=?)")
		args = append(args, DMChannel(as))
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
	if f, a := tagsFilter("m", in.Tags); f != "" {
		sb.WriteString(f)
		args = append(args, a...)
	}
	return sb.String(), args
}

// textSearch runs the FTS5 query joined back to messages (excluding
// tombstones) and returns hits ranked by bm25, best match first. Every
// caller reaches this through Search, which already rejects a blank query,
// so fq (derived from in.Query) is never empty here.
func (b *Bus) textSearch(as string, in SearchInput, limit int) ([]SearchHit, error) {
	fq := ftsQuery(in.Query)
	filters, fargs := b.searchFilters(as, in)
	// ORDER BY rank is FTS5's own alias for bm25(messages_fts) with default
	// weights; used here instead of repeating the bm25() call so the sort
	// and the scored column read the same value by construction.
	q := "SELECT " + messageCols("m") + ", bm25(messages_fts) FROM messages_fts JOIN messages m ON m.seq=messages_fts.rowid WHERE messages_fts MATCH ? AND m.tombstone=0" + filters + " ORDER BY rank LIMIT ?"
	args := append([]any{fq}, fargs...)
	args = append(args, limit)
	rows, err := b.db.Query(q, args...)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	hits := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		var meta, refs, tags *string
		var rank float64
		if err := rows.Scan(&h.Seq, &h.Channel, &h.Sender, &h.Context, &h.CreatedAt, &h.Type, &h.Content, &h.ReplyTo, &meta, &refs, &h.MemoryID, &h.Revision, &tags, &rank); err != nil {
			return nil, internal(err)
		}
		m, err := decodeJSONFields(h.Message, meta, refs)
		if err != nil {
			return nil, internal(err)
		}
		h.Message = m
		if tags != nil {
			h.Tags = strings.Fields(*tags)
		}
		h.Score = -rank
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return hits, nil
}

func (b *Bus) Search(as string, in SearchInput) (SearchResult, error) {
	if err := b.auth(b.db, as); err != nil {
		return SearchResult{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return SearchResult{}, errf("validation", false, "query is required")
	}
	if in.Channel != "" && !b.dmReadable(as, in.Channel) {
		// No %q: see the matching comment in History.
		return SearchResult{}, errf("not_found", false, "channel does not exist")
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
	var err error
	if in.Tags, err = NormalizeTags(in.Tags); err != nil {
		return SearchResult{}, err
	}
	if in.Mode == "" {
		in.Mode = "text"
		if b.embedder != nil {
			in.Mode = "both"
		}
	}
	res := SearchResult{}
	var ranked []SearchHit
	switch in.Mode {
	case "text":
		// ponytail: every page re-runs and re-decodes the full top-1000-candidate
		// query (searchPageMax), then slices to the requested page in Go; fine
		// at this ceiling, add real cursor-based paging in SQL if 1000 rows per
		// call ever shows up in profiling.
		ranked, err = b.textSearch(as, in, searchPageMax)
	case "semantic", "both":
		ranked, res.SemanticUnavailable, err = b.rankedSearch(as, in)
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
	// Reserve the exact serialized size of SearchResult's non-record fields
	// (C2): Next isn't known until after trimming (it depends on the final
	// page length), so reserve against a max-length placeholder of the same
	// digit width used elsewhere for an int64 cursor; SemanticUnavailable is
	// a fixed-size bool.
	placeholderNext := strings.Repeat("9", upperBoundSeqDigits)
	reserve := marshalLen(SearchResult{Next: placeholderNext, SemanticUnavailable: res.SemanticUnavailable})
	limit := min(b.cfg.ResultDefaultKiB*1024, trimHardCeilingBytes) - reserve
	if limit > 0 {
		page = trimToBytes(page, searchHitBytes, limit)
	} else {
		// C2: the reserve alone exceeds the limit; return metadata only
		// rather than fail.
		page = []SearchHit{}
	}
	if offset+len(page) < len(ranked) {
		res.Next = strconv.Itoa(offset + len(page))
	}
	res.Hits = page
	return res, nil
}

// rankedSearch serves the semantic and both modes. If the embedder is unset or
// the endpoint is unreachable, it falls back to text search and reports
// unavailable=true.
func (b *Bus) rankedSearch(as string, in SearchInput) ([]SearchHit, bool, error) {
	if b.embedder == nil {
		hits, err := b.textSearch(as, in, searchPageMax)
		return hits, true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.embedder.queryTimeout)
	defer cancel()
	sem, err := b.semanticSearch(ctx, as, in)
	if err != nil {
		b.log.Warn("semantic search unavailable", "err", err)
		hits, terr := b.textSearch(as, in, searchPageMax)
		return hits, true, terr
	}
	if in.Mode == "semantic" {
		return sem, false, nil
	}
	text, err := b.textSearch(as, in, searchPageMax)
	if err != nil {
		return nil, false, err
	}
	return rrf(text, sem), false, nil
}

// semanticSearch ranks live memory revisions by dot product against the query
// embedding, brute-force in Go over candidates selected by the SQL filters.
func (b *Bus) semanticSearch(ctx context.Context, as string, in SearchInput) ([]SearchHit, error) {
	qv, err := b.embedder.embed(ctx, []string{in.Query})
	if err != nil {
		return nil, err
	}
	filters, fargs := b.searchFilters(as, in)
	candidateWhere := " FROM embeddings e JOIN messages m ON m.seq=e.seq WHERE e.model=? AND m.tombstone=0 AND m.memory_id IS NOT NULL" + filters

	vecRows, err := b.db.Query("SELECT e.seq, e.vector"+candidateWhere, append([]any{b.embedder.model}, fargs...)...)
	if err != nil {
		return nil, internal(err)
	}
	vectors := map[int64][]byte{}
	for vecRows.Next() {
		var seq int64
		var vec []byte
		if err := vecRows.Scan(&seq, &vec); err != nil {
			_ = vecRows.Close()
			return nil, internal(err)
		}
		vectors[seq] = vec
	}
	if err := vecRows.Err(); err != nil {
		_ = vecRows.Close()
		return nil, internal(err)
	}
	_ = vecRows.Close()

	rows, err := b.db.Query("SELECT "+messageCols("m")+candidateWhere, append([]any{b.embedder.model}, fargs...)...)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, internal(err)
	}
	hits := make([]SearchHit, 0, len(msgs))
	for _, m := range msgs {
		vec, ok := vectors[m.Seq]
		if !ok {
			continue
		}
		hits = append(hits, SearchHit{Message: m, Score: dot(qv[0], decodeVec(vec))})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Seq < hits[j].Seq
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > searchPageMax {
		hits = hits[:searchPageMax]
	}
	return hits, nil
}

// rrf merges two rankings by reciprocal rank fusion (k=60).
func rrf(text, sem []SearchHit) []SearchHit {
	score := map[int64]float64{}
	bySeq := map[int64]SearchHit{}
	for _, list := range [][]SearchHit{text, sem} {
		for rank, h := range list {
			score[h.Seq] += 1 / float64(60+rank+1)
			if _, ok := bySeq[h.Seq]; !ok {
				bySeq[h.Seq] = h
			}
		}
	}
	out := make([]SearchHit, 0, len(bySeq))
	for seq, h := range bySeq {
		h.Score = score[seq]
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Seq < out[j].Seq
		}
		return out[i].Score > out[j].Score
	})
	return out
}
