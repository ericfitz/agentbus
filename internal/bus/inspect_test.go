package bus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func hookScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspectionDecisions(t *testing.T) {
	cases := []struct {
		name, body, wantCode string
	}{
		{"allow", `cat >/dev/null; echo '{"allow": true}'`, ""},
		{"deny", `cat >/dev/null; echo '{"allow": false, "reason": "no secrets"}'`, "inspection_rejected"},
		{"timeout", `sleep 3`, "inspection_unavailable"},
		{"crash", `exit 3`, "inspection_unavailable"},
		{"malformed", `echo not-json`, "inspection_unavailable"},
		{"overflow", `head -c 70000 /dev/zero | tr '\0' 'x'; echo`, "inspection_unavailable"},
		{"sees payload", `IN=$(cat); echo "$IN" | grep -q '"content":"hello"' && echo "$IN" | grep -q '"sender":"Sam"' && echo '{"allow":true}' || echo '{"allow":false,"reason":"bad input"}'`, ""},
		// I2: a missing/null allow must not be treated as a denial.
		{"missing allow", `cat >/dev/null; echo '{}'`, "inspection_unavailable"},
		{"null body", `cat >/dev/null; echo 'null'`, "inspection_unavailable"},
		{"allow explicitly null", `cat >/dev/null; echo '{"allow": null}'`, "inspection_unavailable"},
		{"reason only, no allow", `cat >/dev/null; echo '{"reason": "x"}'`, "inspection_unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newTestBus(t)
			b.cfg.InspectionCommand = []string{hookScript(t, c.body)}
			b.cfg.InspectionTimeoutSeconds = 0.5
			sam := reg(t, b, "Sam")
			_, _ = b.CreateChannel(sam, "dev", "ordinary")
			_, err := b.Send(sam, SendInput{Channel: "dev", Content: "hello"})
			if c.wantCode == "" && err != nil {
				t.Fatal(err)
			}
			if c.wantCode != "" && (err == nil || !strings.Contains(err.Error(), c.wantCode)) {
				t.Fatalf("want %s, got %v", c.wantCode, err)
			}
			if c.name == "deny" && !strings.Contains(err.Error(), "no secrets") {
				t.Fatal("reason must be surfaced")
			}
		})
	}
}

// TestInspectionSerializedWithinProcess is I1: the hook must run serially
// within a process. It uses a mkdir-based lock (atomic on POSIX
// filesystems) to track the peak number of concurrently-running hook
// invocations across N unkeyed (no idempotency key, so I3's per-key lock
// never engages) concurrent sends, and asserts that peak is 1.
func TestInspectionSerializedWithinProcess(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	count := filepath.Join(dir, "count")
	peak := filepath.Join(dir, "peak")
	body := fmt.Sprintf(`cat >/dev/null
while ! mkdir %[1]q 2>/dev/null; do sleep 0.01; done
c=$(cat %[2]q 2>/dev/null || echo 0)
c=$((c+1))
echo "$c" > %[2]q
m=$(cat %[3]q 2>/dev/null || echo 0)
if [ "$c" -gt "$m" ]; then echo "$c" > %[3]q; fi
rmdir %[1]q
sleep 0.1
while ! mkdir %[1]q 2>/dev/null; do sleep 0.01; done
c=$(cat %[2]q)
c=$((c-1))
echo "$c" > %[2]q
rmdir %[1]q
echo '{"allow":true}'
`, lock, count, peak)

	b := newTestBus(t)
	b.cfg.InspectionCommand = []string{hookScript(t, body)}
	b.cfg.InspectionTimeoutSeconds = 5

	const n = 6
	senders := make([]string, n)
	for i := range senders {
		senders[i] = reg(t, b, fmt.Sprintf("Sam%d", i))
	}
	if _, err := b.CreateChannel(senders[0], "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i, s := range senders {
		wg.Add(1)
		go func(i int, s string) {
			defer wg.Done()
			_, err := b.Send(s, SendInput{Channel: "dev", Content: "hello"})
			errs[i] = err
		}(i, s)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	peakBytes, err := os.ReadFile(peak)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(peakBytes)); got != "1" {
		t.Fatalf("peak concurrent hook invocations = %s, want 1", got)
	}
}

// TestInspectionSerializesSameIdempotencyKey is I3: two concurrent calls
// with the same (sender, idempotency_key) must not both run the hook. The
// hook blocks until a release file appears; the first send is given time to
// enter the hook and block there before the second, identical, send starts.
func TestInspectionSerializesSameIdempotencyKey(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	body := fmt.Sprintf(`cat >/dev/null
while [ ! -f %q ]; do sleep 0.02; done
echo '{"allow": true}'
`, release)

	b := newTestBus(t)
	b.cfg.InspectionCommand = []string{hookScript(t, body)}
	b.cfg.InspectionTimeoutSeconds = 5
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}

	in := SendInput{Channel: "dev", Content: "hello", IdempotencyKey: "k1"}
	type sendResult struct {
		res SendResult
		err error
	}
	resA := make(chan sendResult, 1)
	resB := make(chan sendResult, 1)

	go func() {
		r, err := b.Send(sam, in)
		resA <- sendResult{r, err}
	}()
	time.Sleep(150 * time.Millisecond) // let A block inside the hook
	go func() {
		r, err := b.Send(sam, in)
		resB <- sendResult{r, err}
	}()
	time.Sleep(150 * time.Millisecond) // let B block on the key lock
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := <-resA
	if a.err != nil {
		t.Fatalf("send A: %v", a.err)
	}
	bRes := <-resB
	if bRes.err != nil {
		t.Fatalf("send B: %v", bRes.err)
	}
	if a.res.Seq != bRes.res.Seq {
		t.Fatalf("seqs differ: A=%d B=%d, want a replayed receipt", a.res.Seq, bRes.res.Seq)
	}
	if got := b.inspectCalls.Load(); got != 1 {
		t.Fatalf("inspectCalls = %d, want 1 (B should replay A's receipt, not re-run the hook)", got)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM receipts WHERE sender=? AND key=?", sam, "k1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("receipt rows = %d, want 1", n)
	}
}
