package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
)

func TestSearchFindsMessagesAndJumps(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "the release script")
	f.receive(t)
	// dev-notes is never received live below (drainAndAck instead of receive):
	// the model has not visited it yet, so jumpTo's first-time load must
	// pull the whole channel via History, not just the page before the hit.
	for i := 0; i < 3; i++ {
		f.agentSend(t, "dev-notes", fmt.Sprintf("before %d", i))
	}
	r := f.agentSend(t, "dev-notes", "release procedure memory")
	for i := 0; i < 2; i++ {
		f.agentSend(t, "dev-notes", fmt.Sprintf("after %d", i))
	}
	f.drainAndAck(t)
	f.key("esc")
	f.key("/")
	if f.m.mode != modeSearch || f.m.search.mode != "text" {
		t.Fatalf("mode=%v search mode=%q", f.m.mode, f.m.search.mode)
	}
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 2 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	v := f.m.View()
	if !strings.Contains(v, "2 results") || !strings.Contains(v, "release procedure") {
		t.Fatalf("view:\n%s", v)
	}
	// Move to the dev-notes hit and open it.
	for i, h := range f.m.search.hits {
		if h.Seq == r.Seq {
			f.m.search.cursor = i
		}
	}
	f.key("enter")
	if f.m.mode != modeNormal || f.m.selected().Name != "dev-notes" {
		t.Fatalf("enter must jump to the hit's channel: mode=%v sel=%v", f.m.mode, f.m.selected())
	}
	if f.m.cursor < 0 || f.m.msgs["dev-notes"][f.m.cursor].Seq != r.Seq {
		t.Fatalf("cursor must sit on the hit, cursor=%d", f.m.cursor)
	}
	if got, want := len(f.m.msgs["dev-notes"]), 6; got != want {
		t.Fatalf("jumpTo must load both the latest page and the page before the hit, leaving no hole: got %d messages, want %d", got, want)
	}
}

// TestSearchJumpFromSessionsPaneLeavesSessions guards a regression: jumping
// to a channel hit while the sessions pane held the selection used to leave
// sessSel set, so the stream stayed on the session's inbox instead of the
// hit's channel.
func TestSearchJumpFromSessionsPaneLeavesSessions(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "the release script")
	f.receive(t)
	f.key("esc")
	f.toSessions()
	if f.m.pane() != paneSessions {
		t.Fatalf("setup: pane=%v", f.m.pane())
	}
	f.key("/")
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	f.key("enter")
	if f.m.sessSel != -1 || f.m.pane() == paneSessions {
		t.Fatalf("jumping to a channel hit must leave the sessions pane: sessSel=%d pane=%v", f.m.sessSel, f.m.pane())
	}
	if got := f.m.selected(); got == nil || got.Name != "dev" {
		t.Fatalf("must land on dev, got %v", got)
	}
}

// TestSearchJumpToADMHitSelectsTheSession: a hit inside a DM inbox is never
// in m.channels, so jumping to it must select that identity's session in the
// sessions pane instead of toasting "not in the rail yet".
func TestSearchJumpToADMHitSelectsTheSession(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd()) // populates m.dms and sessionNames()
	r := f.agentSend(t, "dm/Sam", "note about the release")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 1 || f.m.search.hits[0].Channel != "dm/Sam" {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	f.key("enter")
	if f.m.mode != modeNormal || f.m.sessSel < 0 || f.m.selName() != "dm/Sam" {
		t.Fatalf("jumping to a DM hit must select that session: mode=%v sessSel=%d sel=%q", f.m.mode, f.m.sessSel, f.m.selName())
	}
	if f.m.cursor < 0 || f.m.msgs["dm/Sam"][f.m.cursor].Seq != r.Seq {
		t.Fatalf("cursor must sit on the hit, cursor=%d", f.m.cursor)
	}
}

func TestSearchTabCyclesModeAndSemanticUnavailableShowsBadge(t *testing.T) {
	f := newFixture(t)
	f.m.c.cfg.EmbeddingEndpoint = "http://127.0.0.1:9/v1/embeddings"
	f.key("esc")
	f.key("/")
	if f.m.search.mode != "both" {
		t.Fatalf("with an endpoint the default mode is both, got %q", f.m.search.mode)
	}
	f.key("tab")
	if f.m.search.mode != "text" {
		t.Fatalf("tab: both -> text, got %q", f.m.search.mode)
	}
	f.key("tab")
	f.key("tab")
	if f.m.search.mode != "both" {
		t.Fatalf("tab cycles text -> semantic -> both, got %q", f.m.search.mode)
	}
	f.m.search.textOnly = true
	if v := f.m.View(); !strings.Contains(v, "text only") {
		t.Fatalf("badge missing:\n%s", v)
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes the overlay")
	}
}

func TestSearchTabInvalidatesLastRunAndRerunsOnEnter(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "release dev-notes")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if !f.m.search.ran || f.m.search.mode != "text" {
		t.Fatalf("expected the initial text search to run, mode=%q ran=%v", f.m.search.mode, f.m.search.ran)
	}
	f.key("tab")
	if f.m.search.mode != "semantic" {
		t.Fatalf("tab: text -> semantic, got %q", f.m.search.mode)
	}
	if f.m.search.ran {
		t.Fatal("tab must invalidate the last run so enter re-runs instead of opening the hit under the cursor")
	}
	f.key("enter")
	if f.m.mode != modeSearch {
		t.Fatalf("enter after tab must re-run the search, not open a hit; mode=%v", f.m.mode)
	}
	if !f.m.search.ran {
		t.Fatal("expected a new search to have run")
	}
	// No embedding endpoint is configured, so the semantic leg of the new
	// search is unavailable: textOnly and the sticky semanticDown flag
	// must both be set, and the overlay must show the badge.
	if !f.m.search.textOnly || !f.m.search.semanticDown {
		t.Fatalf("expected textOnly and semanticDown after a semantic search with no endpoint, got textOnly=%v semanticDown=%v", f.m.search.textOnly, f.m.search.semanticDown)
	}
	if v := f.m.View(); !strings.Contains(v, "text only") {
		t.Fatalf("expected the text-only badge in the overlay:\n%s", v)
	}
}

func TestSearchHitListWindowsToFitTheOverlay(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 10; i++ {
		f.agentSend(t, "dev", fmt.Sprintf("release note %d", i))
	}
	f.receive(t)
	f.m.height = 12
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 10 {
		t.Fatalf("hits=%d err=%v", len(f.m.search.hits), f.m.search.err)
	}
	for i := 0; i < len(f.m.search.hits)-1; i++ {
		f.key("down")
	}
	if f.m.search.cursor != len(f.m.search.hits)-1 {
		t.Fatalf("cursor=%d want %d", f.m.search.cursor, len(f.m.search.hits)-1)
	}
	f.m.search.textOnly = true
	v := f.m.View()
	if !strings.Contains(v, "text only") {
		t.Fatalf("text-only badge must render alongside a full hit list:\n%s", v)
	}
	last := f.m.search.hits[len(f.m.search.hits)-1]
	firstLine := strings.SplitN(last.Content, "\n", 2)[0]
	if !strings.Contains(v, firstLine) {
		t.Fatalf("overlay must show the hit under the cursor even when the list is windowed:\n%s", v)
	}
	if lines := strings.Count(v, "\n") + 1; lines > f.m.height {
		t.Fatalf("overlay must fit within height=%d, rendered %d lines:\n%s", f.m.height, lines, v)
	}
}

// TestSearchHitShowsBusForEmptySender covers Minor 4 of the final review: a
// tick reclaim writes its revision with an empty sender (design: "the TUI
// shows it as bus"), reachable through search since text search finds task
// rows. The lease is set to expire rather than killing the owner's session,
// so the reclaim is deterministic without touching the bus's private state.
func TestSearchHitShowsBusForEmptySender(t *testing.T) {
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	other, pat := agent(t, cfg, "Pat")
	if _, err := ab.CreateChannel(sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	tk, err := ab.TaskCreate(sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "zzyzx unique subject"})
	if err != nil {
		t.Fatal(err)
	}
	now := ab.Now()
	lease := now.Add(50 * time.Millisecond).UnixMilli()
	if _, err := other.TaskClaim(pat, tk.ID, lease, ""); err != nil {
		t.Fatal(err)
	}
	future := func() time.Time { return now.Add(2 * time.Second) }
	ab.Now, other.Now = future, future
	ab.Tick(context.Background())

	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	f := &fixture{c: c, ab: ab, sam: sam, m: New(c, LoadTheme(cfg, io.Discard))}
	f.m.width, f.m.height = 100, 32
	f.run(f.m.Init())
	f.key("esc")
	f.key("/")
	f.key("zzyzx")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	v := f.m.View()
	if !strings.Contains(v, "bus") {
		t.Fatalf("reclaim's empty sender must show as bus:\n%s", v)
	}
}

// TestSearchJumpToTaskChannelLandsCursorOnTask covers the review finding on
// #16: on a task channel m.cursor indexes m.tasks[ch] (renderTasks' tree
// order), not rows(ch) (raw revisions in seq order); a search jump must
// resolve the hit to its task by id, not by the row index of its raw
// revision. aaa gets a later revision (a claim) after bbb/target are
// created, so its live seq becomes the newest: that pushes it to the end of
// rows(ch)'s seq-ordered rows while it stays first in m.tasks[ch]'s
// rank-ordered tree, the divergence a row-index/task-index mixup needs to
// surface a wrong answer (verified against the pre-fix code: cursor landed
// on "bbb", not the "zzyzx" target). The channel is never visited before
// the jump, so m.tasks["tasks/work"] is still empty when jumpTo's
// placeCursor call happens -- this also exercises pendingTaskCursor, the
// tasksMsg handler placing the cursor once the tree loads.
func TestSearchJumpToTaskChannelLandsCursorOnTask(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	aaa, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "aaa first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "bbb second"}); err != nil {
		t.Fatal(err)
	}
	target, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "zzyzx target task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskClaim(f.sam, aaa.ID, 0, ""); err != nil { // aaa's revision is now the newest
		t.Fatal(err)
	}
	f.run(f.m.statusCmd()) // learns tasks/work exists; never selects or loads it
	f.key("/")
	f.key("zzyzx")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	f.key("enter")
	if f.m.mode != modeNormal || f.m.selected() == nil || f.m.selected().Name != "tasks/work" {
		t.Fatalf("enter must jump to the hit's channel: mode=%v sel=%v", f.m.mode, f.m.selected())
	}
	got, ok := f.m.cursorTask()
	if !ok || got.ID != target.ID {
		t.Fatalf("cursor must land on the hit task, got %+v ok=%v", got, ok)
	}
}

// TestSearchJumpToDeepTaskScrollsCursorIntoView: landing the cursor on a
// task far down a long tree (via pendingTaskCursorID, since the channel is
// never visited before the jump) must scroll it into view, the same as a
// direct cursor move does.
func TestSearchJumpToDeepTaskScrollsCursorIntoView(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: fmt.Sprintf("filler %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	target, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "zzyzx target task"})
	if err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd()) // never visits tasks/work before the jump
	f.key("/")
	f.key("zzyzx")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	f.key("enter")
	got, ok := f.m.cursorTask()
	if !ok || got.ID != target.ID {
		t.Fatalf("cursor must land on the hit task, got %+v ok=%v", got, ok)
	}
	if f.m.stream.YOffset == 0 {
		t.Fatalf("cursor at the bottom of a %d-task list must scroll into view: YOffset=%d cursorLine=%d height=%d",
			41, f.m.stream.YOffset, f.m.cursorLine, f.m.stream.Height)
	}
}

// TestPendingTaskCursorIgnoredIfChannelChangedBeforeTasksArrive: a
// tasksMsg for ch=X arriving after the user has already navigated away to
// another channel must not steal the cursor (which belongs to whatever
// channel is now selected); it must still clear the pending fields, since
// that pending request has now been answered.
func TestPendingTaskCursorIgnoredIfChannelChangedBeforeTasksArrive(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/x", "memory"); err != nil {
		t.Fatal(err)
	}
	target, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/x", Subject: "target"})
	if err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "dev")
	prevCursor := f.m.cursor

	// Simulate the race: a jump into tasks/x set the pending fields, then
	// (unlike the normal showSelected reset) they're still set when the
	// response for tasks/x finally arrives, because "dev" is now selected.
	f.m.pendingTaskCursorCh, f.m.pendingTaskCursorID = "tasks/x", target.ID
	f.send(tasksMsg{ch: "tasks/x", tasks: []bus.TaskSummary{{ID: target.ID, Subject: "target"}}})

	if f.m.cursor != prevCursor {
		t.Fatalf("cursor changed from %d to %d: a pending target for an unselected channel must not move it", prevCursor, f.m.cursor)
	}
	if f.m.pendingTaskCursorCh != "" || f.m.pendingTaskCursorID != 0 {
		t.Fatalf("pending fields not cleared after consuming tasksMsg for %q: ch=%q id=%d", "tasks/x", f.m.pendingTaskCursorCh, f.m.pendingTaskCursorID)
	}
}
