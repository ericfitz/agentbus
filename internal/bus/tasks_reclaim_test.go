package bus

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/config"
)

func TestClaimOfTaskWithDeadOwnerSucceeds(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	res, err := b.TaskClaim("Sam", tk.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Owner != "Sam" || res.Task.Status != "in_progress" {
		t.Fatalf("%+v", res.Task)
	}

	revs, err := b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, r := range revs {
		types = append(types, r.Type)
	}
	want := []string{"", "", "reclaimed", ""}
	if len(types) != len(want) {
		t.Fatalf("types=%v want=%v", types, want)
	}
	for i, w := range want {
		if types[i] != w {
			t.Fatalf("types=%v want=%v", types, want)
		}
	}
	// T4-1 (final review): Replaced must be the seq of the revision the
	// claim actually replaced, the reclaim's, not the dead owner's earlier
	// claim two revisions back.
	if res.Replaced != revs[2].Seq {
		t.Fatalf("replaced=%d, want the reclaim's seq %d", res.Replaced, revs[2].Seq)
	}
}

func TestClaimOfTaskWithExpiredLeaseSucceeds(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	lease := b.nowMs() + 1000
	if _, err := other.TaskClaim("Pat", tk.ID, lease, ""); err != nil {
		t.Fatal(err)
	}

	// Before the clock advances, the lease is still live: Sam's claim conflicts.
	_, err := b.TaskClaim("Sam", tk.ID, 0, "")
	wantCode(t, err, "conflict")

	advanced := time.UnixMilli(lease + 2000)
	b.Now = func() time.Time { return advanced }
	other.Now = func() time.Time { return advanced }

	res, err := b.TaskClaim("Sam", tk.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Owner != "Sam" {
		t.Fatalf("%+v", res.Task)
	}
}

func TestGetFixesUpAbandonedTask(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	got, err := b.TaskGet("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.Owner != "" {
		t.Fatalf("%+v", got)
	}

	revs, err := b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := revs[len(revs)-1]
	if last.Type != "reclaimed" || last.Sender != "Sam" {
		t.Fatalf("last revision=%+v", last)
	}
}

func TestListFixesUpAbandonedTasks(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != "pending" || list[0].Owner != "" {
		t.Fatalf("%+v", list)
	}

	revs, err := b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := revs[len(revs)-1]
	if last.Type != "reclaimed" || last.Sender != "Sam" {
		t.Fatalf("last revision=%+v", last)
	}
}

func TestReadsDoNotWriteWhenNothingIsAbandoned(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	mustCreate(t, b, TaskCreateInput{Subject: "x"})

	var before int64
	if err := b.db.QueryRow("SELECT max(seq) FROM messages").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"}); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := b.db.QueryRow("SELECT max(seq) FROM messages").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("read-only list wrote a row: max(seq) %d -> %d", before, after)
	}
}

func TestTickReclaimsWithEmptySender(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	if err := b.Subscribe("Sam", "tasks/work", "now"); err != nil {
		t.Fatal(err)
	}
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	b.Tick(context.Background())

	revs, err := b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := revs[len(revs)-1]
	if last.Type != "reclaimed" || last.Sender != "" {
		t.Fatalf("last revision=%+v", last)
	}

	r, err := b.Receive("Sam", ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range r.Messages {
		if m.Type == "reclaimed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Sam did not receive the reclaim without include_own: %+v", r.Messages)
	}
}

// The tick sweeps the machine-wide tasks list too, not only tasks/<repo> (#14).
func TestTickReclaimsOnBareTasksList(t *testing.T) {
	b, other := twoAgents(t)
	// The bare "tasks" list is a default channel ensureDefaults already
	// created; CreateChannel unconditionally rejects the reserved name
	// "tasks" (resolveKind), so there's nothing to create here.
	tk := mustCreate(t, b, TaskCreateInput{Channel: "tasks", Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	b.Tick(context.Background())

	revs, err := b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := revs[len(revs)-1]; last.Type != "reclaimed" {
		t.Fatalf("tick did not reclaim on tasks: last revision %+v", last)
	}
}

func TestReclaimIsNotRateCharged(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	// Exhaust Sam's bucket with a plain send, using the smallest rate
	// limit the config allows.
	b.cfg.SendMessagesPerSecond = 1
	b.limits = newLimiter(b.cfg)
	if _, err := b.CreateChannel("Sam", "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("Sam", SendInput{Channel: "dev", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("Sam", SendInput{Channel: "dev", Content: "y"}); err == nil {
		t.Fatal("bucket should be empty")
	}

	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != "pending" {
		t.Fatalf("reclaim was blocked by the exhausted rate limit: %+v", list)
	}
}

func TestAnyoneCanReleaseAbandonedTask(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	res, err := b.TaskRelease("Sam", tk.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Status != "pending" || res.Task.Owner != "" {
		t.Fatalf("%+v", res.Task)
	}
}

func TestLiveOwnerWithoutLeaseIsNeverAbandoned(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := b.TaskClaim("Sam", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}

	got, err := b.TaskGet("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "in_progress" || got.Owner != "Sam" {
		t.Fatalf("%+v", got)
	}
	if got.Revision != 2 {
		t.Fatalf("unexpected reclaim wrote a revision: %+v", got)
	}
}

// TestKeyedClaimOverDeadOwnerIsIdempotent covers T4-2 of the final review:
// a keyed claim over a dead owner reclaims once, then a retry with the same
// key must replay the stored result rather than reclaiming (and charging)
// again.
func TestKeyedClaimOverDeadOwnerIsIdempotent(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	first, err := b.TaskClaim("Sam", tk.ID, 0, "claim-1")
	if err != nil {
		t.Fatal(err)
	}
	revs, err := b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := len(revs)

	second, err := b.TaskClaim("Sam", tk.ID, 0, "claim-1")
	if err != nil {
		t.Fatal(err)
	}
	// Compare the JSON shape, not the Go struct: Task.seq is an unexported
	// bookkeeping field (the live revision's seq) that a stored receipt
	// replay does not reconstruct, and callers never see it either.
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("replay = %s, want identical to first %s", secondJSON, firstJSON)
	}
	revs, err = b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != n {
		t.Fatalf("replay wrote a revision: %d -> %d", n, len(revs))
	}
}

// TestGetAndListReturnSnapshotWhenFixUpFails covers Important 1 of the
// final review: a fix-up error other than not_registered must not fail the
// read. fixUpBegin is overridden so the failure is injected deterministically
// instead of waiting on the real 5s busy_timeout. TaskGet and TaskList must
// both succeed, still show the task in_progress under its abandoned owner
// (the snapshot loaded before the failed fix-up), and log the failure.
func TestGetAndListReturnSnapshotWhenFixUpFails(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	var buf bytes.Buffer
	b, err := Open(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	other, err := Open(b.cfg, b.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })

	reg(t, b, "Sam")
	if _, err := other.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := other.TaskClaim("Pat", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}

	orig := fixUpBegin
	fixUpBegin = func(*sql.DB) (*sql.Tx, error) { return nil, errors.New("write lock unavailable") }
	t.Cleanup(func() { fixUpBegin = orig })

	got, err := b.TaskGet("Sam", tk.ID)
	if err != nil {
		t.Fatalf("TaskGet must succeed despite the fix-up failure: %v", err)
	}
	if got.Status != "in_progress" || got.Owner != "Pat" {
		t.Fatalf("TaskGet must return the pre-fix-up snapshot: %+v", got)
	}
	if !strings.Contains(buf.String(), "task fix-up failed") {
		t.Fatalf("fix-up failure not logged: %s", buf.String())
	}
	buf.Reset()

	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatalf("TaskList must succeed despite the fix-up failure: %v", err)
	}
	if len(list) != 1 || list[0].Status != "in_progress" || list[0].Owner != "Pat" {
		t.Fatalf("TaskList must return the pre-fix-up snapshot: %+v", list)
	}
	if !strings.Contains(buf.String(), "task fix-up failed") {
		t.Fatalf("fix-up failure not logged: %s", buf.String())
	}
}
