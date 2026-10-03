package bus

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ericfitz/agentbus/internal/config"
)

const (
	embedBatchSize      = 64
	embedRequestTimeout = 30 * time.Second
)

type embedder struct {
	endpoint, model, key string
	client               *http.Client
	queryTimeout         time.Duration // embedding_query_timeout_seconds; bounds the query embedding in Search
}

var exportLine = regexp.MustCompile(`^\s*export\s+[A-Za-z_][A-Za-z0-9_]*\s*=\s*'?"?([^'"\s]+)'?"?\s*$`)

// readKeyFile accepts a bare key or a one-line `export NAME='value'` file.
func readKeyFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(body))
	if m := exportLine.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	return s, nil
}

func newEmbedder(cfg config.Config) (*embedder, error) {
	e := &embedder{endpoint: cfg.EmbeddingEndpoint, model: cfg.EmbeddingModel, client: &http.Client{}, queryTimeout: time.Duration(cfg.EmbeddingQueryTimeoutSeconds * float64(time.Second))}
	// The named environment variable wins; a process started without it
	// (a harness-spawned MCP server) falls back to the key file.
	if cfg.EmbeddingAPIKeyEnv != "" {
		e.key = os.Getenv(cfg.EmbeddingAPIKeyEnv)
	}
	if e.key == "" && cfg.EmbeddingAPIKeyFile != "" {
		k, err := readKeyFile(cfg.EmbeddingAPIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("embedding_api_key_file: %w", err)
		}
		e.key = k
	}
	return e, nil
}

func (e *embedder) embed(ctx context.Context, texts []string) (vecs [][]float32, err error) {
	body, err := json.Marshal(map[string]any{"input": texts, "model": e.model})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, &embedHTTPError{status: resp.Status, code: resp.StatusCode, body: strings.Join(strings.Fields(string(snippet)), " "), noKey: e.key == ""}
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings endpoint returned %d vectors for %d inputs", len(out.Data), len(texts))
	}
	// Validate strictly: each index in range exactly once, every vector
	// nonempty, and all vectors the same dimension. A malformed response
	// (duplicate/missing index, empty or ragged vector) must error rather
	// than silently persist a bad or zero vector (R7).
	vecs = make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	dim := -1
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embedding index %d out of range", d.Index)
		}
		if seen[d.Index] {
			return nil, fmt.Errorf("embedding index %d returned more than once", d.Index)
		}
		seen[d.Index] = true
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("embedding index %d is empty", d.Index)
		}
		if dim == -1 {
			dim = len(d.Embedding)
		} else if len(d.Embedding) != dim {
			return nil, fmt.Errorf("embedding index %d has dimension %d, want %d", d.Index, len(d.Embedding), dim)
		}
		normalize(d.Embedding)
		vecs[d.Index] = d.Embedding
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("embedding index %d missing from response", i)
		}
	}
	return vecs, nil
}

func normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
}

func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		if i < len(b) {
			s += float64(a[i]) * float64(b[i])
		}
	}
	return s
}

func encodeVec(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(x))
	}
	return buf
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// embedText is what a memory revision embeds: subject and content together
// when it has a subject, else the content alone (ADR 0010).
func embedText(subject, content string) string {
	if subject == "" {
		return content
	}
	return subject + "\n\n" + content
}

// unembeddedFrom selects live memories that still need an embedding under the
// model bound as ?1. Task rows are never embedded, and a row the endpoint
// rejected on its own under that model (embed_failures, ADR 0018) is not
// retried. StatusReport counts the same rows, so its backlog drains to 0.
const unembeddedFrom = `FROM messages m LEFT JOIN embeddings e ON e.seq=m.seq AND e.model=?1
	  WHERE m.memory_id IS NOT NULL AND m.tombstone=0 AND e.seq IS NULL AND m.channel <> 'tasks' AND m.channel NOT LIKE 'tasks/%'
	  AND NOT EXISTS (SELECT 1 FROM embed_failures f WHERE f.seq=m.seq AND f.model=?1)`

// embedHTTPError is a non-2xx answer from the embeddings endpoint, with the
// start of its body (which usually says why).
type embedHTTPError struct {
	status string
	code   int
	body   string
	noKey  bool // the request carried no API key
}

func (e *embedHTTPError) Error() string {
	msg := "embeddings endpoint returned " + e.status
	if e.body != "" {
		msg += ": " + e.body
	}
	switch e.code {
	case http.StatusUnauthorized, http.StatusForbidden:
		if e.noKey {
			msg += " (no API key in this process: set embedding_api_key_file, or embedding_api_key_env in the environment of every agentbus mcp)"
		} else {
			msg += " (check the key from embedding_api_key_env or embedding_api_key_file)"
		}
	}
	return msg
}

// rowRejected reports whether err is the endpoint refusing the input itself
// (too long, malformed), so retrying that text alone would fail the same
// way. Auth, rate limit, server and network errors are not a row's fault.
func rowRejected(err error) bool {
	var he *embedHTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.code == http.StatusBadRequest || he.code == http.StatusRequestEntityTooLarge || he.code == http.StatusUnprocessableEntity
}

// noteEmbed records the outcome of an embedding pass in notices (kind
// 'embedding', ADR 0018), so StatusReport shows what the process doing the
// embedding saw rather than only the reader's own query: an error replaces
// the notice, success clears it. A pass cut short by its caller's context
// says nothing either way.
func (b *Bus) noteEmbed(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	var dbErr error
	if err == nil {
		// Read first: an idle tick must not take a write lock (9dfc490).
		var set bool
		if dbErr = b.db.QueryRow("SELECT EXISTS(SELECT 1 FROM notices WHERE kind='embedding')").Scan(&set); dbErr == nil && set {
			_, dbErr = b.db.Exec("DELETE FROM notices WHERE kind='embedding'")
		}
	} else {
		_, dbErr = b.db.Exec("INSERT OR REPLACE INTO notices(kind,message,set_at) VALUES('embedding',?,?)", err.Error(), b.nowMs())
	}
	if dbErr != nil {
		b.log.Warn("record embedding status failed", "err", dbErr)
	}
}

// embedBatch embeds up to embedBatchSize live memory revisions lacking a row for
// the configured model, after deleting rows from other models. The HTTP call
// can outlive an edit/delete of the memory it's embedding, so each insert is
// conditioned on the seq still being a live memory revision at commit time
// (R6b); the returned count is the number of rows embedded or rejected,
// which can be less than the number requested. When the endpoint rejects
// the batch's input (rowRejected), each row is retried alone and the ones
// it rejects alone are recorded in embed_failures, so one bad memory never
// blocks the rest (ADR 0018).
func (b *Bus) embedBatch(ctx context.Context) (n int, err error) {
	if b.embedder == nil {
		return 0, nil
	}
	defer func() { b.noteEmbed(ctx, err) }()
	// Only pay for the DELETE when there is actually a stale-model row to
	// remove (e.g. embedding_model changed since these were written), not on
	// every batch pass. Rejections are per model too: a new model retries.
	var stale bool
	if err := b.db.QueryRow("SELECT EXISTS(SELECT 1 FROM embeddings WHERE model<>?1) OR EXISTS(SELECT 1 FROM embed_failures WHERE model<>?1)", b.embedder.model).Scan(&stale); err != nil {
		return 0, internal(err)
	}
	if stale {
		for _, q := range []string{"DELETE FROM embeddings WHERE model<>?", "DELETE FROM embed_failures WHERE model<>?"} {
			if _, err := b.db.Exec(q, b.embedder.model); err != nil {
				return 0, internal(err)
			}
		}
	}
	rows, err := b.db.Query(`SELECT m.seq, m.subject, m.content `+unembeddedFrom+` ORDER BY m.seq LIMIT ?2`, b.embedder.model, embedBatchSize)
	if err != nil {
		return 0, internal(err)
	}
	defer func() { _ = rows.Close() }()
	var seqs []int64
	var texts []string
	for rows.Next() {
		var s int64
		var subject, c string
		if err := rows.Scan(&s, &subject, &c); err != nil {
			return 0, internal(err)
		}
		seqs = append(seqs, s)
		texts = append(texts, embedText(subject, c))
	}
	if err := rows.Err(); err != nil {
		return 0, internal(err)
	}
	_ = rows.Close()
	if len(seqs) == 0 {
		return 0, nil
	}
	vecs, err := b.embedTimed(ctx, texts)
	rejected := map[int]string{}
	if rowRejected(err) {
		vecs = make([][]float32, len(texts))
		for i, text := range texts {
			v, err := b.embedTimed(ctx, []string{text})
			switch {
			case rowRejected(err):
				rejected[i] = err.Error()
			case err != nil:
				return 0, err
			default:
				vecs[i] = v[0]
			}
		}
	} else if err != nil {
		return 0, err
	}
	tx, err := b.db.Begin()
	if err != nil {
		return 0, internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var done int64
	for i, s := range seqs {
		var res sql.Result
		if reason, bad := rejected[i]; bad {
			res, err = tx.Exec(`INSERT OR REPLACE INTO embed_failures(seq,model,error,failed_at)
			  SELECT ?,?,?,? WHERE EXISTS (SELECT 1 FROM messages WHERE seq=? AND memory_id IS NOT NULL AND tombstone=0)`,
				s, b.embedder.model, reason, b.nowMs(), s)
		} else {
			res, err = tx.Exec(`INSERT OR REPLACE INTO embeddings(seq,model,vector)
			  SELECT ?,?,? WHERE EXISTS (SELECT 1 FROM messages WHERE seq=? AND memory_id IS NOT NULL AND tombstone=0)`,
				s, b.embedder.model, encodeVec(vecs[i]), s)
		}
		if err != nil {
			return 0, internal(err)
		}
		k, err := res.RowsAffected()
		if err != nil {
			return 0, internal(err)
		}
		done += k
	}
	if err := tx.Commit(); err != nil {
		return 0, internal(err)
	}
	return int(done), nil
}

// embedTimed is one embeddings request bounded by embedRequestTimeout.
func (b *Bus) embedTimed(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, embedRequestTimeout)
	defer cancel()
	return b.embedder.embed(ctx, texts)
}
