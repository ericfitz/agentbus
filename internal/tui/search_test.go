package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestSearchFindsMessagesAndJumps(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "the release script")
	f.receive(t)
	// notes is never received live below (drainAndAck instead of receive):
	// the model has not visited it yet, so jumpTo's first-time load must
	// pull the whole channel via History, not just the page before the hit.
	for i := 0; i < 3; i++ {
		f.agentSend(t, "notes", fmt.Sprintf("before %d", i))
	}
	r := f.agentSend(t, "notes", "release procedure memory")
	for i := 0; i < 2; i++ {
		f.agentSend(t, "notes", fmt.Sprintf("after %d", i))
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
	// Move to the notes hit and open it.
	for i, h := range f.m.search.hits {
		if h.Seq == r.Seq {
			f.m.search.cursor = i
		}
	}
	f.key("enter")
	if f.m.mode != modeNormal || f.m.selected().Name != "notes" {
		t.Fatalf("enter must jump to the hit's channel: mode=%v sel=%v", f.m.mode, f.m.selected())
	}
	if f.m.cursor < 0 || f.m.msgs["notes"][f.m.cursor].Seq != r.Seq {
		t.Fatalf("cursor must sit on the hit, cursor=%d", f.m.cursor)
	}
	if got, want := len(f.m.msgs["notes"]), 6; got != want {
		t.Fatalf("jumpTo must load both the latest page and the page before the hit, leaving no hole: got %d messages, want %d", got, want)
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
	f.agentSend(t, "dev", "release notes")
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
