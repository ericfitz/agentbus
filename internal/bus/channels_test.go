package bus

import "testing"

func TestChannelNameRule(t *testing.T) {
	for _, ok := range []string{"reviews", "general", "tasks/work", "tasks/a-b_c"} {
		if err := ChannelNameRule(ok); err != nil {
			t.Errorf("ChannelNameRule(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "bad/name", "tasks/", "tasks/a/b", "dm/Sam"} {
		if err := ChannelNameRule(bad); err == nil {
			t.Errorf("ChannelNameRule(%q): want error", bad)
		}
	}
}

func TestDefaultChannelsExistSurviveResetAndYieldToExisting(t *testing.T) {
	b := newTestBus(t)
	kinds := func() map[string]string {
		rows, err := b.db.Query("SELECT name, kind FROM channels")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		m := map[string]string{}
		for rows.Next() {
			var n, k string
			if err := rows.Scan(&n, &k); err != nil {
				t.Fatal(err)
			}
			m[n] = k
		}
		return m
	}
	if got := kinds(); got["general"] != "ordinary" || got["memory"] != "memory" || got["tasks"] != "memory" || len(got) != 3 {
		t.Fatalf("open must create the defaults, got %v", got)
	}
	if err := b.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := kinds(); len(got) != 3 {
		t.Fatalf("reset must recreate the defaults, got %v", got)
	}
	if _, err := b.db.Exec("UPDATE channels SET kind='ordinary' WHERE name='memory'"); err != nil {
		t.Fatal(err)
	}
	if err := b.ensureDefaults(); err != nil {
		t.Fatal(err)
	}
	if got := kinds(); got["memory"] != "ordinary" {
		t.Fatalf("an existing channel of another kind must win, got %v", got)
	}
}

func TestPrefixedNamesImplyKindAndRenameCarriesHistory(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if c, err := b.CreateChannel(sam, "memory/w", ""); err != nil || c.Kind != "memory" {
		t.Fatalf("memory/ implies memory: %v %v", c, err)
	}
	wantCode(t, func() error { _, err := b.CreateChannel(sam, "general/w", "memory"); return err }(), "validation")
	wantCode(t, func() error { _, err := b.CreateChannel(sam, "x/y", "memory"); return err }(), "validation")

	if _, err := b.CreateChannel(sam, "widgets", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "widgets", Content: "kept"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(sam, "widgets", "oldest"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameChannel("widgets", "general/widgets"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE channel='general/widgets'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("messages must move: %d %v", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE channel='general/widgets' AND sender=?", sam).Scan(&n); err != nil || n != 1 {
		t.Fatalf("subscriptions must move: %d %v", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM channels WHERE name='widgets'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("old name must be gone: %d %v", n, err)
	}
	wantCode(t, b.RenameChannel("widgets", "general/widgets"), "not_found")
	wantCode(t, b.RenameChannel("memory/w", "general/w"), "validation") // kind mismatch
	wantCode(t, b.RenameChannel("memory/w", "general/widgets"), "validation") // still a kind mismatch, checked before the exists check
}
