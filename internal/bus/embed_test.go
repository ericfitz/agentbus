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
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/config"
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
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
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
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "model": req.Model})
	}))
}

func newEmbedBus(t *testing.T, endpoint string) *Bus {
	t.Helper()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	_ = os.WriteFile(keyFile, []byte("export VOYAGE_API_KEY='sk-test'\n"), 0o600)
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
	t.Cleanup(func() { _ = b.Close() })
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
	_ = os.WriteFile(p, []byte("  raw-key \n"), 0o600)
	if k, _ := readKeyFile(p); k != "raw-key" {
		t.Fatal(k)
	}
	_ = os.WriteFile(p, []byte("export X_API_KEY='quoted'\n"), 0o600)
	if k, _ := readKeyFile(p); k != "quoted" {
		t.Fatal(k)
	}
	// The env var wins; unset, the key file is the fallback.
	cfg := config.Config{EmbeddingAPIKeyEnv: "AGENTBUS_TEST_EMBED_KEY", EmbeddingAPIKeyFile: p}
	if e, err := newEmbedder(cfg); err != nil || e.key != "quoted" {
		t.Fatal(e, err)
	}
	t.Setenv("AGENTBUS_TEST_EMBED_KEY", "from-env")
	if e, err := newEmbedder(cfg); err != nil || e.key != "from-env" {
		t.Fatal(e, err)
	}
}

func TestEmbedBatchAndSemanticSearch(t *testing.T) {
	srv := fakeEmbeddings(t)
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	_, _ = b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	_, _ = b.Send(sam, SendInput{Channel: "mem", Content: "violets are blue"})
	_, _ = b.Send(sam, SendInput{Channel: "mem", Content: "git rebase"})
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "roses are red"}) // ordinary: embedded too (ADR 0032)
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
	if embedded != 4 {
		t.Fatalf("embedded=%d", embedded)
	}
	b.waitEmbed()
	if n, _ := b.embedBatch(context.Background()); n != 0 {
		t.Fatal("second pass must find nothing")
	}
	r, err := b.Search(sam, SearchInput{Query: "flowers", Mode: "semantic"})
	if err != nil || len(r.Hits) != 4 || r.Hits[3].Content != "git rebase" || r.SemanticUnavailable {
		t.Fatalf("%+v %v", r, err)
	}
	r, _ = b.Search(sam, SearchInput{Query: "roses", Mode: "both"})
	if len(r.Hits) == 0 || r.Hits[0].Content != "roses are red" {
		t.Fatalf("fusion must rank the text+semantic match first: %+v", r.Hits)
	}
	// Two identical-content memories tie on score; the order must be stable
	// across calls (tie-break on seq), not whatever an unstable sort emits.
	_, _ = b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatalf("expected to embed the duplicate: %v", err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings").Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != 5 {
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
	if n, _ := b.embedBatch(context.Background()); n != 5 {
		t.Fatalf("model change must re-embed, got %d", n)
	}
	var stale int
	_ = b.db.QueryRow("SELECT count(*) FROM embeddings WHERE model='fake-1'").Scan(&stale)
	if stale != 0 {
		t.Fatal("old-model rows must be deleted")
	}
}

// TestEmbedBatchEmbedsBareTasksChannel: the bare "tasks" list is a task
// channel too (ADR 0007, bus.IsTaskChannel), so its rows embed subject plus
// description, not JSON (ADR 0032 removed the #14 exclusion). "tasks" is no
// longer a default (ADR 0013); the row is shaped directly, like a leftover
// from before the upgrade, since create_channel still refuses the name.
func TestEmbedBatchEmbedsBareTasksChannel(t *testing.T) {
	srv, rec := recordEmbeddings(t, nil)
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	if _, err := b.db.Exec("INSERT INTO channels(name,kind,created_seq) VALUES('tasks','memory',0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks", Subject: "a", Description: "roses are red"}); err != nil {
		t.Fatal(err)
	}
	drainEmbed(t, b)
	var embedded int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings").Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != 1 {
		t.Fatalf("embedded=%d, want 1", embedded)
	}
	if _, in := rec.snapshot(); !slices.Contains(in, "a\n\nroses are red") {
		t.Fatalf("inputs %q", in)
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
	_, _ = b.CreateChannel(sam, "mem", "memory")
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
	_, _ = b.CreateChannel(sam, "mem", "memory")
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

// R6b: an HTTP call that started before an edit/delete must not persist a
// vector for a seq that is no longer a live memory revision by the time the
// store transaction runs, and the returned count must exclude it.
func TestEmbedBatchSkipsSeqTombstonedDuringHTTPCall(t *testing.T) {
	reached := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		<-release
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var data []map[string]any
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	// Guarantee the handler unblocks even if an assertion below fails
	// (t.Fatal) before the normal release point below: deferred LIFO, this
	// runs BEFORE srv.Close (which waits for in-flight requests), so a
	// failure path cannot hang the test forever.
	defer releaseHandler()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")

	// Hold embedMu so Send's own background embedSoon pass no-ops; the test
	// drives embedBatch directly so it controls exactly when the HTTP call
	// starts relative to the delete below.
	b.embedMu.Lock()
	c, err := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	b.embedMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := b.embedBatch(context.Background())
		done <- result{n, err}
	}()
	select { // the batch's HTTP call is now blocked mid-flight
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("embedBatch's HTTP call never reached the handler")
	}
	if err := b.DeleteMemory(sam, *c.MemoryID, ""); err != nil {
		t.Fatal(err)
	}
	releaseHandler()
	var r result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("embedBatch did not return after the handler was released")
	}
	if r.err != nil || r.n != 0 {
		t.Fatalf("expected 0 inserted for a seq tombstoned mid-call, got n=%d err=%v", r.n, r.err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings WHERE seq=?", c.Seq).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("embedding for a seq tombstoned during the HTTP call must not be inserted")
	}
}

// R7: embed must reject a malformed response rather than silently store a
// bad or missing vector.
func TestEmbedRejectsMalformedResponses(t *testing.T) {
	texts := []string{"a", "b"}
	cases := []struct {
		name string
		body string
	}{
		{"duplicate index", `{"data":[{"index":0,"embedding":[1,0,0]},{"index":0,"embedding":[0,1,0]}]}`},
		{"missing index", `{"data":[{"index":0,"embedding":[1,0,0]}]}`},
		{"empty vector", `{"data":[{"index":0,"embedding":[1,0,0]},{"index":1,"embedding":[]}]}`},
		{"dimension mismatch", `{"data":[{"index":0,"embedding":[1,0,0]},{"index":1,"embedding":[1,0]}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			e := &embedder{endpoint: srv.URL, model: "m", client: &http.Client{}}
			if _, err := e.embed(context.Background(), texts); err == nil {
				t.Fatalf("%s: expected an error", c.name)
			}
		})
	}
}

// R7: Search in semantic mode must fall back to text with
// SemanticUnavailable when the endpoint returns a malformed response for the
// query embedding.
func TestSemanticFallsBackOnMalformedEmbeddingResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[]}]}`))
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	_, _ = b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	r, err := b.Search(sam, SearchInput{Query: "roses", Mode: "semantic"})
	if err != nil || !r.SemanticUnavailable || len(r.Hits) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestSemanticFallsBackWhenEndpointDown(t *testing.T) {
	srv := fakeEmbeddings(t)
	b := newEmbedBus(t, srv.URL)
	srv.Close()
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	_, _ = b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	r, err := b.Search(sam, SearchInput{Query: "roses", Mode: "semantic"})
	if err != nil || !r.SemanticUnavailable || len(r.Hits) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := b.embedBatch(context.Background()); err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("embedBatch must report the endpoint failure: %v", err)
	}
}

// embedding_query_timeout_seconds bounds the query embedding: a hung
// endpoint yields the text fallback after the configured timeout, not the
// old fixed 10 s.
func TestSemanticQueryTimeoutIsConfigurable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release) // runs before srv.Close so the handler can return
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDirectory = dir
	cfg.Path = filepath.Join(dir, "config.json")
	cfg.EmbeddingEndpoint = srv.URL
	cfg.EmbeddingModel = "fake-1"
	cfg.EmbeddingQueryTimeoutSeconds = 0.2
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	start := time.Now()
	r, err := b.Search(sam, SearchInput{Query: "roses", Mode: "semantic"})
	if err != nil || !r.SemanticUnavailable {
		t.Fatalf("%+v %v", r, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("search took %v; the 0.2 s query timeout was not applied", d)
	}
}

// A memory with a subject embeds subject and content together (ADR 0010);
// one without embeds the content alone.
func TestEmbedBatchEmbedsSubjectWithContent(t *testing.T) {
	var mu sync.Mutex
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		inputs = append(inputs, req.Input...)
		mu.Unlock()
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i := range req.Input {
			data[i] = item{Index: i, Embedding: []float64{1, 0, 0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "model": req.Model})
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	if _, err := b.Send(sam, SendInput{Channel: "mem", Subject: "Roses", Content: "roses are red"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "mem", Content: "violets are blue"}); err != nil {
		t.Fatal(err)
	}
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// Background passes started by each Send may overlap and re-embed a
	// row; only the set of texts matters here.
	sort.Strings(inputs)
	inputs = slices.Compact(inputs)
	if want := []string{"Roses\n\nroses are red", "violets are blue"}; !slices.Equal(inputs, want) {
		t.Fatalf("embedded texts %q, want %q", inputs, want)
	}
}

func TestReadKeyFileCommentLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(p, []byte("# OpenAI key\nexport OPENAI_API_KEY=\"example-placeholder\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := readKeyFile(p)
	if err != nil || k != "example-placeholder" {
		t.Fatalf("got %q, %v", k, err)
	}
}

func TestReadKeyFileFormats(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"bare", "abc123\n", "abc123"},
		{"bare padded with =", "abc123==\n", "abc123=="},
		{"bare crlf", "abc123\r\n", "abc123"},
		{"bare trailing comment", "abc123 # note\n", "abc123"},
		{"export bare", "export K=abc\n", "abc"},
		{"export single", "export K='abc'\n", "abc"},
		{"export double", "export K=\"abc\"\n", "abc"},
		{"export spaced", "  export   K = abc  \n", "abc"},
		{"leading comment and blanks", "# c\n\n  # indented\nexport K=abc\n\n", "abc"},
		{"comment after", "export K=abc\n# c\n", "abc"},
		{"trailing comment unquoted", "export K=abc # c\n", "abc"},
		{"trailing comment single", "export K='abc' # c\n", "abc"},
		{"trailing comment double", "export K=\"abc\"   # c\n", "abc"},
		{"hash in double", "export K=\"ab#c\" # c\n", "ab#c"},
		{"hash in single", "export K='ab#c'\n", "ab#c"},
		{"bom bare", "\ufeffabc123\n", "abc123"},
		{"bom comment", "\ufeff# c\nexport K=abc\n", "abc"},
		{"tab after export", "export\tK=abc\n", "abc"},
		{"tab around", "export K =\tabc\t# c\n", "abc"},
		{"crlf multi", "# c\r\n\r\nexport K=abc\r\n# d\r\n", "abc"},
		{"padded base64", "abc123=\n", "abc123="},
		{"quote then space hash", "export K='abc' # c\n", "abc"},
		{"hash inside token", "export K=ab#c\n", "ab#c"},
	}
	for _, c := range cases {
		p := filepath.Join(t.TempDir(), "k")
		if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		k, err := readKeyFile(p)
		if err != nil || k != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, k, err, c.want)
		}
	}
}

func TestReadKeyFileRejects(t *testing.T) {
	const secret = "sEcReT9"
	cases := map[string]string{
		"empty":                "",
		"only comments":        "# a\n  \n# b\n",
		"two lines":            "export A=" + secret + "\nexport B=" + secret + "\n",
		"bare two lines":       secret + "\n" + secret + "\n",
		"unterminated dq":      "export K=\"" + secret + "\n",
		"unterminated sq":      "export K='" + secret + "\n",
		"mismatched quotes":    "export K='" + secret + "\"\n",
		"quote in bare":        "export K=" + secret + "'\n",
		"space in quoted":      "export K=\"" + secret + " x\"\n",
		"space in bare":        secret + " x\n",
		"garbage after":        "export K='" + secret + "' x\n",
		"control char":         "export K=\"" + secret + "\x01\"\n",
		"empty value":          "export K=\n",
		"empty quoted":         "export K=''\n",
		"name=value no export": "K=" + secret + "==\n",
		"name=value simple":    "K=" + secret + "\n",
		"dq hash glued":        "export K=\"" + secret + "\"#c\n",
		"sq hash glued":        "export K='" + secret + "'# x\n",
		"format char":          "export K=" + secret + "\u200b\n",
		"bare export":          "export\n",
		"export empty hash":    "export K= # c\n",
		"lone CR file":         "# c\rexport K=" + secret + "\n",
		"tab in quoted":        "export K='" + secret + "\tx'\n",
	}
	for name, body := range cases {
		p := filepath.Join(t.TempDir(), "k")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		k, err := readKeyFile(p)
		if err == nil {
			t.Errorf("%s: accepted, key %q", name, k)
			continue
		}
		if k != "" {
			t.Errorf("%s: returned key %q with error", name, k)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaks the key: %v", name, err)
		}
		if !strings.Contains(err.Error(), p) {
			t.Errorf("%s: error does not name the file: %v", name, err)
		}
	}
}

// embedRecorder is a fake embeddings endpoint that records every input it
// receives and counts requests. Texts missing from table get {0.1,0.1,0.1}.
type embedRecorder struct {
	mu     sync.Mutex
	inputs []string
	calls  int
}

func (r *embedRecorder) snapshot() (calls int, inputs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, slices.Clone(r.inputs)
}

func recordEmbeddings(t *testing.T, table map[string][]float64) (*httptest.Server, *embedRecorder) {
	t.Helper()
	rec := &embedRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		rec.mu.Lock()
		rec.calls++
		rec.inputs = append(rec.inputs, req.Input...)
		rec.mu.Unlock()
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i, s := range req.Input {
			v, ok := table[s]
			if !ok {
				v = []float64{0.1, 0.1, 0.1}
			}
			data[i] = item{Index: i, Embedding: v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "model": req.Model})
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// quietly runs fn with the embedder detached, so writes inside it start no
// background pass and the backlog can be observed deterministically.
func quietly(b *Bus, fn func()) {
	e := b.embedder
	b.embedder = nil
	fn()
	b.embedder = e
}

// drainEmbed runs embedding passes until nothing is left.
func drainEmbed(t *testing.T, b *Bus) {
	t.Helper()
	b.waitEmbed()
	for i := 0; i < 20; i++ {
		n, err := b.embedBatch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("embedding backlog did not drain")
}

func embeddedCount(t *testing.T, b *Bus, seq int64) int {
	t.Helper()
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings WHERE seq=?", seq).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEmbedBacklogCoversAllKinds(t *testing.T) {
	srv, rec := recordEmbeddings(t, nil)
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	ann := reg(t, b, "Ann")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	list := taskList(t, b)
	quietly(b, func() {
		if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "chat line"}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Send(ann, SendInput{Channel: "dm/Sam", Content: "dm line"}); err != nil {
			t.Fatal(err)
		}
		mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Fix it", Description: "the long story"})
	})
	st, err := b.StatusReport()
	if err != nil || st.EmbeddingBacklog != 3 {
		t.Fatalf("backlog before = %d (%v), want 3", st.EmbeddingBacklog, err)
	}
	drainEmbed(t, b)
	if st, err = b.StatusReport(); err != nil || st.EmbeddingBacklog != 0 {
		t.Fatalf("backlog after = %d (%v), want 0", st.EmbeddingBacklog, err)
	}
	_, inputs := rec.snapshot()
	for _, want := range []string{"chat line", "dm line", "Fix it\n\nthe long story"} {
		if !slices.Contains(inputs, want) {
			t.Fatalf("inputs %q lack %q", inputs, want)
		}
	}
	for _, in := range inputs {
		if strings.Contains(in, `"status"`) {
			t.Fatalf("task JSON was embedded: %q", in)
		}
	}
}

func TestSemanticSearchFindsChatAndTasks(t *testing.T) {
	srv, _ := recordEmbeddings(t, map[string][]float64{
		"flowers":                {1, 0, 0},
		"roses are red":          {1, 0, 0},
		"Water\n\nroses are red": {1, 0, 0},
		"git rebase":             {0, 0, 1},
		"Other\n\nviolets":       {0, 1, 0},
	})
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	list := taskList(t, b)
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "roses are red"}); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Water", Description: "roses are red"})
	drainEmbed(t, b)
	r, err := b.Search(sam, SearchInput{Query: "flowers", Mode: "semantic"})
	if err != nil || r.SemanticUnavailable {
		t.Fatalf("%+v %v", r, err)
	}
	var chat, task bool
	for _, h := range r.Hits {
		chat = chat || h.Channel == "dev"
		task = task || h.Channel == list
	}
	if !chat || !task {
		t.Fatalf("want a chat and a task hit: %+v", r.Hits)
	}
	r, err = b.Search(sam, SearchInput{Query: "flowers", Mode: "semantic", Kinds: []string{"tasks"}})
	if err != nil || len(r.Hits) != 1 || r.Hits[0].Channel != list {
		t.Fatalf("kinds=tasks: %+v %v", r.Hits, err)
	}
}

func TestSemanticSearchDMVisibility(t *testing.T) {
	srv, _ := recordEmbeddings(t, map[string][]float64{"flowers": {1, 0, 0}, "roses are red": {1, 0, 0}})
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	ann := reg(t, b, "Ann")
	bob := reg(t, b, "Bob")
	if _, err := b.Send(ann, SendInput{Channel: "dm/Sam", Content: "roses are red"}); err != nil {
		t.Fatal(err)
	}
	drainEmbed(t, b)
	for _, mode := range []string{"semantic", "both", "text"} {
		q := "flowers"
		if mode == "text" {
			q = "roses"
		}
		r, err := b.Search(bob, SearchInput{Query: q, Mode: mode})
		if err != nil || len(r.Hits) != 0 {
			t.Fatalf("Bob mode %s saw %+v (%v)", mode, r.Hits, err)
		}
	}
	r, err := b.Search(sam, SearchInput{Query: "flowers", Mode: "semantic"})
	if err != nil || len(r.Hits) != 1 {
		t.Fatalf("Sam must see the DM: %+v %v", r.Hits, err)
	}
}

func TestTaskStatusChangeReusesEmbedding(t *testing.T) {
	srv, rec := recordEmbeddings(t, nil)
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	list := taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Fix", Description: "story"})
	drainEmbed(t, b)
	calls, _ := rec.snapshot()
	done := "completed"
	res, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Status: &done})
	if err != nil {
		t.Fatal(err)
	}
	b.waitEmbed()
	if embeddedCount(t, b, res.Task.seq) != 1 {
		t.Fatal("status-only revision must carry the copied embedding at commit")
	}
	if c, _ := rec.snapshot(); c != calls {
		t.Fatalf("server calls %d -> %d, want unchanged", calls, c)
	}
	desc := "new story"
	res, err = b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Description: &desc})
	if err != nil {
		t.Fatal(err)
	}
	b.waitEmbed()
	// The background pass may already have run; what matters is that the
	// endpoint was called for the new text and the row ends embedded.
	drainEmbed(t, b)
	if embeddedCount(t, b, res.Task.seq) != 1 {
		t.Fatal("changed description must be embedded by the backlog")
	}
	if c, in := rec.snapshot(); c <= calls || !slices.Contains(in, "Fix\n\nnew story") {
		t.Fatalf("expected a call for the new text, calls=%d inputs=%q", c, in)
	}
}

func TestTaskDescriptionChangeNotCopied(t *testing.T) {
	srv, _ := recordEmbeddings(t, nil)
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	list := taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Fix", Description: "story"})
	drainEmbed(t, b)
	desc := "new story"
	var seq int64
	quietly(b, func() {
		res, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Description: &desc})
		if err != nil {
			t.Fatal(err)
		}
		seq = res.Task.seq
	})
	if embeddedCount(t, b, seq) != 0 {
		t.Fatal("a changed description must not inherit the old vector")
	}
	drainEmbed(t, b)
	if embeddedCount(t, b, seq) != 1 {
		t.Fatal("backlog must embed the changed revision")
	}
}

func TestDeletedTaskLeavesSemantic(t *testing.T) {
	srv, _ := recordEmbeddings(t, map[string][]float64{"flowers": {1, 0, 0}, "Water\n\nroses": {1, 0, 0}})
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	ann := reg(t, b, "Ann")
	list := taskList(t, b)
	for _, force := range []bool{false, true} {
		tk := mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Water", Description: "roses"})
		drainEmbed(t, b)
		if force {
			// An in-progress task owned by someone else makes the delete a
			// forced one (marker row).
			if _, err := b.TaskClaim(ann, tk.ID, time.Now().Add(time.Hour).UnixMilli(), ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Delete: true, Force: force}); err != nil {
			t.Fatalf("force=%v: %v", force, err)
		}
		b.waitEmbed()
		r, err := b.Search(sam, SearchInput{Query: "flowers", Mode: "semantic"})
		if err != nil || len(r.Hits) != 0 {
			t.Fatalf("force=%v: deleted task still found: %+v %v", force, r.Hits, err)
		}
		// The forced marker revision is tombstoned in the same transaction
		// as the vector copy; it must not keep the copy.
		var n int
		if err := b.db.QueryRow(`SELECT count(*) FROM embeddings e JOIN messages m ON m.seq=e.seq
		  WHERE m.memory_id=? AND m.tombstone=1 AND m.type='forced'`, tk.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("force=%v: %d forced marker rows carry a vector (%v)", force, n, err)
		}
		if err := b.db.QueryRow(`SELECT count(*) FROM embeddings e JOIN messages m ON m.seq=e.seq WHERE m.memory_id=? AND m.tombstone=0`, tk.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("force=%v: live rows with vectors: %d %v", force, n, err)
		}
	}
}

func TestSendChatTriggersEmbedSoon(t *testing.T) {
	srv, _ := recordEmbeddings(t, nil)
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	list := taskList(t, b)
	r, err := b.Send(sam, SendInput{Channel: "dev", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitEmbedded(t, b, r.Seq)
	// Overlapping embedSoon calls collapse, so the task is created only
	// after the chat pass finished.
	b.waitEmbed()
	tk := mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "T", Description: "d"})
	waitEmbedded(t, b, tk.seq)
}

// waitEmbedded polls (bounded) until seq has an embedding, without calling
// the maintenance tick: embedSoon alone must get it there.
func waitEmbedded(t *testing.T, b *Bus, seq int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for embeddedCount(t, b, seq) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("seq %d was not embedded by embedSoon", seq)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSemanticFallbackForChatKind(t *testing.T) {
	srv := fakeEmbeddings(t)
	b := newEmbedBus(t, srv.URL)
	srv.Close()
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "roses are red"})
	r, err := b.Search(sam, SearchInput{Query: "roses", Mode: "both", Kinds: []string{"chat"}})
	if err != nil || !r.SemanticUnavailable || len(r.Hits) != 1 || r.Hits[0].Channel != "dev" {
		t.Fatalf("%+v %v", r, err)
	}
}

// Semantic search narrowed to chat, run as a non-participant, returns the
// ordinary-channel hit and never the DM between two other sessions.
func TestSemanticKindsChatAsNonParticipant(t *testing.T) {
	srv, _ := recordEmbeddings(t, nil) // every text gets one vector: all rows tie
	b := newEmbedBus(t, srv.URL)
	ann := reg(t, b, "Ann")
	sam := reg(t, b, "Sam")
	bob := reg(t, b, "Bob")
	_, _ = b.CreateChannel(ann, "dev", "ordinary")
	_, _ = b.CreateChannel(ann, "mem", "memory")
	list := taskList(t, b)
	for _, in := range []SendInput{
		{Channel: "dev", Content: "widget chat"},
		{Channel: "mem", Content: "widget memory"},
		{Channel: DMChannel(sam), Content: "widget secret"},
	} {
		if _, err := b.Send(ann, in); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "widget task"})
	drainEmbed(t, b)
	for _, mode := range []string{"semantic", "both"} {
		r, err := b.Search(bob, SearchInput{Query: "widget", Mode: mode, Kinds: []string{"chat"}})
		if err != nil || r.SemanticUnavailable {
			t.Fatalf("%s: %+v %v", mode, r, err)
		}
		if len(r.Hits) != 1 || r.Hits[0].Channel != "dev" {
			t.Fatalf("%s: Bob must see only the dev chat row, got %+v", mode, r.Hits)
		}
	}
}
