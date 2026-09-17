package bus

import (
	"fmt"
	"strings"
	"testing"
)

// wantUnchanged asserts a refused patch left id's stored revision as it
// was (the brief: assert the stored task is unchanged after a refusal).
// Reads via "Sam", registered on every bus handle these tests use for a
// successful call against id — state is shared across handles on the same
// database, so this holds regardless of which identity's call was refused.
func wantUnchanged(t *testing.T, b *Bus, id, rev int64) {
	t.Helper()
	got, err := b.TaskGet("Sam", id)
	if err != nil {
		t.Fatalf("TaskGet after refusal: %v", err)
	}
	if got.Revision != rev {
		t.Fatalf("revision changed after refusal: want %d got %d", rev, got.Revision)
	}
}

func TestClaimSetsOwnerAndStatus(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	res, err := b.TaskClaim("Sam", tk.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Status != "in_progress" || res.Task.Owner != "Sam" || res.Task.Revision != 2 {
		t.Fatalf("%+v", res.Task)
	}
}

func TestRacingClaimsHaveOneWinner(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	type outcome struct {
		who string
		err error
	}
	results := make(chan outcome, 2)
	go func() { _, err := b.TaskClaim("Sam", tk.ID, 0, ""); results <- outcome{"Sam", err} }()
	go func() { _, err := other.TaskClaim("Pat", tk.ID, 0, ""); results <- outcome{"Pat", err} }()
	var failed int
	var loserErr error
	for range 2 {
		if o := <-results; o.err != nil {
			wantCode(t, o.err, "conflict")
			loserErr = o.err
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("want exactly one loser, got %d", failed)
	}
	got, _ := b.TaskGet("Sam", tk.ID)
	if got.Status != "in_progress" || (got.Owner != "Sam" && got.Owner != "Pat") {
		t.Fatalf("task after race: %+v", got)
	}
	if !strings.Contains(loserErr.Error(), got.Owner) {
		t.Fatalf("loser error %v does not name the winner %s", loserErr, got.Owner)
	}
}

func TestClaimAndRawUpdateAreEquivalent(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	claimed, err := other.TaskClaim("Pat", tk.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	rev := claimed.Task.Revision

	_, claimErr := b.TaskClaim("Sam", tk.ID, 0, "")
	wantCode(t, claimErr, "conflict")
	wantUnchanged(t, b, tk.ID, rev)

	owner, status := "Sam", "in_progress"
	_, rawErr := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Owner: &owner, Status: &status})
	wantCode(t, rawErr, "conflict")
	wantUnchanged(t, b, tk.ID, rev)
	if claimErr.Error() != rawErr.Error() {
		t.Fatalf("claim=%v raw=%v", claimErr, rawErr)
	}
}

// TestOwnerGuard covers both directions: a non-owner is refused every kind
// of change to an in_progress task (rename, complete, move, delete), and
// the owner can do each of those same four things.
func TestOwnerGuard(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	claimed, err := b.TaskClaim("Sam", tk.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	rev := claimed.Task.Revision

	newParent := mustCreate(t, b, TaskCreateInput{Subject: "parent"})
	pID := newParent.ID

	subj := "new subject"
	_, err = other.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Subject: &subj})
	wantCode(t, err, "conflict")
	if !strings.Contains(err.Error(), "Sam") {
		t.Fatalf("err=%v", err)
	}
	wantUnchanged(t, b, tk.ID, rev)

	st := "completed"
	_, err = other.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Status: &st})
	wantCode(t, err, "conflict")
	wantUnchanged(t, b, tk.ID, rev)

	_, err = other.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Parent: &pID})
	wantCode(t, err, "conflict")
	wantUnchanged(t, b, tk.ID, rev)

	_, err = other.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Delete: true})
	wantCode(t, err, "conflict")
	wantUnchanged(t, b, tk.ID, rev)

	// Sam, the owner, can do each of the same four things.
	res, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Subject: &subj})
	if err != nil {
		t.Fatal(err)
	}
	res, err = b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Parent: &pID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Parent != pID {
		t.Fatalf("Sam's move did not take: %+v", res.Task)
	}
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &st}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Delete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskGet("Sam", tk.ID); err == nil {
		t.Fatal("Sam's delete did not take")
	}
}

func TestForceOverridesOwnerGuardAndIsRecorded(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := b.TaskClaim("Sam", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	owner := "Pat"
	res, err := other.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Owner: &owner, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Owner != "Pat" {
		t.Fatalf("%+v", res.Task)
	}
	revs, err := b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := revs[len(revs)-1].Type; got != "forced" {
		t.Fatalf("type=%q", got)
	}

	// Pat forcing his own task (no guard to bypass) is not recorded forced.
	subj := "still mine"
	if _, err := other.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Subject: &subj, Force: true}); err != nil {
		t.Fatal(err)
	}
	revs, err = b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := revs[len(revs)-1].Type; got != "" {
		t.Fatalf("type=%q", got)
	}
}

// TestForcedDeleteIsRecorded covers the controller ruling for Important 2:
// force overriding the owner guard on a delete must be recorded, so a
// forced delete writes one "forced" revision (unchanged content) naming
// the deleter before the task is fully tombstoned. MemoryRevisions still
// returns tombstoned rows (retention purges them later, not immediately),
// so it can read the trail back after the delete.
func TestForcedDeleteIsRecorded(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	if _, err := b.TaskClaim("Sam", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := other.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Delete: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskGet("Sam", tk.ID); err == nil {
		t.Fatal("forced delete did not take")
	}
	revs, err := b.MemoryRevisions("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := revs[len(revs)-1]
	if last.Type != "forced" || last.Sender != "Pat" {
		t.Fatalf("last revision=%+v", last)
	}
}

func TestForceDoesNotBypassBlockingOrChildren(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	blocker := mustCreate(t, b, TaskCreateInput{Subject: "blocker"})
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x", BlockedBy: []int64{blocker.ID}})
	st, owner := "in_progress", "Sam"
	_, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &st, Owner: &owner, Force: true})
	wantCode(t, err, "conflict")
	wantUnchanged(t, b, tk.ID, 1)

	parent := mustCreate(t, b, TaskCreateInput{Subject: "parent"})
	_ = mustCreate(t, b, TaskCreateInput{Subject: "child", Parent: parent.ID})
	_, err = b.TaskUpdate("Sam", TaskPatch{ID: parent.ID, Delete: true, Force: true})
	wantCode(t, err, "conflict")
	wantUnchanged(t, b, parent.ID, 1)
}

func TestReleaseClearsOwnerAndLease(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	future := b.nowMs() + 100_000
	if _, err := b.TaskClaim("Sam", tk.ID, future, ""); err != nil {
		t.Fatal(err)
	}
	res, err := b.TaskRelease("Sam", tk.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Status != "pending" || res.Task.Owner != "" || res.Task.LeasedUntil != 0 {
		t.Fatalf("%+v", res.Task)
	}
}

// TestPendingNoopDoesNotClearAssignment covers Minor 6 of the final review:
// {status: "pending"} on a task that is already pending is not a
// transition (the Status table has no pending -> pending row) and must not
// clear an owner assigned earlier. It also writes no new revision, per rule
// 11 (no-op).
func TestPendingNoopDoesNotClearAssignment(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	if _, err := b.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	pat := "Pat"
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Owner: &pat}); err != nil {
		t.Fatal(err)
	}
	before, err := b.TaskGet("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}

	pending := "pending"
	res, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &pending})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Owner != "Pat" || res.Task.Status != "pending" {
		t.Fatalf("owner cleared by a pending->pending no-op: %+v", res.Task)
	}
	wantUnchanged(t, b, tk.ID, before.Revision)
}

// TestOwnerClearedAloneOnInProgressIsRejected covers Important 1: {owner:
// ""} alone (no status change) must not be able to leave an in_progress
// task without an owner — a state the Status table forbids. Force
// bypasses the owner guard, not this validation invariant.
func TestOwnerClearedAloneOnInProgressIsRejected(t *testing.T) {
	b, other := twoAgents(t)
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	claimed, err := b.TaskClaim("Sam", tk.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	rev := claimed.Task.Revision
	none := ""

	_, err = b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Owner: &none})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, tk.ID, rev)

	_, err = other.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Owner: &none, Force: true})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, tk.ID, rev)
}

// TestClaimRenewalIgnoresReopenedBlocker covers Minor 2: the blocked check
// only applies to a transition INTO in_progress. Renewing an already
// in_progress task's lease must not be disturbed by a blocker reopened
// since it started (design: reopening "does not disturb dependents
// already in progress").
func TestClaimRenewalIgnoresReopenedBlocker(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	blocker := mustCreate(t, b, TaskCreateInput{Subject: "blocker"})
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x", BlockedBy: []int64{blocker.ID}})
	completed := "completed"
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: blocker.ID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskClaim("Sam", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	pending := "pending"
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: blocker.ID, Status: &pending}); err != nil {
		t.Fatal(err)
	}
	future := b.nowMs() + 100_000
	if _, err := b.TaskClaim("Sam", tk.ID, future, ""); err != nil {
		t.Fatal(err)
	}
}

func TestStatusTransitions(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	reg(t, b, "Pat")
	taskList(t, b)

	fresh := func() Task { return mustCreate(t, b, TaskCreateInput{Subject: "x"}) }

	// pending -> in_progress needs an owner.
	tk := fresh()
	st := "in_progress"
	_, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &st})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, tk.ID, 1)
	owner := "Sam"
	res, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &st, Owner: &owner})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Status != "in_progress" || res.Task.Owner != "Sam" {
		t.Fatalf("%+v", res.Task)
	}

	// in_progress -> pending clears owner and lease.
	pending := "pending"
	res, err = b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &pending})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Owner != "" || res.Task.Status != "pending" {
		t.Fatalf("%+v", res.Task)
	}

	// in_progress -> completed clears lease, keeps owner.
	tk = fresh()
	if _, err := b.TaskClaim("Sam", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	res, err = b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &completed})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Status != "completed" || res.Task.Owner != "Sam" {
		t.Fatalf("%+v", res.Task)
	}

	// pending -> completed: caller becomes owner of record if none set.
	tk = fresh()
	res, err = b.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Status: &completed})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Status != "completed" || res.Task.Owner != "Pat" {
		t.Fatalf("%+v", res.Task)
	}

	// completed -> pending: reopen, clears owner.
	res, err = b.TaskUpdate("Pat", TaskPatch{ID: tk.ID, Status: &pending})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Status != "pending" || res.Task.Owner != "" {
		t.Fatalf("%+v", res.Task)
	}

	// completed -> in_progress: refused.
	tk = fresh()
	completedRes, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &completed, Owner: &owner})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &st})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, tk.ID, completedRes.Task.Revision)

	// Assignment: owner set on a pending task leaves it pending.
	tk = fresh()
	res, err = b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Owner: &owner})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Status != "pending" || res.Task.Owner != "Sam" {
		t.Fatalf("%+v", res.Task)
	}
}

func TestUnknownOwnerIsNotFound(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	reg(t, b, "Pat")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	nobody := "Nobody"
	_, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Owner: &nobody})
	wantCode(t, err, "not_found")
	wantUnchanged(t, b, tk.ID, 1)
	pat := "Pat"
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Owner: &pat}); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseValidation(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	past := b.nowMs() - 1000
	_, err := b.TaskClaim("Sam", tk.ID, past, "")
	wantCode(t, err, "validation")
	wantUnchanged(t, b, tk.ID, 1)

	future := b.nowMs() + 100_000
	res, err := b.TaskClaim("Sam", tk.ID, future, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.LeasedUntil != future {
		t.Fatalf("%+v", res.Task)
	}

	zero := int64(0)
	res, err = b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, LeasedUntil: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.LeasedUntil != 0 {
		t.Fatalf("%+v", res.Task)
	}

	// Completing clears any lease.
	res, err = b.TaskClaim("Sam", tk.ID, future, "")
	if err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	res, err = b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &completed})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.LeasedUntil != 0 {
		t.Fatalf("%+v", res.Task)
	}

	// A lease on a pending task is cleared.
	tk2 := mustCreate(t, b, TaskCreateInput{Subject: "y"})
	res, err = b.TaskUpdate("Sam", TaskPatch{ID: tk2.ID, LeasedUntil: &future})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.LeasedUntil != 0 {
		t.Fatalf("%+v", res.Task)
	}
}

func TestBlockedTaskCannotStart(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	blocker := mustCreate(t, b, TaskCreateInput{Subject: "blocker"})
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x", BlockedBy: []int64{blocker.ID}})

	_, err := b.TaskClaim("Sam", tk.ID, 0, "")
	wantCode(t, err, "conflict")
	wantUnchanged(t, b, tk.ID, 1)

	completed := "completed"
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: blocker.ID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskClaim("Sam", tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}

	// Reopening a completed blocker after the dependent started leaves the
	// dependent untouched.
	pending := "pending"
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: blocker.ID, Status: &pending}); err != nil {
		t.Fatal(err)
	}
	got, err := b.TaskGet("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "in_progress" {
		t.Fatalf("%+v", got)
	}

	// Deleting the blocker instead also unblocks.
	tk2 := mustCreate(t, b, TaskCreateInput{Subject: "y", BlockedBy: []int64{blocker.ID}})
	_, err = b.TaskClaim("Sam", tk2.ID, 0, "")
	wantCode(t, err, "conflict")
	wantUnchanged(t, b, tk2.ID, 1)
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: blocker.ID, Delete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskClaim("Sam", tk2.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyCycleRejected(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	a := mustCreate(t, b, TaskCreateInput{Subject: "a"})
	bb := mustCreate(t, b, TaskCreateInput{Subject: "b", BlockedBy: []int64{a.ID}})
	c := mustCreate(t, b, TaskCreateInput{Subject: "c", BlockedBy: []int64{bb.ID}})

	_, err := b.TaskUpdate("Sam", TaskPatch{ID: a.ID, AddBlockedBy: []int64{c.ID}})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, a.ID, 1)

	_, err = b.TaskUpdate("Sam", TaskPatch{ID: a.ID, AddBlockedBy: []int64{a.ID}})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, a.ID, 1)
}

// TestBlockedByCapAndDedupe covers coverage owed from the Task 2 review:
// the 64-id blocked_by cap and that duplicates don't count toward it.
func TestBlockedByCapAndDedupe(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	var blockers []int64
	for i := 0; i < 65; i++ {
		bl := mustCreate(t, b, TaskCreateInput{Subject: fmt.Sprintf("b%d", i)})
		blockers = append(blockers, bl.ID)
	}

	_, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, AddBlockedBy: blockers})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, tk.ID, 1)

	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, AddBlockedBy: blockers[:64]}); err != nil {
		t.Fatal(err)
	}

	dup := append(append([]int64{}, blockers[:64]...), blockers[:5]...)
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, AddBlockedBy: dup}); err != nil {
		t.Fatal(err)
	}
}

// TestPrunedBlockedByIsStored covers the review ruling (Minor 4): a dead
// blocked_by id is not just tolerated at validation, it is dropped from
// the stored document itself on the next write.
func TestPrunedBlockedByIsStored(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	blocker := mustCreate(t, b, TaskCreateInput{Subject: "blocker"})
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x", BlockedBy: []int64{blocker.ID}})
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: blocker.ID, Delete: true}); err != nil {
		t.Fatal(err)
	}

	subj := "renamed"
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Subject: &subj}); err != nil {
		t.Fatal(err)
	}
	stored, err := b.GetMemory("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.Content, "blocked_by") {
		t.Fatalf("dead blocked_by id still stored: %s", stored.Content)
	}
	got, err := b.TaskGet("Sam", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BlockedBy) != 0 {
		t.Fatalf("BlockedBy not pruned: %+v", got)
	}
}

// TestOrphanedParentStaysUpdatable covers the review ruling: a task whose
// stored parent is no longer live (an orphan, as after retention or a
// write by an old binary — see TestPlaceRankUsesEffectiveParentForOrphans
// in tasks_test.go for the same simulated-orphan pattern) stays
// updatable — claim, complete, and rename all succeed — and keeps its
// stored parent unless the patch itself moves it.
func TestOrphanedParentStaysUpdatable(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	const orphanID = int64(500)
	orphanDoc := `{"subject":"orphan","status":"pending","rank":"W","parent":999999}`
	if _, err := b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,content,bytes,memory_id,revision) VALUES('tasks/work','Sam','',0,?,8,?,1)", orphanDoc, orphanID); err != nil {
		t.Fatal(err)
	}

	if _, err := b.TaskClaim("Sam", orphanID, 0, ""); err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: orphanID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	subj := "renamed orphan"
	res, err := b.TaskUpdate("Sam", TaskPatch{ID: orphanID, Subject: &subj})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Parent != 999999 {
		t.Fatalf("stored parent changed: %+v", res.Task)
	}
}

func TestParentRules(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	a := mustCreate(t, b, TaskCreateInput{Subject: "a"})
	bb := mustCreate(t, b, TaskCreateInput{Subject: "b"})

	unknown := int64(999)
	_, err := b.TaskUpdate("Sam", TaskPatch{ID: a.ID, Parent: &unknown})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, a.ID, 1)

	self := a.ID
	_, err = b.TaskUpdate("Sam", TaskPatch{ID: a.ID, Parent: &self})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, a.ID, 1)

	bParent := bb.ID
	moved, err := b.TaskUpdate("Sam", TaskPatch{ID: a.ID, Parent: &bParent})
	if err != nil {
		t.Fatal(err)
	}
	aParent := a.ID
	_, err = b.TaskUpdate("Sam", TaskPatch{ID: bb.ID, Parent: &aParent})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, bb.ID, 1)

	root := int64(0)
	res, err := b.TaskUpdate("Sam", TaskPatch{ID: a.ID, Parent: &root})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Parent != 0 {
		t.Fatalf("%+v", res.Task)
	}
	if res.Task.Revision != moved.Task.Revision+1 {
		t.Fatalf("root move did not write a revision: %+v", res.Task)
	}
	list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
	if err != nil {
		t.Fatal(err)
	}
	if list[len(list)-1].ID != a.ID {
		t.Fatalf("a not appended last: %+v", list)
	}

	// Minor 5: naming the task's current parent again (no before/after) is
	// not a move and must not write a revision.
	same, err := b.TaskUpdate("Sam", TaskPatch{ID: a.ID, Parent: &root})
	if err != nil {
		t.Fatal(err)
	}
	if same.Task.Revision != res.Task.Revision {
		t.Fatalf("re-setting the same parent wrote a revision: %+v", same.Task)
	}
}

func TestDeleteRefusedWithChildren(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	parent := mustCreate(t, b, TaskCreateInput{Subject: "parent"})
	child := mustCreate(t, b, TaskCreateInput{Subject: "child", Parent: parent.ID})

	_, err := b.TaskUpdate("Sam", TaskPatch{ID: parent.ID, Delete: true})
	wantCode(t, err, "conflict")
	if !strings.Contains(err.Error(), fmt.Sprint(child.ID)) {
		t.Fatalf("err does not name child: %v", err)
	}
	wantUnchanged(t, b, parent.ID, 1)

	root := int64(0)
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: child.ID, Parent: &root}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: parent.ID, Delete: true}); err != nil {
		t.Fatal(err)
	}
	_, err = b.TaskGet("Sam", parent.ID)
	wantCode(t, err, "not_found")
}

func TestMoveWithBeforeAfter(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	a := mustCreate(t, b, TaskCreateInput{Subject: "a"})
	a0 := mustCreate(t, b, TaskCreateInput{Subject: "a0", Parent: a.ID})
	a1 := mustCreate(t, b, TaskCreateInput{Subject: "a1", Parent: a.ID, After: a0.ID})
	bb := mustCreate(t, b, TaskCreateInput{Subject: "b", After: a.ID})
	c := mustCreate(t, b, TaskCreateInput{Subject: "c", After: bb.ID})

	order := func() string {
		l, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, x := range l {
			s = append(s, strings.Repeat(">", x.Depth)+x.Subject)
		}
		return strings.Join(s, " ")
	}
	if got := order(); got != "a >a0 >a1 b c" {
		t.Fatalf("initial order=%q", got)
	}

	root := int64(0)
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: c.ID, Parent: &root, Before: a.ID}); err != nil {
		t.Fatal(err)
	}
	if got := order(); got != "c a >a0 >a1 b" {
		t.Fatalf("after move order=%q", got)
	}

	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: bb.ID, Delete: true}); err != nil {
		t.Fatal(err)
	}
	if got := order(); got != "c a >a0 >a1" {
		t.Fatalf("after delete order=%q", got)
	}

	d := mustCreate(t, b, TaskCreateInput{Subject: "d", After: c.ID})
	if got := order(); got != "c d a >a0 >a1" {
		t.Fatalf("after create order=%q", got)
	}
	_, _ = d, a1
}

// TestNoopPatchWritesNothing covers rule 11: a no-op patch inserts no row
// (max(seq) is unchanged) and, keyed, replays via the receipt rather than
// re-running applyPatch.
func TestNoopPatchWritesNothing(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	var before int64
	if err := b.db.QueryRow("SELECT max(seq) FROM messages").Scan(&before); err != nil {
		t.Fatal(err)
	}

	subj := "x"
	res, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Subject: &subj})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task.Revision != 1 {
		t.Fatalf("no-op wrote a revision: %+v", res.Task)
	}
	var after int64
	if err := b.db.QueryRow("SELECT max(seq) FROM messages").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("no-op inserted a row: max(seq) %d -> %d", before, after)
	}

	keyed, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Subject: &subj, IdempotencyKey: "noop-key"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Subject: &subj, IdempotencyKey: "noop-key"})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Task.Revision != keyed.Task.Revision {
		t.Fatalf("keyed no-op replay=%+v want=%+v", replay.Task, keyed.Task)
	}
}

func TestTaskUpdateIdempotentReplayAfterDelete(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	completed := "completed"
	first, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &completed, IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Delete: true}); err != nil {
		t.Fatal(err)
	}
	second, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Status: &completed, IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("replay after delete must not fail: %v", err)
	}
	if second.Task.Revision != first.Task.Revision {
		t.Fatalf("replay=%+v want=%+v", second.Task, first.Task)
	}
}

func TestDeleteCombinedWithChangesIsValidation(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	subj := "new"
	_, err := b.TaskUpdate("Sam", TaskPatch{ID: tk.ID, Delete: true, Subject: &subj})
	wantCode(t, err, "validation")
	wantUnchanged(t, b, tk.ID, 1)
}
