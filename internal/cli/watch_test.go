package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/procs"
)

var watchLineRe = regexp.MustCompile(`^agentbus: \d+\+? new message\(s\) for \S+ on [a-z0-9/_, .-]+; call receive$`)

// syncBuf is a bytes.Buffer safe for the watch goroutine to write while the
// test reads.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type watchEnv struct {
	t    *testing.T
	cfg  config.Config
	b    *bus.Bus
	out  *syncBuf
	done chan error
	stop context.CancelFunc
}

func fastWatch(t *testing.T) {
	t.Helper()
	oldPoll, oldGap, oldHarness := bus.WaitPollInterval, watchMinGap, harnessPollInterval
	bus.WaitPollInterval, watchMinGap, harnessPollInterval = 5*time.Millisecond, 40*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { bus.WaitPollInterval, watchMinGap, harnessPollInterval = oldPoll, oldGap, oldHarness })
}

// newWatchEnv registers Pat (the watcher), Sam (the sender) and channel dev.
func newWatchEnv(t *testing.T, mutate func(*config.Config)) *watchEnv {
	t.Helper()
	fastWatch(t)
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	if mutate != nil {
		mutate(&cfg)
	}
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	for _, n := range []string{"Pat", "Sam"} {
		if _, err := b.Register(n, "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.CreateChannel("Sam", "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("Pat", "dev", "now"); err != nil {
		t.Fatal(err)
	}
	return &watchEnv{t: t, cfg: cfg, b: b, out: &syncBuf{}}
}

func (e *watchEnv) start(o WatchOptions) {
	e.t.Helper()
	o.Config, o.As, o.IdentitySource = e.cfg, "Pat", SourceAs
	o.NoHarnessWatch = true
	e.startWith(o, e.out)
}

func (e *watchEnv) startWith(o WatchOptions, out io.Writer) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.stop = cancel
	e.done = make(chan error, 2)
	go func() { e.done <- Watch(ctx, o, out) }()
	e.t.Cleanup(func() {
		cancel()
		select {
		case <-e.done:
		case <-time.After(5 * time.Second):
		}
	})
}

func (e *watchEnv) send(content string) {
	e.t.Helper()
	if _, err := e.b.Send("Sam", bus.SendInput{Channel: "dev", Content: content}); err != nil {
		e.t.Fatal(err)
	}
}

// waitLines polls until out holds n lines and returns them.
func (e *watchEnv) waitLines(n int) []string {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if lines := splitLines(e.out.String()); len(lines) >= n {
			return lines
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("want %d lines, got %q", n, e.out.String())
	return nil
}

// quiet fails if more than want lines appear within d.
func (e *watchEnv) quiet(want int, d time.Duration) {
	e.t.Helper()
	time.Sleep(d)
	if lines := splitLines(e.out.String()); len(lines) != want {
		e.t.Fatalf("want exactly %d lines, got %q", want, e.out.String())
	}
}

func (e *watchEnv) running() {
	e.t.Helper()
	select {
	case err := <-e.done:
		e.t.Fatalf("watch exited: %v", err)
	default:
	}
}

// result waits for Watch to return; ok is false if it did not within 5s. The
// result is put back so the cleanup sees the exit.
func (e *watchEnv) result() (err error, ok bool) {
	select {
	case err = <-e.done:
		e.done <- err
		return err, true
	case <-time.After(5 * time.Second):
		return nil, false
	}
}

func (e *watchEnv) cancelAndWait() error {
	e.t.Helper()
	e.stop()
	err, ok := e.result()
	if !ok {
		e.t.Fatal("watch did not stop")
	}
	return err
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestWatchRepeatedWakesFromOneProcess(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.start(WatchOptions{})
	e.quiet(0, 30*time.Millisecond)
	e.send("first")
	e.waitLines(1)
	e.running()
	e.send("second")
	lines := e.waitLines(2)
	e.running()
	for _, l := range lines {
		if l != "agentbus: 1 new message(s) for Pat on dev; call receive" {
			t.Fatalf("line = %q", l)
		}
	}
	if err := e.cancelAndWait(); !errors.Is(err, ErrWaitReplaced) {
		t.Fatalf("err = %v, want ErrWaitReplaced", err)
	}
}

func TestWatchAnnouncesExistingBacklogOnce(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.send("old")
	e.start(WatchOptions{})
	e.waitLines(1)
	// Still unreceived, but already announced: no repeat.
	e.quiet(1, 200*time.Millisecond)
}

func TestWatchDoesNotReannounceBeforeReceive(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.start(WatchOptions{})
	e.send("one")
	e.waitLines(1)
	e.quiet(1, 3*watchMinGap)
	e.send("two")
	lines := e.waitLines(2)
	if !strings.HasPrefix(lines[1], "agentbus: 1 new message(s)") {
		t.Fatalf("second line must count only the new message: %q", lines[1])
	}
}

func TestWatchPendingBatchDoesNotWake(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.send("seen")
	if r, err := e.b.Receive("Pat", bus.ReceiveInput{}); err != nil || len(r.Messages) != 1 || r.Batch == "" {
		t.Fatalf("receive: %+v %v", r, err)
	}
	e.start(WatchOptions{})
	e.quiet(0, 150*time.Millisecond)
	e.send("fresh")
	e.waitLines(1)
	e.quiet(1, 100*time.Millisecond)
}

func TestWatchFilterRejectsThenLaterMatchWakes(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.start(WatchOptions{Filter: "@Pat"})
	e.send("noise")
	e.quiet(0, 150*time.Millisecond)
	e.send("@Pat ping")
	lines := e.waitLines(1)
	if lines[0] != "agentbus: 1 new message(s) for Pat on dev; call receive" {
		t.Fatalf("line = %q (the rejected message must not be counted)", lines[0])
	}
}

func TestWatchDMWakesDespiteFilter(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.start(WatchOptions{Filter: "@nobody"})
	if _, err := e.b.Send("Sam", bus.SendInput{Channel: bus.DMChannel("Pat"), Content: "secret payload"}); err != nil {
		t.Fatal(err)
	}
	lines := e.waitLines(1)
	if lines[0] != "agentbus: 1 new message(s) for Pat on dm/Pat; call receive" {
		t.Fatalf("line = %q", lines[0])
	}
	if strings.Contains(e.out.String(), "secret") {
		t.Fatalf("content leaked: %q", e.out.String())
	}
}

func TestWatchBurstWithinGapIsOneLine(t *testing.T) {
	e := newWatchEnv(t, nil)
	if _, err := e.b.CreateChannel("Sam", "ops", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := e.b.Subscribe("Pat", "ops", "now"); err != nil {
		t.Fatal(err)
	}
	watchMinGap = 500 * time.Millisecond // wide enough that the sends below cannot span two lines
	e.start(WatchOptions{})
	e.send("a")
	e.waitLines(1) // gap starts
	e.send("b")
	if _, err := e.b.Send("Sam", bus.SendInput{Channel: "ops", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	e.send("d")
	lines := e.waitLines(2)
	if lines[1] != "agentbus: 3 new message(s) for Pat on dev, ops; call receive" {
		t.Fatalf("burst line = %q", lines[1])
	}
	e.quiet(2, 100*time.Millisecond)
}

// A tag-subscription match on a channel the watcher is not subscribed to
// wakes it despite -filter, once.
func TestWatchTagMatchWakesOnceDespiteFilter(t *testing.T) {
	e := newWatchEnv(t, nil)
	if _, err := e.b.CreateChannel("Sam", "ops", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := e.b.SubscribeTags("Pat", []string{"release"}); err != nil {
		t.Fatal(err)
	}
	e.start(WatchOptions{Filter: "@nobody"})
	if _, err := e.b.Send("Sam", bus.SendInput{Channel: "ops", Content: "shipped", Tags: []string{"release"}}); err != nil {
		t.Fatal(err)
	}
	lines := e.waitLines(1)
	if lines[0] != "agentbus: 1 new message(s) for Pat on ops; call receive" {
		t.Fatalf("line = %q", lines[0])
	}
	e.quiet(1, 3*watchMinGap)
}

func TestWatchGapSpacesLines(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.start(WatchOptions{})
	e.send("a")
	e.waitLines(1)
	first := time.Now()
	e.send("b")
	e.waitLines(2)
	if d := time.Since(first); d < watchMinGap-5*time.Millisecond {
		t.Fatalf("second line after %v, want >= %v", d, watchMinGap)
	}
}

func TestWatchPlusSuffixAtReceiveMaxCount(t *testing.T) {
	e := newWatchEnv(t, func(c *config.Config) { c.ReceiveMaxCount = 3 })
	for i := 0; i < 5; i++ {
		e.send("m")
	}
	e.start(WatchOptions{})
	lines := e.waitLines(2)
	if lines[0] != "agentbus: 3+ new message(s) for Pat on dev; call receive" {
		t.Fatalf("line = %q", lines[0])
	}
	// The rest past the cut is announced next, not dropped.
	if lines[1] != "agentbus: 2 new message(s) for Pat on dev; call receive" {
		t.Fatalf("line 2 = %q", lines[1])
	}
}

func TestWatchCanceledPrintsNothingMore(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.start(WatchOptions{})
	e.quiet(0, 30*time.Millisecond)
	if err := e.cancelAndWait(); !errors.Is(err, ErrWaitReplaced) || e.out.String() != "" {
		t.Fatalf("err = %v, out = %q", err, e.out.String())
	}
}

func TestWatchWriteErrorIsAnError(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.send("x")
	boom := errors.New("pipe closed")
	e.startWith(WatchOptions{Config: e.cfg, As: "Pat", IdentitySource: SourceAs, NoHarnessWatch: true}, errWriter{boom})
	if err, ok := e.result(); !ok || !errors.Is(err, boom) {
		t.Fatalf("err = %v (exited %v), want the write error", err, ok)
	}
}

type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWatchRejectsUnknownIdentitySource(t *testing.T) {
	e := newWatchEnv(t, nil)
	var out bytes.Buffer
	for _, src := range []string{"", "bogus"} {
		err := Watch(context.Background(), WatchOptions{Config: e.cfg, As: "Pat", IdentitySource: src, NoHarnessWatch: true}, &out)
		if err == nil || out.Len() != 0 {
			t.Fatalf("source %q: err = %v, out = %q; want an error", src, err, out.String())
		}
	}
}

func TestWatchRejectsBadFilter(t *testing.T) {
	e := newWatchEnv(t, nil)
	var out bytes.Buffer
	err := Watch(context.Background(), WatchOptions{Config: e.cfg, As: "Pat", IdentitySource: SourceAs, Filter: "(", NoHarnessWatch: true}, &out)
	if err == nil || !strings.Contains(err.Error(), "filter") {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchRecordsPidfileAndReleasesIt(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.start(WatchOptions{})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rec, ok := readPidfile(waitPidfile(e.cfg.DataDirectory, "Pat")); ok && rec.Self == selfRef() && rec.Source == SourceAs {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watch did not record itself in the wait pidfile")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = e.cancelAndWait()
	if _, ok := readPidfile(waitPidfile(e.cfg.DataDirectory, "Pat")); ok {
		t.Fatal("pidfile should be released")
	}
}

// A watch replaces a live wait (or watch) recorded for the identity.
func TestWatchReplacesLiveWait(t *testing.T) {
	e := newWatchEnv(t, nil)
	old, gone := startSleeper(t)
	writeRef(t, e.cfg.DataDirectory, "Pat", old)
	e.start(WatchOptions{})
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the older wait was not terminated")
	}
}

// A watch from the same harness replaces a wait for another identity (#39).
func TestWatchReplacesSameHarnessOtherIdentity(t *testing.T) {
	e := newWatchEnv(t, nil)
	h, hdone := startSleeper(t)
	old, gone := startSleeper(t)
	_ = hdone
	writeRecord(t, e.cfg.DataDirectory, "Old", old, h, SourceSession)
	swapTable(t, parentOf(os.Getpid(), h.Pid))
	e.startWith(WatchOptions{Config: e.cfg, As: "Pat", IdentitySource: SourceSession}, e.out)
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait from the same harness was not terminated")
	}
}

func TestWatchHarnessGone(t *testing.T) {
	e := newWatchEnv(t, nil)
	h, hdone := startSleeper(t)
	swapTable(t, parentOf(os.Getpid(), h.Pid))
	e.startWith(WatchOptions{Config: e.cfg, As: "Pat", IdentitySource: SourceAs}, e.out)
	e.send("x")
	e.waitLines(1)
	if err := killRef(h); err != nil {
		t.Fatal(err)
	}
	<-hdone
	if err, ok := e.result(); !ok || !errors.Is(err, ErrHarnessGone) {
		t.Fatalf("err = %v (exited %v), want ErrHarnessGone", err, ok)
	}
	e.send("late")
	if got := len(splitLines(e.out.String())); got != 1 {
		t.Fatalf("printed after harness gone: %q", e.out.String())
	}
}

func TestWatchNoHarnessWatchOutlivesHarness(t *testing.T) {
	e := newWatchEnv(t, nil)
	h, hdone := startSleeper(t)
	swapTable(t, parentOf(os.Getpid(), h.Pid))
	e.startWith(WatchOptions{Config: e.cfg, As: "Pat", IdentitySource: SourceAs, NoHarnessWatch: true}, e.out)
	if err := killRef(h); err != nil {
		t.Fatal(err)
	}
	<-hdone
	time.Sleep(100 * time.Millisecond)
	e.running()
	e.send("still here")
	e.waitLines(1)
}

func TestWatchOutputIsOnlyWakeLines(t *testing.T) {
	e := newWatchEnv(t, nil)
	e.start(WatchOptions{})
	for i := 0; i < 3; i++ {
		e.send(fmt.Sprintf("m%d", i))
		time.Sleep(2 * watchMinGap)
	}
	e.waitLines(3)
	for _, l := range splitLines(e.out.String()) {
		if !watchLineRe.MatchString(l) {
			t.Fatalf("stdout line %q is not a wake line", l)
		}
	}
	if !strings.HasSuffix(e.out.String(), "\n") {
		t.Fatalf("output must end in a newline: %q", e.out.String())
	}
}

// parentTable reports parent as the parent of child; all else is the real table.
type parentTable struct {
	procs.Table
	child, parent int
}

func (p parentTable) Parent(pid int) (int, error) {
	if pid == p.child {
		return p.parent, nil
	}
	return p.Table.Parent(pid)
}

func parentOf(child, parent int) procs.Table {
	return parentTable{Table: procs.System, child: child, parent: parent}
}

// swapTable installs tbl as the process table until the test's cleanups run;
// call it before starting Watch so the watch is stopped first.
func swapTable(t *testing.T, tbl procs.Table) {
	t.Helper()
	old := processTable
	processTable = tbl
	t.Cleanup(func() { processTable = old })
}
