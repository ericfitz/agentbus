package bus

import (
	"fmt"
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
