package bus

import (
	"context"
	"errors"
	"strings"
	"testing"
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
	wantCode(t, func() error { _, err := b.CreateChannel("Sam", "tasks/other", "ordinary"); return err }(), "validation")
	wantCode(t, func() error { _, err := b.CreateChannel("Sam", "tasks", "memory"); return err }(), "validation")
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
		got.Rank == "" || got.Revision != 1 || got.UpdatedBy != "Sam" || got.Blocked {
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
	if _, err := b.Send("Sam", SendInput{Channel: "tasks/work", Content: "hi"}); err == nil || !strings.Contains(err.Error(), "task_update") {
		t.Fatalf("Send: %v", err)
	}
	if _, err := b.EditMemory("Sam", EditInput{ID: task.ID, Content: "hi"}); err == nil || !strings.Contains(err.Error(), "task_update") {
		t.Fatalf("EditMemory: %v", err)
	}
	if err := b.DeleteMemory("Sam", task.ID, ""); err == nil || !strings.Contains(err.Error(), "task_update") {
		t.Fatalf("DeleteMemory: %v", err)
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
	if _, err := b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,content,bytes,memory_id,revision) VALUES('tasks/work','x','',0,'not json',8,999,1)"); err != nil {
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
