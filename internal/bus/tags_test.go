package bus

import (
	"slices"
	"testing"
	"time"
)

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{"Release", "bug", "release", "a_b-1"})
	if err != nil || !slices.Equal(got, []string{"a_b-1", "bug", "release"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := NormalizeTags(nil); err != nil || got != nil {
		t.Fatalf("no tags: %v %v", got, err)
	}
	for _, bad := range [][]string{{""}, {"has space"}, {"x/y"}, {"ünïcode"}, {"123456789012345678901"}} {
		if _, err := NormalizeTags(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "t" + string(rune('a'+i))
	}
	wantCode(t, func() error { _, err := NormalizeTags(eleven); return err }(), "validation")
	if _, err := NormalizeTags(append(eleven[:10], "TA")); err != nil {
		t.Fatalf("duplicates are removed before the limit applies: %v", err)
	}
}

func TestSendStoresTagsAndHistorySearchFilter(t *testing.T) {
	b, sam, kim := setupTwo(t)
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "one", Tags: []string{"Release", "bug", "release"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "two", Tags: []string{"docs"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "three"}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, func() error {
		_, err := b.Send(sam, SendInput{Channel: "dev", Content: "x", Tags: []string{"ok", "not ok"}})
		return err
	}(), "validation")
	wantCode(t, func() error {
		_, err := b.Send(sam, SendInput{Channel: "tasks", Content: "x", Tags: []string{"a"}})
		return err
	}(), "validation")

	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r.Messages) != 3 || !slices.Equal(r.Messages[0].Tags, []string{"bug", "release"}) || r.Messages[2].Tags != nil {
		t.Fatalf("receive tags: %+v %v", r.Messages, err)
	}
	h, err := b.History(sam, "dev", nil, nil, 10, "docs", "release")
	if err != nil || len(h) != 2 || h[0].Content != "one" || h[1].Content != "two" {
		t.Fatalf("history any-of filter: %+v %v", h, err)
	}
	if h, _ := b.History(sam, "dev", nil, nil, 10); len(h) != 3 || !slices.Equal(h[1].Tags, []string{"docs"}) {
		t.Fatalf("history returns tags: %+v", h)
	}
	wantCode(t, func() error { _, err := b.History(sam, "dev", nil, nil, 10, "bad tag"); return err }(), "validation")
	s, err := b.Search(sam, SearchInput{Query: "one two three", Mode: "text"})
	if err != nil || len(s.Hits) != 0 {
		t.Fatalf("implicit AND: %+v %v", s.Hits, err)
	}
	s, err = b.Search(sam, SearchInput{Query: "two", Mode: "text", Tags: []string{"docs"}})
	if err != nil || len(s.Hits) != 1 || !slices.Equal(s.Hits[0].Tags, []string{"docs"}) {
		t.Fatalf("search tags filter: %+v %v", s.Hits, err)
	}
	if s, _ := b.Search(sam, SearchInput{Query: "two", Mode: "text", Tags: []string{"release"}}); len(s.Hits) != 0 {
		t.Fatalf("search filter excludes: %+v", s.Hits)
	}
}

func TestEditMemoryTagsKeepAndClear(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	r, err := b.Send(sam, SendInput{Channel: "memory", Content: "v1", Tags: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	id := *r.MemoryID
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); !slices.Equal(m.Tags, []string{"go"}) {
		t.Fatalf("omitted tags keep the current ones: %+v", m)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v3", Tags: []string{"sqlite", "GO"}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); !slices.Equal(m.Tags, []string{"go", "sqlite"}) {
		t.Fatalf("replace: %+v", m)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v4", Tags: []string{}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); m.Tags != nil {
		t.Fatalf("empty tags clear: %+v", m)
	}
	revs, _ := b.MemoryRevisions(sam, id)
	if len(revs) != 4 || !slices.Equal(revs[0].Tags, []string{"go"}) || !slices.Equal(revs[2].Tags, []string{"go", "sqlite"}) {
		t.Fatalf("tags belong to each revision: %+v", revs)
	}
	wantCode(t, func() error {
		_, err := b.EditMemory(sam, EditInput{ID: id, Content: "v5", Tags: []string{"no way"}})
		return err
	}(), "validation")
}

func TestSendTagsAreIdempotentAndCascadeOnDelete(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	a, err := b.Send(sam, SendInput{Channel: "general", Content: "k", Tags: []string{"B", "a"}, IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Send(sam, SendInput{Channel: "general", Content: "k", Tags: []string{"a", "b"}, IdempotencyKey: "k1"})
	if err != nil || c.Seq != a.Seq {
		t.Fatalf("normalized tags fingerprint the same: %+v %+v %v", a, c, err)
	}
	if _, err := b.db.Exec("DELETE FROM messages WHERE seq=?", a.Seq); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM message_tags WHERE seq=?", a.Seq).Scan(&n); err != nil || n != 0 {
		t.Fatalf("message_tags must cascade: %d %v", n, err)
	}
}

// tagSetup: Sam posts, Kim follows tags only (no channel subscription
// besides the inbox register mints).
func tagSetup(t *testing.T) (*Bus, string, string) {
	t.Helper()
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	kim := reg(t, b, "Kim")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	return b, sam, kim
}

func sendTagged(t *testing.T, b *Bus, as, ch, content string, tags ...string) SendResult {
	t.Helper()
	r, err := b.Send(as, SendInput{Channel: ch, Content: content, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTagSubscriptionAndMatching(t *testing.T) {
	b, sam, kim := tagSetup(t)
	wantCode(t, b.SubscribeTags(kim, nil), "validation")
	wantCode(t, b.SubscribeTags(kim, []string{"bad tag"}), "validation")
	sendTagged(t, b, sam, "dev", "before", "release")
	if err := b.SubscribeTags(kim, []string{"Release"}); err != nil {
		t.Fatal(err)
	}
	if err := b.SubscribeTags(kim, []string{"bug", "agentbus"}); err != nil {
		t.Fatal(err)
	}
	if sets, _ := b.TagSubscriptions(kim); len(sets) != 2 || sets[0][0] != "agentbus" || sets[1][0] != "release" {
		t.Fatalf("sets: %v", sets)
	}
	sendTagged(t, b, sam, "dev", "one tag only", "bug")
	sendTagged(t, b, sam, "dev", "and set", "agentbus", "bug", "extra")
	sendTagged(t, b, sam, "general", "both sets", "release", "bug", "agentbus")
	sendTagged(t, b, sam, "dev", "untagged")
	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r.Messages) != 2 {
		t.Fatalf("AND sets, created_seq, once per message: %+v %v", r.Messages, err)
	}
	if !slices.Equal(r.Messages[0].MatchedTags, []string{"agentbus", "bug"}) || !slices.Equal(r.Messages[1].MatchedTags, []string{"agentbus", "bug", "release"}) {
		t.Fatalf("matched_tags: %+v", r.Messages)
	}
	// Ack advances the shared tag cursor; an unacked batch redelivers.
	r2, _ := b.Receive(kim, ReceiveInput{})
	if !r2.Redelivered || len(r2.Messages) != 2 {
		t.Fatalf("redeliver: %+v", r2)
	}
	sendTagged(t, b, sam, "dev", "later", "release")
	r3, _ := b.Receive(kim, ReceiveInput{Ack: r.Batch})
	if len(r3.Messages) != 1 || r3.Messages[0].Content != "later" || r3.Redelivered {
		t.Fatalf("ack advances: %+v", r3)
	}
	if err := b.UnsubscribeTags(kim, []string{"release"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "gone", "release")
	if r4, _ := b.Receive(kim, ReceiveInput{Ack: r3.Batch}); len(r4.Messages) != 0 {
		t.Fatalf("unsubscribed set no longer matches: %+v", r4.Messages)
	}
	_ = b.UnsubscribeTags(kim, []string{"bug", "agentbus"})
	var n int
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=? AND channel=?", kim, tagSource).Scan(&n)
	if n != 0 {
		t.Fatal("last unsubscribe drops the tags/ row")
	}
}

func TestTagSourceExcludesDirectDMTaskAndMemory(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if _, err := b.CreateChannel(sam, "memory/x", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(kim, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "direct", "t")
	sendTagged(t, b, sam, "dm/Kim", "dm", "t")
	sendTagged(t, b, sam, "dm/Sam", "other dm", "t")
	sendTagged(t, b, sam, "memory", "mem", "t")
	sendTagged(t, b, sam, "memory/x", "mem2", "t")
	sendTagged(t, b, sam, "general", "via tag", "t")
	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range r.Messages {
		got = append(got, m.Content)
		if m.Channel == "dev" && m.MatchedTags != nil {
			t.Fatalf("a directly subscribed channel is not a tag delivery: %+v", m)
		}
	}
	if !slices.Equal(got, []string{"direct", "dm", "via tag"}) {
		t.Fatalf("delivered %v", got)
	}
}

func TestTagSubscriptionsResumeFalseAndExpiry(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "old", "t")
	if _, err := b.Register("Kim", "", "r", false); err != nil {
		t.Fatal(err)
	}
	if sets, _ := b.TagSubscriptions(kim); len(sets) != 0 {
		t.Fatalf("resume=false drops tag sets: %v", sets)
	}
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "new", "t")
	if r, _ := b.Receive(kim, ReceiveInput{}); len(r.Messages) != 1 || r.Messages[0].Content != "new" {
		t.Fatalf("future-only after resume=false: %+v", r.Messages)
	}
	// Idle expiry drops the row and its sets and reports tags/.
	idle := int64(b.cfg.CursorIdleHours)*3_600_000 + 1
	if _, err := b.db.Exec("UPDATE subscriptions SET last_activity=last_activity-? WHERE sender=? AND channel=?", idle, kim, tagSource); err != nil {
		t.Fatal(err)
	}
	r, _ := b.Receive(kim, ReceiveInput{})
	if !slices.Contains(r.Expired, tagSource) {
		t.Fatalf("expired must name tags/: %+v", r)
	}
	if sets, _ := b.TagSubscriptions(kim); len(sets) != 0 {
		t.Fatalf("expiry drops the sets: %v", sets)
	}
}

func TestTagRowSurvivesSweeps(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	if err := b.reapEmptyChannels(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Receive(kim, ReceiveInput{}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=? AND channel=?", kim, tagSource).Scan(&n); err != nil || n != 1 {
		t.Fatalf("tags/ row swept: %d %v", n, err)
	}
	// general, not dev: dev is empty with no live subscriber, so the reap
	// above legitimately dropped it.
	sendTagged(t, b, sam, "general", "still", "t")
	if r, _ := b.Receive(kim, ReceiveInput{}); len(r.Messages) != 1 {
		t.Fatalf("still delivering: %+v", r.Messages)
	}
	reg, err := b.Register("Kim", "", "r", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range reg.Pending {
		if p.Channel == tagSource {
			t.Fatalf("pending must not list the pseudo row: %+v", reg.Pending)
		}
	}
}

func TestWaitWakesOnTagMatch(t *testing.T) {
	b, sam, kim := tagSetup(t)
	if err := b.SubscribeTags(kim, []string{"t"}); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "no mention", "t")
	msgs, err := b.Wait(kim, nil, false, nil, time.Second)
	if err != nil || len(msgs) != 1 || !slices.Equal(msgs[0].MatchedTags, []string{"t"}) {
		t.Fatalf("wait peeks the tag source with matched_tags: %+v %v", msgs, err)
	}
}
