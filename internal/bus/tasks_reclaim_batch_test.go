package bus

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestTickTakesNoWriteTxWhenNothingAbandoned: the maintenance tick checks
// each task channel read-only and only opens a write transaction for one
// that has something to reclaim. tickBegin counts the write transactions.
func TestTickTakesNoWriteTxWhenNothingAbandoned(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	live := mustCreate(t, b, TaskCreateInput{Subject: "live"})
	mustCreate(t, b, TaskCreateInput{Subject: "pending"})
	if _, err := other.TaskClaim("Pat", live.ID, 0, ""); err != nil {
		t.Fatal(err)
	}

	begins := 0
	orig := tickBegin
	tickBegin = func(db *sql.DB) (*sql.Tx, error) { begins++; return db.Begin() }
	t.Cleanup(func() { tickBegin = orig })

	if err := b.reclaimAllAbandonedTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if begins != 0 {
		t.Fatalf("tick opened %d write transactions with nothing abandoned", begins)
	}

	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}
	if err := b.reclaimAllAbandonedTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if begins != 1 {
		t.Fatalf("tick opened %d write transactions, want 1", begins)
	}
	got, err := b.TaskGet("Sam", live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.Owner != "" {
		t.Fatalf("abandoned task not reclaimed: %+v", got)
	}
}

// TestTaskListReclaimsOnlyAbandonedAmongMixedOwners pins the semantics of the
// batched liveness lookup: a task whose owner has a live session is left
// alone, one whose owner has no session row (or whose lease expired) is
// reclaimed, and pending tasks are never touched.
func TestTaskListReclaimsOnlyAbandonedAmongMixedOwners(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	pat := mustCreate(t, b, TaskCreateInput{Subject: "pat-live"})
	sam := mustCreate(t, b, TaskCreateInput{Subject: "sam-live"})
	ghost := mustCreate(t, b, TaskCreateInput{Subject: "ghost"})
	expired := mustCreate(t, b, TaskCreateInput{Subject: "expired"})
	mustCreate(t, b, TaskCreateInput{Subject: "pending"})
	if _, err := other.TaskClaim("Pat", pat.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskClaim("Sam", sam.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := other.TaskClaim("Pat", ghost.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskClaim("Sam", expired.ID, b.nowMs()+1000, ""); err != nil {
		t.Fatal(err)
	}
	// Rewrite ghost's owner to one with no session row.
	if _, err := b.db.Exec(`UPDATE messages SET content=json_set(content,'$.owner','Ghost') WHERE memory_id=? AND tombstone=0`, ghost.ID); err != nil {
		t.Fatal(err)
	}
	advanced := time.UnixMilli(b.nowMs() + 2000)
	b.Now = func() time.Time { return advanced }

	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range list {
		got[x.Subject] = x.Status + "/" + x.Owner
	}
	want := map[string]string{
		"pat-live": "in_progress/Pat", "sam-live": "in_progress/Sam",
		"ghost": "pending/", "expired": "pending/", "pending": "pending/",
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("%s: got %q want %q (all: %v)", k, got[k], w, got)
		}
	}
}
