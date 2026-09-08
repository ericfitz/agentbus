package bus

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ericfitz/agentbus-local/internal/config"
)

const (
	embedBatchSize      = 64
	embedRequestTimeout = 30 * time.Second
	embedQueryTimeout   = 10 * time.Second
)

type embedder struct {
	endpoint, model, key string
	client               *http.Client
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
	e := &embedder{endpoint: cfg.EmbeddingEndpoint, model: cfg.EmbeddingModel, client: &http.Client{}}
	if cfg.EmbeddingAPIKeyFile != "" {
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
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("embeddings endpoint returned %s", resp.Status)
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
	vecs = make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embedding index %d out of range", d.Index)
		}
		normalize(d.Embedding)
		vecs[d.Index] = d.Embedding
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

// embedBatch embeds up to embedBatchSize live memory revisions lacking a row for
// the configured model, after deleting rows from other models.
func (b *Bus) embedBatch(ctx context.Context) (int, error) {
	if b.embedder == nil {
		return 0, nil
	}
	if _, err := b.db.Exec("DELETE FROM embeddings WHERE model<>?", b.embedder.model); err != nil {
		return 0, internal(err)
	}
	rows, err := b.db.Query(`SELECT m.seq, m.content FROM messages m LEFT JOIN embeddings e ON e.seq=m.seq
	  WHERE m.memory_id IS NOT NULL AND m.tombstone=0 AND e.seq IS NULL ORDER BY m.seq LIMIT ?`, embedBatchSize)
	if err != nil {
		return 0, internal(err)
	}
	var seqs []int64
	var texts []string
	for rows.Next() {
		var s int64
		var c string
		if err := rows.Scan(&s, &c); err != nil {
			rows.Close()
			return 0, internal(err)
		}
		seqs = append(seqs, s)
		texts = append(texts, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, internal(err)
	}
	rows.Close()
	if len(seqs) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, embedRequestTimeout)
	defer cancel()
	vecs, err := b.embedder.embed(ctx, texts)
	if err != nil {
		return 0, err
	}
	tx, err := b.db.Begin()
	if err != nil {
		return 0, internal(err)
	}
	defer tx.Rollback()
	for i, s := range seqs {
		if _, err := tx.Exec("INSERT OR REPLACE INTO embeddings(seq,model,vector) VALUES(?,?,?)", s, b.embedder.model, encodeVec(vecs[i])); err != nil {
			return 0, internal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, internal(err)
	}
	return len(seqs), nil
}
