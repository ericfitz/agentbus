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

// edit_memory keeps the subject when omitted, replaces it when set, clears
// it on "", and refuses a bad one before writing; a memory with no subject
// stays without one unless the edit sets it.
func TestEditMemorySubjectKeepChangeClear(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	c, err := b.Send(sam, SendInput{Channel: "mem", Subject: "Rule", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	id := *c.MemoryID
	subjectIs := func(want string) {
		t.Helper()
		m, err := b.GetMemory(sam, id)
		if err != nil || m.Subject != want {
			t.Fatalf("subject %q, want %q (%v)", m.Subject, want, err)
		}
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	subjectIs("Rule")
	revised := " Rule, revised "
	if _, err := b.EditMemory(sam, EditInput{ID: id, Subject: &revised, Content: "v3"}); err != nil {
		t.Fatal(err)
	}
	subjectIs("Rule, revised")
	empty := ""
	if _, err := b.EditMemory(sam, EditInput{ID: id, Subject: &empty, Content: "v4"}); err != nil {
		t.Fatal(err)
	}
	subjectIs("")
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v5"}); err != nil {
		t.Fatal(err)
	}
	subjectIs("")
	bad := "a\nb"
	_, err = b.EditMemory(sam, EditInput{ID: id, Subject: &bad, Content: "v6"})
	wantCode(t, err, "validation")
	revs, err := b.MemoryRevisions(sam, id)
	if err != nil || len(revs) != 5 {
		t.Fatalf("refused edit must write nothing: %d %v", len(revs), err)
	}
	if revs[0].Subject != "Rule" || revs[1].Subject != "Rule" || revs[2].Subject != "Rule, revised" || revs[3].Subject != "" {
		t.Fatalf("each revision keeps its own subject: %+v", revs)
	}
}

// task_create and task_update keep their API; every revision row they
// write carries the task's subject, so it is searchable and the TUI's
// revision browser sees it.
func TestTaskRevisionsCarrySubject(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "tasks/repo", "memory"); err != nil {
		t.Fatal(err)
	}
	tk := mustCreate(t, b, TaskCreateInput{Channel: "tasks/repo", Subject: "Ship v6"})
	renamed := "Ship v6 today"
	if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Subject: &renamed}); err != nil {
		t.Fatal(err)
	}
	revs, err := b.MemoryRevisions(sam, tk.ID)
	if err != nil || len(revs) != 2 || revs[0].Subject != "Ship v6" || revs[1].Subject != "Ship v6 today" {
		t.Fatalf("%+v %v", revs, err)
	}
	// The task's JSON content already contains "today" (it's embedded in
	// the subject field of the document), so a text search for "today"
	// would hit regardless of whether the row's own subject column is set;
	// it can't distinguish the two. Query the FTS subject column directly
	// instead: only it can satisfy a `subject:` column filter.
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'subject:today'").Scan(&n); err != nil || n < 1 {
		t.Fatalf("task subject is searchable via FTS: n=%d err=%v", n, err)
	}
}

// The pre-transaction preflight envelope check (tasks_update.go) must count
// the row's subject column, not just the JSON document, so a patch whose
// subject alone pushes the row over max_message_kib is refused there,
// before inspect/checkCapacity run and before any revision is written
// (human decision 2026-09-24).
func TestTaskUpdateSubjectCountsInPreflight(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "tasks/repo", "memory"); err != nil {
		t.Fatal(err)
	}
	tk := mustCreate(t, b, TaskCreateInput{Channel: "tasks/repo", Subject: "s"})

	senderCtx, err := b.senderContext(b.db, sam)
	if err != nil {
		t.Fatal(err)
	}
	subj := strings.Repeat("s", 256) // task subject rule: 1-256 bytes

	// Find a description length whose preflight document, alone, fits
	// under some max_message_kib, but adding the 256-byte subject column
	// to the same envelope pushes it over. Sizes come from
	// envelopeUpperBound (the function the preflight itself calls via
	// sendEnvelope), not from guessed byte counts.
	var desc string
	var limitKiB int
	for n := 0; n < 1500; n++ {
		d := strings.Repeat("d", n)
		doc, err := json.Marshal(taskDoc{Subject: subj, Description: d, Status: "pending", Rank: "V"})
		if err != nil {
			t.Fatal(err)
		}
		without := envelopeUpperBound(sam, senderCtx, SendInput{Channel: "tasks/repo", Content: string(doc)}, true)
		with := envelopeUpperBound(sam, senderCtx, SendInput{Channel: "tasks/repo", Subject: subj, Content: string(doc)}, true)
		boundary := ((without + 1023) / 1024) * 1024
		if with > boundary {
			desc, limitKiB = d, boundary/1024
			break
		}
	}
	if desc == "" {
		t.Fatal("could not find a description length that straddles a max_message_kib boundary")
	}
	b.cfg.MaxMessageKiB = limitKiB

	before, err := b.MemoryRevisions(sam, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	b.inspectCalls.Store(0) // setup above legitimately called the hook; only count from here
	_, err = b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Subject: &subj, Description: &desc})
	wantCode(t, err, "validation")
	// The refusal must come from the preflight, before inspect ever runs
	// (C1) — not merely from the later authoritative check in the write
	// tx, which would also refuse this but only after paying for the hook.
	if n := b.inspectCalls.Load(); n != 0 {
		t.Fatalf("subject-oversized update must be refused before the inspect hook runs: %d calls", n)
	}
	after, err := b.MemoryRevisions(sam, tk.ID)
	if err != nil || len(after) != len(before) {
		t.Fatalf("refused update must write nothing: before=%d after=%d err=%v", len(before), len(after), err)
	}
}
