package bus

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
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
	if got := parkFiles(t, b); len(got) != 1 || got[0] != parkFileName("Sam") {
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
	for _, bad := range []string{"", "a/b", "tui/x", "bad\nname", strings.Repeat("x", 129)} {
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

func TestParkPathStaysInsideParkDirAndIsInjective(t *testing.T) {
	b := newTestBus(t)
	names := []string{".", "..", `..\x`, `..\..\x`, `a\b`, `a_b`, `a:b`, "CON", "con", "NUL", "Pat", "pat", "PAT", ".tmp-x", ".tmp-", "x.", "x ", "aé", "á"}
	seen := map[string]string{}
	for _, n := range names {
		p, err := b.parkPath(n)
		if err != nil {
			t.Fatalf("parkPath(%q): %v", n, err)
		}
		if filepath.Dir(p) != b.parkDir() {
			t.Errorf("parkPath(%q) = %q: parent must be %q", n, p, b.parkDir())
		}
		base := filepath.Base(p)
		if strings.HasPrefix(base, ".") || strings.ContainsAny(base, `\:/ `) || base != strings.ToLower(base) {
			t.Errorf("parkPath(%q): unsafe or case-sensitive file name %q", n, base)
		}
		if prev, dup := seen[base]; dup {
			t.Errorf("%q and %q map to the same file %q", prev, n, base)
		}
		seen[base] = n
		if err := b.Park(n); err != nil {
			t.Fatalf("Park(%q): %v", n, err)
		}
	}
	if got := parkFiles(t, b); len(got) != len(names) {
		t.Fatalf("want %d files directly in park/, got %v", len(names), got)
	}
	for _, n := range names {
		if !b.Parked(n) {
			t.Errorf("Parked(%q) false", n)
		}
		if err := b.Unpark(n); err != nil {
			t.Fatal(err)
		}
	}
	if got := parkFiles(t, b); len(got) != 0 {
		t.Fatalf("leftovers: %v", got)
	}
	long := strings.Repeat("é", 64) // 128 bytes: the longest name
	if p, err := b.parkPath(long); err != nil || len(filepath.Base(p)) > 255 {
		t.Fatalf("longest name: err=%v len=%d", err, len(filepath.Base(p)))
	}
}

func TestParkSweepsStaleTempFiles(t *testing.T) {
	b := newTestBus(t)
	if err := os.MkdirAll(b.parkDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(b.parkDir(), parkTempPrefix+"old")
	fresh := filepath.Join(b.parkDir(), parkTempPrefix+"fresh")
	stray := filepath.Join(b.parkDir(), "notatemp")
	for _, f := range []string{old, fresh, stray} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := b.Park("Sam"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a stale temp file must be swept, stat err=%v", err)
	}
	for _, f := range []string{fresh, stray, filepath.Join(b.parkDir(), parkFileName("Sam"))} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s must survive: %v", filepath.Base(f), err)
		}
	}
}

func TestResetSurvivesParkClearFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	b := newTestBus(t)
	if err := b.Park("Sam"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(b.parkDir(), 0o500); err != nil { // files inside cannot be removed
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(b.parkDir(), 0o700) })
	if err := b.clearParks(); err == nil {
		t.Fatal("test setup: clearParks must fail here")
	}
	if err := b.Reset(); err != nil {
		t.Fatalf("a park-file failure must not fail Reset: %v", err)
	}
	for _, c := range DefaultChannels {
		if k, err := b.ChannelKind(c.Name); err != nil || k != c.Kind {
			t.Fatalf("default channel %q after Reset: kind %q err %v", c.Name, k, err)
		}
	}
}
