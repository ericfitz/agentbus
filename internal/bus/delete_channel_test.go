package bus

import (
	"context"
	"strings"
	"testing"
	"time"
)

func countRows(t *testing.T, b *Bus, q string, args ...any) int {
	t.Helper()
	var n int
	if err := b.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteChannelRefusals(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	if err := b.Subscribe(sam, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, channel, except, want string }{
		{"default general", "general", "", "default channel"},
		{"default memory", "memory", "", "default channel"},
		{"missing", "nope", "", "not_found"},
		{"live subscriber", "dev", "", "active subscriber"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := b.DeleteChannel(c.channel, c.except)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want message containing %q", err, c.want)
			}
		})
	}
	if _, err := b.DeleteChannel("dev", ""); !strings.Contains(err.Error(), sam) {
		t.Fatalf("error should name the blocking sender: %v", err)
	}
}

func TestDeleteChannelDropsEverything(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	if err := b.Subscribe(sam, "mem", "now"); err != nil {
		t.Fatal(err)
	}
	fill(t, b, sam, "mem", 3, 10)
	// The excepted sender's own subscription does not block.
	got, err := b.DeleteChannel("mem", sam)
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages != 3 || got.Kind != "memory" {
		t.Fatalf("got %+v", got)
	}
	for _, q := range []string{
		"SELECT count(*) FROM channels WHERE name='mem'",
		"SELECT count(*) FROM messages WHERE channel='mem'",
		"SELECT count(*) FROM subscriptions WHERE channel='mem'",
		"SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'xxxxxxxxxx'",
	} {
		if n := countRows(t, b, q); n != 0 {
			t.Fatalf("%s = %d", q, n)
		}
	}
}

func TestDeleteChannelIgnoresStaleSubscribers(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	if err := b.Subscribe(sam, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if _, err := b.DeleteChannel("dev", ""); err != nil {
		t.Fatalf("stale subscriber should not block: %v", err)
	}
}

func TestTickReapsEmptyUnsubscribedChannels(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "empty", "ordinary")
	_, _ = b.CreateChannel(sam, "subscribed", "ordinary")
	_, _ = b.CreateChannel(sam, "full", "memory")
	if err := b.Subscribe(sam, "subscribed", "now"); err != nil {
		t.Fatal(err)
	}
	fill(t, b, sam, "full", 1, 10)
	b.Tick(context.Background())
	for name, want := range map[string]int{"empty": 0, "subscribed": 1, "full": 1, "general": 1, "memory": 1} {
		if n := countRows(t, b, "SELECT count(*) FROM channels WHERE name=?", name); n != want {
			t.Fatalf("channel %s: exists=%d want %d", name, n, want)
		}
	}
	// A channel whose only subscriber went stale is reaped once the
	// session expires, even though its subscription row lingers.
	b.Now = func() time.Time { return time.Now().Add(time.Minute) }
	b.Tick(context.Background())
	if n := countRows(t, b, "SELECT count(*) FROM channels WHERE name='subscribed'"); n != 0 {
		t.Fatal("stale-subscribed empty channel not reaped")
	}
}
