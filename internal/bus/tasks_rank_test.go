package bus

import (
	"strings"
	"testing"
)

// TestRankCollisionStillHonorsBefore forces sibling tasks onto one rank
// (only a corrupt write can) and checks that before=x still lands before x,
// for both create and update, instead of falling back to "after x".
func TestRankCollisionStillHonorsBefore(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	mustCreate(t, b, TaskCreateInput{Subject: "a"})
	mustCreate(t, b, TaskCreateInput{Subject: "c"})
	d := mustCreate(t, b, TaskCreateInput{Subject: "d"})
	collide := func() {
		t.Helper()
		if _, err := b.db.Exec(`UPDATE messages SET content=json_set(content,'$.rank','M') WHERE channel='tasks/work' AND memory_id IS NOT NULL AND tombstone=0`); err != nil {
			t.Fatal(err)
		}
	}
	subjects := func() string {
		t.Helper()
		list, err := b.TaskList("Sam", TaskListInput{Channel: "tasks/work"})
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, x := range list {
			s = append(s, x.Subject)
		}
		return strings.Join(s, ",")
	}

	// Ties order by id, so the collided siblings read a,c,d; before=d has
	// neighbors c and d with the same rank.
	collide()
	mustCreate(t, b, TaskCreateInput{Subject: "x", Before: d.ID})
	if got, want := subjects(), "a,c,x,d"; got != want {
		t.Fatalf("create: order=%q want=%q", got, want)
	}

	collide()
	y := mustCreate(t, b, TaskCreateInput{Subject: "y"})
	collide()
	if _, err := b.TaskUpdate("Sam", TaskPatch{ID: y.ID, Before: d.ID}); err != nil {
		t.Fatal(err)
	}
	if got, want := subjects(), "a,c,y,d,x"; got != want {
		t.Fatalf("update: order=%q want=%q", got, want)
	}
}
