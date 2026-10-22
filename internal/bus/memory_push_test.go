package bus

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ADR 0025 decision 7: agents never receive memory channels.

// leftoverMemorySub inserts the subscription an older version left behind,
// bypassing Subscribe's refusal.
func leftoverMemorySub(t *testing.T, b *Bus, as, channel string) {
	t.Helper()
	if _, err := b.db.Exec("INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,0,?)", as, channel, b.nowMs()); err != nil {
		t.Fatal(err)
	}
}

func TestSubscribeRefusesMemoryChannel(t *testing.T) {
	b, other := twoAgents(t)
	_ = other
	err := b.Subscribe("Sam", "memory", "now")
	be, ok := err.(*Error)
	if !ok || be.Code != "validation" || !strings.Contains(be.Message, "use search") {
		t.Fatalf("want validation error pointing at search, got %v", err)
	}
	if _, err := b.CreateChannel("Sam", "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("Sam", "mem", "now"); err == nil {
		t.Fatal("an unprefixed memory-kind channel must be refused too")
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender='Sam' AND channel IN ('memory','mem')").Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused subscribe must not write a row: %d %v", n, err)
	}
}

func TestSubscribeStillAllowsTaskList(t *testing.T) {
	b, _ := twoAgents(t)
	if _, err := b.CreateChannel("Sam", "tasks/x", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("Sam", "tasks/x", "now"); err != nil {
		t.Fatal(err)
	}
}

func TestObserverMayStillSubscribeAndReceiveMemory(t *testing.T) {
	b, other := twoAgents(t)
	b.SetObserver("Sam")
	if err := b.Subscribe("Sam", "memory", "now"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Send("Pat", SendInput{Channel: "memory", Content: "a fact"}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Receive("Sam", ReceiveInput{})
	if err != nil || len(r.Messages) != 1 {
		t.Fatalf("observer must receive memory: %+v %v", r, err)
	}
}

func TestLeftoverMemorySubscriptionDeliversNothing(t *testing.T) {
	b, other := twoAgents(t)
	leftoverMemorySub(t, b, "Sam", "memory")
	if _, err := other.Send("Pat", SendInput{Channel: "memory", Content: "a fact"}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Receive("Sam", ReceiveInput{})
	if err != nil || len(r.Messages) != 0 {
		t.Fatalf("leftover memory subscription must deliver nothing: %+v %v", r, err)
	}
	got, err := b.Wait(context.Background(), "Sam", nil, false, nil, 50*time.Millisecond)
	if err != nil || len(got) != 0 {
		t.Fatalf("leftover memory subscription must not wake Wait: %v %v", got, err)
	}
	if got, _, err := b.WaitAbove(waitCtx(t), "Sam", nil, false, nil, 0); err == nil {
		t.Fatalf("WaitAbove must not wake on memory: %v", got)
	}
}

func waitCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestLeftoverMemorySubscriptionKeepsTaskListDelivering(t *testing.T) {
	b, other := twoAgents(t)
	if _, err := other.CreateChannel("Pat", "tasks/x", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("Sam", "tasks/x", "now"); err != nil {
		t.Fatal(err)
	}
	leftoverMemorySub(t, b, "Sam", "memory")
	if _, err := other.Send("Pat", SendInput{Channel: "memory", Content: "a fact"}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.TaskCreate("Pat", TaskCreateInput{Channel: "tasks/x", Subject: "a task", Description: "d"}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Receive("Sam", ReceiveInput{})
	if err != nil || len(r.Messages) != 1 || r.Messages[0].Channel != "tasks/x" {
		t.Fatalf("only the task list may deliver: %+v %v", r, err)
	}
}
