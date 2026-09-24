package bus

import (
	"encoding/json"
	"strings"
	"testing"
)

// A subject round-trips through send, receive, history, search, get_memory
// and memory_revisions; a reply does not inherit its parent's; the wire
// form carries it only when set (ADR 0010).
func TestSubjectRoundTrip(t *testing.T) {
	b, sam, kim := setupTwo(t)
	r, err := b.Send(sam, SendInput{Channel: "dev", Subject: "  Deploy plan  ", Content: "step one\nstep two"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(got.Messages) != 1 || got.Messages[0].Subject != "Deploy plan" {
		t.Fatalf("receive: %+v %v", got, err)
	}
	h, err := b.History(sam, "dev", nil, nil, 10)
	if err != nil || len(h) != 1 || h[0].Subject != "Deploy plan" {
		t.Fatalf("history: %+v %v", h, err)
	}
	s, err := b.Search(sam, SearchInput{Query: "step", Mode: "text"})
	if err != nil || len(s.Hits) != 1 || s.Hits[0].Subject != "Deploy plan" {
		t.Fatalf("search: %+v %v", s, err)
	}
	rep, err := b.Send(sam, SendInput{Channel: "dev", Content: "ack", ReplyTo: &r.Seq})
	if err != nil {
		t.Fatal(err)
	}
	h, _ = b.History(sam, "dev", nil, nil, 10)
	if len(h) != 2 || h[1].Seq != rep.Seq || h[1].Subject != "" {
		t.Fatalf("a reply must not inherit the subject: %+v", h)
	}
	if j, _ := json.Marshal(h[1]); strings.Contains(string(j), `"subject"`) {
		t.Fatalf("no subject must be omitted on the wire: %s", j)
	}
	if _, err := b.CreateChannel(sam, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	c, err := b.Send(sam, SendInput{Channel: "mem", Subject: "Fixture rule", Content: "no tea.Sequence"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.GetMemory(sam, *c.MemoryID)
	if err != nil || m.Subject != "Fixture rule" {
		t.Fatalf("get_memory: %+v %v", m, err)
	}
	if j, _ := json.Marshal(m); !strings.Contains(string(j), `"subject":"Fixture rule"`) {
		t.Fatalf("subject on the wire: %s", j)
	}
	revs, err := b.MemoryRevisions(sam, *c.MemoryID)
	if err != nil || len(revs) != 1 || revs[0].Subject != "Fixture rule" {
		t.Fatalf("memory_revisions: %+v %v", revs, err)
	}
}

// A line break or more than 200 runes is refused before anything is
// written, with code validation naming the field; whitespace is trimmed
// and a blank subject is stored as none.
func TestSubjectValidation(t *testing.T) {
	b, sam, _ := setupTwo(t)
	for _, bad := range []string{"two\nlines", "line\rbreak", strings.Repeat("é", 201)} {
		_, err := b.Send(sam, SendInput{Channel: "dev", Subject: bad, Content: "x"})
		wantCode(t, err, "validation")
		if !strings.Contains(err.Error(), "subject") {
			t.Fatalf("error must name the field: %v", err)
		}
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: strings.Repeat("é", 200), Content: "x"}); err != nil {
		t.Fatalf("200 runes is the limit: %v", err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: "   ", Content: "first\nsecond"}); err != nil {
		t.Fatal(err)
	}
	h, err := b.History(sam, "dev", nil, nil, 10)
	if err != nil || len(h) != 2 {
		t.Fatalf("refused sends must write nothing: %d %v", len(h), err)
	}
	if h[1].Subject != "" {
		t.Fatalf("blank subject must store as none, got %q", h[1].Subject)
	}
}

// FTS finds a word that appears only in the subject.
func TestSearchFindsWordOnlyInSubject(t *testing.T) {
	b, sam, _ := setupTwo(t)
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: "Zebra sighting", Content: "in the north field"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "nothing to see"}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Search(sam, SearchInput{Query: "zebra", Mode: "text"})
	if err != nil || len(r.Hits) != 1 || r.Hits[0].Subject != "Zebra sighting" {
		t.Fatalf("%+v %v", r, err)
	}
}
