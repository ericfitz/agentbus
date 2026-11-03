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
	for _, tag := range []string{"", "offline", "Idle", "done"} {
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
