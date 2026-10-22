package bus

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func parkFiles(t *testing.T, b *Bus) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(b.cfg.DataDirectory, "park"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestParkStateTransitions(t *testing.T) {
	b := newTestBus(t)
	if b.Parked("Sam") {
		t.Fatal("never parked: Parked must be false")
	}
	if err := b.Unpark("Sam"); err != nil {
		t.Fatalf("unpark when not parked must succeed: %v", err)
	}
	for i := 0; i < 2; i++ { // a second park is a no-op
		if err := b.Park("Sam"); err != nil {
			t.Fatalf("park #%d: %v", i+1, err)
		}
	}
	if !b.Parked("Sam") || b.Parked("Pat") {
		t.Fatal("Sam parked, Pat not")
	}
	if got := parkFiles(t, b); len(got) != 1 || got[0] != "Sam" {
		t.Fatalf("want exactly the file Sam, got %v", got)
	}
	for i := 0; i < 2; i++ { // a second unpark is a no-op
		if err := b.Unpark("Sam"); err != nil {
			t.Fatalf("unpark #%d: %v", i+1, err)
		}
	}
	if b.Parked("Sam") || len(parkFiles(t, b)) != 0 {
		t.Fatal("unparked: no file left")
	}
}

func TestParkRejectsBadNames(t *testing.T) {
	b := newTestBus(t)
	for _, bad := range []string{"", "a/b", "tui/x", "bad\nname", ".", "..", strings.Repeat("x", 129)} {
		if err := b.Park(bad); err == nil {
			t.Errorf("Park(%q): want error", bad)
		}
		if err := b.Unpark(bad); err == nil {
			t.Errorf("Unpark(%q): want error", bad)
		}
		if b.Parked(bad) {
			t.Errorf("Parked(%q) must be false", bad)
		}
	}
	if got := parkFiles(t, b); len(got) != 0 {
		t.Fatalf("rejected names left files: %v", got)
	}
}

func TestParkOverlappingRunsLeaveOneFile(t *testing.T) {
	b := newTestBus(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = b.Park("Sam") }()
		go func() { defer wg.Done(); _ = b.Park("Pat") }()
	}
	wg.Wait()
	if got := parkFiles(t, b); len(got) != 2 {
		t.Fatalf("want files Sam and Pat only (no temp leftovers), got %v", got)
	}
}

func TestRegisterClearsLeftoverPark(t *testing.T) {
	b := newTestBus(t)
	if err := b.Park("Sam"); err != nil { // left by a session that died
		t.Fatal(err)
	}
	if err := b.Park("Pat"); err != nil {
		t.Fatal(err)
	}
	reg(t, b, "Sam")
	if b.Parked("Sam") {
		t.Fatal("register must clear the park file for its display name")
	}
	if !b.Parked("Pat") {
		t.Fatal("register must not clear another name's park")
	}
	// A suffixed display name clears its own file, not the requested one.
	o := openOther(t, b)
	r, err := o.Register("Sam", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Sender == "Sam" {
		t.Fatal("test setup: want a suffixed name")
	}
	_ = b.Park("Sam")
	_ = b.Park(r.Sender)
	if _, err := o.Register("Sam", "", "", true); err != nil {
		t.Fatal(err)
	}
	if b.Parked(r.Sender) || !b.Parked("Sam") {
		t.Fatalf("want only %q cleared", r.Sender)
	}
}

func TestResetClearsParkDirectory(t *testing.T) {
	b := newTestBus(t)
	_ = b.Park("Sam")
	_ = b.Park("Pat")
	if err := b.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := parkFiles(t, b); len(got) != 0 {
		t.Fatalf("reset left %v", got)
	}
	if err := b.Park("Sam"); err != nil { // the directory is recreated on demand
		t.Fatal(err)
	}
}
