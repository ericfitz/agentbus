package bus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// deadTaskVectors counts vectors and rejection records held by tombstoned
// rows: embeddings hold live revisions only (as for memories).
func deadTaskVectors(t *testing.T, b *Bus) (vectors, failures int) {
	t.Helper()
	if err := b.db.QueryRow(`SELECT count(*) FROM embeddings e JOIN messages m ON m.seq=e.seq WHERE m.tombstone=1`).Scan(&vectors); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow(`SELECT count(*) FROM embed_failures e JOIN messages m ON m.seq=e.seq WHERE m.tombstone=1`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	return vectors, failures
}

// Finding 5: a tombstoned task revision keeps no vector on any branch of
// writeTaskRevision (unchanged text: moved; changed text and deletes:
// dropped), including the forced delete.
func TestTombstonedTaskRevisionKeepsNoVector(t *testing.T) {
	srv, _ := recordEmbeddings(t, nil)
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	ann := reg(t, b, "Ann")
	list := taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Fix", Description: "story"})
	drainEmbed(t, b)

	check := func(step string) {
		t.Helper()
		b.waitEmbed()
		if v, f := deadTaskVectors(t, b); v != 0 || f != 0 {
			t.Fatalf("%s: tombstoned rows hold %d vectors and %d failure records", step, v, f)
		}
	}

	// Unchanged text: the vector moves to the new revision.
	done := "in_progress"
	res, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Status: &done, Owner: &sam})
	if err != nil {
		t.Fatal(err)
	}
	if embeddedCount(t, b, res.Task.seq) != 1 {
		t.Fatal("unchanged text: the new revision must hold the moved vector")
	}
	check("status change")

	// Changed text: the old vector is dropped.
	quietly(b, func() {
		desc := "new story"
		if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Description: &desc}); err != nil {
			t.Fatal(err)
		}
	})
	check("description change")
	drainEmbed(t, b)

	// Ordinary delete.
	tk2 := mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Other", Description: "x"})
	drainEmbed(t, b)
	if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk2.ID, Delete: true}); err != nil {
		t.Fatal(err)
	}
	check("delete")

	// Forced delete of a task another session owns.
	tk3 := mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Third", Description: "y"})
	drainEmbed(t, b)
	if _, err := b.TaskClaim(ann, tk3.ID, time.Now().Add(time.Hour).UnixMilli(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk3.ID, Delete: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	check("forced delete")
}

// Finding 6: a task whose text the endpoint rejects keeps its rejection
// record across claims and releases, so the text is not sent again.
func TestRejectedTaskTextIsNotResentOnClaim(t *testing.T) {
	var badInputs atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var data []map[string]any
		for i, s := range req.Input {
			if strings.Contains(s, "BAD") {
				badInputs.Add(1)
				http.Error(w, `{"error":{"message":"input is too long"}}`, http.StatusBadRequest)
				return
			}
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	list := taskList(t, b)
	// A good row proves the endpoint works, so the bad one is recorded.
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "good"}); err != nil {
		t.Fatal(err)
	}
	tk := mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "BAD", Description: "huge"})
	drainEmbed(t, b)
	st, err := b.StatusReport()
	if err != nil || st.EmbeddingRejected != 1 {
		t.Fatalf("rejected=%d %v, want 1", st.EmbeddingRejected, err)
	}
	sent := badInputs.Load()

	inProgress, pending := "in_progress", "pending"
	none := ""
	if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Status: &inProgress, Owner: &sam}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Status: &pending, Owner: &none}); err != nil {
		t.Fatal(err)
	}
	drainEmbed(t, b)
	if got := badInputs.Load(); got != sent {
		t.Fatalf("rejected text was sent %d more times after claim and release", got-sent)
	}
	if st, err = b.StatusReport(); err != nil || st.EmbeddingRejected != 1 || st.EmbeddingBacklog != 0 {
		t.Fatalf("rejected=%d backlog=%d %v, want 1 and 0", st.EmbeddingRejected, st.EmbeddingBacklog, err)
	}
	if v, f := deadTaskVectors(t, b); v != 0 || f != 0 {
		t.Fatalf("tombstoned rows hold %d vectors and %d failure records", v, f)
	}
}

// Finding 9: a reclaim followed by a no-op commit must start an embedding
// pass. The task was never embedded, so the reclaimed revision inherits no
// vector and only embedSoon at the no-op commit can embed it before a tick.
func TestReclaimOnNoOpUpdateTriggersEmbedSoon(t *testing.T) {
	srv, _ := recordEmbeddings(t, nil)
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	pat := reg(t, b, "Pat")
	list := taskList(t, b)
	var tk Task
	quietly(b, func() {
		tk = mustCreate(t, b, TaskCreateInput{Channel: list, Subject: "Stuck", Description: "d"})
		if _, err := b.TaskClaim(pat, tk.ID, 0, ""); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender=?", pat); err != nil {
		t.Fatal(err)
	}
	pending := "pending"
	res, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Status: &pending})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced != 0 {
		t.Fatalf("the patch must be a no-op after the reclaim, got %+v", res)
	}
	var seq int64
	if err := b.db.QueryRow("SELECT seq FROM messages WHERE memory_id=? AND tombstone=0", tk.ID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	waitEmbedded(t, b, seq)
}

// Finding 7: a missing old row counts as "changed" for vector reuse; it must
// not fail the write with an internal error.
func TestWriteTaskRevisionMissingOldRowIsChanged(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	list := taskList(t, b)
	tx, err := b.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	got, err := b.writeTaskRevision(tx, "", "", Task{ID: 1, Channel: list, Subject: "s", Status: "pending", Revision: 1, seq: 999999}, "")
	if err != nil {
		t.Fatalf("a missing old row must read as changed, got %v", err)
	}
	if got.Revision != 2 || got.seq == 999999 {
		t.Fatalf("revision not written: %+v", got)
	}
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM embeddings WHERE seq=?", got.seq).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the new revision must not inherit a vector: %d %v", n, err)
	}
}
