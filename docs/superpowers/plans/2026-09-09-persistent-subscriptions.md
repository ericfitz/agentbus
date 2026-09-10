# Persistent Subscriptions, JSONL Logs, and Session Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make channel subscriptions persistent through a `channels` list in the repo's `.local/agentbus.json`, applied automatically by `register`; add CLI and MCP ways to edit that list; switch the log file to JSONL with UTC millisecond timestamps; and replace the permissive hook text with a prescriptive session protocol.

**Architecture:** A new package `internal/repoconfig` owns reading and writing `.local/agentbus.json` (the upward walk that today lives privately in `internal/cli`, plus add/remove of channels). The MCP server's `register`, `subscribe`, and `unsubscribe` handlers call into it from the server's working directory; the CLI's new `subscribe`/`unsubscribe` commands call into it from cwd, requiring a repo root. The bus package is unchanged except for two new fields on `Registration`. Logging changes only in `OpenLog`.

**Tech Stack:** Go 1.27, `log/slog`, `github.com/modelcontextprotocol/go-sdk/mcp` v1.7.0, modernc SQLite. Tests are plain `testing` with `t.TempDir()` and `t.Chdir()`.

**Spec:** `docs/superpowers/specs/2026-09-09-persistent-subscriptions-design.md`

## Global Constraints

- Go toolchain from `go.mod` (`go 1.27.1`); no new dependencies.
- Every MCP tool description starts with `Agentbus:` (enforced by `TestToolsRegisteredWithPrefixDescriptions`).
- Channel and identity names are validated by `bus.NameRule`; never invent a separate rule.
- American English in all text (`color`, not `colour`).
- Log file name stays `agentbus.log`; rotation (four 16 MiB files) and flock unchanged.
- The hook output's first line must start with `Agentbus: ` and contain the identity in double quotes.
- Lint (`go vet ./...`) and all tests (`go test ./...`) must pass before each commit.
- Work on a branch `persistent-subscriptions` created from `main`; never commit `HANDOFF.md`.

---

## File Structure

| Path | Responsibility |
|---|---|
| `internal/repoconfig/repoconfig.go` (create) | Locate, read, and edit `.local/agentbus.json`: `Find`, `Load`, `File.Channels()`, `AddChannel`, `RemoveChannel`, `DefaultChannels`. |
| `internal/repoconfig/repoconfig_test.go` (create) | Unit tests for the above. |
| `internal/cli/identity.go` (modify) | Drop `readIdentityFile`; call `repoconfig.Find`. New prescriptive `identityLine`. Hold the shared `Protocol` constant. |
| `internal/cli/init.go` (modify) | `InitPrompt` embeds `Protocol`. |
| `internal/cli/subscribe.go` (create) | `Subscribe(cwd, channel, out)` and `Unsubscribe(cwd, channel, out)` CLI commands. |
| `internal/cli/subscribe_test.go` (create) | Tests for the CLI commands. |
| `internal/cli/cli_test.go` (modify) | Existing identity tests keep using `identityLine(name)`; add a protocol-content assertion. |
| `internal/bus/sessions.go` (modify) | Add `Subscribed` and `SubscribeFailed` fields to `Registration`. |
| `internal/mcpserver/server.go` (modify) | `register` applies the persistent list; `subscribe`/`unsubscribe` gain `persistent`. |
| `internal/mcpserver/server_test.go` (modify) | Tests for the three handler changes. |
| `internal/mcpserver/logfile.go` (modify) | `OpenLog` emits JSONL, UTC ms timestamps, `pid`. |
| `internal/mcpserver/logfile_test.go` (modify) | Test one line's shape. |
| `main.go` (modify) | Dispatch `subscribe` and `unsubscribe`; usage line. |
| `docs/install.md`, `README.md` (modify) | Document `channels`, the new commands, the `persistent` parameter, the log format. |

---

### Task 0: Branch

**Files:** none.

- [ ] **Step 1: Create the branch**

```bash
cd /Users/efitz/Projects/agentbus
git checkout -b persistent-subscriptions main
go build ./... && go test ./... 2>&1 | tail -5
```

Expected: build ok, all packages `ok`.

---

### Task 1: `internal/repoconfig` — find and load

**Files:**
- Create: `internal/repoconfig/repoconfig.go`
- Test: `internal/repoconfig/repoconfig_test.go`

**Interfaces:**
- Produces:

```go
package repoconfig

// DefaultChannels is the persistent list when the file has no "channels" key.
var DefaultChannels = []string{"general", "memory"}

// File is a parsed .local/agentbus.json. Raw holds every key so writers
// preserve ones they do not understand.
type File struct {
	Path     string
	Identity string
	Raw      map[string]any
}

// Find walks up from dir to the nearest .local/agentbus.json, stopping at the
// nearest .git. It returns (nil, nil) when no file is found. A file that is
// unreadable or malformed, or whose identity fails bus.NameRule, returns an
// error naming the path.
func Find(dir string) (*File, error)

// Load reads exactly dir/.local/agentbus.json. os.IsNotExist(err) is true
// when the file is absent.
func Load(dir string) (*File, error)

// Channels returns the persistent channel list: DefaultChannels when the key
// is absent, otherwise the deduplicated entries in file order. Entries that
// are not strings or fail bus.NameRule are returned in bad and omitted.
func (f *File) Channels() (channels []string, bad []string)
```

- [ ] **Step 1: Write the failing tests**

`internal/repoconfig/repoconfig_test.go`:

```go
package repoconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".local", "agentbus.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindWalksUpAndStopsAtGit(t *testing.T) {
	outer := filepath.Join(t.TempDir(), "outer")
	inner := filepath.Join(outer, "inner")
	sub := filepath.Join(inner, "a", "b")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.MkdirAll(filepath.Join(outer, ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(inner, ".git"), 0o755)
	writeFile(t, outer, `{"identity":"Outer"}`)

	f, err := Find(sub)
	if err != nil || f != nil {
		t.Fatalf("outer file must not leak past inner .git: %v %v", f, err)
	}
	p := writeFile(t, inner, `{"identity":"Inner"}`)
	f, err = Find(sub)
	if err != nil || f == nil || f.Identity != "Inner" || f.Path != p {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestFindReportsMalformedAndInvalidIdentity(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{not json`)
	if _, err := Find(dir); err == nil {
		t.Fatal("malformed file must error")
	}
	writeFile(t, dir, `{"identity":"has space"}`)
	if _, err := Find(dir); err == nil {
		t.Fatal("invalid identity must error")
	}
}

func TestLoadNotExist(t *testing.T) {
	_, err := Load(t.TempDir())
	if !os.IsNotExist(err) {
		t.Fatalf("want IsNotExist, got %v", err)
	}
}

func TestChannelsDefaultsWhenKeyAbsent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam"}`)
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, bad := f.Channels()
	if !reflect.DeepEqual(got, []string{"general", "memory"}) || len(bad) != 0 {
		t.Fatalf("%v %v", got, bad)
	}
}

func TestChannelsEmptyListMeansNone(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","channels":[]}`)
	f, _ := Load(dir)
	got, _ := f.Channels()
	if len(got) != 0 {
		t.Fatal(got)
	}
}

func TestChannelsDedupesAndReportsBad(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","channels":["reviews","general","reviews","bad name",7]}`)
	f, _ := Load(dir)
	got, bad := f.Channels()
	if !reflect.DeepEqual(got, []string{"reviews", "general"}) {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(bad, []string{"bad name", "7"}) {
		t.Fatal(bad)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/repoconfig/`
Expected: FAIL, "undefined: Find" (package does not compile yet).

- [ ] **Step 3: Implement**

`internal/repoconfig/repoconfig.go`:

```go
// Package repoconfig reads and edits the per-repository Agentbus file,
// .local/agentbus.json, which names the identity to register and the
// channels to subscribe persistently.
package repoconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ericfitz/agentbus/internal/bus"
)

// DefaultChannels is the persistent list when the file has no "channels" key.
var DefaultChannels = []string{"general", "memory"}

// File is a parsed .local/agentbus.json. Raw holds every key so writers
// preserve ones they do not understand.
type File struct {
	Path     string
	Identity string
	Raw      map[string]any
}

func pathIn(dir string) string { return filepath.Join(dir, ".local", "agentbus.json") }

// Load reads exactly dir/.local/agentbus.json. os.IsNotExist(err) is true
// when the file is absent.
func Load(dir string) (*File, error) {
	p := pathIn(dir)
	body, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	raw := map[string]any{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	id, _ := raw["identity"].(string)
	if id == "" {
		return nil, fmt.Errorf("%s: missing identity", p)
	}
	if err := bus.NameRule(id); err != nil {
		return nil, fmt.Errorf("%s: identity %q: %w", p, id, err)
	}
	return &File{Path: p, Identity: id, Raw: raw}, nil
}

// Find walks up from dir to the nearest .local/agentbus.json, stopping at the
// nearest .git. It returns (nil, nil) when no file is found. A file that is
// unreadable or malformed, or whose identity fails bus.NameRule, returns an
// error naming the path.
func Find(dir string) (*File, error) {
	for {
		f, err := Load(dir)
		if err == nil {
			return f, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return nil, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

// Channels returns the persistent channel list: DefaultChannels when the key
// is absent, otherwise the deduplicated entries in file order. Entries that
// are not strings or fail bus.NameRule are returned in bad and omitted.
func (f *File) Channels() (channels []string, bad []string) {
	v, ok := f.Raw["channels"]
	if !ok {
		return append([]string(nil), DefaultChannels...), nil
	}
	list, _ := v.([]any)
	channels = []string{}
	seen := map[string]bool{}
	for _, e := range list {
		s, ok := e.(string)
		if !ok || bus.NameRule(s) != nil {
			bad = append(bad, fmt.Sprint(e))
			continue
		}
		if !seen[s] {
			seen[s] = true
			channels = append(channels, s)
		}
	}
	return channels, bad
}
```

Note: `fmt.Sprint(7.0)` for a JSON number prints `7`, matching the test.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/repoconfig/ && go vet ./internal/repoconfig/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repoconfig/
git commit -m "feat(repoconfig): find and load .local/agentbus.json with channels

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: `internal/repoconfig` — add and remove channels

**Files:**
- Modify: `internal/repoconfig/repoconfig.go`
- Test: `internal/repoconfig/repoconfig_test.go`

**Interfaces:**
- Produces:

```go
// AddChannel appends channel to the file's list (materializing DefaultChannels
// first when the key is absent), dedupes, writes the file, and returns the
// resulting list. channel must pass bus.NameRule.
func (f *File) AddChannel(channel string) ([]string, error)

// RemoveChannel removes channel from the list (materializing DefaultChannels
// first when the key is absent), writes the file, and returns the resulting
// list. Removing an absent channel is a no-op that still writes.
func (f *File) RemoveChannel(channel string) ([]string, error)

// Create writes a new file at dir/.local/agentbus.json with the given identity
// and no channels key, creating .local/ if needed. It fails if the file exists.
func Create(dir, identity string) (*File, error)
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/repoconfig/repoconfig_test.go`:

```go
func TestAddChannelMaterializesDefaultsAndPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam","future":{"x":1}}`)
	f, _ := Load(dir)
	got, err := f.AddChannel("reviews")
	if err != nil || !reflect.DeepEqual(got, []string{"general", "memory", "reviews"}) {
		t.Fatalf("%v %v", got, err)
	}
	f2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f2.Raw["future"]; !ok {
		t.Fatal("unknown key dropped")
	}
	again, _ := f2.AddChannel("reviews")
	if !reflect.DeepEqual(again, []string{"general", "memory", "reviews"}) {
		t.Fatal("duplicate added:", again)
	}
	body, _ := os.ReadFile(f.Path)
	if body[len(body)-1] != '\n' {
		t.Fatal("file must end with newline")
	}
}

func TestAddChannelRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam"}`)
	f, _ := Load(dir)
	if _, err := f.AddChannel("bad name"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRemoveChannel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, `{"identity":"Sam"}`)
	f, _ := Load(dir)
	got, err := f.RemoveChannel("memory")
	if err != nil || !reflect.DeepEqual(got, []string{"general"}) {
		t.Fatalf("%v %v", got, err)
	}
	got, err = f.RemoveChannel("nope")
	if err != nil || !reflect.DeepEqual(got, []string{"general"}) {
		t.Fatalf("%v %v", got, err)
	}
	f2, _ := Load(dir)
	ch, _ := f2.Channels()
	if !reflect.DeepEqual(ch, []string{"general"}) {
		t.Fatal(ch)
	}
}

func TestCreate(t *testing.T) {
	dir := t.TempDir()
	f, err := Create(dir, "Sam")
	if err != nil || f.Identity != "Sam" {
		t.Fatalf("%v %v", f, err)
	}
	if _, err := Create(dir, "Sam"); err == nil {
		t.Fatal("second create must fail")
	}
	if _, err := Create(t.TempDir(), "bad name"); err == nil {
		t.Fatal("invalid identity must fail")
	}
	ch, _ := f.Channels()
	if !reflect.DeepEqual(ch, []string{"general", "memory"}) {
		t.Fatal(ch)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/repoconfig/`
Expected: FAIL, "f.AddChannel undefined".

- [ ] **Step 3: Implement**

Append to `internal/repoconfig/repoconfig.go`:

```go
// Create writes a new file at dir/.local/agentbus.json with the given identity
// and no channels key, creating .local/ if needed. It fails if the file exists.
func Create(dir, identity string) (*File, error) {
	if err := bus.NameRule(identity); err != nil {
		return nil, fmt.Errorf("identity %q: %w", identity, err)
	}
	f := &File{Path: pathIn(dir), Identity: identity, Raw: map[string]any{"identity": identity}}
	if _, err := os.Stat(f.Path); err == nil {
		return nil, fmt.Errorf("%s already exists", f.Path)
	}
	return f, f.write()
}

// AddChannel appends channel to the file's list (materializing DefaultChannels
// first when the key is absent), dedupes, writes the file, and returns the
// resulting list. channel must pass bus.NameRule.
func (f *File) AddChannel(channel string) ([]string, error) {
	if err := bus.NameRule(channel); err != nil {
		return nil, fmt.Errorf("channel %q: %w", channel, err)
	}
	list, _ := f.Channels()
	found := false
	for _, c := range list {
		if c == channel {
			found = true
		}
	}
	if !found {
		list = append(list, channel)
	}
	return list, f.setChannels(list)
}

// RemoveChannel removes channel from the list (materializing DefaultChannels
// first when the key is absent), writes the file, and returns the resulting
// list. Removing an absent channel is a no-op that still writes.
func (f *File) RemoveChannel(channel string) ([]string, error) {
	list, _ := f.Channels()
	out := []string{}
	for _, c := range list {
		if c != channel {
			out = append(out, c)
		}
	}
	return out, f.setChannels(out)
}

func (f *File) setChannels(list []string) error {
	arr := make([]any, len(list))
	for i, c := range list {
		arr[i] = c
	}
	f.Raw["channels"] = arr
	return f.write()
}

func (f *File) write() error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f.Raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.Path, append(data, '\n'), 0o600)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/repoconfig/ && go vet ./internal/repoconfig/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repoconfig/
git commit -m "feat(repoconfig): add, remove, and create persistent channels

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: Hook text and init prompt use `repoconfig` and the protocol

**Files:**
- Modify: `internal/cli/identity.go`
- Modify: `internal/cli/init.go` (the `InitPrompt` constant only)
- Modify: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `repoconfig.Find`.
- Produces: `cli.Protocol` (string constant), `cli.identityLine(name string) string` (unchanged signature, new content).

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/cli_test.go`:

```go
func TestIdentityLineIsPrescriptive(t *testing.T) {
	got := identityLine("Sam")
	for _, want := range []string{
		`Agentbus: call the register tool now with the name parameter set to "Sam"`,
		"Then follow this protocol:",
		"- Call receive right after registering",
		"- Post to your subscribed chat channel",
		"- Search all your subscribed memory channels",
		"- Post to a memory channel whenever you discover a non-obvious fact",
		"- Call discover before assuming you are the only agent working.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("must end with newline")
	}
	if !strings.Contains(InitPrompt, Protocol) {
		t.Fatal("InitPrompt must embed Protocol")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/cli/ -run TestIdentityLineIsPrescriptive`
Expected: FAIL, "undefined: Protocol".

- [ ] **Step 3: Implement**

Replace the body of `internal/cli/identity.go` from `func identity(` to the end of the file with:

```go
func identity(cwd string, out, warn io.Writer) error {
	name := filepath.Base(cwd)
	f, err := repoconfig.Find(cwd)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(warn, "agentbus: %v\n", err)
		name = gitBaseOr(cwd, name)
	case f != nil:
		name = f.Identity
		if _, bad := f.Channels(); len(bad) > 0 {
			_, _ = fmt.Fprintf(warn, "agentbus: %s: ignoring invalid channels %q\n", f.Path, bad)
		}
	default:
		name = gitBaseOr(cwd, name)
	}
	_, err = fmt.Fprint(out, identityLine(name))
	return err
}

// gitBaseOr returns the basename of the nearest directory at or above cwd
// that contains .git, or fallback when there is none.
func gitBaseOr(cwd, fallback string) string {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Base(dir)
		}
		if filepath.Dir(dir) == dir {
			return fallback
		}
	}
}

// Protocol is what every agent is told to do on Agentbus after registering.
// The SessionStart hook prints it after the register sentence and the init
// prompt embeds it, so there is one source of truth.
const Protocol = `Then follow this protocol:
- Call receive right after registering, whenever you finish a task, and before
  you ask the user a question. Pass each batch's token as ack on your next
  receive.
- Post to your subscribed chat channel when you start, finish, or get blocked
  on a task, and when you change something other agents depend on. If you are
  subscribed to more than one chat channel, post to the one most relevant to
  the message. Reply to messages addressed to you.
- Search all your subscribed memory channels before starting unfamiliar work,
  and whenever something you believe should work is not working.
- Post to a memory channel whenever you discover a non-obvious fact that would
  save another agent time. Examples: "tool X does not honor --y; workaround is
  Z"; "the spec for feature A says B, but I verified with <test> that the
  correct behavior is C"; "to accomplish J, I tried K, L, and M, which failed;
  P worked."
- Call discover before assuming you are the only agent working.
`

// identityLine is what the SessionStart hook prints: the register sentence
// naming the identity, then the protocol.
func identityLine(name string) string {
	return "Agentbus: call the register tool now with the name parameter set to \"" + name + "\",\n" +
		"and pass the \"as\" value it returns on every later Agentbus call. Register\n" +
		"subscribes you to this repository's persistent channels (from\n" +
		".local/agentbus.json; default: general for chat, memory for memories).\n" +
		Protocol
}
```

Update the imports of `identity.go` to:

```go
import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ericfitz/agentbus/internal/repoconfig"
)
```

(`encoding/json` and `internal/bus` are no longer used there.) Delete the old `readIdentityFile` function and its comment.

In `internal/cli/init.go`, replace the `InitPrompt` constant with:

```go
const InitPrompt = `Set up Agentbus for this repository:
1. Run ` + "`agentbus init`" + ` in a shell. Inside a git repository it writes the
   repository's identity file and prints a block beginning
   "Agentbus: call the register tool now with the name parameter set to ...".
2. Call the Agentbus register tool with that name, then pass the returned
   "as" value on every later Agentbus call. Register subscribes you to the
   repository's persistent channels (default: general for chat, memory for
   memories).
` + Protocol + `If the command reports that the MCP server is not configured yet, tell the
user to run ` + "`agentbus init --global`" + ` from a shell and restart the harness.`
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/cli/ && go vet ./internal/cli/`
Expected: PASS. The existing identity tests compare against `identityLine(...)` so they pass with the new text. If `init_test.go` asserts on the old "default channels:" string, update that assertion to `strings.Contains(out, "call the register tool now with the name parameter set to")`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/identity.go internal/cli/init.go internal/cli/cli_test.go internal/cli/init_test.go
git commit -m "feat(cli): prescriptive session protocol in hook and init prompt

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: CLI `subscribe` and `unsubscribe`

**Files:**
- Create: `internal/cli/subscribe.go`
- Test: `internal/cli/subscribe_test.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: `repoconfig.Load`, `repoconfig.Create`, `(*File).AddChannel`, `(*File).RemoveChannel`.
- Produces: `cli.Subscribe(cwd, channel string, out io.Writer) error`, `cli.Unsubscribe(cwd, channel string, out io.Writer) error`.

- [ ] **Step 1: Write the failing tests**

`internal/cli/subscribe_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/repoconfig"
)

func repoRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSubscribeRequiresRepoRoot(t *testing.T) {
	var out bytes.Buffer
	err := Subscribe(t.TempDir(), "reviews", &out)
	if err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("want repository root error, got %v", err)
	}
}

func TestSubscribeCreatesFileAndAddsChannel(t *testing.T) {
	root := repoRoot(t, "myrepo")
	var out bytes.Buffer
	if err := Subscribe(root, "reviews", &out); err != nil {
		t.Fatal(err)
	}
	want := "Agentbus: persistent channels for myrepo: general, memory, reviews\n"
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("%q", out.String())
	}
	if !strings.Contains(out.String(), "next register") {
		t.Fatal("must say when it takes effect")
	}
	f, err := repoconfig.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := f.Channels()
	if len(ch) != 3 || ch[2] != "reviews" {
		t.Fatal(ch)
	}
}

func TestSubscribeRejectsBadName(t *testing.T) {
	root := repoRoot(t, "myrepo")
	if err := Subscribe(root, "bad name", &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnsubscribe(t *testing.T) {
	root := repoRoot(t, "myrepo")
	var out bytes.Buffer
	if err := Unsubscribe(root, "memory", &out); err == nil {
		t.Fatal("unsubscribe on a missing file must error")
	}
	if err := Subscribe(root, "reviews", &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Unsubscribe(root, "memory", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Agentbus: persistent channels for myrepo: general, reviews\n") {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if err := Unsubscribe(root, "nope", &out); err != nil {
		t.Fatal("unsubscribing an unlisted channel is a no-op, got", err)
	}
	if !strings.HasPrefix(out.String(), "Agentbus: persistent channels for myrepo: general, reviews\n") {
		t.Fatalf("%q", out.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/cli/ -run 'TestSubscribe|TestUnsubscribe'`
Expected: FAIL, "undefined: Subscribe".

- [ ] **Step 3: Implement**

`internal/cli/subscribe.go`:

```go
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericfitz/agentbus/internal/repoconfig"
)

var errNotRepoRoot = errors.New("run this from the repository root (the directory containing .git)")

// Subscribe adds channel to the persistent list in cwd/.local/agentbus.json,
// creating the file when absent. cwd must be a repository root.
func Subscribe(cwd, channel string, out io.Writer) error {
	f, err := openRepoFile(cwd, true)
	if err != nil {
		return err
	}
	list, err := f.AddChannel(channel)
	if err != nil {
		return err
	}
	return printChannels(out, f.Identity, list)
}

// Unsubscribe removes channel from the persistent list in
// cwd/.local/agentbus.json. cwd must be a repository root with the file present.
func Unsubscribe(cwd, channel string, out io.Writer) error {
	f, err := openRepoFile(cwd, false)
	if err != nil {
		return err
	}
	list, err := f.RemoveChannel(channel)
	if err != nil {
		return err
	}
	return printChannels(out, f.Identity, list)
}

// openRepoFile loads cwd/.local/agentbus.json after checking cwd is a
// repository root. With create set, a missing file is created with the
// identity agentbus init would choose (the basename of cwd).
func openRepoFile(cwd string, create bool) (*repoconfig.File, error) {
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err != nil {
		return nil, errNotRepoRoot
	}
	f, err := repoconfig.Load(cwd)
	if os.IsNotExist(err) && create {
		return repoconfig.Create(cwd, filepath.Base(cwd))
	}
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w; run `agentbus init` first", err)
	}
	return f, err
}

func printChannels(out io.Writer, identity string, list []string) error {
	shown := strings.Join(list, ", ")
	if shown == "" {
		shown = "(none)"
	}
	_, err := fmt.Fprintf(out, "Agentbus: persistent channels for %s: %s\nApplied at the next register in this repository.\n", identity, shown)
	return err
}
```

In `main.go`, add two cases before `default:` and update the usage line:

```go
	case "subscribe", "unsubscribe":
		if len(args) != 1 {
			fmt.Fprintf(os.Stderr, "usage: agentbus %s <channel>\n", cmd)
			return 2
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		if cmd == "subscribe" {
			err = cli.Subscribe(cwd, args[0], os.Stdout)
		} else {
			err = cli.Unsubscribe(cwd, args[0], os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentbus:", err)
			return 1
		}
		return 0
```

Usage line:

```go
fmt.Fprintln(os.Stderr, "usage: agentbus <init|mcp|tui|status|reset|identity|subscribe|unsubscribe|version> [flags]")
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/cli/ && go vet ./... && go build ./...`
Expected: PASS. Then a manual smoke test in a scratch repo:

```bash
d=$(mktemp -d) && cd $d && git init -q && go run /Users/efitz/Projects/agentbus subscribe reviews && cat .local/agentbus.json && go run /Users/efitz/Projects/agentbus unsubscribe memory && cd /Users/efitz/Projects/agentbus
```

Expected: the two result lines, and the file showing `"channels": ["general", "memory", "reviews"]` then `["general", "reviews"]`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/subscribe.go internal/cli/subscribe_test.go main.go
git commit -m "feat(cli): subscribe and unsubscribe edit the repo's persistent channels

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: `register` applies the persistent list

**Files:**
- Modify: `internal/bus/sessions.go:12-16` (the `Registration` struct)
- Modify: `internal/mcpserver/server.go` (the `register` handler and `newServer`)
- Test: `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: `repoconfig.Find`, `(*File).Channels`, `bus.Subscribe(as, channel, from string) error`.
- Produces: `bus.Registration.Subscribed []string` (JSON `subscribed`), `bus.Registration.SubscribeFailed map[string]string` (JSON `subscribe_failed,omitempty`); `mcpserver.applyPersistent(b *bus.Bus, cwd string, reg *bus.Registration)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/mcpserver/server_test.go`:

```go
// testSessionIn is testSession with the server's working directory set to
// dir, so register finds dir/.local/agentbus.json.
func testSessionIn(t *testing.T, dir string) *mcp.ClientSession {
	t.Helper()
	t.Chdir(dir)
	return testSession(t)
}

func writeRepoFile(t *testing.T, dir, body string) {
	t.Helper()
	p := filepath.Join(dir, ".local", "agentbus.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := []string{}
	for _, e := range list {
		out = append(out, e.(string))
	}
	return out
}

func TestRegisterSubscribesDefaultsWithoutRepoFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	cs := testSessionIn(t, dir)
	reg, _ := call(t, cs, "register", map[string]any{"name": "Sam"})
	if got := stringsOf(reg["subscribed"]); len(got) != 2 || got[0] != "general" || got[1] != "memory" {
		t.Fatal(reg)
	}
	if _, ok := reg["subscribe_failed"]; ok {
		t.Fatal("subscribe_failed must be omitted when empty", reg)
	}
	// A message on general is now received without an explicit subscribe.
	kim, _ := call(t, cs, "register", map[string]any{"name": "Kim"})
	call(t, cs, "send", map[string]any{"as": kim["as"], "channel": "general", "content": "hi"})
	got, _ := call(t, cs, "receive", map[string]any{"as": "Sam"})
	if msgs, _ := got["messages"].([]any); len(msgs) != 1 {
		t.Fatal(got)
	}
}

func TestRegisterSubscribesListedChannelsAndReportsUnknown(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	writeRepoFile(t, dir, `{"identity":"Sam","channels":["memory","reviews"]}`)
	cs := testSessionIn(t, dir)
	reg, _ := call(t, cs, "register", map[string]any{"name": "Sam"})
	if got := stringsOf(reg["subscribed"]); len(got) != 1 || got[0] != "memory" {
		t.Fatal(reg)
	}
	failed, _ := reg["subscribe_failed"].(map[string]any)
	if msg, _ := failed["reviews"].(string); !strings.Contains(msg, "does not exist") {
		t.Fatal(reg)
	}
	// Once the channel exists, the next register picks it up; general stays out.
	call(t, cs, "create_channel", map[string]any{"as": "Sam", "name": "reviews", "kind": "ordinary"})
	reg, _ = call(t, cs, "register", map[string]any{"name": "Sam"})
	if got := stringsOf(reg["subscribed"]); len(got) != 2 || got[1] != "reviews" {
		t.Fatal(reg)
	}
	kim, _ := call(t, cs, "register", map[string]any{"name": "Kim"})
	call(t, cs, "send", map[string]any{"as": kim["as"], "channel": "general", "content": "unseen"})
	got, _ := call(t, cs, "receive", map[string]any{"as": "Sam"})
	if msgs, _ := got["messages"].([]any); len(msgs) != 0 {
		t.Fatal("Sam must not be subscribed to general:", got)
	}
}

func TestRegisterEmptyListSubscribesNothing(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	writeRepoFile(t, dir, `{"identity":"Sam","channels":[]}`)
	cs := testSessionIn(t, dir)
	reg, _ := call(t, cs, "register", map[string]any{"name": "Sam"})
	if got := stringsOf(reg["subscribed"]); len(got) != 0 {
		t.Fatal(reg)
	}
}

func TestRegisterPersistentAppliesToSubagentsAndNoResume(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	cs := testSessionIn(t, dir)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	sub, _ := call(t, cs, "register", map[string]any{"name": "worker", "parent": "Sam"})
	if got := stringsOf(sub["subscribed"]); len(got) != 2 {
		t.Fatal(sub)
	}
	call(t, cs, "unsubscribe", map[string]any{"as": "Sam", "channel": "general"})
	reg, _ := call(t, cs, "register", map[string]any{"name": "Sam", "resume": false})
	if got := stringsOf(reg["subscribed"]); len(got) != 2 || got[0] != "general" {
		t.Fatal(reg)
	}
}

func TestRegisterExistingCursorUntouched(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	cs := testSessionIn(t, dir)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	kim, _ := call(t, cs, "register", map[string]any{"name": "Kim"})
	call(t, cs, "send", map[string]any{"as": kim["as"], "channel": "general", "content": "before"})
	// Re-register; the general cursor must still be behind the message.
	call(t, cs, "register", map[string]any{"name": "Sam"})
	got, _ := call(t, cs, "receive", map[string]any{"as": "Sam"})
	if msgs, _ := got["messages"].([]any); len(msgs) != 1 {
		t.Fatal("cursor moved on re-register:", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/mcpserver/ -run TestRegister`
Expected: FAIL on `subscribed` being nil.

- [ ] **Step 3: Implement**

In `internal/bus/sessions.go`, change the struct to:

```go
type Registration struct {
	Sender  string           `json:"as"`
	Resumed bool             `json:"resumed"`
	Pending []PendingChannel `json:"pending"`
	// Subscribed and SubscribeFailed report the persistent channel list the
	// MCP server applied after registering (from .local/agentbus.json). The
	// bus itself never sets them.
	Subscribed      []string          `json:"subscribed"`
	SubscribeFailed map[string]string `json:"subscribe_failed,omitempty"`
}
```

In `Register`, after `reg := Registration{Sender: display, Pending: []PendingChannel{}}`, add `Subscribed: []string{}` to the literal so the JSON is `[]` rather than `null`:

```go
	reg := Registration{Sender: display, Pending: []PendingChannel{}, Subscribed: []string{}}
```

In `internal/mcpserver/server.go`, add the import `"github.com/ericfitz/agentbus/internal/repoconfig"` and this function after `defaultContextFor`:

```go
// applyPersistent subscribes reg.Sender to the repository's persistent
// channel list (from the nearest .local/agentbus.json above cwd, or
// repoconfig.DefaultChannels when there is none) and records the outcome on
// reg. A file that cannot be read counts as absent, so register still
// succeeds; the problem is surfaced in SubscribeFailed under the key "".
func applyPersistent(b *bus.Bus, cwd string, reg *bus.Registration) {
	reg.Subscribed = []string{}
	channels := repoconfig.DefaultChannels
	f, err := repoconfig.Find(cwd)
	if err != nil {
		reg.SubscribeFailed = map[string]string{"": err.Error()}
	} else if f != nil {
		var bad []string
		channels, bad = f.Channels()
		for _, c := range bad {
			if reg.SubscribeFailed == nil {
				reg.SubscribeFailed = map[string]string{}
			}
			reg.SubscribeFailed[c] = "invalid channel name in " + f.Path
		}
	}
	for _, c := range channels {
		if err := b.Subscribe(reg.Sender, c, "now"); err != nil {
			if reg.SubscribeFailed == nil {
				reg.SubscribeFailed = map[string]string{}
			}
			var be *bus.Error
			if errors.As(err, &be) {
				reg.SubscribeFailed[c] = be.Message
			} else {
				reg.SubscribeFailed[c] = err.Error()
			}
			continue
		}
		reg.Subscribed = append(reg.Subscribed, c)
	}
}
```

Add `"errors"` to the imports if not present. Change the `register` handler body to:

```go
		func(ctx context.Context, req *mcp.CallToolRequest, in registerIn) (*mcp.CallToolResult, any, error) {
			c := in.Context
			if c == "" {
				c = defaultContext
			}
			resume := in.Resume == nil || *in.Resume
			reg, err := b.Register(in.Name, in.Parent, c, resume)
			if err != nil {
				return nil, nil, err
			}
			applyPersistent(b, cwd, &reg)
			return result(reg, nil)
		})
```

`cwd` is the `os.Getwd()` result already captured in `newServer`; when `err != nil` it is `""` and `repoconfig.Find("")` walks nothing and returns `(nil, nil)`, which falls through to the defaults.

Update the `register` tool description to:

```go
"Agentbus: register your identity for this session. Idempotent: calling it again from the same session returns the same name. Subscribes you to the repository's persistent channels (.local/agentbus.json; default general for chat and memory for memories) and reports them in subscribed. Returns the display name to pass as `as` on every other Agentbus call, plus pending message counts if the name was resumed."
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/bus/ ./internal/mcpserver/ && go vet ./...`
Expected: PASS. If an existing test asserts an exact register JSON shape (search `"resumed"` in `internal/mcpserver/*_test.go` and `internal/bus/sessions_test.go`), extend its expected value with `"subscribed":[...]`.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/sessions.go internal/mcpserver/server.go internal/mcpserver/server_test.go
git commit -m "feat(mcp): register subscribes to the repo's persistent channels

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: `persistent` parameter on MCP `subscribe` and `unsubscribe`

**Files:**
- Modify: `internal/mcpserver/server.go` (`subscribeIn`, `unsubscribeIn`, both handlers and descriptions)
- Test: `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: `repoconfig.Find`, `repoconfig.Create`, `(*File).AddChannel`, `(*File).RemoveChannel`, `testSessionIn`, `writeRepoFile` from Task 5.
- Produces: `mcpserver.persistFile(cwd string) (*repoconfig.File, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/mcpserver/server_test.go`:

```go
func TestSubscribePersistentWritesRepoFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	writeRepoFile(t, dir, `{"identity":"Sam"}`)
	cs := testSessionIn(t, dir)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	call(t, cs, "create_channel", map[string]any{"as": "Sam", "name": "reviews", "kind": "ordinary"})
	out, _ := call(t, cs, "subscribe", map[string]any{"as": "Sam", "channel": "reviews", "persistent": true})
	if out["subscribed"] != "reviews" || out["persistent"] != true {
		t.Fatal(out)
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".local", "agentbus.json"))
	if !strings.Contains(string(body), `"reviews"`) || !strings.Contains(string(body), `"general"`) {
		t.Fatalf("%s", body)
	}
	out, _ = call(t, cs, "unsubscribe", map[string]any{"as": "Sam", "channel": "memory", "persistent": true})
	if out["unsubscribed"] != "memory" || out["persistent"] != true {
		t.Fatal(out)
	}
	body, _ = os.ReadFile(filepath.Join(dir, ".local", "agentbus.json"))
	if strings.Contains(string(body), `"memory"`) {
		t.Fatalf("memory still listed: %s", body)
	}
}

func TestSubscribePersistentNotWrittenWhenSessionSubscribeFails(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	writeRepoFile(t, dir, `{"identity":"Sam"}`)
	cs := testSessionIn(t, dir)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	_, res := call(t, cs, "subscribe", map[string]any{"as": "Sam", "channel": "nope", "persistent": true})
	if !res.IsError {
		t.Fatal("expected not_found error")
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".local", "agentbus.json"))
	if strings.Contains(string(body), "channels") {
		t.Fatalf("file must be untouched: %s", body)
	}
}

func TestSubscribeNonPersistentLeavesFileAlone(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	cs := testSessionIn(t, dir)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	out, _ := call(t, cs, "subscribe", map[string]any{"as": "Sam", "channel": "general"})
	if _, ok := out["persistent"]; ok {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".local", "agentbus.json")); !os.IsNotExist(err) {
		t.Fatal("no file must be created without persistent")
	}
}

func TestSubscribePersistentCreatesMissingFileAtGitRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myrepo")
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	cs := testSessionIn(t, dir)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	out, _ := call(t, cs, "subscribe", map[string]any{"as": "Sam", "channel": "general", "persistent": true})
	if out["persistent"] != true {
		t.Fatal(out)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".local", "agentbus.json"))
	if err != nil || !strings.Contains(string(body), `"identity": "myrepo"`) {
		t.Fatalf("%s %v", body, err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/mcpserver/ -run TestSubscribe`
Expected: FAIL (`persistent` missing from results, or a schema validation error about an unknown argument).

- [ ] **Step 3: Implement**

In `internal/mcpserver/server.go`, change the input structs:

```go
type subscribeIn struct {
	As         string `json:"as,omitempty"`
	Channel    string `json:"channel"`
	From       string `json:"from,omitempty" jsonschema:"now (default) or oldest"`
	Persistent bool   `json:"persistent,omitempty" jsonschema:"also add the channel to this repository's .local/agentbus.json so register subscribes it in later sessions"`
}
type unsubscribeIn struct {
	As         string `json:"as,omitempty"`
	Channel    string `json:"channel"`
	Persistent bool   `json:"persistent,omitempty" jsonschema:"also remove the channel from this repository's .local/agentbus.json"`
}
```

Add after `applyPersistent`:

```go
// persistFile returns the repository file for persistent subscribe and
// unsubscribe: the nearest .local/agentbus.json above cwd, or a new one at
// the nearest git root (identity = that directory's basename) when none
// exists. Outside a git repository it fails.
func persistFile(cwd string) (*repoconfig.File, error) {
	f, err := repoconfig.Find(cwd)
	if err != nil || f != nil {
		return f, err
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return repoconfig.Create(dir, filepath.Base(dir))
		}
		if filepath.Dir(dir) == dir || dir == "" {
			return nil, errors.New("not inside a git repository; nothing to persist to")
		}
	}
}

// persistErr wraps a repo-file problem in the bus's error envelope so the
// agent sees the same {code,message,retryable} shape as every other failure.
func persistErr(err error) error {
	return &bus.Error{Code: "validation", Message: bus.TruncateErrorMessage(err.Error()), Retryable: false}
}
```

Replace the `subscribe` and `unsubscribe` handlers:

```go
	mcp.AddTool(s, &mcp.Tool{Name: "subscribe", Description: "Agentbus: subscribe to a channel so receive returns its messages. from=now (default) starts at the current position; from=oldest starts at the oldest retained message. persistent=true also records the channel in this repository's .local/agentbus.json so register subscribes it in later sessions."},
		func(ctx context.Context, req *mcp.CallToolRequest, in subscribeIn) (*mcp.CallToolResult, any, error) {
			if err := b.Subscribe(in.As, in.Channel, in.From); err != nil {
				return nil, nil, err
			}
			out := map[string]any{"subscribed": in.Channel}
			if in.Persistent {
				f, err := persistFile(cwd)
				if err != nil {
					return nil, nil, persistErr(err)
				}
				if _, err := f.AddChannel(in.Channel); err != nil {
					return nil, nil, persistErr(err)
				}
				out["persistent"] = true
			}
			return result(out, nil)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "unsubscribe", Description: "Agentbus: unsubscribe from a channel and drop its cursor. persistent=true also removes the channel from this repository's .local/agentbus.json."},
		func(ctx context.Context, req *mcp.CallToolRequest, in unsubscribeIn) (*mcp.CallToolResult, any, error) {
			if err := b.Unsubscribe(in.As, in.Channel); err != nil {
				return nil, nil, err
			}
			out := map[string]any{"unsubscribed": in.Channel}
			if in.Persistent {
				f, err := persistFile(cwd)
				if err != nil {
					return nil, nil, persistErr(err)
				}
				if _, err := f.RemoveChannel(in.Channel); err != nil {
					return nil, nil, persistErr(err)
				}
				out["persistent"] = true
			}
			return result(out, nil)
		})
```

Returning `nil, nil, err` from a handler is the existing error path: the SDK turns it into an `IsError` result whose text is `err.Error()`, and `(*bus.Error).Error()` renders the `{code,message,retryable}` JSON envelope. `persistErr` returns a `*bus.Error`, so it renders identically to every other tool failure. Add `"errors"` to the imports of `server.go` (it is not imported today).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/mcpserver/ && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/server_test.go
git commit -m "feat(mcp): persistent flag on subscribe and unsubscribe

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: JSONL log with UTC millisecond timestamps and pid

**Files:**
- Modify: `internal/mcpserver/logfile.go:139-151` (`OpenLog`)
- Test: `internal/mcpserver/logfile_test.go`

**Interfaces:**
- Produces: `OpenLog(cfg config.Config) (*slog.Logger, error)` (unchanged signature).

- [ ] **Step 1: Write the failing test**

Append to `internal/mcpserver/logfile_test.go`:

```go
func TestOpenLogWritesJSONLWithUTCMillisAndPid(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	log, err := OpenLog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "k", "v")
	body, err := os.ReadFile(filepath.Join(cfg.DataDirectory, "agentbus.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one line, got %d: %q", len(lines), body)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("not JSON: %v: %s", err, lines[0])
	}
	ts, _ := rec["time"].(string)
	if ok, _ := regexp.MatchString(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`, ts); !ok {
		t.Fatalf("time %q is not UTC RFC3339 with milliseconds", ts)
	}
	if rec["msg"] != "hello" || rec["level"] != "INFO" || rec["k"] != "v" {
		t.Fatal(rec)
	}
	if pid, _ := rec["pid"].(float64); int(pid) != os.Getpid() {
		t.Fatalf("pid %v != %d", rec["pid"], os.Getpid())
	}
}
```

Add `"encoding/json"`, `"regexp"`, and `"github.com/ericfitz/agentbus/internal/config"` to the test file's imports.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/mcpserver/ -run TestOpenLogWritesJSONL`
Expected: FAIL, "not JSON".

- [ ] **Step 3: Implement**

Replace `OpenLog` in `internal/mcpserver/logfile.go`:

```go
// OpenLog opens the rotating log file in cfg.DataDirectory (four 16 MiB
// files) and returns a JSON-lines logger writing to it, one record per
// line with a UTC RFC3339 millisecond timestamp and this process's pid.
// Never writes to stdout or stderr.
func OpenLog(cfg config.Config) (*slog.Logger, error) {
	if err := os.MkdirAll(cfg.DataDirectory, 0o700); err != nil {
		return nil, err
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	w := &rotatingWriter{path: filepath.Join(cfg.DataDirectory, "agentbus.log"), maxBytes: 16 << 20, keep: 4}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl, ReplaceAttr: utcMillis})
	return slog.New(h).With("pid", os.Getpid()), nil
}

// utcMillis rewrites the top-level time attribute as UTC RFC3339 with
// millisecond precision, e.g. 2026-09-09T17:04:05.123Z.
func utcMillis(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		a.Value = slog.StringValue(a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	}
	return a
}
```

No new imports are needed (`slog`, `os`, `filepath` are already imported).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/mcpserver/ && go vet ./...`
Expected: PASS. Also grep for any test reading `agentbus.log` and matching the old text format (`rg -n 'agentbus.log' internal --type go`); update any such assertion to parse JSON.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/logfile.go internal/mcpserver/logfile_test.go
git commit -m "feat(log): JSON lines with UTC millisecond timestamps and pid

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 8: Documentation

**Files:**
- Modify: `docs/install.md` (sections "Per-repo identity", "Operating", and the log mention if any)
- Modify: `README.md` (tool list)

- [ ] **Step 1: Update `docs/install.md`**

Replace the "Per-repo identity" section body with:

```markdown
## Per-repo identity and channels (what `agentbus init` writes)

`.local/agentbus.json` in the repository (git-ignored):

```json
{ "identity": "Sam", "channels": ["general", "memory", "reviews"] }
```

`identity` is the name the agent registers with. `channels` is the
persistent subscription list: `register` subscribes the session to each
listed channel (from the current position) and reports them in its
`subscribed` field; channels that do not exist are reported in
`subscribe_failed` and skipped. Without a `channels` key the list is
`general` and `memory`; an empty list means no automatic subscriptions.

Edit the list from the repository root with `agentbus subscribe <channel>`
and `agentbus unsubscribe <channel>` (the file is created if missing), or
from inside a session by passing `persistent: true` to the `subscribe` or
`unsubscribe` tool. Changes apply at the next `register`.

`agentbus identity` looks for this file by walking up from the current
directory, stopping at the nearest `.git`, so a nested repository reports its
own name rather than an enclosing one. Without a matching file it suggests
the repository directory's basename. Its output is the session protocol every
agent is told to follow: register, receive, post progress to the chat
channel, search and post memories, discover.

Every bus has two channels from the start, `general` (ordinary) and
`memory` (memory), recreated after `agentbus reset`.
```

In the "Operating" list, after the `agentbus identity` bullet, add:

```markdown
- `agentbus subscribe <channel>` / `agentbus unsubscribe <channel>` edit the
  persistent channel list in `.local/agentbus.json`. Run from the repository
  root; takes effect at the next register.
- The MCP server logs to `agentbus.log` in the data directory as JSON lines
  (one object per line with `time` in UTC RFC3339 milliseconds, `level`,
  `msg`, `pid`), rotated across four 16 MiB files.
```

- [ ] **Step 2: Update `README.md`**

Change the `register` and `subscribe` bullets to:

```markdown
- `register` — get a display name to pass as `as` on every other call. Also
  subscribes you to the repository's persistent channels (default `general`
  and `memory`).
- `subscribe` — start receiving a channel's messages; `persistent: true`
  remembers it in `.local/agentbus.json` for later sessions.
```

- [ ] **Step 3: Commit**

```bash
git add docs/install.md README.md
git commit -m "docs: persistent channels, subscribe commands, JSONL log format

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 9: Full verification and finish

- [ ] **Step 1: Lint, build, test**

```bash
cd /Users/efitz/Projects/agentbus
go vet ./... && go build ./... && go test ./... 2>&1 | tail -15
```

Expected: every package `ok`.

- [ ] **Step 2: Manual end-to-end in this repo**

```bash
go build -o /tmp/agentbus-dev . && /tmp/agentbus-dev identity | head -3 && /tmp/agentbus-dev subscribe general
```

Expected: the identity output begins with the register sentence quoting `"agentbus"`; the subscribe command prints `Agentbus: persistent channels for agentbus: general, memory` and `.local/agentbus.json` now has a `channels` key.

- [ ] **Step 3: Hand off**

Invoke `superpowers:finishing-a-development-branch` to merge `persistent-subscriptions` into `main`, then update `PROGRESS.md` (pushed) and `HANDOFF.md` (local, untracked).
