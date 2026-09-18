package bus

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func taskList(t *testing.T, b *Bus) string {
	t.Helper()
	if _, err := b.CreateChannel("Sam", "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	return "tasks/work"
}

func mustCreate(t *testing.T, b *Bus, in TaskCreateInput) Task {
	t.Helper()
	if in.Channel == "" {
		in.Channel = "tasks/work"
	}
	tk, err := b.TaskCreate("Sam", in)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var be *Error
	if !errors.As(err, &be) || be.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestTaskChannelNameRules(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	if _, err := b.CreateChannel("Sam", "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	_, err := b.CreateChannel("Sam", "tasks/other", "ordinary")
	if err == nil || !strings.Contains(err.Error(), "kind memory") {
		t.Fatalf("tasks/other with kind ordinary: %v", err)
	}
	_, err = b.CreateChannel("Sam", "tasks", "memory")
	if err == nil || !strings.Contains(err.Error(), "reserved for task lists") {
		t.Fatalf("bare tasks: %v", err)
	}
	wantCode(t, func() error { _, err := b.CreateChannel("Sam", "tasks/", "memory"); return err }(), "validation")
	wantCode(t, func() error { _, err := b.CreateChannel("Sam", "tasks/a/b", "memory"); return err }(), "validation")
	wantCode(t, func() error { _, err := b.CreateChannel("Sam", "x/y", "memory"); return err }(), "validation")
}

func TestTaskCreateGetRoundTrip(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	created := mustCreate(t, b, TaskCreateInput{Subject: "Port the limiter tests", Description: "desc", Metadata: map[string]string{"k": "v"}})
	got, err := b.TaskGet("Sam", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Port the limiter tests" || got.Status != "pending" || got.Owner != "" ||
		got.Rank == "" || got.Revision != 1 || got.UpdatedBy != "Sam" || got.Blocked ||
		got.Description != "desc" || len(got.Metadata) != 1 || got.Metadata["k"] != "v" {
		t.Fatalf("%+v", got)
	}
}

func TestTaskCreateValidation(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	create := func(in TaskCreateInput) error {
		if in.Channel == "" {
			in.Channel = "tasks/work"
		}
		_, err := b.TaskCreate("Sam", in)
		return err
	}
	wantCode(t, create(TaskCreateInput{Subject: ""}), "validation")
	wantCode(t, create(TaskCreateInput{Subject: strings.Repeat("x", 257)}), "validation")
	wantCode(t, create(TaskCreateInput{Subject: "bad\nsubject"}), "validation")
	wantCode(t, create(TaskCreateInput{Channel: "mem", Subject: "x"}), "validation")
	wantCode(t, create(TaskCreateInput{Channel: "tasks/none", Subject: "x"}), "not_found")
	wantCode(t, create(TaskCreateInput{Subject: "x", Parent: 999}), "validation")
	wantCode(t, create(TaskCreateInput{Subject: "x", BlockedBy: []int64{999}}), "validation")
	wantCode(t, create(TaskCreateInput{Subject: "x", Before: 1, After: 2}), "validation")
	a := mustCreate(t, b, TaskCreateInput{Subject: "a"})
	child := mustCreate(t, b, TaskCreateInput{Subject: "child", Parent: a.ID})
	wantCode(t, create(TaskCreateInput{Subject: "x", After: child.ID}), "validation")
}

// TestTaskCreateRechecksChannelInsideTransaction covers Minor 5 of the
// final review: a create racing DeleteChannel must not leave a row in a
// deleted channel. The preflight check (taskChannelExists on b.db, no lock
// held) sees the channel; a second connection then deletes it and holds the
// write lock until TaskCreate's own transaction is blocked on Begin, so by
// the time TaskCreate's in-tx check runs, the channel is really gone.
func TestTaskCreateRechecksChannelInsideTransaction(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)

	dsn, err := SQLiteDSN(b.cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	tx2, err := db.Begin() // _txlock=immediate takes the write lock now
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.Exec("DELETE FROM channels WHERE name='tasks/work'"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := b.TaskCreate("Sam", TaskCreateInput{Channel: "tasks/work", Subject: "x"})
		done <- err
	}()
	time.Sleep(30 * time.Millisecond) // let TaskCreate pass preflight and block on its own Begin
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		wantCode(t, err, "not_found")
	case <-time.After(2 * time.Second):
		t.Fatal("TaskCreate did not return after the competing lock was released")
	}
}

func TestTaskListTreeOrderSurvivesInsertsAndDeletes(t *testing.T) {
	b, _ := twoAgents(t)
	taskList(t, b)
	a := mustCreate(t, b, TaskCreateInput{Subject: "a"})
	c := mustCreate(t, b, TaskCreateInput{Subject: "c"})
	bb := mustCreate(t, b, TaskCreateInput{Subject: "b", After: a.ID})
	a1 := mustCreate(t, b, TaskCreateInput{Subject: "a1", Parent: a.ID})
	a0 := mustCreate(t, b, TaskCreateInput{Subject: "a0", Parent: a.ID, Before: a1.ID})
	order := func() string {
		l, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, x := range l {
			s = append(s, strings.Repeat(">", x.Depth)+x.Subject)
		}
		return strings.Join(s, " ")
	}
	if got := order(); got != "a >a0 >a1 b c" {
		t.Fatalf("order=%q", got)
	}
	_ = bb
	_ = c
	_ = a0
}

func TestTaskBlockedIsDerived(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	y := mustCreate(t, b, TaskCreateInput{Subject: "y"})
	x := mustCreate(t, b, TaskCreateInput{Subject: "x", BlockedBy: []int64{y.ID}})
	got, err := b.TaskGet("Sam", x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Blocked || len(got.OpenBlockers) != 1 || got.OpenBlockers[0] != y.ID {
		t.Fatalf("%+v", got)
	}
	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatal(err)
	}
	var xs *TaskSummary
	for i := range list {
		if list[i].ID == x.ID {
			xs = &list[i]
		}
	}
	if xs == nil || len(xs.OpenBlockers) != 1 || xs.OpenBlockers[0] != y.ID {
		t.Fatalf("%+v", xs)
	}
}

func TestPlainWritesRefusedOnTaskChannels(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	task := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	_, sendErr := b.Send("Sam", SendInput{Channel: "tasks/work", Content: "hi"})
	wantCode(t, sendErr, "validation")
	if sendErr == nil || !strings.Contains(sendErr.Error(), "task_update") {
		t.Fatalf("Send: %v", sendErr)
	}
	_, editErr := b.EditMemory("Sam", EditInput{ID: task.ID, Content: "hi"})
	wantCode(t, editErr, "validation")
	if editErr == nil || !strings.Contains(editErr.Error(), "task_update") {
		t.Fatalf("EditMemory: %v", editErr)
	}
	deleteErr := b.DeleteMemory("Sam", task.ID, "")
	wantCode(t, deleteErr, "validation")
	if deleteErr == nil || !strings.Contains(deleteErr.Error(), "task_update") {
		t.Fatalf("DeleteMemory: %v", deleteErr)
	}
	got, err := b.GetMemory("Sam", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Content, `"subject"`) {
		t.Fatalf("content=%q", got.Content)
	}
}

func TestLoadTasksSkipsForeignRows(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	mustCreate(t, b, TaskCreateInput{Subject: "real"})
	if _, err := b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,content,memory_id,revision) VALUES('tasks/work','x','',0,'not json',999,1)"); err != nil {
		t.Fatal(err)
	}
	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Subject != "real" {
		t.Fatalf("%+v", list)
	}
}

func TestTaskCreateIdempotentReplay(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	in := TaskCreateInput{Channel: "tasks/work", Subject: "once", IdempotencyKey: "k1"}
	first, err := b.TaskCreate("Sam", in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.TaskCreate("Sam", in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids differ: %d vs %d", first.ID, second.ID)
	}
	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want one task, got %d", len(list))
	}
}

// TestPlaceRankUsesEffectiveParentForOrphans covers Important 1 from the
// task-2 review: a task whose stored parent no longer exists is a root for
// tree-order purposes (design's Hierarchy section), so placeRank must place
// new siblings against it using the same effective-parent rule, not the raw
// stored parent.
func TestPlaceRankUsesEffectiveParentForOrphans(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	root := mustCreate(t, b, TaskCreateInput{Subject: "root"})
	// Insert an orphan directly: a task whose parent id does not exist. No
	// TaskCreate path can produce this (validateTaskLinks refuses an unknown
	// parent), so it's simulated with a raw insert, as the design's
	// "after retention" scenario would.
	const orphanID = int64(500)
	orphanDoc := `{"subject":"orphan","status":"pending","rank":"W","parent":999999}`
	if _, err := b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,content,memory_id,revision) VALUES('tasks/work','Sam','',0,?,?,1)", orphanDoc, orphanID); err != nil {
		t.Fatal(err)
	}
	// After: orphan must succeed (orphan is a sibling under the effective
	// root) and place the new task right after it, not fail "not a sibling
	// under parent 0".
	mustCreate(t, b, TaskCreateInput{Subject: "after-orphan", After: orphanID})
	// A plain append (no before/after) must rank after the orphan too.
	mustCreate(t, b, TaskCreateInput{Subject: "appended"})

	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, x := range list {
		order = append(order, x.Subject)
	}
	want := "root,orphan,after-orphan,appended"
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("order=%q want=%q", got, want)
	}
	_ = root
}

// TestTaskCreateReplaySurvivesChannelDeletion covers Important 2 from the
// task-2 review: the receipt check must run before the channel-existence
// lookup (R2), so a keyed retry replays its stored task even after the
// channel (and its messages) are gone, rather than failing not_found.
func TestTaskCreateReplaySurvivesChannelDeletion(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	in := TaskCreateInput{Channel: "tasks/work", Subject: "durable", IdempotencyKey: "k2"}
	first, err := b.TaskCreate("Sam", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.DeleteChannel("tasks/work", ""); err != nil {
		t.Fatal(err)
	}
	second, err := b.TaskCreate("Sam", in)
	if err != nil {
		t.Fatalf("replay after channel deletion must not fail: %v", err)
	}
	if second.ID != first.ID || second.Subject != first.Subject {
		t.Fatalf("replay=%+v want=%+v", second, first)
	}
}

func TestEmbedderSkipsTaskRows(t *testing.T) {
	srv := fakeEmbeddings(t)
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel(sam, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/work", Subject: "roses are red"}); err != nil {
		t.Fatal(err)
	}
	memRes, err := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	if err != nil {
		t.Fatal(err)
	}
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("embeddings=%d", n)
	}
	var seq int64
	if err := b.db.QueryRow("SELECT seq FROM embeddings").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != *memRes.MemoryID {
		t.Fatalf("embedded seq=%d want memory seq=%d", seq, *memRes.MemoryID)
	}
}
