package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// memBus is a test bus with Sam and Kim registered and a memory channel
// "mem".
func memBus(t *testing.T) (b *Bus, sam, kim string) {
	t.Helper()
	b = newTestBus(t)
	sam = reg(t, b, "Sam")
	kim = reg(t, b, "Kim")
	if _, err := b.CreateChannel(sam, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	return b, sam, kim
}

// remember sends content to "mem" as as and returns the memory id.
func remember(t *testing.T, b *Bus, as, content string) int64 {
	t.Helper()
	r, err := b.Send(as, SendInput{Channel: "mem", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if r.MemoryID == nil {
		t.Fatal("Send to a memory channel returned no memory id")
	}
	return *r.MemoryID
}

// stats reads memory id's memory_access row.
func stats(t *testing.T, b *Bus, id int64) MemoryStats {
	t.Helper()
	rows, err := b.db.Query("SELECT "+memoryStatsCols+" FROM memory_access a WHERE a.memory_id=?", id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanMemoryStats(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("memory_access rows for %d = %d, want 1", id, len(got))
	}
	return got[0]
}

// setAccess runs UPDATE memory_access SET <set> WHERE memory_id=id.
func setAccess(t *testing.T, b *Bus, id int64, set string, args ...any) {
	t.Helper()
	if _, err := b.db.Exec(fmt.Sprintf("UPDATE memory_access SET %s WHERE memory_id=?", set), append(args, id)...); err != nil {
		t.Fatal(err)
	}
}

// clockAt pins b.Now to t0 plus d.
func clockAt(b *Bus, t0 time.Time, d time.Duration) {
	b.Now = func() time.Time { return t0.Add(d) }
}

// TestCreateSetsGraceFromAndZeroCounters (#53): a new memory starts its grace
// period now with zero counters and no flag.
func TestCreateSetsGraceFromAndZeroCounters(t *testing.T) {
	b, sam, _ := memBus(t)
	t0 := time.Unix(1_800_000_000, 0)
	clockAt(b, t0, 0)
	id := remember(t, b, sam, "fact")
	s := stats(t, b, id)
	if s.ServedCount != 0 || s.UsedCount != 0 || s.GraceServedBase != 0 || s.IncorrectServedBase != 0 {
		t.Fatalf("counters not zero: %+v", s)
	}
	if s.GraceFrom != t0.UnixMilli() || s.AccessedAt != t0.UnixMilli() {
		t.Fatalf("grace_from=%d accessed_at=%d, want %d", s.GraceFrom, s.AccessedAt, t0.UnixMilli())
	}
	if s.UsedAt != nil || s.IncorrectAt != nil || s.IncorrectBy != nil || s.PrunedAt != nil || s.PrunedReason != nil {
		t.Fatalf("nullable columns not NULL: %+v", s)
	}
}

// TestEditRestartsGraceClearsFlagKeepsCounters (#53): an edit restarts the
// grace period, clears the INCORRECT flag, and keeps the counters.
func TestEditRestartsGraceClearsFlagKeepsCounters(t *testing.T) {
	b, sam, _ := memBus(t)
	t0 := time.Unix(1_800_000_000, 0)
	clockAt(b, t0, 0)
	id := remember(t, b, sam, "v1")
	setAccess(t, b, id, "served_count=7, used_count=2, used_at=5, incorrect_at=9, incorrect_by='Kim', incorrect_served_base=4")
	clockAt(b, t0, time.Hour)
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	s := stats(t, b, id)
	if s.ServedCount != 7 || s.UsedCount != 2 || s.UsedAt == nil || *s.UsedAt != 5 {
		t.Fatalf("counters not kept: %+v", s)
	}
	if s.IncorrectAt != nil || s.IncorrectBy != nil || s.IncorrectServedBase != 0 {
		t.Fatalf("flag not cleared: %+v", s)
	}
	want := t0.Add(time.Hour).UnixMilli()
	if s.GraceFrom != want || s.AccessedAt != want || s.GraceServedBase != 7 {
		t.Fatalf("grace_from=%d accessed_at=%d grace_served_base=%d, want %d, %d, 7", s.GraceFrom, s.AccessedAt, s.GraceServedBase, want, want)
	}
}

// TestTouchCountsOncePerDistinctID (#53): a serve counts once per distinct id
// per call, however often the id repeats in the list.
func TestTouchCountsOncePerDistinctID(t *testing.T) {
	b, sam, _ := memBus(t)
	id := remember(t, b, sam, "fact")
	if err := b.TouchMemoryAccess([]int64{id, id, id}); err != nil {
		t.Fatal(err)
	}
	if got := stats(t, b, id).ServedCount; got != 1 {
		t.Fatalf("served_count after a repeated id = %d, want 1", got)
	}
	if err := b.TouchMemoryAccess([]int64{id}); err != nil {
		t.Fatal(err)
	}
	if got := stats(t, b, id).ServedCount; got != 2 {
		t.Fatalf("served_count after a second call = %d, want 2", got)
	}
}

// TestTouchSkipsPrunedMemory (#53): a pruned memory's row is left alone.
func TestTouchSkipsPrunedMemory(t *testing.T) {
	b, sam, _ := memBus(t)
	id := remember(t, b, sam, "fact")
	setAccess(t, b, id, "pruned_at=1, pruned_reason='noise', accessed_at=50, served_count=3")
	if err := b.TouchMemoryAccess([]int64{id}); err != nil {
		t.Fatal(err)
	}
	if s := stats(t, b, id); s.ServedCount != 3 || s.AccessedAt != 50 {
		t.Fatalf("a pruned memory must not be touched: %+v", s)
	}
}

// pruneRaw marks memory id pruned now with reason.
func pruneRaw(t *testing.T, b *Bus, id int64, reason string) {
	t.Helper()
	setAccess(t, b, id, "pruned_at=?, pruned_reason=?", b.nowMs(), reason)
}

// prunedAndDeleted returns a pruned memory p and a deleted control d, both
// holding "zebracorn" and tag kind:gotcha.
func prunedAndDeleted(t *testing.T, b *Bus, sam string) (p, d int64) {
	t.Helper()
	p = *sendTagged(t, b, sam, "mem", "zebracorn pruned", "kind:gotcha").MemoryID
	d = *sendTagged(t, b, sam, "mem", "zebracorn deleted", "kind:gotcha").MemoryID
	if err := b.DeleteMemory(sam, d, ""); err != nil {
		t.Fatal(err)
	}
	pruneRaw(t, b, p, "noise")
	return p, d
}

func hasHit(hits []SearchHit, id int64) bool {
	for _, h := range hits {
		if h.MemoryID != nil && *h.MemoryID == id {
			return true
		}
	}
	return false
}

func hasRow(ms []Message, id int64) bool {
	for _, m := range ms {
		if m.MemoryID != nil && *m.MemoryID == id {
			return true
		}
	}
	return false
}

// sameErr fails unless a and b carry the same code and message once each
// call's own id is replaced by N.
func sameErr(t *testing.T, name string, a, b error, ida, idb int64) {
	t.Helper()
	if a == nil || b == nil {
		t.Fatalf("%s: errors a=%v b=%v, want both set", name, a, b)
	}
	norm := func(e error, id int64) string {
		var be *Error
		if !errors.As(e, &be) {
			t.Fatalf("%s: %v is not a bus error", name, e)
		}
		return strings.ReplaceAll(be.Code+"|"+be.Message, fmt.Sprint(id), "N")
	}
	if na, nb := norm(a, ida), norm(b, idb); na != nb {
		t.Fatalf("%s: pruned %q differs from deleted %q", name, na, nb)
	}
}

// TestPrunedHiddenFromAgentReads (#53): a pruned memory answers agent reads
// as a deleted one; the human variants still return it.
func TestPrunedHiddenFromAgentReads(t *testing.T) {
	b, sam, _ := memBus(t)
	p, d := prunedAndDeleted(t, b, sam)

	_, errP := b.GetMemory(sam, p)
	_, errD := b.GetMemory(sam, d)
	sameErr(t, "GetMemory", errP, errD, p, d)
	wantCode(t, errP, "not_found")
	var be *Error
	if want := fmt.Sprintf("memory %d has no live revision", p); !errors.As(errP, &be) || be.Message != want {
		t.Fatalf("GetMemory(p) = %v, want message %q", errP, want)
	}

	inputs := map[string]SearchInput{
		"text":    {Query: "zebracorn"},
		"tags":    {Tags: []string{"kind:gotcha"}},
		"channel": {Query: "zebracorn", Channel: "mem"},
	}
	for name, in := range inputs {
		r, err := b.Search(sam, in)
		if err != nil || hasHit(r.Hits, p) {
			t.Fatalf("Search %s: p leaked or failed: %+v %v", name, r.Hits, err)
		}
		r, err = b.SearchAll(sam, in)
		if err != nil || !hasHit(r.Hits, p) {
			t.Fatalf("SearchAll %s: p missing: %+v %v", name, r.Hits, err)
		}
	}
	ms, err := b.History(sam, "mem", nil, nil, 0)
	if err != nil || hasRow(ms, p) {
		t.Fatalf("History leaked p: %+v %v", ms, err)
	}
	ms, err = b.HistoryAll(sam, "mem", nil, nil, 0)
	if err != nil || !hasRow(ms, p) {
		t.Fatalf("HistoryAll lost p: %+v %v", ms, err)
	}
	revs, err := b.MemoryRevisions(sam, p)
	if err != nil || len(revs) != 1 {
		t.Fatalf("MemoryRevisions(p) = %d revisions, %v; want 1", len(revs), err)
	}
	if _, _, _, err := liveRevision(b.db, p); err == nil {
		t.Fatal("liveRevision found a pruned memory")
	} else {
		wantCode(t, err, "not_found")
	}
	seq, rev, ch, err := liveRevisionAny(b.db, p)
	if err != nil || seq != p || rev != 1 || ch != "mem" {
		t.Fatalf("liveRevisionAny(p) = %d,%d,%q,%v; want %d,1,mem", seq, rev, ch, err, p)
	}
}

// TestPrunedSemanticSearchExcluded (#53): both ranked queries drop a pruned
// memory for agents and keep it for SearchAll.
func TestPrunedSemanticSearchExcluded(t *testing.T) {
	srv := fakeEmbeddings(t)
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	p, _ := prunedAndDeleted(t, b, sam)
	drainEmbed(t, b)
	for _, mode := range []string{"semantic", "both"} {
		in := SearchInput{Query: "zebracorn", Mode: mode}
		r, err := b.Search(sam, in)
		if err != nil || hasHit(r.Hits, p) {
			t.Fatalf("Search %s leaked p: %+v %v", mode, r.Hits, err)
		}
		r, err = b.SearchAll(sam, in)
		if err != nil || !hasHit(r.Hits, p) {
			t.Fatalf("SearchAll %s lost p: %+v %v", mode, r.Hits, err)
		}
	}
}

// TestPrunedRefusedLikeDeleted (#53): every id-taking operation refuses a
// pruned memory exactly as it refuses a deleted one, and changes nothing.
func TestPrunedRefusedLikeDeleted(t *testing.T) {
	b, sam, _ := memBus(t)
	p, d := prunedAndDeleted(t, b, sam)
	before := stats(t, b, p)
	status := "completed"
	calls := map[string]func(id int64) error{
		"EditMemory":   func(id int64) error { _, err := b.EditMemory(sam, EditInput{ID: id, Content: "x"}); return err },
		"DeleteMemory": func(id int64) error { return b.DeleteMemory(sam, id, "") },
		"TaskGet":      func(id int64) error { _, err := b.TaskGet(sam, id); return err },
		"TaskUpdate": func(id int64) error {
			_, err := b.TaskUpdate(sam, TaskPatch{ID: id, Status: &status})
			return err
		},
		"TaskClaim":   func(id int64) error { _, err := b.TaskClaim(sam, id, 0, ""); return err },
		"TaskRelease": func(id int64) error { _, err := b.TaskRelease(sam, id, ""); return err },
	}
	for name, call := range calls {
		sameErr(t, name, call(p), call(d), p, d)
	}
	if got := stats(t, b, p); !reflect.DeepEqual(got, before) {
		t.Fatalf("stats changed: %+v -> %+v", before, got)
	}
	if _, _, _, err := liveRevisionAny(b.db, p); err != nil {
		t.Fatalf("p lost its live revision: %v", err)
	}
}

// TestFeedbackUsed (#53): USED counts a use and stamps used_at and
// accessed_at.
func TestFeedbackUsed(t *testing.T) {
	b, sam, _ := memBus(t)
	t0 := time.Unix(1_800_000_000, 0)
	clockAt(b, t0, 0)
	id := remember(t, b, sam, "fact")
	clockAt(b, t0, time.Hour)
	now := t0.Add(time.Hour).UnixMilli()
	res, err := b.MemoryFeedback(sam, []FeedbackItem{{MemoryID: id, Verdict: "USED"}})
	if err != nil {
		t.Fatal(err)
	}
	s := stats(t, b, id)
	if s.UsedCount != 1 || s.UsedAt == nil || *s.UsedAt != now || s.AccessedAt != now {
		t.Fatalf("stats = %+v, want used_count 1 and used_at = accessed_at = %d", s, now)
	}
	if want := []FeedbackItem{{MemoryID: id, Verdict: "USED"}}; !reflect.DeepEqual(res.Applied, want) {
		t.Fatalf("applied = %+v, want %+v", res.Applied, want)
	}
}

// TestFeedbackIncorrectAndReflag (#53): INCORRECT records who and when and
// the serve count at that moment; a later INCORRECT replaces all three.
func TestFeedbackIncorrectAndReflag(t *testing.T) {
	b, sam, kim := memBus(t)
	t0 := time.Unix(1_800_000_000, 0)
	clockAt(b, t0, 0)
	id := remember(t, b, sam, "fact")
	setAccess(t, b, id, "served_count=3")
	clockAt(b, t0, time.Hour)
	if _, err := b.MemoryFeedback(kim, []FeedbackItem{{MemoryID: id, Verdict: "INCORRECT"}}); err != nil {
		t.Fatal(err)
	}
	s := stats(t, b, id)
	if s.IncorrectAt == nil || *s.IncorrectAt != t0.Add(time.Hour).UnixMilli() || s.IncorrectBy == nil || *s.IncorrectBy != "Kim" || s.IncorrectServedBase != 3 {
		t.Fatalf("first flag: %+v", s)
	}
	setAccess(t, b, id, "served_count=5")
	clockAt(b, t0, 2*time.Hour)
	if _, err := b.MemoryFeedback(sam, []FeedbackItem{{MemoryID: id, Verdict: "INCORRECT"}}); err != nil {
		t.Fatal(err)
	}
	s = stats(t, b, id)
	if s.IncorrectAt == nil || *s.IncorrectAt != t0.Add(2*time.Hour).UnixMilli() || s.IncorrectBy == nil || *s.IncorrectBy != "Sam" || s.IncorrectServedBase != 5 {
		t.Fatalf("reflag: %+v", s)
	}
}

// TestFeedbackDuplicateIdsApplyInOrder (#53): the same id may appear more
// than once; items apply in list order under one timestamp.
func TestFeedbackDuplicateIdsApplyInOrder(t *testing.T) {
	b, sam, _ := memBus(t)
	id := remember(t, b, sam, "fact")
	_, err := b.MemoryFeedback(sam, []FeedbackItem{{id, "USED"}, {id, "INCORRECT"}, {id, "USED"}})
	if err != nil {
		t.Fatal(err)
	}
	s := stats(t, b, id)
	if s.UsedCount != 2 || s.UsedAt == nil || s.IncorrectAt == nil || *s.UsedAt != *s.IncorrectAt {
		t.Fatalf("stats = %+v, want used_count 2 and one shared timestamp", s)
	}
}

// TestFeedbackAllOrNothing (#53): one error names every bad item in list
// order, nothing is applied, and a pruned memory reads as a deleted one.
func TestFeedbackAllOrNothing(t *testing.T) {
	b, sam, _ := memBus(t)
	if _, err := b.CreateChannel(sam, "tasks/w", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel(sam, "chat", "ordinary"); err != nil {
		t.Fatal(err)
	}
	good := remember(t, b, sam, "good")
	deleted := remember(t, b, sam, "deleted")
	if err := b.DeleteMemory(sam, deleted, ""); err != nil {
		t.Fatal(err)
	}
	pruned := remember(t, b, sam, "pruned")
	pruneRaw(t, b, pruned, "noise")
	tk, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/w", Subject: "job"})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := b.Send(sam, SendInput{Channel: "chat", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	chatSeq := cr.Seq
	_, err = b.MemoryFeedback(sam, []FeedbackItem{
		{good, "USED"}, {deleted, "USED"}, {tk.ID, "INCORRECT"}, {pruned, "USED"}, {good, "used"}, {chatSeq, "USED"},
	})
	wantCode(t, err, "validation")
	var be *Error
	if !errors.As(err, &be) {
		t.Fatal(err)
	}
	msg := be.Message
	wants := []string{
		fmt.Sprintf("item 1 (memory %d): not found", deleted),
		fmt.Sprintf("item 2 (memory %d): task row", tk.ID),
		fmt.Sprintf("item 3 (memory %d): not found", pruned),
		fmt.Sprintf(`item 4 (memory %d): unknown verdict "used"`, good),
		fmt.Sprintf("item 5 (memory %d): not found", chatSeq),
	}
	at := 0
	for _, w := range wants {
		i := strings.Index(msg[at:], w)
		if i < 0 {
			t.Fatalf("message %q lacks %q in order", msg, w)
		}
		at += i + len(w)
	}
	if strings.Contains(msg, "item 0") {
		t.Fatalf("message names the valid item 0: %q", msg)
	}
	if n := stats(t, b, good).UsedCount; n != 0 {
		t.Fatalf("used_count = %d after a refused call, want 0", n)
	}
	// Pruned and deleted read identically.
	_, e1 := b.MemoryFeedback(sam, []FeedbackItem{{deleted, "USED"}})
	_, e2 := b.MemoryFeedback(sam, []FeedbackItem{{pruned, "USED"}})
	if e1 == nil || e2 == nil || strings.ReplaceAll(e1.Error(), fmt.Sprint(deleted), "N") != strings.ReplaceAll(e2.Error(), fmt.Sprint(pruned), "N") {
		t.Fatalf("deleted %v, pruned %v: texts differ", e1, e2)
	}
}

// TestFeedbackBounds (#53): 1 to 50 items.
func TestFeedbackBounds(t *testing.T) {
	b, sam, _ := memBus(t)
	id := remember(t, b, sam, "fact")
	_, err := b.MemoryFeedback(sam, nil)
	wantCode(t, err, "validation")
	_, err = b.MemoryFeedback(sam, []FeedbackItem{})
	wantCode(t, err, "validation")
	items := make([]FeedbackItem, 51)
	for i := range items {
		items[i] = FeedbackItem{MemoryID: id, Verdict: "USED"}
	}
	_, err = b.MemoryFeedback(sam, items)
	wantCode(t, err, "validation")
	if n := stats(t, b, id).UsedCount; n != 0 {
		t.Fatalf("used_count = %d after a refused call, want 0", n)
	}
	if _, err := b.MemoryFeedback(sam, items[:50]); err != nil {
		t.Fatal(err)
	}
	if n := stats(t, b, id).UsedCount; n != 50 {
		t.Fatalf("used_count = %d, want 50", n)
	}
}

// TestFeedbackEditClearsFlag (#53): an edit clears INCORRECT and keeps the
// use counter.
func TestFeedbackEditClearsFlag(t *testing.T) {
	b, sam, _ := memBus(t)
	id := remember(t, b, sam, "fact")
	if _, err := b.MemoryFeedback(sam, []FeedbackItem{{id, "USED"}, {id, "INCORRECT"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "fixed"}); err != nil {
		t.Fatal(err)
	}
	s := stats(t, b, id)
	if s.IncorrectAt != nil || s.IncorrectBy != nil || s.UsedCount != 1 {
		t.Fatalf("stats = %+v, want flag cleared and used_count 1", s)
	}
}

// TestFeedbackRateLimited (#53): a call is charged once; a refused call
// applies nothing.
func TestFeedbackRateLimited(t *testing.T) {
	b, sam, _ := memBus(t)
	id := remember(t, b, sam, "fact")
	b.cfg.SendMessagesPerSecond = 1
	b.limits = newLimiter(b.cfg)
	if _, err := b.MemoryFeedback(sam, []FeedbackItem{{id, "USED"}}); err != nil {
		t.Fatal(err)
	}
	_, err := b.MemoryFeedback(sam, []FeedbackItem{{id, "USED"}})
	wantCode(t, err, "rate_limited")
	if n := stats(t, b, id).UsedCount; n != 1 {
		t.Fatalf("used_count = %d, want 1", n)
	}
}

func TestGetMemoryFlagged(t *testing.T) {
	b, sam, kim := memBus(t)
	id := remember(t, b, sam, "alpha")
	plain, err := b.GetMemory(sam, id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.GetMemoryFlagged(sam, id)
	if err != nil || got.Incorrect {
		t.Fatalf("%+v %v", got, err)
	}
	gj, _ := json.Marshal(got)
	pj, _ := json.Marshal(plain)
	if string(gj) != string(pj) {
		t.Fatalf("unflagged JSON differs:\n%s\n%s", gj, pj)
	}
	flag(t, b, kim, id)
	got, err = b.GetMemoryFlagged(sam, id)
	if err != nil || !got.Incorrect {
		t.Fatalf("%+v %v", got, err)
	}
	gj, _ = json.Marshal(got)
	if !strings.Contains(string(gj), `"incorrect":true`) || strings.Contains(string(gj), "count") {
		t.Fatalf("flagged JSON: %s", gj)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "alpha fixed"}); err != nil {
		t.Fatal(err)
	}
	if got, err = b.GetMemoryFlagged(sam, id); err != nil || got.Incorrect {
		t.Fatalf("edit must clear the flag: %+v %v", got, err)
	}
	pruneRaw(t, b, id, "noise")
	_, err = b.GetMemoryFlagged(sam, id)
	var be *Error
	if !errors.As(err, &be) || be.Code != "not_found" || be.Message != fmt.Sprintf("memory %d has no live revision", id) {
		t.Fatalf("pruned: %v", err)
	}
}

// TestChannelMemoryStatsIncludesPruned (#53): the map holds every live
// memory of the channel, pruned ones included, and nothing else.
func TestChannelMemoryStatsIncludesPruned(t *testing.T) {
	b, sam, _ := memBus(t)
	if _, err := b.CreateChannel(sam, "other", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel(sam, "tasks/w", "memory"); err != nil {
		t.Fatal(err)
	}
	live := remember(t, b, sam, "live")
	pruned := remember(t, b, sam, "pruned")
	deleted := remember(t, b, sam, "deleted")
	pruneRaw(t, b, pruned, "noise")
	if err := b.DeleteMemory(sam, deleted, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/w", Subject: "job"}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Send(sam, SendInput{Channel: "other", Content: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.ChannelMemoryStats(sam, "mem")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("stats = %v, want exactly ids %d and %d", got, live, pruned)
	}
	if s, ok := got[live]; !ok || s.PrunedAt != nil {
		t.Fatalf("live memory: %+v, %v", s, ok)
	}
	if s, ok := got[pruned]; !ok || s.PrunedAt == nil {
		t.Fatalf("pruned memory: %+v, %v", s, ok)
	}
	if _, ok := got[*r.MemoryID]; ok {
		t.Fatal("a memory on another channel is in the map")
	}
}

// TestRestoreMemory (#53): restore clears the pruned and flag state, restarts
// grace, keeps counters, and survives the next ticks.
func TestRestoreMemory(t *testing.T) {
	for _, reason := range []string{"noise", "incorrect"} {
		t.Run(reason, func(t *testing.T) {
			b, sam, _ := memBus(t)
			t0 := time.Unix(1_800_000_000, 0)
			clockAt(b, t0, 0)
			id := remember(t, b, sam, "fact")
			setAccess(t, b, id, "served_count=30, used_count=0, pruned_at=?, pruned_reason=?", t0.UnixMilli(), reason)
			if reason == "incorrect" {
				setAccess(t, b, id, "incorrect_at=?, incorrect_by='Kim', incorrect_served_base=2", t0.UnixMilli())
			}
			now := t0.Add(1000 * time.Hour)
			clockAt(b, t0, 1000*time.Hour)
			if err := b.RestoreMemory(sam, id); err != nil {
				t.Fatal(err)
			}
			s := stats(t, b, id)
			if s.PrunedAt != nil || s.PrunedReason != nil || s.IncorrectAt != nil || s.IncorrectBy != nil ||
				s.IncorrectServedBase != 0 || s.GraceFrom != now.UnixMilli() || s.ServedCount != 30 || s.GraceServedBase != 30 {
				t.Fatalf("after restore: %+v", s)
			}
			if _, err := b.GetMemory(sam, id); err != nil {
				t.Fatal(err)
			}
			for _, d := range []time.Duration{0, time.Hour} {
				clockAt(b, t0, 1000*time.Hour+d)
				if err := b.Heartbeat(); err != nil {
					t.Fatal(err)
				}
				b.Tick(context.Background())
				if s := stats(t, b, id); s.PrunedAt != nil {
					t.Fatalf("pruned again at +%v: %+v", d, s)
				}
			}
			before := stats(t, b, id)
			err := b.RestoreMemory(sam, id)
			wantCode(t, err, "validation")
			var be *Error
			if !errors.As(err, &be) || be.Message != fmt.Sprintf("memory %d is not pruned", id) {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(before, stats(t, b, id)) {
				t.Fatal("a refused restore changed the row")
			}
		})
	}
}

// TestRestoredMemoryNeedsNewServes (#53, spec item 15): after a restore the
// noise rule counts only serves since the restore.
func TestRestoredMemoryNeedsNewServes(t *testing.T) {
	b, sam, _ := memBus(t)
	t0 := time.Unix(1_800_000_000, 0)
	clockAt(b, t0, 0)
	id := remember(t, b, sam, "fact")
	setAccess(t, b, id, "served_count=30, used_count=0, pruned_at=?, pruned_reason='noise'", t0.UnixMilli())
	const r = 1000 * time.Hour
	clockAt(b, t0, r)
	if err := b.RestoreMemory(sam, id); err != nil {
		t.Fatal(err)
	}
	tickAt := func(d time.Duration) MemoryStats {
		clockAt(b, t0, r+d)
		if err := b.Heartbeat(); err != nil {
			t.Fatal(err)
		}
		b.Tick(context.Background())
		return stats(t, b, id)
	}
	if s := tickAt(721 * time.Hour); s.PrunedAt != nil {
		t.Fatalf("pruned with no new serve: %+v", s)
	}
	for i := 0; i < 19; i++ {
		if err := b.TouchMemoryAccess([]int64{id}); err != nil {
			t.Fatal(err)
		}
	}
	if s := tickAt(722 * time.Hour); s.PrunedAt != nil {
		t.Fatalf("pruned after 19 serves: %+v", s)
	}
	if err := b.TouchMemoryAccess([]int64{id}); err != nil {
		t.Fatal(err)
	}
	s := tickAt(723 * time.Hour)
	if s.PrunedAt == nil || s.PrunedReason == nil || *s.PrunedReason != "noise" {
		t.Fatalf("not pruned after 20 serves: %+v", s)
	}
}

// TestClearMemoryIncorrect (#53): clears only the flag columns, on a pruned
// memory too, and refuses an unflagged memory.
func TestClearMemoryIncorrect(t *testing.T) {
	b, sam, _ := memBus(t)
	t0 := time.Unix(1_800_000_000, 0)
	clockAt(b, t0, 0)
	flag := "served_count=9, used_count=3, grace_served_base=4, incorrect_at=7, incorrect_by='Kim', incorrect_served_base=6"
	id := remember(t, b, sam, "flagged")
	setAccess(t, b, id, flag)
	before := stats(t, b, id)
	if err := b.ClearMemoryIncorrect(sam, id); err != nil {
		t.Fatal(err)
	}
	want := before
	want.IncorrectAt, want.IncorrectBy, want.IncorrectServedBase = nil, nil, 0
	if got := stats(t, b, id); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	pid := remember(t, b, sam, "flagged and pruned")
	setAccess(t, b, pid, flag+", pruned_at=5, pruned_reason='incorrect'")
	if err := b.ClearMemoryIncorrect(sam, pid); err != nil {
		t.Fatal(err)
	}
	if s := stats(t, b, pid); s.IncorrectAt != nil || s.IncorrectBy != nil || s.PrunedAt == nil || s.PrunedReason == nil {
		t.Fatalf("pruned memory: %+v", s)
	}

	err := b.ClearMemoryIncorrect(sam, id)
	wantCode(t, err, "validation")
	var be *Error
	if !errors.As(err, &be) || be.Message != fmt.Sprintf("memory %d is not flagged incorrect", id) {
		t.Fatalf("err = %v", err)
	}
	gone := remember(t, b, sam, "gone")
	if err := b.DeleteMemory(sam, gone, ""); err != nil {
		t.Fatal(err)
	}
	wantCode(t, b.ClearMemoryIncorrect(sam, gone), "not_found")
	wantCode(t, b.RestoreMemory(sam, gone), "not_found")
}

// TestDeleteMemoryAny (#53): deletes pruned and unpruned memories through the
// hook, refuses tasks, replays as not_found, and honors a refusing hook.
func TestDeleteMemoryAny(t *testing.T) {
	b, sam, _ := memBus(t)
	pruned := remember(t, b, sam, "pruned")
	pruneRaw(t, b, pruned, "noise")
	plain := remember(t, b, sam, "plain")
	for _, id := range []int64{pruned, plain} {
		calls := b.inspectCalls.Load()
		if err := b.DeleteMemoryAny(sam, id); err != nil {
			t.Fatal(err)
		}
		if got := b.inspectCalls.Load(); got != calls+1 {
			t.Fatalf("inspectCalls = %d, want %d", got, calls+1)
		}
		var tomb, rows int
		if err := b.db.QueryRow("SELECT tombstone FROM messages WHERE memory_id=?", id).Scan(&tomb); err != nil || tomb != 1 {
			t.Fatalf("tombstone = %d, %v", tomb, err)
		}
		if err := b.db.QueryRow("SELECT COUNT(*) FROM memory_access WHERE memory_id=?", id).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("memory_access rows = %d, %v", rows, err)
		}
		wantCode(t, b.DeleteMemoryAny(sam, id), "not_found")
	}

	if _, err := b.CreateChannel(sam, "tasks/w", "memory"); err != nil {
		t.Fatal(err)
	}
	tk, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/w", Subject: "job"})
	if err != nil {
		t.Fatal(err)
	}
	calls := b.inspectCalls.Load()
	wantCode(t, b.DeleteMemoryAny(sam, tk.ID), "validation")
	if got := b.inspectCalls.Load(); got != calls+1 {
		t.Fatalf("task id: inspectCalls = %d, want %d (the hook runs before the task check)", got, calls+1)
	}
	wantCode(t, b.RestoreMemory(sam, tk.ID), "validation")
	wantCode(t, b.ClearMemoryIncorrect(sam, tk.ID), "validation")
	for _, err := range []error{b.RestoreMemory(sam, tk.ID), b.ClearMemoryIncorrect(sam, tk.ID)} {
		var be *Error
		if !errors.As(err, &be) || !strings.Contains(be.Message, "is a task list") {
			t.Fatalf("task id error = %v, want the task-list text", err)
		}
	}

	keep := remember(t, b, sam, "keep")
	b.cfg.InspectionCommand = []string{hookScript(t, `cat >/dev/null; echo '{"allow": false, "reason": "no"}'`)}
	b.cfg.InspectionTimeoutSeconds = 5
	wantCode(t, b.DeleteMemoryAny(sam, keep), "inspection_rejected")
	if _, err := b.GetMemory(sam, keep); err != nil {
		t.Fatalf("memory refused by the hook is gone: %v", err)
	}
}

// TestExportMemories (#53): live memories with an access row, pruned ones
// included, no tasks, deleted memories or chat; read-only.
func TestExportMemories(t *testing.T) {
	b, sam, _ := memBus(t)
	if _, err := b.CreateChannel(sam, "tasks/w", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel(sam, "chat", "ordinary"); err != nil {
		t.Fatal(err)
	}
	a := *sendTagged(t, b, sam, "mem", "alpha", "kind:gotcha").MemoryID
	if _, err := b.db.Exec("UPDATE messages SET subject='S' WHERE memory_id=?", a); err != nil {
		t.Fatal(err)
	}
	bb := remember(t, b, sam, "beta")
	if _, err := b.EditMemory(sam, EditInput{ID: bb, Content: "beta2"}); err != nil {
		t.Fatal(err)
	}
	c := remember(t, b, sam, "gamma")
	if err := b.DeleteMemory(sam, c, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/w", Subject: "job"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "chat", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	pruneRaw(t, b, bb, "noise")
	sa, sb := stats(t, b, a), stats(t, b, bb)

	ids := func(l []MemoryExport) []int64 {
		out := []int64{}
		for _, e := range l {
			out = append(out, e.MemoryID)
		}
		return out
	}
	all, err := b.ExportMemories(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(all); !reflect.DeepEqual(got, []int64{a, bb}) {
		t.Fatalf("ids %v, want [%d %d]", got, a, bb)
	}
	if !reflect.DeepEqual(all[0].Tags, []string{"kind:gotcha"}) || all[0].Subject != "S" || all[0].Channel != "mem" || all[0].Content != "alpha" {
		t.Fatalf("a = %+v", all[0])
	}
	if all[1].Revision != 2 || all[1].Tags == nil || len(all[1].Tags) != 0 || all[1].PrunedReason == nil || *all[1].PrunedReason != "noise" {
		t.Fatalf("b = %+v", all[1])
	}
	only, err := b.ExportMemories(true)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(only); !reflect.DeepEqual(got, []int64{bb}) {
		t.Fatalf("pruned ids %v, want [%d]", got, bb)
	}
	if !reflect.DeepEqual(stats(t, b, a), sa) || !reflect.DeepEqual(stats(t, b, bb), sb) {
		t.Fatal("export changed the access rows")
	}
}
