package bus

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	wantTagText = "tag must be one of idle, planning, implementing, reviewing, waiting, blocked, needs-human (offline is set by the bus)"
	wantSubText = "subagents do not set a status; the top-level session's status covers them"
)

func wantMsg(t *testing.T, err error, code, msg string) {
	t.Helper()
	wantCode(t, err, code)
	var be *Error
	if !errors.As(err, &be) || be.Message != msg {
		t.Fatalf("message = %v, want %q", err, msg)
	}
}

func TestSetStatusValidation(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	if _, err := b.RegisterWithClient("helper", "Sam", "repo", true, false, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, tag := range SettableStatusTags {
		if _, err := b.SetStatus("Sam", tag, "ok", ""); err != nil {
			t.Fatalf("tag %q: %v", tag, err)
		}
	}
	for _, tag := range []string{"", "offline", "Idle", "done", " idle", "idle "} {
		_, err := b.SetStatus("Sam", tag, "ok", "")
		wantMsg(t, err, "validation", wantTagText)
	}
	for _, text := range []string{"", "   ", "a\nb", "a\x1b[31m", strings.Repeat("é", 201)} {
		_, err := b.SetStatus("Sam", "idle", text, "")
		wantCode(t, err, "validation")
	}
	if _, err := b.SetStatus("Sam", "idle", strings.Repeat("é", 200), ""); err != nil {
		t.Fatalf("200 runes: %v", err)
	}
	for _, p := range []string{"a\nb", strings.Repeat("é", 201)} {
		_, err := b.SetStatus("Sam", "idle", "ok", p)
		wantCode(t, err, "validation")
	}
	_, err := b.SetStatus("Sam/helper", "idle", "ok", "")
	wantMsg(t, err, "validation", wantSubText)
	_, err = b.SetStatusFor("Sam/helper", "idle", "ok", "")
	wantMsg(t, err, "validation", wantSubText)
}

func TestSetStatusRevisionsAndPrune(t *testing.T) {
	b := newTestBus(t)
	b.cfg.StatusHistoryCount = 3
	reg(t, b, "Sam")
	for i := 1; i <= 5; i++ {
		if _, err := b.SetStatus("Sam", "idle", "s", ""); err != nil {
			t.Fatal(err)
		}
	}
	h, live, err := b.StatusHistory("Sam")
	if err != nil || !live || len(h) != 3 || h[0].Revision != 3 || h[2].Revision != 5 {
		t.Fatalf("history = %+v live=%v err=%v", h, live, err)
	}
	s, err := b.SetStatus("Sam", "idle", "s", "")
	if err != nil || s.Revision != 6 {
		t.Fatalf("sixth = %+v %v", s, err)
	}
}

func TestSetStatusIdenticalIsNewRevision(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	for want := int64(1); want <= 2; want++ {
		s, err := b.SetStatus("Sam", "planning", "same", "p")
		if err != nil || s.Revision != want || s.Problem != "p" {
			t.Fatalf("got %+v %v, want revision %d", s, err, want)
		}
	}
}

func TestSetStatusConcurrent(t *testing.T) {
	b := newTestBus(t)
	b.cfg.StatusHistoryCount = 20
	reg(t, b, "Sam")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.SetStatus("Sam", "idle", "s", ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	h, _, err := b.StatusHistory("Sam")
	if err != nil || len(h) != 10 {
		t.Fatalf("history = %d %v", len(h), err)
	}
	for i, s := range h {
		if s.Revision != int64(i+1) {
			t.Fatalf("revision %d at %d", s.Revision, i)
		}
	}
}

func TestSetStatusExpiredSession(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	if _, err := b.SetStatus("Sam", "idle", "old", ""); err != nil {
		t.Fatal(err)
	}
	// A reset bumps the generation, so the swept row is not restored.
	if _, err := b.db.Exec("INSERT INTO meta(key,value) VALUES('reset_generation',1) ON CONFLICT(key) DO UPDATE SET value=value+1"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DELETE FROM sessions"); err != nil {
		t.Fatal(err)
	}
	_, err := b.SetStatus("Sam", "idle", "new", "")
	wantCode(t, err, "not_registered")
	l, err := b.LatestStatus("Sam")
	if err != nil || l == nil || l.Text != "old" || l.Revision != 1 {
		t.Fatalf("latest = %+v %v", l, err)
	}
}

func TestSetStatusForNeedsLiveSession(t *testing.T) {
	b := newTestBus(t)
	_, err := b.SetStatusFor("Ghost", "idle", "x", "")
	wantCode(t, err, "not_registered")
	reg(t, b, "Sam")
	if s, err := b.SetStatusFor("Sam", "idle", "x", ""); err != nil || s.Revision != 1 {
		t.Fatalf("live: %+v %v", s, err)
	}
}

func TestStatusHistoryUnknownName(t *testing.T) {
	b := newTestBus(t)
	h, live, err := b.StatusHistory("Nobody")
	if err != nil || live || h == nil || len(h) != 0 {
		t.Fatalf("got %v %v %v", h, live, err)
	}
	if l, err := b.LatestStatus("Nobody"); l != nil || err != nil {
		t.Fatalf("latest = %v %v", l, err)
	}
}

func TestShortAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second: "0m", 3 * time.Minute: "3m", 59 * time.Minute: "59m",
		5 * time.Hour: "5h", 49 * time.Hour: "2d",
	} {
		if got := ShortAge(d); got != want {
			t.Errorf("ShortAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestResetClearsStatuses(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	if _, err := b.SetStatus("Sam", "idle", "x", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.Reset(); err != nil {
		t.Fatal(err)
	}
	if l, err := b.LatestStatus("Sam"); err != nil || l != nil {
		t.Fatalf("latest after reset = %+v %v", l, err)
	}
}

// ageStatus moves every stored status of name back by d.
func ageStatus(t *testing.T, b *Bus, name string, d time.Duration) {
	t.Helper()
	if _, err := b.db.Exec("UPDATE statuses SET set_at = set_at - ? WHERE identity=?", d.Milliseconds(), name); err != nil {
		t.Fatal(err)
	}
}

func endSession(t *testing.T, b *Bus, name string) {
	t.Helper()
	if _, err := b.db.Exec("DELETE FROM sessions WHERE sender=?", name); err != nil {
		t.Fatal(err)
	}
}

func boardMap(t *testing.T, b *Bus) map[string]BoardEntry {
	t.Helper()
	es, err := b.Board()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]BoardEntry{}
	for _, e := range es {
		m[e.Identity] = e
	}
	return m
}

func TestBoard(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Live")
	reg(t, b, "Quiet")
	reg(t, b, "Recent")
	reg(t, b, "Old")
	if _, err := b.Register("sub", "Live", "repo", true); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Live", "Recent", "Old"} {
		if _, err := b.SetStatus(n, "blocked", "text "+n, "prob"); err != nil {
			t.Fatal(err)
		}
	}
	ageStatus(t, b, "Recent", 23*time.Hour)
	ageStatus(t, b, "Old", 25*time.Hour)
	endSession(t, b, "Recent")
	endSession(t, b, "Old")

	m := boardMap(t, b)
	if e := m["Live"]; !e.Live || e.Tag != "blocked" || e.Text != "text Live" || e.Revision != 1 {
		t.Fatalf("Live = %+v", e)
	}
	if e, ok := m["Quiet"]; !ok || !e.Live || e.Tag != "" || e.Revision != 0 {
		t.Fatalf("Quiet = %+v ok=%v", e, ok)
	}
	if e := m["Recent"]; e.Live || e.Tag != StatusOffline || e.Text != "text Recent" {
		t.Fatalf("Recent = %+v", e)
	}
	if _, ok := m["Old"]; ok {
		t.Fatalf("Old (25h) present: %+v", m["Old"])
	}
	for n := range m {
		if strings.Contains(n, "/") {
			t.Fatalf("subagent %q on board", n)
		}
	}
	if len(m) != 3 {
		t.Fatalf("board = %v", m)
	}

	b.cfg.StatusOfflineHours = 0
	m = boardMap(t, b)
	if _, ok := m["Recent"]; ok || len(m) != 2 {
		t.Fatalf("offline_hours=0 board = %v", m)
	}
}

func TestBoardRename(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	if _, err := b.SetStatus("Sam", "idle", "last words", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RegisterWithClient("Pat", "", "repo", true, true, "", ""); err != nil {
		t.Fatal(err)
	}
	m := boardMap(t, b)
	if e := m["Sam"]; e.Live || e.Tag != StatusOffline || e.Text != "last words" {
		t.Fatalf("Sam = %+v", e)
	}
	if e, ok := m["Pat"]; !ok || !e.Live || e.Tag != "" || e.Revision != 0 {
		t.Fatalf("Pat = %+v ok=%v", e, ok)
	}
}

func TestDiscoverIncludesStatus(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	reg(t, b, "Pat")
	reg(t, b, "Gone")
	if _, err := b.SetStatus("Sam", "planning", "plan it", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetStatus("Gone", "idle", "bye", ""); err != nil {
		t.Fatal(err)
	}
	endSession(t, b, "Gone")
	ss, err := b.Discover(sam)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Session{}
	for _, s := range ss {
		got[s.Sender] = s
	}
	if s := got["Sam"]; s.Status == nil || s.Status.Tag != "planning" || s.Status.Text != "plan it" {
		t.Fatalf("Sam = %+v", s)
	}
	if s, ok := got["Pat"]; !ok || s.Status != nil {
		t.Fatalf("Pat = %+v ok=%v", s, ok)
	}
	if _, ok := got["Gone"]; ok {
		t.Fatal("offline name listed")
	}
}
