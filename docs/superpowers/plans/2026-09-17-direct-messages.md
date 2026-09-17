# Direct Messages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Any registered identity can send a one-way message to one other identity by sending to the channel `dm/<identity>`; the TUI shows each session's inbox.

**Architecture:** A DM inbox is an ordinary channel whose name starts with `dm/`. `/` is illegal in user channel names, so DM-ness is derived from the name; there is no schema change. `Register` creates the inbox and the owner's subscription. A single read guard (`dmReadable`) protects history/search/subscribe; the in-process TUI lifts it with `SetObserver`.

**Tech Stack:** Go, modernc SQLite (`database/sql`), MCP go-sdk, Bubble Tea v1 / Lip Gloss v1.

**Spec:** `docs/superpowers/specs/2026-09-17-direct-messages-design.md` (read it first; its "Human decisions" are binding).

## Global Constraints

- Branch `direct-messages`. No schema change, no `user_version` bump, no new MCP tool or parameter.
- DM channel of identity `X` is exactly `dm/X`; owner is everything after the first `dm/`.
- American English. Match surrounding comment density and idiom.
- Errors use `errf(kind, retryable, fmt, ...)` with kinds already in use: `validation`, `not_found`.
- macOS host: no GNU `timeout`; `rg` not `grep`; do not `git stash`; stage only files you touched; never `git add -A`.
- Every task ends with `gofmt -l internal` (must print nothing), `golangci-lint run ./...` (0 issues), `go build ./...`, and `go test ./...` green, then one commit. Commit messages end with:
  ```
  Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
  ```
- TUI test fixture rules: no `tea.Sequence`, `tick` is a package var already stubbed in tests, cursors are static. Use `newFixture`, `f.key`, `f.agentSend`, `f.receive`, `f.run`, `f.send`.
- mcpserver integration tests take ~25 s; run them once per task, not per step.

## File map

| File | Change |
|------|--------|
| `internal/bus/dm.go` (new) | `DMPrefix`, `DMChannel`, `dmOwner`, `dmReadable`, `SetObserver`, `ensureInbox` |
| `internal/bus/dm_test.go` (new) | all bus DM tests |
| `internal/bus/sessions.go` | `Register` calls `ensureInbox`; idle purge skips own inbox |
| `internal/bus/channels.go` | reject name `dm`; `ListChannels` omits DM; `DeleteChannel` rejects DM; reaper skips DM |
| `internal/bus/receive.go` | `Subscribe`/`Unsubscribe` guard; idle expiry skips own inbox |
| `internal/bus/messages.go` | DM-specific `not_found` text; `History` guard |
| `internal/bus/search.go` | channel-filter guard; unscoped exclusion |
| `internal/cli/wait.go` | own-inbox messages bypass `-filter` |
| `internal/mcpserver/server.go` | tool descriptions |
| `internal/cli/identity.go` | protocol bullet |
| `internal/tui/*` | Tasks 5 and 6 |
| `internal/cli/skills/using-agentbus/SKILL.md`, docs, ADR | Task 7 |

---

### Task 1: Inbox creation, naming, and sending

**Files:**
- Create: `internal/bus/dm.go`, `internal/bus/dm_test.go`
- Modify: `internal/bus/sessions.go` (Register), `internal/bus/channels.go` (`EnsureChannel`, `CreateChannel`), `internal/bus/messages.go:116-119`

**Interfaces:**
- Produces: `const DMPrefix = "dm/"`; `func DMChannel(identity string) string`; `func dmOwner(channel string) (string, bool)`; `func (b *Bus) ensureInbox(tx *sql.Tx, display string, now int64) error`.

- [ ] **Step 1: Write the failing tests** in `internal/bus/dm_test.go`:

```go
package bus

import (
	"strings"
	"testing"
)

func TestDMOwner(t *testing.T) {
	for ch, want := range map[string]string{"dm/Sam": "Sam", "dm/Sam/impl": "Sam/impl"} {
		if got, ok := dmOwner(ch); !ok || got != want {
			t.Fatalf("dmOwner(%q)=%q,%v", ch, got, ok)
		}
	}
	for _, ch := range []string{"dm", "dm/", "general", "xdm/Sam"} {
		if _, ok := dmOwner(ch); ok {
			t.Fatalf("%q must not be a DM channel", ch)
		}
	}
	if DMChannel("Sam") != "dm/Sam" {
		t.Fatal(DMChannel("Sam"))
	}
}

func TestRegisterCreatesInboxIdempotently(t *testing.T) {
	b := newTestBus(t)
	for range 2 {
		r, err := b.Register("Sam", "", "repo", true)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range r.Pending {
			found = found || p.Channel == "dm/Sam"
		}
		if !found {
			t.Fatalf("register must report the inbox in pending: %+v", r.Pending)
		}
	}
	var chans, subs int
	_ = b.db.QueryRow("SELECT count(*) FROM channels WHERE name='dm/Sam' AND kind='ordinary'").Scan(&chans)
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender='Sam' AND channel='dm/Sam'").Scan(&subs)
	if chans != 1 || subs != 1 {
		t.Fatalf("channels=%d subs=%d", chans, subs)
	}
	// resume=false drops subscriptions but the inbox subscription comes back.
	if _, err := b.Register("Sam", "", "repo", false); err != nil {
		t.Fatal(err)
	}
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender='Sam' AND channel='dm/Sam'").Scan(&subs)
	if subs != 1 {
		t.Fatalf("resume=false must recreate the inbox subscription, got %d", subs)
	}
}

func TestDMDeliveryAndQueueWhileOffline(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	other, _ := Open(b.cfg, b.log)
	defer func() { _ = other.Close() }()
	if _, err := other.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	// Pat goes away; the inbox persists and queues.
	if err := other.EndSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("Sam", SendInput{Channel: "dm/Pat", Content: "rename landed"}); err != nil {
		t.Fatalf("DM to a known offline identity must queue: %v", err)
	}
	if _, err := other.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	res, err := other.Receive("Pat", ReceiveInput{})
	if err != nil || len(res.Messages) != 1 || res.Messages[0].Content != "rename landed" || res.Messages[0].Sender != "Sam" {
		t.Fatalf("%+v %v", res, err)
	}
	// A reply is a DM back, optionally naming the original.
	seq := res.Messages[0].Seq
	if _, err := other.Send("Pat", SendInput{Channel: "dm/Sam", Content: "thanks", ReplyTo: &seq}); err != nil {
		t.Fatalf("cross-channel reply_to must be accepted: %v", err)
	}
}

func TestDMToUnknownIdentityIsNotFound(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	_, err := b.Send("Sam", SendInput{Channel: "dm/Nobody", Content: "hi"})
	if err == nil || !strings.Contains(err.Error(), "not_found") || !strings.Contains(err.Error(), "never registered") {
		t.Fatalf("got %v", err)
	}
}

func TestChannelNameDMIsReserved(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel("Sam", "dm", "ordinary"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("create_channel dm: %v", err)
	}
	if err := b.EnsureChannel("dm", "ordinary"); err == nil {
		t.Fatal("EnsureChannel dm must fail")
	}
}
```

- [ ] **Step 2: Run to verify failure.** `go test ./internal/bus -run 'DM|Inbox' 2>&1 | tail -5` → build failure: `dmOwner` undefined.

- [ ] **Step 3: Implement.** Create `internal/bus/dm.go`:

```go
package bus

import (
	"database/sql"
	"strings"
)

// DMPrefix starts the name of every direct-message inbox. validateName
// rejects '/', so no user-created channel can carry it; DM-ness is derived
// from the name alone and needs no schema support.
const DMPrefix = "dm/"

// DMChannel is the inbox channel of identity.
func DMChannel(identity string) string { return DMPrefix + identity }

// dmOwner reports the identity that owns a DM inbox channel: everything
// after the first "dm/", so a subagent "Sam/impl" owns "dm/Sam/impl".
func dmOwner(channel string) (string, bool) {
	owner, ok := strings.CutPrefix(channel, DMPrefix)
	return owner, ok && owner != ""
}

// ensureInbox creates display's inbox and its subscription to it, both
// idempotently, inside Register's transaction. A new subscription starts at
// 0 so DMs sent before a resume=false re-register are still delivered.
func (b *Bus) ensureInbox(tx *sql.Tx, display string, now int64) error {
	ch := DMChannel(display)
	if _, err := tx.Exec("INSERT OR IGNORE INTO channels(name,kind,created_seq,evicted_before_seq) VALUES(?,'ordinary',(SELECT coalesce(max(seq),0) FROM messages),0)", ch); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,0,?)", display, ch, now); err != nil {
		return internal(err)
	}
	return nil
}
```

In `Register` (`sessions.go`), immediately after the `if !resume { ... } else { ... }` block and before the pending query, add:

```go
	if err := b.ensureInbox(tx, display, now); err != nil {
		return Registration{}, err
	}
```

In `channels.go`, add to both `EnsureChannel` and `CreateChannel` right after their `validateName` call:

```go
	if name == "dm" {
		return errf("validation", false, "channel name %q is reserved for direct messages", name) // CreateChannel: return Channel{}, errf(...)
	}
```

In `messages.go` `validateSendRefs`, replace the `sql.ErrNoRows` case body with:

```go
		if owner, ok := dmOwner(in.Channel); ok {
			return "", errf("not_found", false, "%q has never registered; direct messages reach only known identities (see discover)", owner)
		}
		return "", errf("not_found", false, "channel %q does not exist; create it first", in.Channel)
```

- [ ] **Step 4: Run.** `go test ./internal/bus 2>&1 | tail -5` → `ok`. Existing tests that count channels or pending entries may now see `dm/*` rows; fix each by filtering DM channels in the assertion, not by weakening `ensureInbox`.

- [ ] **Step 5: Full gate and commit** (`feat(bus): direct-message inboxes created at register`).

---

### Task 2: Guards and the observer

**Files:**
- Modify: `internal/bus/dm.go`, `internal/bus/receive.go` (`Subscribe`, `Unsubscribe`, idle check at the `last < now-idle` branch), `internal/bus/sessions.go:157`, `internal/bus/channels.go` (`ListChannels`, `DeleteChannel`, `reapEmptyChannels`), `internal/bus/messages.go` (`History`), `internal/bus/search.go`
- Test: `internal/bus/dm_test.go`

**Interfaces:**
- Consumes: `dmOwner`, `DMPrefix` (Task 1).
- Produces: `func (b *Bus) SetObserver(as string)`; `func (b *Bus) dmReadable(as, channel string) bool`. `StatusReport().Channels` still includes DM channels; `ListChannels` does not.

- [ ] **Step 1: Write the failing tests** (append to `dm_test.go`):

```go
// twoAgents registers Sam on b and Pat on a second process.
func twoAgents(t *testing.T) (b, other *Bus) {
	t.Helper()
	b = newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	other, err := Open(b.cfg, b.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, err := other.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	return b, other
}

func TestDMGuards(t *testing.T) {
	b, other := twoAgents(t)
	if _, err := b.Send("Sam", SendInput{Channel: "dm/Pat", Content: "secret zebra"}); err != nil {
		t.Fatal(err)
	}
	wantErr := func(name, kind string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), kind) {
			t.Fatalf("%s: want %s, got %v", name, kind, err)
		}
	}
	wantErr("subscribe other", "validation", b.Subscribe("Sam", "dm/Pat", "now"))
	wantErr("subscribe own", "validation", other.Subscribe("Pat", "dm/Pat", "now"))
	wantErr("unsubscribe own", "validation", other.Unsubscribe("Pat", "dm/Pat"))
	_, err := b.DeleteChannel("dm/Pat", "")
	wantErr("delete", "validation", err)
	_, err = b.History("Sam", "dm/Pat", nil, nil, 10)
	wantErr("history by sender", "not_found", err)
	_, err = b.Search("Sam", SearchInput{Query: "zebra", Channel: "dm/Pat", Mode: "text"})
	wantErr("scoped search by sender", "not_found", err)
	res, err := b.Search("Sam", SearchInput{Query: "zebra", Mode: "text"})
	if err != nil || len(res.Results) != 0 {
		t.Fatalf("unscoped search must not leak another inbox: %+v %v", res, err)
	}
	// The owner can read its own inbox both ways.
	if ms, err := other.History("Pat", "dm/Pat", nil, nil, 10); err != nil || len(ms) != 1 {
		t.Fatalf("%+v %v", ms, err)
	}
	if res, err := other.Search("Pat", SearchInput{Query: "zebra", Mode: "text"}); err != nil || len(res.Results) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	chans, _ := b.ListChannels("Sam")
	for _, c := range chans {
		if _, ok := dmOwner(c.Name); ok {
			t.Fatalf("list_channels must omit DM channels: %s", c.Name)
		}
	}
}

func TestObserverReadsEveryInbox(t *testing.T) {
	b, _ := twoAgents(t)
	if _, err := b.Send("Sam", SendInput{Channel: "dm/Pat", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	b.SetObserver("Sam")
	if err := b.Subscribe("Sam", "dm/Pat", "oldest"); err != nil {
		t.Fatalf("observer subscribe: %v", err)
	}
	if ms, err := b.History("Sam", "dm/Pat", nil, nil, 10); err != nil || len(ms) != 1 {
		t.Fatalf("observer history: %+v %v", ms, err)
	}
	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range st.Channels {
		found = found || c.Name == "dm/Pat"
	}
	if !found {
		t.Fatal("StatusReport must list DM channels for the TUI")
	}
}

func TestInboxSurvivesReaperAndIdleExpiry(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	idle := int64(b.cfg.CursorIdleHours)*3_600_000 + 1
	if _, err := b.db.Exec("UPDATE subscriptions SET last_activity=? WHERE sender='Sam'", b.nowMs()-idle); err != nil {
		t.Fatal(err)
	}
	res, err := b.Receive("Sam", ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range res.Expired {
		if ch == "dm/Sam" {
			t.Fatal("the inbox subscription must never idle-expire")
		}
	}
	// Session gone, inbox empty: the reaper must still keep it.
	if err := b.EndSessions(); err != nil {
		t.Fatal(err)
	}
	if err := b.reapEmptyChannels(); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = b.db.QueryRow("SELECT count(*) FROM channels WHERE name='dm/Sam'").Scan(&n)
	if n != 1 {
		t.Fatal("reaper dropped an inbox")
	}
	// Resume after idling: register's purge must keep the inbox subscription.
	if _, err := b.db.Exec("UPDATE subscriptions SET last_activity=0 WHERE sender='Sam'"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender='Sam' AND channel='dm/Sam'").Scan(&n)
	if n != 1 {
		t.Fatal("register purge dropped the inbox subscription")
	}
}
```

Check `SearchInput`/`SearchResult` field names in `search.go` before relying on `Results`/`Mode`; adjust the test to the real names, not the code to the test.

- [ ] **Step 2: Run to verify failure.** `go test ./internal/bus -run 'DMGuards|Observer|InboxSurvives'` → FAIL (`SetObserver` undefined).

- [ ] **Step 3: Implement.**

`dm.go` — add a field `observer string` to `Bus` in `bus.go` (guarded by no lock: it is set once before use), and:

```go
// SetObserver lets identity as, registered in this process, read and
// subscribe to every DM inbox. Only the in-process TUI calls it; the MCP
// server never does, so no agent can obtain it through a tool.
func (b *Bus) SetObserver(as string) { b.observer = as }

// dmReadable reports whether as may read channel: always for ordinary
// channels, and for a DM inbox only its owner or the observer.
func (b *Bus) dmReadable(as, channel string) bool {
	owner, ok := dmOwner(channel)
	return !ok || owner == as || (b.observer != "" && b.observer == as)
}
```

`receive.go` `Subscribe`, after the `from` validation:

```go
	if _, ok := dmOwner(channel); ok && b.observer != as {
		return errf("validation", false, "direct-message channels cannot be subscribed to; register manages your inbox")
	}
```

`Unsubscribe`, after the first `auth`:

```go
	if _, ok := dmOwner(channel); ok && b.observer != as {
		return errf("validation", false, "direct-message channels cannot be unsubscribed from")
	}
```

`receiveOnce`: change `if last < now-idle {` to `if last < now-idle && s.channel != DMChannel(as) {`.

`sessions.go:157`: change the purge to `"DELETE FROM subscriptions WHERE sender=? AND last_activity < ? AND channel<>?"` with args `display, now-idle, DMChannel(display)`.

`channels.go`:
- `ListChannels`: after `listChannels`, `chans = slices.DeleteFunc(chans, func(c Channel) bool { _, dm := dmOwner(c.Name); return dm })`.
- `DeleteChannel`: first statement, `if _, ok := dmOwner(name); ok { return Channel{}, errf("validation", false, "can't delete direct-message channel %q", name) }`.
- `reapEmptyChannels`: add `AND name NOT LIKE 'dm/%'` to the `DELETE FROM channels` statement.

`messages.go` `History`, after `auth`:

```go
	if !b.dmReadable(as, channel) {
		return nil, errf("not_found", false, "channel %q does not exist", channel)
	}
```

Use the exact not_found text `History` already returns for a missing channel (read the function; match it) so existence does not leak.

`search.go`: after `auth` and the query check, `if in.Channel != "" && !b.dmReadable(as, in.Channel) { return SearchResult{}, errf("not_found", ...same text as a missing channel...) }`. In the SQL builder where `in.Channel` is applied (around line 61), add an `else` for the unscoped case:

```go
	} else if b.observer != as {
		sb.WriteString(" AND (m.channel NOT LIKE 'dm/%' OR m.channel=?)")
		args = append(args, DMChannel(as))
	}
```

The builder function may not have `as`/`b` in scope; thread `as` through its signature. Apply the same predicate to the semantic (embedding) query path if it selects from `messages` separately — DM channels are ordinary, so they normally have no embeddings, but the text path is shared by `both`.

- [ ] **Step 4: Run** `go test ./internal/bus` → ok.
- [ ] **Step 5: Full gate and commit** (`feat(bus): guard direct-message inboxes; TUI observer`).

---

### Task 3: `agentbus wait` wakes on DMs regardless of `-filter`

**Files:** Modify `internal/cli/wait.go`; Test `internal/cli/cli_test.go` (or the existing wait test file; find with `rg -n 'func TestWait' internal/cli`).

**Interfaces:** Consumes `bus.DMChannel`.

- [ ] **Step 1: Failing test.** Model it on the nearest existing `TestWait*` test in `internal/cli` (same config/bus helpers). Behavior to assert: identity `Pat` registered; `Sam` sends `dm/Pat` content `"rename landed"` (no `@Pat`); `Wait(WaitOptions{Config: cfg, As: "Pat", Filter: "@Pat", Timeout: 2 * time.Second}, &buf)` returns nil and `buf` contains `rename landed`. Second assertion: the same content sent to an ordinary subscribed channel with the same filter returns `ErrWaitTimeout` (use `Timeout: 300 * time.Millisecond`).

- [ ] **Step 2: Run** → first assertion fails with `ErrWaitTimeout`.

- [ ] **Step 3: Implement** in `Wait`:

```go
		inbox := bus.DMChannel(o.As)
		// A direct message is addressed to this identity by definition, so
		// it wakes the waiter even when its text does not match the filter.
		match = func(m bus.Message) bool { return m.Channel == inbox || re.MatchString(m.Content) }
```

Update the `Filter` field comment: `// regexp on content; non-matching messages are skipped, except direct messages`. Update the `-filter` flag help in `main.go` (`rg -n '"filter"' main.go`) the same way.

- [ ] **Step 4/5: Run, gate, commit** (`feat(wait): direct messages bypass -filter`).

---

### Task 4: MCP surface and protocol text

**Files:** Modify `internal/mcpserver/server.go` (descriptions at ~255, 277, 295, 313, 273), `internal/cli/identity.go` (`protocol`); Test `internal/mcpserver/integration_more_test.go`, and whichever `internal/cli` test pins the protocol text (`rg -n 'protocol' internal/cli/*_test.go`).

- [ ] **Step 1: Failing integration test** (append to `integration_more_test.go`; `spawn`, `writeConfig`, `call` exist in `integration_test.go`):

```go
func TestDirectMessageAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	a, b := spawn(t, dir), spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := b.call(t, "register", map[string]any{"name": "Pat"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "dm/Pat", "content": "ping"}); e != "" {
		t.Fatal(e)
	}
	got, e := b.call(t, "receive", map[string]any{"as": "Pat"})
	if e != "" {
		t.Fatal(e)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "ping" {
		t.Fatalf("%v", got)
	}
	if _, e := a.call(t, "history", map[string]any{"as": "Sam", "channel": "dm/Pat"}); !strings.Contains(e, "not_found") {
		t.Fatalf("sender must not read the recipient's inbox: %q", e)
	}
	if _, e := a.call(t, "subscribe", map[string]any{"as": "Sam", "channel": "dm/Pat"}); !strings.Contains(e, "validation") {
		t.Fatalf("subscribe to an inbox must be rejected: %q", e)
	}
}
```

This should already pass after Tasks 1-2 (it is the end-to-end proof, not a driver). If it fails, fix the bus, not the test.

- [ ] **Step 2: Descriptions.** Append to each tool's `Description`:
  - `send`: ` To message one agent directly, set channel to dm/<name>, with a name from register's others or discover; answer a direct message by sending to dm/<its sender>, optionally with reply_to.`
  - `register`: ` Also creates your direct-message inbox dm/<as>, which receive reads like any subscribed channel.`
  - `subscribe` and `unsubscribe`: ` Direct-message channels (dm/...) are not accepted.`
  - `list_channels`: ` Direct-message inboxes are not listed.`

- [ ] **Step 3: Protocol.** In `identity.go` `protocol`, after the "Post to your subscribed chat channel..." bullet add:

```
- When a message concerns exactly one agent, send it to the channel
  dm/<that agent's name> instead of a shared channel. Direct messages are
  one-way: answer one by sending to dm/<its sender>, with reply_to set to
  its seq.
```

Update any test that pins the protocol or description text.

- [ ] **Step 4/5: Run, gate, commit** (`feat(mcp): document direct messages in tool descriptions and hook protocol`).

---

### Task 5: TUI data layer — observer, hidden DM channels, per-session unread

**Files:** Modify `internal/tui/client.go` (`newClient`), `internal/tui/model.go` (`Model`, `setChannels`), `internal/tui/view.go` (`renderRails`); Test `internal/tui/dm_test.go` (new).

**Interfaces:**
- Consumes: `bus.SetObserver`, `bus.DMChannel`, `bus.DMPrefix`; `StatusReport().Channels` includes DM channels.
- Produces: `Model.dms []bus.Channel` (DM channels, sorted by name, never in `m.channels`); `func (m *Model) sessionNames() []string` (sorted names from `m.sessionsSeen`, the order the rail draws and Task 6 navigates).

- [ ] **Step 1: Failing tests** in `internal/tui/dm_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
)

func TestRailHidesDMChannelsAndCountsSessionUnread(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd()) // learn Sam's session and the DM channels
	for _, c := range f.m.channels {
		if strings.HasPrefix(c.Name, bus.DMPrefix) {
			t.Fatalf("DM channel in the channel list: %s", c.Name)
		}
	}
	if len(f.m.dms) < 2 { // dm/Sam and dm/eric
		t.Fatalf("dms=%v", f.m.dms)
	}
	f.agentSend(t, "dm/Sam", "note to self") // Sam -> own inbox; any sender works
	f.receive(t)
	if got := f.m.unread("dm/Sam"); got != 1 {
		t.Fatalf("unread(dm/Sam)=%d", got)
	}
	rail := f.m.renderRails()
	var samRow string
	for _, l := range strings.Split(rail, "\n") {
		if strings.Contains(l, "Sam") {
			samRow = l
		}
	}
	if !strings.Contains(samRow, "1") {
		t.Fatalf("session row must show its unread DM count: %q", samRow)
	}
}
```

If `f.receive` only drains the TUI's own receive (check its body), that is the path under test: the observer subscription must deliver `dm/Sam` to the TUI.

- [ ] **Step 2: Run** → FAIL (`f.m.dms` undefined).

- [ ] **Step 3: Implement.**
  - `newClient`: call `b.SetObserver(reg.Sender)` right after `Register`. It subscribes to `ListChannels` results, which now omit DM channels; DM channels arrive via `onStatus → setChannels` and are subscribed there.
  - `Model`: add `dms []bus.Channel`.
  - `setChannels`: after cloning and sorting, partition: DM channels (`strings.HasPrefix(c.Name, bus.DMPrefix)`) go to `m.dms`, the rest to `m.channels`. The subscribe loop runs over both. The selection-restore loop runs over `m.channels` only.
  - `sessionNames()`: extract the existing name-collection-and-sort from `renderRails` into this method; `renderRails` calls it.
  - `renderRails` session rows: after building `row`, `if n := m.unread(bus.DMChannel(n_)); n > 0 { row += " " + th.Style(th.Agent).Render(strconv.Itoa(n)) }` (mind the existing loop variable is named `n`; rename the count).
  - `Init` currently seeds channels from somewhere (`rg -n 'setChannels' internal/tui`); make sure the initial call also sees DM channels, by seeding from `StatusReport` or by letting the first status tick add them. The test calls `statusCmd` explicitly, so either satisfies it; prefer the smaller diff.

- [ ] **Step 4/5: Run `go test ./internal/tui`, gate, commit** (`feat(tui): observe DM inboxes; per-session unread counts`).

---

### Task 6: TUI sessions pane — navigation, header, compose, reply

**Files:** Modify `internal/tui/model.go` (pane enum, `pane`, `paneKey`, `focusPane`, up/down, `selected`, `r`/enter reply, `d`/`s` keys), `internal/tui/view.go` (`renderHeader`, `renderRails`, `renderCompose`, `chanStyle`), `internal/tui/compose.go` (send target), `internal/tui/help.go`; Test `internal/tui/dm_test.go`, and update `TestTabCyclesPanesAndHomeReturnsToChannels`.

**Interfaces:**
- Consumes: `m.dms`, `m.sessionNames()` (Task 5).
- Produces: `paneSessions`; `Model.sessSel int` (index into `sessionNames()`, `-1` when the sessions pane is not the selection source).

**Design.** Focus stays derived. Pane order: `paneChannels, paneSessions, paneStream, paneCompose`. `m.sessSel >= 0` means "the stream shows a session's inbox". `selected()` becomes the single switch point:

```go
func (m *Model) selected() *bus.Channel {
	if m.sessSel >= 0 {
		names := m.sessionNames()
		if m.sessSel < len(names) {
			want := bus.DMChannel(names[m.sessSel])
			for i := range m.dms {
				if m.dms[i].Name == want {
					return &m.dms[i]
				}
			}
		}
		return nil
	}
	// existing body
}
```

Everything keyed on `selName()` (stream, history load, unread, markSeen, divider) then works for inboxes unchanged. `pane()`: insert/compose and stream cases first as today, then `m.sessSel >= 0 → paneSessions`, else `paneChannels`.

- `focusPane(paneSessions)`: if `len(m.sessionNames()) == 0` skip (paneKey's eligibility check); else set `m.sessSel = max(m.sessSel, 0)` and run the same reset `selectChannel` does (cursor -1, follow, replyTo nil, divider, markSeen, refreshStream, GotoBottom, loadHistory if not loaded). Factor that tail of `selectChannel` into `func (m *Model) showSelected() tea.Cmd` and call it from both.
- `focusPane(paneChannels)` (and `home`): set `m.sessSel = -1`, then `showSelected()` so the stream returns to the channel.
- Moving from sessions to stream/compose keeps `sessSel`, so `pane()` needs the stream/compose checks to win — they already come first.
- Up/down in `paneSessions`: `m.sessSel = clamp(m.sessSel±1)`, then `showSelected()`.
- `renderRails`: highlight the `sessSel` row with `th.Highlight(markSel+row, rail)` exactly as the channel list does; when `sessSel >= 0` the channel list draws no highlighted row.
- `renderHeader`: for a DM channel, `@<owner> · direct · n unread · m messages`, name in `th.User` when owner is `m.c.as`, else `th.Agent`. `chanStyle` gets the same rule for DM channels; `renderCompose` label is `iconAgent`/`iconUser` + `@owner` — reuse the icons the session rows use.
- Keys `d` (delete channel) and `s` (toggle subscribe): no-ops while `sessSel >= 0`.
- Reply (`r`, and enter-on-cursor): while `sessSel >= 0`, allowed only if the inbox owner is `m.c.as`; otherwise set the toast `can't reply inside another identity's inbox; compose sends them a direct message` and do nothing else.
- `compose.go` send: target channel is `m.selName()`, except when `m.replyTo != nil` and the selected channel is the TUI's own inbox: then `bus.DMChannel(m.replyTo.Sender)` with `ReplyTo` set. The sent message will not appear in the viewed inbox (it went to the sender's); show the toast-free confirmation by leaving the view as is.
- `help.go`: document the sessions pane and that compose there sends a direct message.

- [ ] **Step 1: Failing tests** (append to `dm_test.go`):

```go
func TestSessionsPaneShowsInboxAndComposesDM(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.key("esc")
	f.key("tab") // channels -> sessions
	if f.m.pane() != paneSessions {
		t.Fatalf("pane=%v", f.m.pane())
	}
	names := f.m.sessionNames()
	for f.m.sessSel < len(names)-1 && names[f.m.sessSel] != "Sam" {
		f.key("down")
	}
	if f.m.selName() != "dm/Sam" {
		t.Fatalf("selected=%q", f.m.selName())
	}
	if h := f.m.renderHeader(); !strings.Contains(h, "@Sam") || !strings.Contains(h, "direct") {
		t.Fatalf("header=%q", h)
	}
	f.key("i")
	for _, r := range "hello sam" {
		f.key(string(r))
	}
	f.key("enter")
	ms, err := f.ab.History(f.sam, "dm/Sam", nil, nil, 10)
	if err != nil || len(ms) != 1 || ms[0].Content != "hello sam" || ms[0].Sender != f.c.as {
		t.Fatalf("%+v %v", ms, err)
	}
	f.key("esc")
	f.key("home")
	if f.m.pane() != paneChannels || strings.HasPrefix(f.m.selName(), "dm/") {
		t.Fatalf("home must return to the channel list: pane=%v sel=%q", f.m.pane(), f.m.selName())
	}
}

func TestReplyOnlyInOwnInbox(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.agentSend(t, "dm/"+f.c.as, "which oauth provider?")
	f.agentSend(t, "dm/Sam", "note")
	f.receive(t)
	f.key("esc")
	f.key("tab")
	sel := func(name string) {
		for i, n := range f.m.sessionNames() {
			if n == name {
				for f.m.sessSel < i {
					f.key("down")
				}
				for f.m.sessSel > i {
					f.key("up")
				}
			}
		}
	}
	sel("Sam")
	f.key("r")
	if f.m.replyTo != nil || f.m.toast == "" {
		t.Fatalf("reply must be refused in another identity's inbox: replyTo=%v toast=%q", f.m.replyTo, f.m.toast)
	}
	f.key("x") // clears the toast
	sel(f.c.as)
	f.key("r")
	if f.m.replyTo == nil {
		t.Fatal("reply must work in the TUI's own inbox")
	}
	for _, r := range "github" {
		f.key(string(r))
	}
	f.key("enter")
	ms, _ := f.ab.History(f.sam, "dm/Sam", nil, nil, 10)
	last := ms[len(ms)-1]
	if last.Content != "github" || last.ReplyTo == nil {
		t.Fatalf("reply must land in the sender's inbox with reply_to: %+v", last)
	}
}

// A reply whose parent lives in another channel renders as a top-level row.
func TestCrossChannelReplyRendersTopLevel(t *testing.T) {
	f := newFixture(t)
	parent := f.agentSend(t, "dev", "parent in dev")
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dm/" + f.c.as, Content: "child elsewhere", ReplyTo: &parent.Seq}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.receive(t)
	rs := f.m.rows("dm/" + f.c.as)
	if len(rs) != 1 || rs[0].depth != 0 || rs[0].msg.Content != "child elsewhere" {
		t.Fatalf("%+v", rs)
	}
}
```

Adjust key names to what `f.key` accepts (read `fixture.key`). `f.key("x")` stands for any key that clears a toast per `TestToastClearsOnKey`; use what that test uses.

- [ ] **Step 2: Run** → FAIL (`paneSessions` undefined).
- [ ] **Step 3: Implement** per the Design block. Update `TestTabCyclesPanesAndHomeReturnsToChannels` and `TestArrowsFollowTheFocusedPane` for the new pane order.
- [ ] **Step 4: Run `go test ./internal/tui`**; then build and eyeball: `go build -o "$TMPDIR/agentbus" . && AGENTBUS_DATA_DIR=$(mktemp -d) "$TMPDIR/agentbus" tui` cannot be run by a subagent (no TTY) — note it in the report as owed to the human.
- [ ] **Step 5: Gate and commit** (`feat(tui): sessions pane shows and sends direct messages`).

---

### Task 7: Skill, docs, ADR (controller task, not a subagent)

**Files:** `internal/cli/skills/using-agentbus/SKILL.md`, `docs/superpowers/specs/2026-09-07-agentbus-local-design-v2.md` (tool table / channel section: add DM paragraph pointing at the DM spec), `docs/install.md` if it lists TUI keys, `docs/adr/0004-direct-messages-and-session-end.md` (new), `PROGRESS.md`, `HANDOFF.md`.

- [ ] **Step 1 (RED is already recorded):** baseline with the current skill: cross-repo completion reached the dependent repo in 2/5 runs (2026-09-17).
- [ ] **Step 2:** Edit the skill: add a "Direct messages" section (send to `dm/<name>`; one-way; answer with a DM to the sender and `reply_to`; inbox is read by `receive`; `agentbus wait` wakes on DMs regardless of `-filter`), and these table rows:
  - `Changed something one specific other agent depends on` → `dm/<that agent>` and `<repo>` → what changed, what they must do; send the completion notice the same way.
  - `Question only the user can answer` → `dm/<the human's TUI identity>` when it is live in `others`, else `<repo>`.
  Keep the description frontmatter free of workflow.
- [ ] **Step 3 (GREEN):** rerun the paper scenario (5 control, 5 with-skill, Sonnet, plain Agent dispatch — no Workflow tool) with S3 extended: "`others` lists `tmi-ux`". Pass = ≥4/5 with-skill runs DM `dm/tmi-ux` for both the start and the completion notice. Tighten wording and rerun if not.
- [ ] **Step 4:** ADR 0004, sections: Human decisions (copy the spec's nine, plus "session end without a harness hook", dated 2026-09-17); Controller decisions (inbox subscription recreated at cursor 0 on `resume=false`; `not_found` rather than a permission error for foreign inboxes).
- [ ] **Step 5:** `agentbus init --global` reinstall is owed to the human after release; note it in HANDOFF. Commit (`docs: direct messages skill, ADR 0004`).

---

## Self-review notes

- Spec coverage: naming/creation → T1; guard table → T2 (every row has a test line); observer → T2/T5; wait → T3; MCP text + protocol → T4; TUI bullets → T5/T6; skill + retest → T7; "existing channel named dm keeps working" → only creation is blocked in T1, nothing else touches it.
- The spec's "`channels` filter naming another identity's DM channel yields nothing" needs no code: receive only reads the caller's subscriptions.
- Names used across tasks: `DMPrefix`, `DMChannel`, `dmOwner`, `dmReadable`, `SetObserver`, `ensureInbox`, `Model.dms`, `Model.sessSel`, `sessionNames()`, `showSelected()`, `paneSessions`.
