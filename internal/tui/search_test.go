package tui

import (
	"strings"
	"testing"
)

func TestSearchFindsMessagesAndJumps(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "the release script")
	r := f.agentSend(t, "notes", "release procedure memory")
	f.receive(t)
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
