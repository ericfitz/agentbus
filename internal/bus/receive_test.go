package bus

import (
	"testing"
	"time"
)

func setupTwo(t *testing.T) (*Bus, string, string) {
	t.Helper()
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	kim := reg(t, b, "Kim")
	b.CreateChannel(sam, "dev", "ordinary")
	if err := b.Subscribe(kim, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	return b, sam, kim
}

func TestReceiveAckRedeliver(t *testing.T) {
	b, sam, kim := setupTwo(t)
	b.Send(sam, SendInput{Channel: "dev", Content: "a"})
	b.Send(sam, SendInput{Channel: "dev", Content: "b"})
	r1, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r1.Messages) != 2 || r1.Batch == "" || r1.Redelivered {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, _ := b.Receive(kim, ReceiveInput{})
	if len(r2.Messages) != 2 || !r2.Redelivered || r2.Batch != r1.Batch || r2.Instruction == "" {
		t.Fatalf("unacked batch must be redelivered: %+v", r2)
	}
	b.Send(sam, SendInput{Channel: "dev", Content: "c"})
	r3, _ := b.Receive(kim, ReceiveInput{Ack: r1.Batch})
	if len(r3.Messages) != 1 || r3.Messages[0].Content != "c" || r3.Redelivered {
		t.Fatalf("ack must advance: %+v", r3)
	}
	r4, _ := b.Receive(kim, ReceiveInput{Ack: "stale"})
	if !r4.AckIgnored || len(r4.Messages) != 1 {
		t.Fatalf("stale ack ignored and batch redelivered: %+v", r4)
	}
	r5, _ := b.Receive(kim, ReceiveInput{Ack: r3.Batch})
	if len(r5.Messages) != 0 || r5.Batch != "" {
		t.Fatalf("empty receive has no batch: %+v", r5)
	}
}

func TestReceiveExcludesOwnAndMergesBySeq(t *testing.T) {
	b, sam, kim := setupTwo(t)
	b.CreateChannel(sam, "ops", "ordinary")
	b.Subscribe(kim, "ops", "now")
	b.Send(kim, SendInput{Channel: "dev", Content: "mine"})
	b.Send(sam, SendInput{Channel: "ops", Content: "o1"})
	b.Send(sam, SendInput{Channel: "dev", Content: "d1"})
	r, _ := b.Receive(kim, ReceiveInput{})
	if len(r.Messages) != 2 || r.Messages[0].Content != "o1" || r.Messages[1].Content != "d1" {
		t.Fatalf("%+v", r.Messages)
	}
	r, _ = b.Receive(kim, ReceiveInput{Ack: r.Batch, IncludeOwn: true, Channels: []string{"dev"}})
	if len(r.Messages) != 0 {
		t.Fatalf("own message was already before cursor? %+v", r.Messages)
	}
	// Fresh subscription from oldest sees everything including own when asked.
	b.Unsubscribe(kim, "dev")
	b.Subscribe(kim, "dev", "oldest")
	r, _ = b.Receive(kim, ReceiveInput{IncludeOwn: true, Channels: []string{"dev"}})
	if len(r.Messages) != 2 {
		t.Fatalf("from oldest with own: %+v", r.Messages)
	}
}

func TestReceiveGapAndExpiry(t *testing.T) {
	b, sam, kim := setupTwo(t)
	for i := 0; i < 3; i++ {
		b.Send(sam, SendInput{Channel: "dev", Content: "x"})
	}
	// Simulate eviction of seq 1..2.
	b.db.Exec("DELETE FROM messages WHERE seq<=2")
	b.db.Exec("UPDATE channels SET evicted_before_seq=3 WHERE name='dev'")
	r, _ := b.Receive(kim, ReceiveInput{})
	if len(r.Gaps) != 1 || r.Gaps[0].From != 1 || r.Gaps[0].To != 2 || len(r.Messages) != 1 || r.Messages[0].Seq != 3 {
		t.Fatalf("%+v", r)
	}
	b.Now = func() time.Time { return time.Now().Add(73 * time.Hour) }
	r, _ = b.Receive(kim, ReceiveInput{Ack: r.Batch})
	if len(r.Expired) != 1 || r.Expired[0] != "dev" {
		t.Fatalf("idle subscription must expire: %+v", r)
	}
	var n int
	b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=?", kim).Scan(&n)
	if n != 0 {
		t.Fatal("expired subscription not deleted")
	}
}

func TestReceiveCountAndByteLimits(t *testing.T) {
	b, sam, kim := setupTwo(t)
	for i := 0; i < 5; i++ {
		b.Send(sam, SendInput{Channel: "dev", Content: "0123456789"})
	}
	r, _ := b.Receive(kim, ReceiveInput{Count: 2})
	if len(r.Messages) != 2 {
		t.Fatal(len(r.Messages))
	}
	b.cfg.ResultDefaultKiB = 1
	b.Send(sam, SendInput{Channel: "dev", Content: string(make([]byte, 900))})
	b.Send(sam, SendInput{Channel: "dev", Content: string(make([]byte, 900))})
	r, _ = b.Receive(kim, ReceiveInput{Ack: r.Batch, Count: 100})
	if len(r.Messages) >= 5 {
		t.Fatalf("byte ceiling not applied: %d", len(r.Messages))
	}
}

func TestReceiveWaitsThenReturnsEmpty(t *testing.T) {
	b, _, kim := setupTwo(t)
	start := time.Now()
	r, err := b.Receive(kim, ReceiveInput{WaitSeconds: 1})
	if err != nil || len(r.Messages) != 0 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("%+v %v %v", r, err, time.Since(start))
	}
}

func TestReceiveWaitSecondsClampedNotRejected(t *testing.T) {
	b, _, kim := setupTwo(t)
	b.cfg.ReceiveMaxWaitSeconds = 1
	start := time.Now()
	r, err := b.Receive(kim, ReceiveInput{WaitSeconds: 999})
	if err != nil || len(r.Messages) != 0 || time.Since(start) < 900*time.Millisecond || time.Since(start) > 3*time.Second {
		t.Fatalf("over-cap wait must be clamped, not rejected: %+v %v %v", r, err, time.Since(start))
	}
}
