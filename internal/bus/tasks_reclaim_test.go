package bus

import (
	"context"
	"testing"
	"time"
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
