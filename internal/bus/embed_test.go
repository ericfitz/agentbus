package bus

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus-local/internal/config"
)

// fakeEmbeddings maps each input to a 3-vector from a fixed table; unknown text gets a zero-ish vector.
func fakeEmbeddings(t *testing.T) *httptest.Server {
	table := map[string][]float64{
		"roses are red":    {1, 0, 0},
		"violets are blue": {0, 1, 0},
		"flowers":          {0.7, 0.7, 0},
		"git rebase":       {0, 0, 1},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		var data []item
		for i, s := range req.Input {
			v, ok := table[s]
			if !ok {
				v = []float64{0.1, 0.1, 0.1}
			}
			data = append(data, item{Index: i, Embedding: v})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "model": req.Model})
	}))
}

func newEmbedBus(t *testing.T, endpoint string) *Bus {
	t.Helper()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	os.WriteFile(keyFile, []byte("export VOYAGE_API_KEY='sk-test'\n"), 0o600)
	cfg := config.Default()
	cfg.DataDirectory = dir
	cfg.Path = filepath.Join(dir, "config.json")
	cfg.EmbeddingEndpoint = endpoint
	cfg.EmbeddingModel = "fake-1"
	cfg.EmbeddingAPIKeyFile = keyFile
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestVectorCodecAndKeyFile(t *testing.T) {
	v := []float32{3, 4}
	normalize(v)
	if math.Abs(float64(v[0])-0.6) > 1e-6 {
		t.Fatal(v)
	}
	if d := decodeVec(encodeVec(v)); len(d) != 2 || d[1] != v[1] {
		t.Fatal(d)
	}
	p := filepath.Join(t.TempDir(), "k")
	os.WriteFile(p, []byte("  raw-key \n"), 0o600)
	if k, _ := readKeyFile(p); k != "raw-key" {
		t.Fatal(k)
	}
	os.WriteFile(p, []byte("export X_API_KEY='quoted'\n"), 0o600)
	if k, _ := readKeyFile(p); k != "quoted" {
		t.Fatal(k)
	}
}

func TestEmbedBatchAndSemanticSearch(t *testing.T) {
	srv := fakeEmbeddings(t)
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	b.CreateChannel(sam, "dev", "ordinary")
	b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	b.Send(sam, SendInput{Channel: "mem", Content: "violets are blue"})
	b.Send(sam, SendInput{Channel: "mem", Content: "git rebase"})
	b.Send(sam, SendInput{Channel: "dev", Content: "roses are red"}) // ordinary: never embedded
	// embedSoon runs a background pass per memory Send above; wait for any
	// in-flight pass before asserting, and assert on the committed total
	// rather than this call's own n, since the background pass may have
	// already embedded some or all of the rows.
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatalf("err=%v", err)
	}
	var embedded int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings").Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != 3 {
		t.Fatalf("embedded=%d", embedded)
	}
	b.waitEmbed()
	if n, _ := b.embedBatch(context.Background()); n != 0 {
		t.Fatal("second pass must find nothing")
	}
	r, err := b.Search(sam, SearchInput{Query: "flowers", Mode: "semantic"})
	if err != nil || len(r.Hits) != 3 || r.Hits[2].Content != "git rebase" || r.SemanticUnavailable {
		t.Fatalf("%+v %v", r, err)
	}
	r, _ = b.Search(sam, SearchInput{Query: "roses", Mode: "both"})
	if len(r.Hits) == 0 || r.Hits[0].Content != "roses are red" {
		t.Fatalf("fusion must rank the text+semantic match first: %+v", r.Hits)
	}
	// Two identical-content memories tie on score; the order must be stable
	// across calls (tie-break on seq), not whatever an unstable sort emits.
	b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatalf("expected to embed the duplicate: %v", err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings").Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != 4 {
		t.Fatalf("embedded=%d", embedded)
	}
	first, _ := b.Search(sam, SearchInput{Query: "roses are red", Mode: "semantic"})
	second, _ := b.Search(sam, SearchInput{Query: "roses are red", Mode: "semantic"})
	if len(first.Hits) < 2 || len(first.Hits) != len(second.Hits) {
		t.Fatalf("expected at least 2 tied hits: %+v", first.Hits)
	}
	for i := range first.Hits {
		if first.Hits[i].Seq != second.Hits[i].Seq {
			t.Fatalf("tie order must be stable across calls: %+v vs %+v", first.Hits, second.Hits)
		}
	}
	// Model change: old rows orphaned and re-embedded.
	b.cfg.EmbeddingModel = "fake-2"
	b.embedder.model = "fake-2"
	b.waitEmbed()
	if n, _ := b.embedBatch(context.Background()); n != 4 {
		t.Fatalf("model change must re-embed, got %d", n)
	}
	var stale int
	b.db.QueryRow("SELECT count(*) FROM embeddings WHERE model='fake-1'").Scan(&stale)
	if stale != 0 {
		t.Fatal("old-model rows must be deleted")
	}
}

// R6a: editing a memory must remove the old revision's embedding vector
// rather than waiting for the FK cascade, which only fires at purge, up to
// 72h later. embeddings must hold live revisions only.
func TestEditMemoryRemovesOldEmbedding(t *testing.T) {
	srv := fakeEmbeddings(t)
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings WHERE seq=?", c.Seq).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("expected an embedding for the original revision before editing")
	}
	if _, err := b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "roses are blue"}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings WHERE seq=?", c.Seq).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("old revision's embedding must be removed on edit")
	}
}

// R6a: deleting a memory must remove its embedding vector the same way.
func TestDeleteMemoryRemovesEmbedding(t *testing.T) {
	srv := fakeEmbeddings(t)
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteMemory(sam, *c.MemoryID, ""); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings WHERE seq=?", c.Seq).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("deleted memory's embedding must be removed")
	}
}

func TestSemanticFallsBackWhenEndpointDown(t *testing.T) {
	srv := fakeEmbeddings(t)
	b := newEmbedBus(t, srv.URL)
	srv.Close()
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	r, err := b.Search(sam, SearchInput{Query: "roses", Mode: "semantic"})
	if err != nil || !r.SemanticUnavailable || len(r.Hits) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := b.embedBatch(context.Background()); err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("embedBatch must report the endpoint failure: %v", err)
	}
}
