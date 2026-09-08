package bus

import (
	"context"
	"strings"
	"testing"
	"time"
)

// waitEmbed blocks until any in-flight embedSoon background pass has
// finished, so tests can assert on committed embeddings deterministically.
func (b *Bus) waitEmbed() {
	b.embedMu.Lock()
	b.embedMu.Unlock()
}

func fill(t *testing.T, b *Bus, as, ch string, n int, size int) {
	t.Helper()
	for i := 0; i < n; i++ {
		b.limits = newLimiter(b.cfg) // never rate limit in tests
		if _, err := b.Send(as, SendInput{Channel: ch, Content: strings.Repeat("x", size)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgeCleanupRecordsBoundary(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	b.CreateChannel(sam, "mem", "memory")
	fill(t, b, sam, "dev", 3, 10)
	b.Send(sam, SendInput{Channel: "mem", Content: "keep me"})
	b.Now = func() time.Time { return time.Now().Add(169 * time.Hour) }
	b.Send(sam, SendInput{Channel: "dev", Content: "fresh"})
	b.Tick(context.Background())
	var n int64
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE channel='dev'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("aged rows not removed: %d", n)
	}
	var ev int64
	if err := b.db.QueryRow("SELECT evicted_before_seq FROM channels WHERE name='dev'").Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if ev != 4 {
		t.Fatalf("evicted_before_seq=%d", ev)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE channel='mem'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("memory must survive age cleanup")
	}
}

func TestTombstonePurgeAndStaleSessions(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "v2"})
	b.Tick(context.Background())
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE memory_id=?", *c.MemoryID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatal("young tombstone purged")
	}
	b.Now = func() time.Time { return time.Now().Add(73 * time.Hour) }
	b.Tick(context.Background())
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE memory_id=?", *c.MemoryID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("old tombstone not purged")
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("stale session not removed")
	}
}

func TestCapacityEvictionAndExhaustion(t *testing.T) {
	b := newTestBus(t)
	b.cfg.SQLiteBudgetMiB = 64 // minimum; usage is measured in pages so we shrink via a test hook
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	b.CreateChannel(sam, "mem", "memory")
	fill(t, b, sam, "dev", 200, 4000)
	before, _ := b.Usage()
	b.budgetOverride = before / 2
	if err := b.checkCapacity(); err != nil {
		t.Fatalf("inline eviction must succeed: %v", err)
	}
	after, _ := b.Usage()
	if after >= before {
		t.Fatalf("no eviction: %d -> %d", before, after)
	}
	var ev int64
	if err := b.db.QueryRow("SELECT evicted_before_seq FROM channels WHERE name='dev'").Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if ev == 0 {
		t.Fatal("eviction must record channel boundary")
	}
	// Fill the budget with memories, which cannot be evicted.
	for i := 0; i < 50; i++ {
		b.limits = newLimiter(b.cfg)
		b.Send(sam, SendInput{Channel: "mem", Content: strings.Repeat("m", 4000)})
	}
	b.budgetOverride = 4096
	err := b.checkCapacity()
	if err == nil || !strings.Contains(err.Error(), "capacity_exhausted") {
		t.Fatalf("want capacity_exhausted, got %v", err)
	}
	b.Tick(context.Background())
	var notice string
	if err := b.db.QueryRow("SELECT message FROM notices WHERE kind='capacity'").Scan(&notice); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "sqlite_budget_mib") || !strings.Contains(notice, b.cfg.Path) {
		t.Fatalf("notice missing budget or config path: %q", notice)
	}
	b.budgetOverride = 0
	b.Tick(context.Background())
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM notices").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("notice must clear when under budget")
	}
}

func TestLeaseAllowsOneProcess(t *testing.T) {
	b := newTestBus(t)
	other, _ := Open(b.cfg, b.log)
	defer other.Close()
	if !b.takeLease() {
		t.Fatal("first take must succeed")
	}
	if other.takeLease() {
		t.Fatal("second process must not take a live lease")
	}
	if !b.takeLease() {
		t.Fatal("owner may renew")
	}
	other.Now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	if !other.takeLease() {
		t.Fatal("expired lease must be takeable")
	}
}
