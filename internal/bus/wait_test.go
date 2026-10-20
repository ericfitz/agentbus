package bus

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWaitReturnsUndeliveredWithoutAdvancingCursor(t *testing.T) {
	b, sam, kim := setupTwo(t)
	if got, err := b.Wait(context.Background(), kim, nil, false, nil, 10*time.Millisecond); err != nil || len(got) != 0 {
		t.Fatalf("timeout must return nothing: %v %v", got, err)
	}
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "hello"})
	_, _ = b.Send(kim, SendInput{Channel: "dev", Content: "own"})
	got, err := b.Wait(context.Background(), kim, nil, false, nil, time.Second)
	if err != nil || len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("%v %v", got, err)
	}
	if again, _ := b.Wait(context.Background(), kim, nil, false, nil, 0); len(again) != 1 {
		t.Fatalf("wait must not advance the cursor: %v", again)
	}
	own, _ := b.Wait(context.Background(), kim, nil, true, nil, 0)
	if len(own) != 2 {
		t.Fatalf("include_own: %v", own)
	}
	r, _ := b.Receive(kim, ReceiveInput{})
	if len(r.Messages) != 1 || r.Messages[0].Content != "hello" {
		t.Fatalf("receive must still deliver the same batch: %+v", r)
	}
	if got, err := b.Wait(context.Background(), kim, []string{"nope"}, false, nil, 0); err == nil {
		t.Fatalf("unsubscribed channel filter must error: %v", got)
	}
	if got, err := b.Wait(context.Background(), "nobody", nil, false, nil, 0); err == nil {
		t.Fatalf("unknown identity must error: %v", got)
	}
}

// A batch receive handed out but the agent has not acked yet must not wake
// the waiter (or the Stop hook) again; only later messages do.
func TestWaitSkipsPendingBatch(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "seen"})
	if r, _ := b.Receive(kim, ReceiveInput{}); len(r.Messages) != 1 || r.Batch == "" {
		t.Fatalf("receive: %+v", r)
	}
	if got, err := b.Wait(context.Background(), kim, nil, false, nil, 10*time.Millisecond); err != nil || len(got) != 0 {
		t.Fatalf("pending batch woke the waiter: %v %v", got, err)
	}
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "new"})
	if got, err := b.Wait(context.Background(), kim, nil, false, nil, time.Second); err != nil || len(got) != 1 || got[0].Content != "new" {
		t.Fatalf("want only the new message: %v %v", got, err)
	}
}

func TestWaitMatchSkipsRejectedMessages(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "noise"})
	at := func(m Message) bool { return strings.Contains(m.Content, "@kim") }
	if got, err := b.Wait(context.Background(), kim, nil, false, at, 10*time.Millisecond); err != nil || len(got) != 0 {
		t.Fatalf("no match must time out: %v %v", got, err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "@kim ping"})
	}()
	got, err := b.Wait(context.Background(), kim, nil, false, at, 2*time.Second)
	if err != nil || len(got) != 1 || got[0].Content != "@kim ping" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestWaitAboveFloorSkipsAnnouncedAndRejected(t *testing.T) {
	old := WaitPollInterval
	WaitPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { WaitPollInterval = old })
	b, sam, kim := setupTwo(t)
	m1, _ := b.Send(sam, SendInput{Channel: "dev", Content: "one"})
	got, floor, err := b.WaitAbove(context.Background(), kim, nil, false, nil, 0)
	if err != nil || len(got) != 1 || got[0].Seq != m1.Seq || floor != m1.Seq {
		t.Fatalf("first: %v floor %d err %v", got, floor, err)
	}
	// Nothing new above the floor, though the message is still undelivered.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if got, _, err := b.WaitAbove(ctx, kim, nil, false, nil, floor); err != context.DeadlineExceeded || len(got) != 0 {
		t.Fatalf("announced message came back: %v %v", got, err)
	}
	// A rejected message raises the floor too, and a later match wakes.
	at := func(m Message) bool { return strings.Contains(m.Content, "@kim") }
	m2, _ := b.Send(sam, SendInput{Channel: "dev", Content: "noise"})
	go func() {
		time.Sleep(40 * time.Millisecond)
		_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "@kim ping"})
	}()
	got, floor2, err := b.WaitAbove(context.Background(), kim, nil, false, at, floor)
	if err != nil || len(got) != 1 || got[0].Content != "@kim ping" || floor2 <= m2.Seq {
		t.Fatalf("second: %v floor %d err %v", got, floor2, err)
	}
}

func TestWaitAboveSkipsPendingBatch(t *testing.T) {
	old := WaitPollInterval
	WaitPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { WaitPollInterval = old })
	b, sam, kim := setupTwo(t)
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "seen"})
	if r, _ := b.Receive(kim, ReceiveInput{}); len(r.Messages) != 1 || r.Batch == "" {
		t.Fatalf("receive: %+v", r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if got, _, err := b.WaitAbove(ctx, kim, nil, false, nil, 0); err != context.DeadlineExceeded || len(got) != 0 {
		t.Fatalf("pending batch woke: %v %v", got, err)
	}
}

// A peek truncated at ReceiveMaxCount must not lose the messages past the cut:
// the floor rises only to the highest seq returned.
func TestWaitAboveTruncatedBatchKeepsRest(t *testing.T) {
	b, sam, kim := setupTwo(t)
	b.cfg.ReceiveMaxCount = 2
	for i := 0; i < 5; i++ {
		_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "m"})
	}
	got, floor, err := b.WaitAbove(context.Background(), kim, nil, false, nil, 0)
	if err != nil || len(got) != 2 || floor != got[1].Seq {
		t.Fatalf("first: %v floor %d err %v", got, floor, err)
	}
	got2, floor2, err := b.WaitAbove(context.Background(), kim, nil, false, nil, floor)
	if err != nil || len(got2) != 2 || got2[0].Seq <= floor || floor2 != got2[1].Seq {
		t.Fatalf("second: %v floor %d err %v", got2, floor2, err)
	}
}

func TestWaitAboveRequiresIdentity(t *testing.T) {
	b, _, _ := setupTwo(t)
	if _, _, err := b.WaitAbove(context.Background(), "", nil, false, nil, 0); err == nil {
		t.Fatal("empty as must error")
	}
}

// A tag-subscription match on a channel kim has no subscription to wakes
// WaitAbove, and is not announced twice.
func TestWaitAboveTagMatchWakesOnce(t *testing.T) {
	old := WaitPollInterval
	WaitPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { WaitPollInterval = old })
	b, sam, kim := setupTwo(t)
	if _, err := b.CreateChannel(sam, "ops", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := b.SubscribeTags(kim, []string{"release"}); err != nil {
		t.Fatal(err)
	}
	sent, err := b.Send(sam, SendInput{Channel: "ops", Content: "shipped", Tags: []string{"release"}})
	if err != nil {
		t.Fatal(err)
	}
	got, floor, err := b.WaitAbove(context.Background(), kim, nil, false, nil, 0)
	if err != nil || len(got) != 1 || got[0].Channel != "ops" || len(got[0].MatchedTags) == 0 || floor != got[0].Seq {
		t.Fatalf("tag match: %v floor %d err %v (sent %+v)", got, floor, err, sent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if got, _, err := b.WaitAbove(ctx, kim, nil, false, nil, floor); err != context.DeadlineExceeded || len(got) != 0 {
		t.Fatalf("re-announced: %v %v", got, err)
	}
}

// WaitAbove's single floor assumes seqs are assigned in commit order. That
// holds because every write transaction takes the write lock at BEGIN
// (_txlock=immediate): a second writer cannot start, so cannot draw a seq,
// until the first has committed.
func TestWriteTransactionsStartSerialized(t *testing.T) {
	dsn, err := SQLiteDSN(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "_txlock=immediate") {
		t.Fatalf("DSN must use immediate transactions: %s", dsn)
	}
	b := newTestBus(t)
	tx1, err := b.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	go func() {
		tx2, err := b.db.Begin()
		if err == nil {
			_ = tx2.Rollback()
		}
		close(started)
	}()
	select {
	case <-started:
		_ = tx1.Rollback()
		t.Fatal("a second write transaction began while the first was open")
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("second transaction never began after the first committed")
	}
}
