package bus

import "testing"

// ADR 0025 decision 6: a process holds one top-level name; a top-level
// register under a new name drops its other top-level rows.

func topLevelRows(t *testing.T, b *Bus) []string {
	t.Helper()
	rows, err := b.db.Query("SELECT sender FROM sessions WHERE owner=? AND instr(sender,'/')=0 ORDER BY sender", b.owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func wantRows(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("top-level rows = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("top-level rows = %v, want %v", got, want)
		}
	}
}

func TestRegisterNewTopLevelNameDropsOthers(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("alpha", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("beta", "", "", true); err != nil {
		t.Fatal(err)
	}
	wantRows(t, topLevelRows(t, b), "beta")
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	wantRows(t, topLevelRows(t, b), "beta")
	if _, ok := b.rememberedRow("alpha"); ok {
		t.Fatal("alpha still remembered")
	}
	if err := b.auth(b.db, "alpha"); err == nil {
		t.Fatal("auth(alpha) succeeded after rename")
	}
}

func TestRegisterSameNameKeepsOneRow(t *testing.T) {
	b := newTestBus(t)
	for range 2 {
		if _, err := b.Register("alpha", "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	wantRows(t, topLevelRows(t, b), "alpha")
}

func TestSubagentRegisterKeepsParentRow(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("alpha", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("helper", "alpha", "", true); err != nil {
		t.Fatal(err)
	}
	wantRows(t, topLevelRows(t, b), "alpha")
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM sessions WHERE sender='alpha/helper'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("subagent row count = %d %v", n, err)
	}
	// A rename keeps the process's subagent rows.
	if _, err := b.Register("beta", "", "", true); err != nil {
		t.Fatal(err)
	}
	wantRows(t, topLevelRows(t, b), "beta")
	if err := b.db.QueryRow("SELECT count(*) FROM sessions WHERE sender='alpha/helper'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("subagent row dropped by rename: %d %v", n, err)
	}
}

func TestRenameAndBackKeepsCursors(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("alpha", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel("alpha", "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("alpha", "dev", "now"); err != nil {
		t.Fatal(err)
	}
	cursor := func() (c int64) {
		if err := b.db.QueryRow("SELECT cursor_seq FROM subscriptions WHERE sender='alpha' AND channel='dev'").Scan(&c); err != nil {
			t.Fatalf("alpha cursor: %v", err)
		}
		return c
	}
	before := cursor()
	if _, err := b.Register("beta", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("beta", SendInput{Channel: "dev", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	reg, err := b.Register("alpha", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Sender != "alpha" || !reg.Resumed {
		t.Fatalf("re-register = %+v", reg)
	}
	if got := cursor(); got != before {
		t.Fatalf("cursor moved %d -> %d", before, got)
	}
	var pending int64
	for _, p := range reg.Pending {
		if p.Channel == "dev" {
			pending = p.Pending
		}
	}
	if pending != 1 {
		t.Fatalf("pending on dev = %d, want 1 (%+v)", pending, reg.Pending)
	}
	wantRows(t, topLevelRows(t, b), "alpha")
}

func insertForeignSession(t *testing.T, b *Bus, sender, owner string) {
	t.Helper()
	if _, err := b.db.Exec("INSERT INTO sessions(sender,context,owner,heartbeat,registered_at) VALUES(?,?,?,?,?)",
		sender, "", owner, b.nowMs(), b.nowMs()); err != nil {
		t.Fatal(err)
	}
}

// A heartbeat that restores the dropped name right after the rename commits
// must not leave a row behind.
func TestRenameRestoreAtCommitLeavesNoZombie(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("alpha", "", "", true); err != nil {
		t.Fatal(err)
	}
	old, ok := b.rememberedRow("alpha")
	if !ok {
		t.Fatal("alpha not remembered")
	}
	b.afterRegisterCommit = func() {
		if _, err := b.restore(b.db, old); err != nil {
			t.Error(err)
		}
	}
	if _, err := b.Register("beta", "", "", true); err != nil {
		t.Fatal(err)
	}
	b.afterRegisterCommit = nil
	wantRows(t, topLevelRows(t, b), "beta")
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	wantRows(t, topLevelRows(t, b), "beta")
}

func TestRenameKeepsOtherOwnersTopLevelRows(t *testing.T) {
	b := newTestBus(t)
	insertForeignSession(t, b, "gamma", "someone-else")
	if _, err := b.Register("alpha", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("beta", "", "", true); err != nil {
		t.Fatal(err)
	}
	wantRows(t, topLevelRows(t, b), "beta")
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM sessions WHERE sender='gamma'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("other owner's row count = %d %v", n, err)
	}
}

func TestRenameOntoSuffixedNameDropsOld(t *testing.T) {
	b := newTestBus(t)
	insertForeignSession(t, b, "beta", "someone-else")
	if _, err := b.Register("alpha", "", "", true); err != nil {
		t.Fatal(err)
	}
	reg, err := b.Register("beta", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Sender != "beta2" {
		t.Fatalf("sender = %q, want beta2", reg.Sender)
	}
	wantRows(t, topLevelRows(t, b), "beta2")
	if _, ok := b.rememberedRow("alpha"); ok {
		t.Fatal("alpha still remembered")
	}
}

func TestRenameAfterResetDoesNotRestoreOld(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("alpha", "", "", true); err != nil {
		t.Fatal(err)
	}
	old, _ := b.rememberedRow("alpha")
	if _, err := b.db.Exec("INSERT INTO meta(key,value) VALUES('reset_generation',1) ON CONFLICT(key) DO UPDATE SET value=value+1"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("beta", "", "", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.restore(b.db, old); err != nil || ok {
		t.Fatalf("restore(alpha) = %v %v, want false", ok, err)
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	wantRows(t, topLevelRows(t, b), "beta")
}
