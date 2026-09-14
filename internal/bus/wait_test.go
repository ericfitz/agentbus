package bus

import (
	"strings"
	"testing"
	"time"
)

func TestWaitReturnsUndeliveredWithoutAdvancingCursor(t *testing.T) {
	b, sam, kim := setupTwo(t)
	if got, err := b.Wait(kim, nil, false, nil, 10*time.Millisecond); err != nil || len(got) != 0 {
		t.Fatalf("timeout must return nothing: %v %v", got, err)
	}
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "hello"})
	_, _ = b.Send(kim, SendInput{Channel: "dev", Content: "own"})
	got, err := b.Wait(kim, nil, false, nil, time.Second)
	if err != nil || len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("%v %v", got, err)
	}
	if again, _ := b.Wait(kim, nil, false, nil, 0); len(again) != 1 {
		t.Fatalf("wait must not advance the cursor: %v", again)
	}
	own, _ := b.Wait(kim, nil, true, nil, 0)
	if len(own) != 2 {
		t.Fatalf("include_own: %v", own)
	}
	r, _ := b.Receive(kim, ReceiveInput{})
	if len(r.Messages) != 1 || r.Messages[0].Content != "hello" {
		t.Fatalf("receive must still deliver the same batch: %+v", r)
	}
	if got, err := b.Wait(kim, []string{"nope"}, false, nil, 0); err == nil {
		t.Fatalf("unsubscribed channel filter must error: %v", got)
	}
	if got, err := b.Wait("nobody", nil, false, nil, 0); err == nil {
		t.Fatalf("unknown identity must error: %v", got)
	}
}

func TestWaitMatchSkipsRejectedMessages(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "noise"})
	at := func(m Message) bool { return strings.Contains(m.Content, "@kim") }
	if got, err := b.Wait(kim, nil, false, at, 10*time.Millisecond); err != nil || len(got) != 0 {
		t.Fatalf("no match must time out: %v %v", got, err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "@kim ping"})
	}()
	got, err := b.Wait(kim, nil, false, at, 2*time.Second)
	if err != nil || len(got) != 1 || got[0].Content != "@kim ping" {
		t.Fatalf("%v %v", got, err)
	}
}
