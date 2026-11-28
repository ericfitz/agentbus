package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
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
	r := f.patSend(t, "dm/Sam", "note about the release")
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
	for _, x := range []*bus.Bus{ab, other} {
		x.Mono = func() time.Duration { return x.Now().Sub(time.Unix(0, 0)) } // not a sleep
	}
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

func (f *fixture) agentSendTagged(t *testing.T, ch, content string, tags ...string) {
	t.Helper()
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: ch, Content: content, Tags: tags}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchTagTokensFilterHits(t *testing.T) {
	f := newFixture(t)
	f.agentSendTagged(t, "dev", "release one", "deployment", "failed")
	f.agentSendTagged(t, "dev", "release two", "deployment")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("tag:deployment tag:failed release")
	f.key("enter")
	if len(f.m.search.hits) != 1 || f.m.search.hits[0].Content != "release one" {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	if v := f.m.View(); !strings.Contains(v, "tags deployment failed") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestSearchTagOnlyLine(t *testing.T) {
	f := newFixture(t)
	f.agentSendTagged(t, "dev", "release one", "deployment", "failed")
	f.agentSendTagged(t, "dev", "release two", "deployment")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("tag:deployment")
	f.key("enter")
	h := f.m.search.hits
	if len(h) != 2 || h[0].Content != "release two" || h[1].Content != "release one" {
		t.Fatalf("hits=%+v err=%v", h, f.m.search.err)
	}
}

func TestSearchInvalidTagShowsErrorThenReruns(t *testing.T) {
	f := newFixture(t)
	f.agentSendTagged(t, "dev", "release one", "deployment")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("tag:bad_tag x")
	f.key("enter")
	if f.m.search.err == nil || !f.m.search.ran || len(f.m.search.hits) != 0 {
		t.Fatalf("err=%v ran=%v hits=%+v", f.m.search.err, f.m.search.ran, f.m.search.hits)
	}
	if v := f.m.View(); !strings.Contains(v, "bad_tag") {
		t.Fatalf("view must show the error:\n%s", v)
	}
	f.key("ctrl+u")
	f.key("tag:deployment release")
	f.key("enter")
	if f.m.mode != modeSearch || f.m.search.err != nil || len(f.m.search.hits) != 1 {
		t.Fatalf("mode=%v err=%v hits=%+v", f.m.mode, f.m.search.err, f.m.search.hits)
	}
}

// A tags-only search never contacts the embedding endpoint, so it must not
// clear the unreachable flag a worded search set.
func TestSearchTagsOnlyKeepsSemanticDown(t *testing.T) {
	f := newFixture(t)
	f.agentSendTagged(t, "dev", "release one", "deployment")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("tab")
	f.key("tab")
	if f.m.search.mode != "both" {
		t.Fatalf("mode %q", f.m.search.mode)
	}
	f.key("release")
	f.key("enter")
	if !f.m.search.semanticDown {
		t.Fatal("worded search with no endpoint must set semanticDown")
	}
	f.key("ctrl+u")
	f.key("tag:deployment")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	if !f.m.search.semanticDown {
		t.Fatal("a tags-only search must leave semanticDown set")
	}
	f.key("ctrl+u")
	f.key("tag:deployment *")
	f.key("enter")
	if !f.m.search.semanticDown {
		t.Fatal("star-only text with a tag is tags-only too")
	}
}

// A line that parses to nothing must not become the remembered query, or the
// next enter would jump to the previous search's hit.
func TestSearchEmptyQuotesDoNotArmStaleHit(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "release dev-notes")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%d", len(f.m.search.hits))
	}
	f.key("ctrl+u")
	f.key(`""`)
	f.key("enter")
	f.key("enter")
	if f.m.mode != modeSearch {
		t.Fatalf("enter on an empty-quotes line must not jump; mode=%v", f.m.mode)
	}
}

func TestSearchChannelTokenRejectsUnopenableChannels(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd()) // populates sessionNames()
	f.agentSend(t, "dev", "release note")
	f.receive(t)
	f.key("esc")
	for _, line := range []string{"channel:nope release", "channel:dm/NoSuchSession release"} {
		f.key("/")
		f.key(line)
		cmd := f.m.runSearch()
		if cmd != nil {
			t.Fatalf("%q: an unopenable channel must not run a search", line)
		}
		if f.m.search.err == nil || !f.m.search.ran || len(f.m.search.hits) != 0 {
			t.Fatalf("%q: err=%v ran=%v hits=%+v", line, f.m.search.err, f.m.search.ran, f.m.search.hits)
		}
		if v := f.m.View(); !strings.Contains(v, "not a channel you can open") {
			t.Fatalf("%q: view must show the error:\n%s", line, v)
		}
		f.key("esc")
	}
}

func TestSearchChannelTokenScopesHitsAndTitle(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "release alpha")
	f.agentSend(t, "dev-notes", "release beta")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("channel:dev release")
	f.key("enter")
	h := f.m.search.hits
	if f.m.search.err != nil || len(h) != 1 || h[0].Channel != "dev" {
		t.Fatalf("hits=%+v err=%v", h, f.m.search.err)
	}
	if v := f.m.View(); !strings.Contains(v, "in dev") {
		t.Fatalf("title must show the channel scope:\n%s", v)
	}
	f.key("ctrl+u")
	f.key("in:chat release")
	f.key("enter")
	if v := f.m.View(); !strings.Contains(v, "in chat") || strings.Contains(v, "in dev") {
		t.Fatalf("title must show the kinds scope:\n%s", v)
	}
	f.key("ctrl+u")
	f.key("in:chat in:tasks release")
	f.key("enter")
	if v := f.m.View(); !strings.Contains(v, "in chat, tasks") {
		t.Fatalf("title must list the kinds:\n%s", v)
	}
	f.key("ctrl+u")
	f.key("release")
	f.key("enter")
	if v := f.m.View(); strings.Contains(v, " · in ") {
		t.Fatalf("default scope must add nothing to the title:\n%s", v)
	}
}

func TestSearchInTasksOpensTheTaskInTheTree(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	parent, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "parent thing"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "zzyzx deep child", Parent: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.key("/")
	f.key("in:tasks zzyzx")
	f.key("enter")
	h := f.m.search.hits
	if f.m.search.err != nil || len(h) != 1 || h[0].MemoryID == nil || *h[0].MemoryID != target.ID {
		t.Fatalf("hits=%+v err=%v", h, f.m.search.err)
	}
	f.key("enter")
	if f.m.mode != modeNormal || f.m.selName() != "tasks/work" {
		t.Fatalf("mode=%v sel=%q", f.m.mode, f.m.selName())
	}
	got, ok := f.m.cursorTask()
	if !ok || got.ID != target.ID {
		t.Fatalf("task tree cursor must land on the hit task, got %+v ok=%v", got, ok)
	}
	if len(f.m.msgs["tasks/work"]) != 0 {
		t.Fatalf("a task hit must not load message rows: %+v", f.m.msgs["tasks/work"])
	}
}

// A line of scope tokens with no word and no tag has nothing to match: it
// shows an error through the parse-error path and clears the old hits.
func TestSearchScopeOnlyLineShowsErrorAndClearsHits(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "release alpha")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("setup: hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	for _, line := range []string{"in:tasks", "channel:dev", "in:chat channel:dev"} {
		f.key("ctrl+u")
		f.key(line)
		f.key("enter")
		if f.m.search.err == nil || !f.m.search.ran || len(f.m.search.hits) != 0 {
			t.Fatalf("%q: err=%v ran=%v hits=%+v", line, f.m.search.err, f.m.search.ran, f.m.search.hits)
		}
		if v := f.m.View(); !strings.Contains(v, "add a word or tag:") {
			t.Fatalf("%q: view must show the error:\n%s", line, v)
		}
		// Editing the line to add a word searches again.
		f.key("ctrl+u")
		f.key("release")
		f.key("enter")
		if f.m.search.err != nil || len(f.m.search.hits) != 1 {
			t.Fatalf("%q: rerun err=%v hits=%+v", line, f.m.search.err, f.m.search.hits)
		}
	}
}

// A session's DM inbox is a valid channel: scope.
func TestSearchChannelTokenAcceptsASessionInbox(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd()) // populates sessionNames()
	f.agentSend(t, "dev", "release in the channel")
	f.patSend(t, "dm/Sam", "note about the release")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("channel:dm/Sam release")
	f.key("enter")
	h := f.m.search.hits
	if f.m.search.err != nil || len(h) != 1 || h[0].Channel != "dm/Sam" {
		t.Fatalf("hits=%+v err=%v", h, f.m.search.err)
	}
	if v := f.m.View(); !strings.Contains(v, "in dm/Sam") {
		t.Fatalf("title must show the inbox scope:\n%s", v)
	}
}

func TestSearchOverlayMarksPrunedAndIncorrect(t *testing.T) {
	f := newFixture(t)
	p := f.agentSend(t, "dev-notes", "zebra pruned note")
	g := f.agentSend(t, "dev-notes", "zebra flagged note")
	if _, err := rawDB(t, f.c.cfg).Exec("UPDATE memory_access SET pruned_at=?, pruned_reason='noise' WHERE memory_id=?", time.Now().UnixMilli(), *p.MemoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.MemoryFeedback(f.sam, []bus.FeedbackItem{{MemoryID: *g.MemoryID, Verdict: "INCORRECT"}}); err != nil {
		t.Fatal(err)
	}
	f.key("esc")
	f.key("/")
	f.key("zebra")
	f.key("enter")
	if len(f.m.search.hits) != 2 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	seen := 0
	for _, l := range strings.Split(ansi.Strip(f.m.View()), "\n") {
		l = strings.TrimRight(strings.Trim(l, "│ "), " ")
		switch {
		case strings.Contains(l, "pruned note"):
			seen++
			if !strings.HasSuffix(l, " pruned") || strings.Contains(l, "incorrect") {
				t.Fatalf("pruned hit line wrong: %q", l)
			}
		case strings.Contains(l, "flagged note"):
			seen++
			if !strings.HasSuffix(l, " incorrect") || strings.Contains(l, " pruned") {
				t.Fatalf("flagged hit line wrong: %q", l)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d of the 2 hit lines", seen)
	}
}
