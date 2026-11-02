package bus

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// capHandler is a slog handler that keeps every record it is given.
type capHandler struct {
	mu   *sync.Mutex
	recs *[]slog.Record
}

func newCapHandler() *capHandler { return &capHandler{mu: &sync.Mutex{}, recs: &[]slog.Record{}} }

func (h *capHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.recs = append(*h.recs, r.Clone())
	return nil
}
func (h *capHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capHandler) WithGroup(string) slog.Handler      { return h }

func (h *capHandler) records() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), *h.recs...)
}

// dump renders every record as "msg k=v ..." lines, for substring checks.
func (h *capHandler) dump() string {
	var sb strings.Builder
	for _, r := range h.records() {
		sb.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value.Any())
			return true
		})
		sb.WriteByte('\n')
	}
	return sb.String()
}

func attrOf(r slog.Record, key string) (slog.Value, bool) {
	var v slog.Value
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v, ok = a.Value, true
			return false
		}
		return true
	})
	return v, ok
}

// failingEmbedBus is an embedding bus whose endpoint answers every request
// with status and body, logging to the returned capture handler. The test
// holds embedMu so the post-write passes stay out of the way.
func failingEmbedBus(t *testing.T, status int, body string, endpointSuffix string) (*Bus, *capHandler, []int64) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	b := newEmbedBus(t, srv.URL+endpointSuffix)
	h := newCapHandler()
	b.log = slog.New(h)
	b.embedMu.Lock()
	t.Cleanup(b.embedMu.Unlock)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	for _, c := range []string{"one", "two", "three"} {
		if _, err := b.Send(sam, SendInput{Channel: "mem", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	var seqs []int64
	rows, err := b.db.Query("SELECT seq FROM messages WHERE memory_id IS NOT NULL ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, s)
	}
	return b, h, seqs
}

func TestEmbedFailureLogsDiagnostics(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			b, h, seqs := failingEmbedBus(t, status, `{"error":"model overloaded"}`, "/v1/embeddings")
			if _, err := b.embedBatch(context.Background()); err == nil {
				t.Fatal("want an error")
			}
			recs := h.records()
			if len(recs) != 1 {
				t.Fatalf("want 1 log record, got %d:\n%s", len(recs), h.dump())
			}
			r := recs[0]
			if r.Level != slog.LevelWarn {
				t.Errorf("level %v", r.Level)
			}
			get := func(k string) string {
				v, ok := attrOf(r, k)
				if !ok {
					t.Fatalf("missing attr %q:\n%s", k, h.dump())
				}
				return fmt.Sprint(v.Any())
			}
			if !strings.HasSuffix(get("endpoint"), "/v1/embeddings") {
				t.Errorf("endpoint=%s", get("endpoint"))
			}
			if get("model") != "fake-1" || get("status") != fmt.Sprint(status) || get("batch_size") != "3" {
				t.Errorf("fields wrong:\n%s", h.dump())
			}
			if !strings.Contains(get("body"), "model overloaded") {
				t.Errorf("body=%s", get("body"))
			}
			want := fmt.Sprint(seqs)
			if got := get("row_ids"); got != want {
				t.Errorf("row_ids=%s want %s", got, want)
			}
		})
	}
}

func TestEmbedFailureLogNeverHasKeyAndBoundsBody(t *testing.T) {
	// The key (sk-test) is echoed by the server straddling the 300-byte excerpt
	// limit, and again later; neither may survive into the log or the status notice.
	body := strings.Repeat("x", 296) + "sk-test" + strings.Repeat("y", 5000) + " sk-test"
	b, h, _ := failingEmbedBus(t, http.StatusUnauthorized, body, "")
	_, _ = b.embedBatch(context.Background())
	out := h.dump()
	if strings.Contains(out, "sk-") || strings.Contains(out, "Bearer") || strings.Contains(strings.ToLower(out), "authorization") {
		t.Fatalf("secret leaked into log:\n%s", out)
	}
	v, ok := attrOf(h.records()[0], "body")
	if !ok || len(v.String()) == 0 || len(v.String()) > 300 {
		t.Fatalf("body excerpt len %d ok=%v", len(v.String()), ok)
	}
	var notice string
	_ = b.db.QueryRow("SELECT message FROM notices WHERE kind='embedding'").Scan(&notice)
	if strings.Contains(notice, "sk-test") {
		t.Fatalf("key in notice: %s", notice)
	}
}

func TestEmbedFailureLogRedactsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 500) }))
	t.Cleanup(srv.Close)
	ep := strings.Replace(srv.URL, "http://", "http://bob:hunter2@", 1) + "/v1/e?api_key=QUERYSECRET&x=1"
	b := newEmbedBus(t, ep)
	h := newCapHandler()
	b.log = slog.New(h)
	b.embedMu.Lock()
	defer b.embedMu.Unlock()
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	_, _ = b.Send(sam, SendInput{Channel: "mem", Content: "one"})
	_, _ = b.embedBatch(context.Background())
	// A refused connection puts the URL in the Go error text too.
	srv.Close()
	b.Now = func() time.Time { return time.Now().Add(2 * failLogInterval) }
	_, _ = b.embedBatch(context.Background())
	out := h.dump()
	if len(h.records()) < 2 {
		t.Fatalf("want 2 records:\n%s", out)
	}
	for _, s := range []string{"hunter2", "QUERYSECRET", "bob"} {
		if strings.Contains(out, s) {
			t.Errorf("%q leaked:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "/v1/e") {
		t.Errorf("endpoint path missing:\n%s", out)
	}
}

func TestEmbedFailureLogRateLimited(t *testing.T) {
	b, h, _ := failingEmbedBus(t, http.StatusInternalServerError, "boom", "")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	b.Now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		_, _ = b.embedBatch(context.Background())
	}
	if n := len(h.records()); n != 1 {
		t.Fatalf("5 identical failures logged %d records, want 1:\n%s", n, h.dump())
	}
	now = now.Add(failLogInterval + time.Second)
	_, _ = b.embedBatch(context.Background())
	recs := h.records()
	if len(recs) != 2 {
		t.Fatalf("after the interval want 2 records, got %d", len(recs))
	}
	if v, ok := attrOf(recs[1], "suppressed"); !ok || v.Int64() != 4 {
		t.Fatalf("summary must report 4 suppressed, got %v ok=%v:\n%s", v, ok, h.dump())
	}
	if v, ok := attrOf(recs[0], "suppressed"); ok {
		t.Fatalf("first record must not carry suppressed=%v", v)
	}
}

func TestWarnLimitedKeysAreIndependent(t *testing.T) {
	b := newTestBus(t)
	h := newCapHandler()
	b.log = slog.New(h)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	b.Now = func() time.Time { return now }
	b.warnLimited("a", "m")
	b.warnLimited("b", "m")
	b.warnLimited("a", "m")
	if n := len(h.records()); n != 2 {
		t.Fatalf("records=%d, want 2", n)
	}
	// The map stays bounded.
	for i := 0; i < 3*failLogMaxKeys; i++ {
		b.warnLimited(fmt.Sprint("k", i), "m")
	}
	if n := len(b.failLog.seen); n > failLogMaxKeys {
		t.Fatalf("limiter holds %d keys, cap %d", n, failLogMaxKeys)
	}
}

func TestMaintenanceStepFailureLoggedWithContextAndLimited(t *testing.T) {
	b := newTestBus(t)
	h := newCapHandler()
	b.log = slog.New(h)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	b.Now = func() time.Time { return now }
	err := fmt.Errorf("disk on fire")
	for i := 0; i < 3; i++ {
		b.logStepFailure("vacuum", err, 12*time.Millisecond)
	}
	recs := h.records()
	if len(recs) != 1 {
		t.Fatalf("records=%d:\n%s", len(recs), h.dump())
	}
	if v, ok := attrOf(recs[0], "step"); !ok || v.String() != "vacuum" {
		t.Fatalf("step attr: %v", h.dump())
	}
	if _, ok := attrOf(recs[0], "elapsed_ms"); !ok {
		t.Fatalf("elapsed_ms missing:\n%s", h.dump())
	}
}
