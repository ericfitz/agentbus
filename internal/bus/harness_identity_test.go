package bus

import (
	"testing"

	"github.com/ericfitz/agentbus/internal/procs"
)

// ADR 0017: a hook or wait finds its session by the harness process that
// registered it, so a second session sharing a repository's name resolves
// to its own suffixed identity, not the first session's.
func TestIdentityForHarness(t *testing.T) {
	b := newTestBus(t)
	b.SetHarness(procs.Ref{Pid: 100, Start: 5})
	if _, err := b.Register("repo", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	other, err := Open(b.cfg, b.log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.SetHarness(procs.Ref{Pid: 200, Start: 6})
	if r, err := other.Register("repo", "", "repo", true); err != nil || r.Sender != "repo2" {
		t.Fatalf("second session: %+v %v", r, err)
	}
	// A subagent shares its parent's MCP process, and so its harness; the
	// lookup still names the top-level session.
	if r, err := other.Register("impl", "repo2", "repo", true); err != nil || r.Sender != "repo2/impl" {
		t.Fatalf("subagent: %+v %v", r, err)
	}
	for _, tc := range []struct {
		ref  procs.Ref
		want string
	}{
		{procs.Ref{Pid: 100, Start: 5}, "repo"},
		{procs.Ref{Pid: 200, Start: 6}, "repo2"},
		{procs.Ref{Pid: 200, Start: 7}, ""}, // pid reused by another process
		{procs.Ref{Pid: 300, Start: 6}, ""},
	} {
		if got, err := b.IdentityForHarness(tc.ref); err != nil || got != tc.want {
			t.Errorf("IdentityForHarness(%+v) = %q, %v; want %q", tc.ref, got, err, tc.want)
		}
	}
	// A session whose heartbeat lapsed is not live and does not match.
	if _, err := b.db.Exec("UPDATE sessions SET heartbeat=0 WHERE sender='repo2'"); err != nil {
		t.Fatal(err)
	}
	if got, err := b.IdentityForHarness(procs.Ref{Pid: 200, Start: 6}); err != nil || got != "" {
		t.Fatalf("stale session matched: %q %v", got, err)
	}
}

// Of several live top-level sessions registered from one harness, the most
// recently registered wins; an unknown start time (0) matches on the pid.
func TestIdentityForHarnessPicksNewestAndToleratesUnknownStart(t *testing.T) {
	b := newTestBus(t)
	b.SetHarness(procs.Ref{Pid: 100})
	if _, err := b.Register("first", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("second", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("UPDATE sessions SET registered_at = registered_at - 1000 WHERE sender='first'"); err != nil {
		t.Fatal(err)
	}
	if got, err := b.IdentityForHarness(procs.Ref{Pid: 100}); err != nil || got != "second" {
		t.Fatalf("got %q %v, want second", got, err)
	}
}
