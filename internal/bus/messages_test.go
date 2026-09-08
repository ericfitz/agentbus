package bus

import (
	"strings"
	"testing"
)

func reg(t *testing.T, b *Bus, name string) string {
	t.Helper()
	r, err := b.Register(name, "", "repo", true)
	if err != nil {
		t.Fatal(err)
	}
	return r.Sender
}

func TestCreateChannelIdempotentAndConflict(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal("repeat with same kind must succeed:", err)
	}
	if _, err := b.CreateChannel(sam, "dev", "memory"); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatal("kind mismatch must conflict:", err)
	}
	if _, err := b.CreateChannel("", "x", "ordinary"); err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatal("missing as must be validation error")
	}
}

func TestSendAndHistory(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	one := int64(1)
	r1, err := b.Send(sam, SendInput{Channel: "dev", Content: "first", Refs: []Ref{{Kind: "windows_path", Value: `C:\x\y`}}})
	if err != nil || r1.Seq != 1 {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, _ := b.Send(sam, SendInput{Channel: "dev", Content: "second", ReplyTo: &one, Metadata: map[string]string{"k": "v"}})
	if r2.Seq != 2 {
		t.Fatal(r2.Seq)
	}
	h, err := b.History(sam, "dev", nil, nil, 10)
	if err != nil || len(h) != 2 || h[0].Content != "first" || h[1].ReplyTo == nil || *h[1].ReplyTo != 1 || h[0].Refs[0].Value != `C:\x\y` || h[1].Metadata["k"] != "v" {
		t.Fatalf("%+v %v", h, err)
	}
	before := int64(2)
	h, _ = b.History(sam, "dev", &before, nil, 10)
	if len(h) != 1 || h[0].Seq != 1 {
		t.Fatalf("before filter: %+v", h)
	}
	ch, _ := b.ListChannels(sam)
	if len(ch) != 1 || ch[0].Messages != 2 || ch[0].LatestSeq != 2 {
		t.Fatalf("%+v", ch)
	}
}

func TestSendValidation(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	cases := []SendInput{
		{Channel: "nope", Content: "x"},
		{Channel: "dev", Content: ""},
		{Channel: "dev", Content: strings.Repeat("x", 65*1024)},
		{Channel: "dev", Content: "x", Refs: []Ref{{Kind: "ftp", Value: "v"}}},
		{Channel: "dev", Content: "x", ReplyTo: new(int64)},
	}
	for i, c := range cases {
		if _, err := b.Send(sam, c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestSendOnMemoryChannelCreatesMemory(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	r, err := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	if err != nil || r.MemoryID == nil || *r.MemoryID != r.Seq {
		t.Fatalf("%+v %v", r, err)
	}
	h, _ := b.History(sam, "mem", nil, nil, 10)
	if h[0].Revision == nil || *h[0].Revision != 1 {
		t.Fatalf("%+v", h[0])
	}
}
