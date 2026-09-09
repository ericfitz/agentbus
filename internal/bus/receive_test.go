package bus

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func setupTwo(t *testing.T) (*Bus, string, string) {
	t.Helper()
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	kim := reg(t, b, "Kim")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	if err := b.Subscribe(kim, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	return b, sam, kim
}

func TestReceiveAckRedeliver(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "a"})
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "b"})
	r1, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r1.Messages) != 2 || r1.Batch == "" || r1.Redelivered {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, _ := b.Receive(kim, ReceiveInput{})
	if len(r2.Messages) != 2 || !r2.Redelivered || r2.Batch != r1.Batch || r2.Instruction == "" {
		t.Fatalf("unacked batch must be redelivered: %+v", r2)
	}
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "c"})
	r3, _ := b.Receive(kim, ReceiveInput{Ack: r1.Batch})
	if len(r3.Messages) != 1 || r3.Messages[0].Content != "c" || r3.Redelivered {
		t.Fatalf("ack must advance: %+v", r3)
	}
	r4, _ := b.Receive(kim, ReceiveInput{Ack: "stale"})
	if !r4.AckIgnored || len(r4.Messages) != 1 {
		t.Fatalf("stale ack ignored and batch redelivered: %+v", r4)
	}
	r5, _ := b.Receive(kim, ReceiveInput{Ack: r3.Batch})
	if len(r5.Messages) != 0 || r5.Batch != "" {
		t.Fatalf("empty receive has no batch: %+v", r5)
	}
}

func TestReceiveExcludesOwnAndMergesBySeq(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.CreateChannel(sam, "ops", "ordinary")
	_ = b.Subscribe(kim, "ops", "now")
	_, _ = b.Send(kim, SendInput{Channel: "dev", Content: "mine"})
	_, _ = b.Send(sam, SendInput{Channel: "ops", Content: "o1"})
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "d1"})
	r, _ := b.Receive(kim, ReceiveInput{})
	if len(r.Messages) != 2 || r.Messages[0].Content != "o1" || r.Messages[1].Content != "d1" {
		t.Fatalf("%+v", r.Messages)
	}
	r, _ = b.Receive(kim, ReceiveInput{Ack: r.Batch, IncludeOwn: true, Channels: []string{"dev"}})
	if len(r.Messages) != 0 {
		t.Fatalf("own message was already before cursor? %+v", r.Messages)
	}
	// Fresh subscription from oldest sees everything including own when asked.
	_ = b.Unsubscribe(kim, "dev")
	_ = b.Subscribe(kim, "dev", "oldest")
	r, _ = b.Receive(kim, ReceiveInput{IncludeOwn: true, Channels: []string{"dev"}})
	if len(r.Messages) != 2 {
		t.Fatalf("from oldest with own: %+v", r.Messages)
	}
}

func TestReceiveGapAndExpiry(t *testing.T) {
	b, sam, kim := setupTwo(t)
	for i := 0; i < 3; i++ {
		_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "x"})
	}
	// Simulate eviction of seq 1..2.
	_, _ = b.db.Exec("DELETE FROM messages WHERE seq<=2")
	_, _ = b.db.Exec("UPDATE channels SET evicted_before_seq=3 WHERE name='dev'")
	r, _ := b.Receive(kim, ReceiveInput{})
	if len(r.Gaps) != 1 || r.Gaps[0].From != 1 || r.Gaps[0].To != 2 || len(r.Messages) != 1 || r.Messages[0].Seq != 3 {
		t.Fatalf("%+v", r)
	}
	b.Now = func() time.Time { return time.Now().Add(73 * time.Hour) }
	r, _ = b.Receive(kim, ReceiveInput{Ack: r.Batch})
	if len(r.Expired) != 1 || r.Expired[0] != "dev" {
		t.Fatalf("idle subscription must expire: %+v", r)
	}
	var n int
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=?", kim).Scan(&n)
	if n != 0 {
		t.Fatal("expired subscription not deleted")
	}
}

func TestReceiveCountAndByteLimits(t *testing.T) {
	b, sam, kim := setupTwo(t)
	for i := 0; i < 5; i++ {
		_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "0123456789"})
	}
	r, _ := b.Receive(kim, ReceiveInput{Count: 2})
	if len(r.Messages) != 2 {
		t.Fatal(len(r.Messages))
	}
	b.cfg.ResultDefaultKiB = 1
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: string(make([]byte, 900))})
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: string(make([]byte, 900))})
	r, _ = b.Receive(kim, ReceiveInput{Ack: r.Batch, Count: 100})
	if len(r.Messages) >= 5 {
		t.Fatalf("byte ceiling not applied: %d", len(r.Messages))
	}
}

func TestReceiveWaitsThenReturnsEmpty(t *testing.T) {
	b, _, kim := setupTwo(t)
	start := time.Now()
	r, err := b.Receive(kim, ReceiveInput{WaitSeconds: 1})
	if err != nil || len(r.Messages) != 0 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("%+v %v %v", r, err, time.Since(start))
	}
}

func TestReceiveWaitSecondsClampedNotRejected(t *testing.T) {
	b, _, kim := setupTwo(t)
	b.cfg.ReceiveMaxWaitSeconds = 1
	start := time.Now()
	r, err := b.Receive(kim, ReceiveInput{WaitSeconds: 999})
	if err != nil || len(r.Messages) != 0 || time.Since(start) < 900*time.Millisecond || time.Since(start) > 3*time.Second {
		t.Fatalf("over-cap wait must be clamped, not rejected: %+v %v %v", r, err, time.Since(start))
	}
}

func TestReceiveLongPollPreservesAckIgnored(t *testing.T) {
	b, _, kim := setupTwo(t)
	r, err := b.Receive(kim, ReceiveInput{Ack: "stale", WaitSeconds: 1})
	if err != nil || !r.AckIgnored || len(r.Messages) != 0 {
		t.Fatalf("stale ack must survive an empty long poll: %+v %v", r, err)
	}
}

// A redelivered batch is bounded solely by the seq range fixed at creation:
// a smaller count or a shrunk result_default_kib on a later call must not
// truncate it, and acking it in full must leave nothing behind.
func TestReceiveRedeliveryIgnoresCountAndByteLimits(t *testing.T) {
	b, sam, kim := setupTwo(t)
	// Each message is ~200 bytes of content, so 10 of them exceed the 1 KiB
	// limit set below by several times over: the old (broken) behavior of
	// trimming redelivery to result_default_kib would visibly cut this batch.
	for i := 0; i < 10; i++ {
		_, _ = b.Send(sam, SendInput{Channel: "dev", Content: string(make([]byte, 200))})
	}
	r1, err := b.Receive(kim, ReceiveInput{Count: 100})
	if err != nil || len(r1.Messages) != 10 {
		t.Fatalf("%+v %v", r1, err)
	}
	b.cfg.ResultDefaultKiB = 1
	r2, err := b.Receive(kim, ReceiveInput{Count: 2})
	if err != nil || len(r2.Messages) != 10 || !r2.Redelivered || r2.Batch != r1.Batch {
		t.Fatalf("redelivery must ignore a smaller count and a shrunk byte limit: %+v %v", r2, err)
	}
	r3, err := b.Receive(kim, ReceiveInput{Ack: r2.Batch})
	if err != nil || len(r3.Messages) != 0 {
		t.Fatalf("acking a fully-redelivered batch must leave nothing behind: %+v %v", r3, err)
	}
}

// A pending batch is redelivered regardless of the channels filter: step 3
// (redeliver pending) is unconditional and takes priority over step 4 (new
// selection), even when the filter names only a different channel.
func TestReceivePendingBypassesChannelsFilter(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.CreateChannel(sam, "ops", "ordinary")
	_ = b.Subscribe(kim, "ops", "now")
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "a"})
	r1, err := b.Receive(kim, ReceiveInput{Channels: []string{"dev"}})
	if err != nil || len(r1.Messages) != 1 || r1.Batch == "" {
		t.Fatalf("%+v %v", r1, err)
	}
	_, _ = b.Send(sam, SendInput{Channel: "ops", Content: "b"})
	r2, err := b.Receive(kim, ReceiveInput{Channels: []string{"ops"}})
	if err != nil || !r2.Redelivered || r2.Batch != r1.Batch || len(r2.Messages) != 1 || r2.Messages[0].Content != "a" {
		t.Fatalf("pending dev batch must be redelivered even when filtering to ops: %+v %v", r2, err)
	}
}

// Acking a pending batch that a channels-filtered call surfaced must not then
// pull that same, now-unfiltered, out-of-filter channel into new selection.
func TestReceiveAckOfOutOfFilterPendingDoesNotLeakIntoNewSelection(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.CreateChannel(sam, "ops", "ordinary")
	_ = b.Subscribe(kim, "ops", "now")
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "a"})
	r1, err := b.Receive(kim, ReceiveInput{Channels: []string{"dev"}})
	if err != nil || len(r1.Messages) != 1 {
		t.Fatalf("%+v %v", r1, err)
	}
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "a2"})
	_, _ = b.Send(sam, SendInput{Channel: "ops", Content: "b"})
	r2, err := b.Receive(kim, ReceiveInput{Ack: r1.Batch, Channels: []string{"ops"}})
	if err != nil || len(r2.Messages) != 1 || r2.Messages[0].Content != "b" {
		t.Fatalf("ack of an out-of-filter batch must not admit dev into ops-filtered selection: %+v %v", r2, err)
	}
	var pending string
	if err := b.db.QueryRow("SELECT pending_token FROM subscriptions WHERE sender=? AND channel='dev'", kim).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != "" {
		t.Fatalf("dev must not have gotten a new pending pair from an ops-filtered call: %q", pending)
	}
}

// (i) The controller's original small-scale repro: own messages ahead of an
// external one in seq order must not crowd the external message out from
// under the byte ceiling, and acking the redelivered batch must not skip it.
func TestReceiveRedeliveryPreservesOriginalIncludeOwnAndNeverSkipsExternal(t *testing.T) {
	b, sam, kim := setupTwo(t)
	b.cfg.ResultDefaultKiB = 1
	for i := 0; i < 5; i++ {
		_, _ = b.Send(kim, SendInput{Channel: "dev", Content: "mine"})
	}
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "external"})
	r1, err := b.Receive(kim, ReceiveInput{Count: 1})
	if err != nil || len(r1.Messages) != 1 || r1.Messages[0].Content != "external" {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, err := b.Receive(kim, ReceiveInput{})
	if err != nil || !r2.Redelivered || r2.Batch != r1.Batch || len(r2.Messages) != 1 || r2.Messages[0].Content != "external" {
		t.Fatalf("redelivery must still show the external message: %+v %v", r2, err)
	}
	r3, err := b.Receive(kim, ReceiveInput{Ack: r2.Batch})
	if err != nil || len(r3.Messages) != 0 {
		t.Fatalf("ack after redelivery must leave nothing skipped: %+v %v", r3, err)
	}
}

// (ii) A batch created with include_own=true, redelivered by a call that now
// passes include_own=false, must still show the own message: the ORIGINAL
// membership wins over whatever the redelivering call requests.
func TestReceiveRedeliveryPreservesOriginalIncludeOwnRegardlessOfCurrentCall(t *testing.T) {
	b, _, kim := setupTwo(t)
	_, _ = b.Send(kim, SendInput{Channel: "dev", Content: "mine"})
	r1, err := b.Receive(kim, ReceiveInput{IncludeOwn: true})
	if err != nil || len(r1.Messages) != 1 || r1.Messages[0].Content != "mine" {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, err := b.Receive(kim, ReceiveInput{}) // include_own now false (default)
	if err != nil || !r2.Redelivered || len(r2.Messages) != 1 || r2.Messages[0].Content != "mine" {
		t.Fatalf("redelivery must replay the batch's original include_own=true, not this call's false: %+v %v", r2, err)
	}
}

// (iii) A channel legitimately trimmed out of a multi-channel batch entirely
// (zero rows contributed) must not be blocked by that batch's pending state:
// once the batch (which never touched it) is acked, its own messages must
// still be delivered as new.
func TestReceivePendingBatchDoesNotBlockAChannelTrimmedOutOfItEntirely(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.CreateChannel(sam, "ops", "ordinary")
	_ = b.Subscribe(kim, "ops", "now")
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "d1"})
	_, _ = b.Send(sam, SendInput{Channel: "ops", Content: "o1"})
	// count=1 trims the merged dev+ops candidate set down to just dev's
	// earlier-seq message; ops never contributes to this batch at all.
	r1, err := b.Receive(kim, ReceiveInput{Count: 1})
	if err != nil || len(r1.Messages) != 1 || r1.Messages[0].Content != "d1" {
		t.Fatalf("%+v %v", r1, err)
	}
	var opsPending string
	if err := b.db.QueryRow("SELECT pending_token FROM subscriptions WHERE sender=? AND channel='ops'", kim).Scan(&opsPending); err != nil {
		t.Fatal(err)
	}
	if opsPending != "" {
		t.Fatalf("ops must not have been given a pending token it never contributed to: %q", opsPending)
	}
	if _, err := b.Receive(kim, ReceiveInput{Ack: r1.Batch}); err != nil {
		t.Fatal(err)
	}
	r2, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r2.Messages) != 1 || r2.Messages[0].Content != "o1" {
		t.Fatalf("ops's message must still be delivered as new after dev's batch is acked: %+v %v", r2, err)
	}
}

// A redelivery whose bound now includes a tombstoned (superseded) message
// must re-stamp pending_end_seq to what it actually showed, so a later ack
// cannot skip past a message that was never actually redelivered.
// Covers both re-stamp cases in one pending, multi-channel batch: dev keeps
// one surviving message (pending_end_seq pulled back to it), while ops's
// entire share is tombstoned away (pending_end_seq falls back to its own
// cursor_seq, i.e. nothing pending) — both under the same shared token.
func TestReceiveRedeliveryReStampsPendingEndSeqWhenAMessageIsTombstoned(t *testing.T) {
	b, sam, kim := setupTwo(t)
	_, _ = b.CreateChannel(sam, "ops", "ordinary")
	_ = b.Subscribe(kim, "ops", "now")
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "keep"})
	res2, err := b.Send(sam, SendInput{Channel: "dev", Content: "superseded"})
	if err != nil {
		t.Fatal(err)
	}
	opsRes, err := b.Send(sam, SendInput{Channel: "ops", Content: "o1"})
	if err != nil {
		t.Fatal(err)
	}
	r1, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r1.Messages) != 3 {
		t.Fatalf("%+v %v", r1, err)
	}
	if _, err := b.db.Exec("UPDATE messages SET tombstone=1 WHERE seq IN (?, ?)", res2.Seq, opsRes.Seq); err != nil {
		t.Fatal(err)
	}
	r2, err := b.Receive(kim, ReceiveInput{})
	if err != nil || !r2.Redelivered || r2.Batch != r1.Batch || len(r2.Messages) != 1 || r2.Messages[0].Content != "keep" {
		t.Fatalf("%+v %v", r2, err)
	}
	var devPendingEnd, opsPendingEnd, opsCursor int64
	if err := b.db.QueryRow("SELECT pending_end_seq FROM subscriptions WHERE sender=? AND channel='dev'", kim).Scan(&devPendingEnd); err != nil {
		t.Fatal(err)
	}
	if devPendingEnd != r2.Messages[0].Seq {
		t.Fatalf("dev's pending_end_seq must be re-stamped to what was actually shown: got %d want %d", devPendingEnd, r2.Messages[0].Seq)
	}
	if err := b.db.QueryRow("SELECT pending_end_seq, cursor_seq FROM subscriptions WHERE sender=? AND channel='ops'", kim).Scan(&opsPendingEnd, &opsCursor); err != nil {
		t.Fatal(err)
	}
	if opsPendingEnd != opsCursor {
		t.Fatalf("ops's entire share was trimmed away: pending_end_seq must fall back to cursor_seq: got %d want %d", opsPendingEnd, opsCursor)
	}
	if _, err := b.Receive(kim, ReceiveInput{Ack: r2.Batch}); err != nil {
		t.Fatal(err)
	}
	var devCursor, opsCursorAfter int64
	if err := b.db.QueryRow("SELECT cursor_seq FROM subscriptions WHERE sender=? AND channel='dev'", kim).Scan(&devCursor); err != nil {
		t.Fatal(err)
	}
	if devCursor != r2.Messages[0].Seq {
		t.Fatalf("ack must advance dev only to what was shown: got %d want %d", devCursor, r2.Messages[0].Seq)
	}
	if err := b.db.QueryRow("SELECT cursor_seq FROM subscriptions WHERE sender=? AND channel='ops'", kim).Scan(&opsCursorAfter); err != nil {
		t.Fatal(err)
	}
	if opsCursorAfter != opsCursor {
		t.Fatalf("ack must not move ops's cursor at all: got %d want %d", opsCursorAfter, opsCursor)
	}
}

// C2: many expired subscriptions plus messages near the (small, scaled-down)
// limit must not push the serialized ReceiveResult over its byte ceiling.
// Expired channel names are variable length and, before this fix, were
// covered only by a fixed 512-byte guess that a large enough expired list
// could exceed on its own.
// seedExpired inserts n long-idle subscriptions for as, directly via SQL, so
// they report as expired on the next receive. Subscriptions carry no FK to
// channels, so no channel row is needed for these.
func seedExpired(t *testing.T, b *Bus, as string, n int, nameFmt string) {
	t.Helper()
	idleMs := int64(b.cfg.CursorIdleHours)*3_600_000 + 1
	for i := 0; i < n; i++ {
		name := fmt.Sprintf(nameFmt, i)
		if _, err := b.db.Exec("INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,0,?)", as, name, b.nowMs()-idleMs); err != nil {
			t.Fatal(err)
		}
	}
}

// C2: many candidate messages plus enough expired channels to force real
// trimming (not just pass because too little data was offered) must still
// produce a serialized result within the configured byte ceiling.
func TestReceiveResultNeverExceedsLimitWithManyExpiredChannels(t *testing.T) {
	b, sam, kim := setupTwo(t)
	b.cfg.ResultDefaultKiB = 4 // small and scaled so the test is fast

	seedExpired(t, b, kim, 20, "stale-channel-%030d") // ~44 bytes each

	// Enough candidate messages, each large enough, to comfortably exceed
	// the 4 KiB budget on their own: real trimming must engage.
	for i := 0; i < 40; i++ {
		if _, err := b.Send(sam, SendInput{Channel: "dev", Content: strings.Repeat("m", 200)}); err != nil {
			t.Fatal(err)
		}
	}

	r, err := b.Receive(kim, ReceiveInput{Count: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Expired) != 20 {
		t.Fatalf("expected 20 expired channels, got %d: %v", len(r.Expired), r.Expired)
	}
	if len(r.Messages) == 0 || len(r.Messages) >= 40 {
		t.Fatalf("trimming must have engaged (some but not all candidate messages): got %d", len(r.Messages))
	}
	j, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if limit := b.cfg.ResultDefaultKiB * 1024; len(j) > limit {
		t.Fatalf("serialized ReceiveResult exceeds its byte ceiling: %d > %d", len(j), limit)
	}
}

// C2: when the (bounded) metadata reserve alone exceeds the configured soft
// limit, receive must return no messages rather than exceed the limit — and
// the metadata-only result must still comfortably respect the hard ceiling.
func TestReceiveMetadataOnlyWhenReserveExceedsSoftLimit(t *testing.T) {
	b, sam, kim := setupTwo(t)
	b.cfg.ResultDefaultKiB = 1 // 1 KiB: smaller than 20 expired names' reserve

	seedExpired(t, b, kim, 20, "stale-channel-%030d")
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "hi"}); err != nil {
		t.Fatal(err)
	}

	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Expired) != 20 {
		t.Fatalf("expected 20 expired channels, got %d", len(r.Expired))
	}
	if len(r.Messages) != 0 {
		t.Fatalf("reserve alone exceeding the soft limit must yield no messages: got %d", len(r.Messages))
	}
	j, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(j) > trimHardCeilingBytes {
		t.Fatalf("metadata-only result must still respect the hard ceiling: %d > %d", len(j), trimHardCeilingBytes)
	}
}

// C2: a legal max_message_kib-sized message must still be delivered even
// when it alone exceeds a tiny soft limit (the documented first-record
// exception), as long as the whole result still fits the hard ceiling.
func TestReceiveKeepsFirstOversizedRecordWithinHardCeiling(t *testing.T) {
	b, sam, kim := setupTwo(t)
	b.cfg.ResultDefaultKiB = 1 // tiny soft limit

	big := strings.Repeat("m", (b.cfg.MaxMessageKiB-1)*1024) // leave margin for envelope overhead
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: big}); err != nil {
		t.Fatal(err)
	}

	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 1 {
		t.Fatalf("the first oversized-for-soft-limit record must still be kept: got %d messages", len(r.Messages))
	}
	j, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(j) > trimHardCeilingBytes {
		t.Fatalf("first-record exception must never exceed the hard ceiling: %d > %d", len(j), trimHardCeilingBytes)
	}
}

// C2: Expired notices are bounded per call (maxNoticesPerReceive); the
// remainder are left untouched as subscription rows and surface on a later
// call, rather than inflating one call's metadata without limit.
func TestReceiveBoundsExpiredNoticesPerCall(t *testing.T) {
	b, _, kim := setupTwo(t)
	const extra = 50
	const n = maxNoticesPerReceive + extra
	seedExpired(t, b, kim, n, "stale-%05d")

	r1, err := b.Receive(kim, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Expired) != maxNoticesPerReceive {
		t.Fatalf("first receive must report exactly the bound: got %d want %d", len(r1.Expired), maxNoticesPerReceive)
	}
	if r1.Expired[0] != "stale-00000" || r1.Expired[len(r1.Expired)-1] != fmt.Sprintf("stale-%05d", maxNoticesPerReceive-1) {
		t.Fatalf("reported subset must be the lowest channel names in order: %s..%s", r1.Expired[0], r1.Expired[len(r1.Expired)-1])
	}
	var remaining int
	if err := b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=?", kim).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if want := extra + 1; remaining != want { // +1 for the still-live "dev" subscription from setupTwo
		t.Fatalf("unreported expired subscriptions must remain as rows: got %d want %d", remaining, want)
	}

	r2, err := b.Receive(kim, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Expired) != extra {
		t.Fatalf("second receive must report the next batch: got %d want %d", len(r2.Expired), extra)
	}
}

// C3: a gap deferred by the notice cap must not be silently lost. While any
// gap notice is deferred, the call must return metadata only (no messages,
// no new pending batch) so a client cannot ack past a survivor whose gap it
// never saw. An unrelated ack processed in the same call (a prior pending
// batch on another channel) must not interfere with that guarantee either.
func TestReceiveDefersDeliveryWhileGapsExceedCap(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	kim := reg(t, b, "Kim")

	// An unrelated pending batch, acked in the same call that first hits the
	// gap cap below, to prove ack processing cannot bury a deferred gap.
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	if err := b.Subscribe(kim, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "unrelated"})
	pending, err := b.Receive(kim, ReceiveInput{})
	if err != nil || pending.Batch == "" {
		t.Fatalf("setup: expected a pending batch: %+v %v", pending, err)
	}

	// c256 gets a real gap (two evicted messages) plus a surviving message
	// past it, subscribed before any of that happens so its cursor starts
	// at 0.
	_, _ = b.CreateChannel(sam, "c256", "ordinary")
	if err := b.Subscribe(kim, "c256", "now"); err != nil {
		t.Fatal(err)
	}
	gone1, _ := b.Send(sam, SendInput{Channel: "c256", Content: "gone1"})
	gone2, _ := b.Send(sam, SendInput{Channel: "c256", Content: "gone2"})
	survivor, _ := b.Send(sam, SendInput{Channel: "c256", Content: "survivor"})
	if _, err := b.db.Exec("DELETE FROM messages WHERE seq IN (?,?)", gone1.Seq, gone2.Seq); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("UPDATE channels SET evicted_before_seq=? WHERE name='c256'", survivor.Seq); err != nil {
		t.Fatal(err)
	}

	// c000..c255: 256 more gapped subscriptions with no messages, so c256
	// (lexicographically last) is the 257th and must be deferred.
	for i := 0; i < 256; i++ {
		name := fmt.Sprintf("c%03d", i)
		if _, err := b.db.Exec("INSERT INTO channels(name,kind,created_seq,evicted_before_seq) VALUES(?,'ordinary',0,5)", name); err != nil {
			t.Fatal(err)
		}
		if _, err := b.db.Exec("INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,0,?)", kim, name, b.nowMs()); err != nil {
			t.Fatal(err)
		}
	}

	r1, err := b.Receive(kim, ReceiveInput{Ack: pending.Batch})
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Gaps) != maxNoticesPerReceive {
		t.Fatalf("first receive must report exactly the bound: got %d want %d", len(r1.Gaps), maxNoticesPerReceive)
	}
	if len(r1.Messages) != 0 || r1.Batch != "" {
		t.Fatalf("deferred gaps must suppress delivery entirely: %+v", r1)
	}
	if r1.Instruction == "" {
		t.Fatal("a deferred-gap result must explain why nothing was delivered")
	}
	for _, g := range r1.Gaps {
		if g.Channel == "c256" {
			t.Fatal("c256's gap must be deferred to a later call, not reported now")
		}
	}

	r2, err := b.Receive(kim, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Gaps) != 1 || r2.Gaps[0].Channel != "c256" {
		t.Fatalf("second receive must report the deferred gap: %+v", r2.Gaps)
	}
	if len(r2.Messages) != 1 || r2.Messages[0].Content != "survivor" {
		t.Fatalf("survivor must be delivered once the gap backlog drains: %+v", r2.Messages)
	}
}
