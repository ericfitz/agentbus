package bus

import "testing"

func TestDefaultChannelsExistSurviveResetAndYieldToExisting(t *testing.T) {
	b := newTestBus(t)
	kinds := func() map[string]string {
		rows, err := b.db.Query("SELECT name, kind FROM channels")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		m := map[string]string{}
		for rows.Next() {
			var n, k string
			if err := rows.Scan(&n, &k); err != nil {
				t.Fatal(err)
			}
			m[n] = k
		}
		return m
	}
	if got := kinds(); got["general"] != "ordinary" || got["memory"] != "memory" || len(got) != 2 {
		t.Fatalf("open must create the defaults, got %v", got)
	}
	if err := b.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := kinds(); len(got) != 2 {
		t.Fatalf("reset must recreate the defaults, got %v", got)
	}
	if _, err := b.db.Exec("UPDATE channels SET kind='ordinary' WHERE name='memory'"); err != nil {
		t.Fatal(err)
	}
	if err := b.ensureDefaults(); err != nil {
		t.Fatal(err)
	}
	if got := kinds(); got["memory"] != "ordinary" {
		t.Fatalf("an existing channel of another kind must win, got %v", got)
	}
}
