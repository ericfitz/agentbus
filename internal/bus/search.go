package bus

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type SearchInput struct {
	Query   string   `json:"query,omitempty" jsonschema:"words to search for; may be omitted when tags are given, then the matches come back newest first"`
	Mode    string   `json:"mode,omitempty"`
	Channel string   `json:"channel,omitempty"`
	Sender  string   `json:"sender,omitempty"`
	Since   *int64   `json:"since,omitempty"`
	Until   *int64   `json:"until,omitempty"`
	Thread  *int64   `json:"thread,omitempty"`
	Tags    []string `json:"tags,omitempty" jsonschema:"only messages carrying all of these tags; a tag ending in * matches every tag with that prefix (env:*)"`
	Count   int      `json:"count,omitempty"`
	Cursor  string   `json:"cursor,omitempty"`
	Kinds   []string `json:"kinds,omitempty" jsonschema:"narrow to these kinds: memories, chat, tasks; default all; ignored when channel is set, but an unknown kind is still an error"`
}

// SearchKinds lists the values SearchInput.Kinds accepts.
var SearchKinds = []string{"memories", "chat", "tasks"}

// normalizeKinds rejects every unknown kind (case-sensitive, listed in input
// order) and returns the known ones deduplicated in input order.
func normalizeKinds(kinds []string) ([]string, error) {
	var out, bad []string
	for _, k := range kinds {
		switch {
		case slices.Contains(SearchKinds, k):
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		case !slices.Contains(bad, strconv.Quote(k)):
			bad = append(bad, strconv.Quote(k))
		}
	}
	if len(bad) > 0 {
		return nil, errf("validation", false, "unknown kind %s; kinds are %s", strings.Join(bad, ", "), strings.Join(SearchKinds, ", "))
	}
	return out, nil
}

// kindsPredicate returns the AND clause narrowing m.channel to kinds, or ""
// when kinds is empty or names all three. It must agree with IsTaskChannel.
func kindsPredicate(kinds []string) string {
	if len(kinds) == 0 || len(kinds) >= len(SearchKinds) {
		return ""
	}
	const tasks = "(m.channel='tasks' OR m.channel LIKE 'tasks/%')"
	const memKind = "m.channel IN (SELECT name FROM channels WHERE kind='memory')"
	var ps []string
	for _, k := range kinds {
		switch k {
		case "tasks":
			ps = append(ps, tasks)
		case "memories":
			ps = append(ps, "("+memKind+" AND NOT "+tasks+")")
		case "chat":
			ps = append(ps, "(m.channel NOT IN (SELECT name FROM channels WHERE kind='memory'))")
		}
	}
	return " AND (" + strings.Join(ps, " OR ") + ")"
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

// searchTerm is one whitespace-separated word of a search query with its
// trailing stars removed; Prefix reports whether it had any.
type searchTerm struct {
	Text   string
	Prefix bool
}

// searchTerms splits a query into its terms (ADR 0019). A word that is only
// stars (`*`, `**`) is dropped: FTS5 would reject it as syntax, and it names
// nothing. A star anywhere but the end is part of the word; the tokenizer
// discards it. Shared by ftsQuery and semanticQuery so the text and
// semantic halves of a search read the query the same way.
func searchTerms(q string) []searchTerm {
	var terms []searchTerm
	for _, w := range strings.Fields(q) {
		bare := strings.TrimRight(w, "*")
		if bare == "" {
			continue
		}
		terms = append(terms, searchTerm{Text: bare, Prefix: len(bare) < len(w)})
	}
	return terms
}

// HasSearchTerms reports whether q holds at least one word once star-only
// words are dropped. A search without terms is tags-only (or an error when
// it has no tags either); callers such as the TUI use this so they read a
// query the same way Search does.
func HasSearchTerms(q string) bool { return len(searchTerms(q)) > 0 }

// ftsQuery turns free text into an FTS5 query of quoted terms (implicit AND),
// so user punctuation cannot produce a syntax error. A prefix term becomes a
// quoted prefix phrase, "e159e8c"*: FTS5 applies the star to the phrase's
// last token, so commit:e159e8c* and owner/repo@e159* work too. The result
// is empty only when the query has no terms; Search diverts that to a
// tags-only search (or refuses it without tags) before textSearch builds a
// MATCH.
func ftsQuery(q string) string {
	var terms []string
	for _, st := range searchTerms(q) {
		term := `"` + strings.ReplaceAll(st.Text, `"`, `""`) + `"`
		if st.Prefix {
			term += "*"
		}
		terms = append(terms, term)
	}
	return strings.Join(terms, " ")
}

// semanticQuery is the text the semantic modes embed: the query's terms
// with their trailing stars removed, joined by single spaces (ADR 0019).
// The embedding should see the words, not the prefix syntax.
func semanticQuery(q string) string {
	var words []string
	for _, st := range searchTerms(q) {
		words = append(words, st.Text)
	}
	return strings.Join(words, " ")
}

// searchFilters builds the shared WHERE clauses for the text and semantic
// search queries. Scoped to a channel, that channel's own equality clause is
// enough (Search has already checked as may read it); unscoped, a DM is
// included only when it is in as's own inbox or as sent it, so a global
// search never leaks a conversation as is not part of.
func (b *Bus) searchFilters(as string, in SearchInput) (string, []any) {
	var sb strings.Builder
	var args []any
	if in.Channel != "" {
		sb.WriteString(" AND m.channel=?")
		args = append(args, in.Channel)
	} else if !b.isObserver(as) {
		sb.WriteString(" AND (m.channel NOT LIKE 'dm/%' OR m.channel=? OR m.sender=?)")
		args = append(args, DMChannel(as), as)
	}
	sb.WriteString(kindsPredicate(in.Kinds))
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
// caller reaches this through Search, which sends a query left with no terms
// (blank, or star-only words, ADR 0019) to tagsOnlySearch or rejects it, so
// fq (derived from in.Query) is never empty here.
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
		var meta, tags *string
		var rank float64
		if err := rows.Scan(&h.Seq, &h.Channel, &h.Sender, &h.Context, &h.CreatedAt, &h.Type, &h.Subject, &h.Content, &h.ReplyTo, &meta, &h.MemoryID, &h.Revision, &tags, &rank); err != nil {
			return nil, internal(err)
		}
		m, err := decodeJSONFields(h.Message, meta)
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

// tagsOnlySearch serves a search with tags and no query terms: the live
// messages matching the filters, newest first, each with Score 0. Search
// has validated the filters and tags; nothing is embedded.
func (b *Bus) tagsOnlySearch(as string, in SearchInput, limit int) ([]SearchHit, error) {
	filters, fargs := b.searchFilters(as, in)
	q := "SELECT " + messageCols("m") + " FROM messages m WHERE m.tombstone=0" + filters + " ORDER BY m.seq DESC LIMIT ?"
	rows, err := b.db.Query(q, append(fargs, limit)...)
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
		hits = append(hits, SearchHit{Message: m})
	}
	return hits, nil
}

func (b *Bus) Search(as string, in SearchInput) (SearchResult, error) {
	if err := b.auth(b.db, as); err != nil {
		return SearchResult{}, err
	}
	var err error
	if in.Tags, err = NormalizeTagPatterns(in.Tags); err != nil {
		return SearchResult{}, err
	}
	if in.Kinds, err = normalizeKinds(in.Kinds); err != nil {
		return SearchResult{}, err
	}
	if in.Channel != "" {
		in.Kinds = nil
	}
	// No terms with tags is a tags-only search (no ranking, never embeds);
	// without tags a blank query is an error, and a star-only one keeps the
	// ADR 0019 refusal. Both are decided before the mode switch.
	tagsOnly := len(searchTerms(in.Query)) == 0
	if tagsOnly && len(in.Tags) == 0 {
		if strings.TrimSpace(in.Query) == "" {
			return SearchResult{}, errf("validation", false, "query is required (or give tags)")
		}
		return SearchResult{}, errf("validation", false, "query must contain a word; * alone matches nothing")
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
	if in.Mode == "" {
		in.Mode = "text"
		if b.embedder != nil {
			in.Mode = "both"
		}
	}
	res := SearchResult{}
	var ranked []SearchHit
	switch in.Mode {
	case "text", "semantic", "both":
	default:
		return SearchResult{}, errf("validation", false, "mode must be text, semantic, or both")
	}
	switch {
	case tagsOnly:
		ranked, err = b.tagsOnlySearch(as, in, searchPageMax)
	case in.Mode == "text":
		// ponytail: every page re-runs and re-decodes the full top-1000-candidate
		// query (searchPageMax), then slices to the requested page in Go; fine
		// at this ceiling, add real cursor-based paging in SQL if 1000 rows per
		// call ever shows up in profiling.
		ranked, err = b.textSearch(as, in, searchPageMax)
	default:
		ranked, res.SemanticUnavailable, err = b.rankedSearch(as, in)
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
		b.warnLimited("semantic search:"+normText(b.embedder.errText(err)), "semantic search unavailable",
			"endpoint", b.embedder.safeEndpoint(), "model", b.embedder.model, "mode", in.Mode, "err", b.embedder.errText(err))
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

// semanticSearch ranks live rows of every kind by dot product against the query
// embedding, brute-force in Go over candidates selected by the SQL filters.
func (b *Bus) semanticSearch(ctx context.Context, as string, in SearchInput) ([]SearchHit, error) {
	qv, err := b.embedder.embed(ctx, []string{semanticQuery(in.Query)})
	if err != nil {
		return nil, err
	}
	filters, fargs := b.searchFilters(as, in)
	candidateWhere := " FROM embeddings e JOIN messages m ON m.seq=e.seq WHERE e.model=? AND m.tombstone=0" + filters

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
