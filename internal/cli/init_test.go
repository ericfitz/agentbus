package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// fakeHarness records CLI invocations and simulates `codex mcp add` writing
// its config.toml the way the real CLI does.
type fakeHarness struct {
	home  string
	calls []string
}

func (f *fakeHarness) lookPath(name string) (string, error) { return "/fake/" + name, nil }

func (f *fakeHarness) run(name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "codex" && len(args) > 1 && args[1] == "add" {
		p := filepath.Join(f.home, ".codex", "config.toml")
		_ = os.MkdirAll(filepath.Dir(p), 0o755) // the real CLI creates ~/.codex
		old, _ := os.ReadFile(p)
		return os.WriteFile(p, append(old, "\n[mcp_servers.agentbus]\ncommand = \"agentbus\"\nargs = [\"mcp\"]\n"...), 0o644)
	}
	return nil
}

func initOpts(t *testing.T, home string, f *fakeHarness) InitOptions {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	return InitOptions{Home: home, Cwd: t.TempDir(), LookPath: f.lookPath, Run: f.run, Config: cfg}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func sessionStartCommands(m map[string]any) []string {
	var out []string
	hooks, _ := m["hooks"].(map[string]any)
	starts, _ := hooks["SessionStart"].([]any)
	for _, s := range starts {
		inner, _ := s.(map[string]any)["hooks"].([]any)
		for _, h := range inner {
			out = append(out, h.(map[string]any)["command"].(string))
		}
	}
	return out
}

func TestInitGlobalConfiguresDetectedHarnessesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	f := &fakeHarness{home: home}
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	// Pre-existing settings with an unrelated hook must survive the merge.
	settings := filepath.Join(home, ".claude", "settings.json")
	_ = os.WriteFile(settings, []byte(`{"model":"opus","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"}]}],"Stop":[]}}`), 0o644)
	_ = os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-5\"\n"), 0o644)

	var out bytes.Buffer
	if err := Init(initOpts(t, home, f), &out); err != nil {
		t.Fatal(err, out.String())
	}
	want := []string{
		"claude mcp remove -s user agentbus",
		"claude mcp add -s user agentbus -- agentbus mcp",
		"codex mcp remove agentbus",
		"codex mcp add agentbus -- agentbus mcp",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
	m := readJSON(t, settings)
	if m["model"] != "opus" || m["hooks"].(map[string]any)["Stop"] == nil {
		t.Fatalf("unrelated settings lost: %v", m)
	}
	if got := sessionStartCommands(m); len(got) != 2 || got[0] != "echo hi" || got[1] != "agentbus identity" {
		t.Fatalf("SessionStart commands = %v", got)
	}
	if _, err := os.Stat(settings + ".bak"); err != nil {
		t.Fatal("no backup of settings.json:", err)
	}
	if got := sessionStartCommands(readJSON(t, filepath.Join(home, ".codex", "hooks.json"))); len(got) != 1 || got[0] != "agentbus identity" {
		t.Fatalf("codex hooks = %v", got)
	}
	toml, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(toml), "[mcp_servers.agentbus]\ntool_timeout_sec = 300\n") || !strings.HasPrefix(string(toml), "model = \"gpt-5\"") {
		t.Fatalf("config.toml:\n%s", toml)
	}
	prompt, err := os.ReadFile(filepath.Join(home, ".codex", "prompts", "agentbus.md"))
	if err != nil || !strings.Contains(string(prompt), "$ARGUMENTS") || !strings.Contains(string(prompt), "agentbus init") {
		t.Fatalf("codex prompt: %v\n%s", err, prompt)
	}

	// Second run: hooks and timeout are not duplicated.
	out.Reset()
	if err := Init(initOpts(t, home, f), &out); err != nil {
		t.Fatal(err)
	}
	if got := sessionStartCommands(readJSON(t, settings)); len(got) != 2 {
		t.Fatalf("hook duplicated on rerun: %v", got)
	}
	toml, _ = os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if strings.Count(string(toml), "tool_timeout_sec") != 1 {
		t.Fatalf("timeout duplicated on rerun:\n%s", toml)
	}
	if !strings.Contains(out.String(), "already present") {
		t.Fatalf("rerun should report the existing hook:\n%s", out.String())
	}
}

func TestInitGlobalSkipsAbsentHarnessUnlessForced(t *testing.T) {
	home := t.TempDir()
	f := &fakeHarness{home: home}
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	var out bytes.Buffer
	if err := Init(initOpts(t, home, f), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); err == nil {
		t.Fatal("codex configured without ~/.codex and without --harness codex")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "codex") {
			t.Fatal("codex CLI invoked:", c)
		}
	}

	// --harness codex forces it even without the directory, and skips claude.
	f.calls = nil
	o := initOpts(t, home, f)
	o.Harness = "codex"
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "prompts", "agentbus.md")); err != nil {
		t.Fatal("--harness codex did not write the prompt file:", err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "claude") {
			t.Fatal("claude CLI invoked under --harness codex:", c)
		}
	}
	o.Harness = "vim"
	if err := Init(o, &out); err == nil {
		t.Fatal("bad --harness accepted")
	}
}

func TestInitGlobalWithoutCLIPrintsSnippet(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	f := &fakeHarness{home: home}
	o := initOpts(t, home, f)
	o.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 || !strings.Contains(out.String(), `"mcpServers"`) {
		t.Fatalf("calls=%v out:\n%s", f.calls, out.String())
	}
}

func TestInitDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	f := &fakeHarness{home: home}
	o := initOpts(t, home, f)
	o.DryRun = true
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatal("dry run invoked CLIs:", f.calls)
	}
	for _, p := range []string{".claude/settings.json", ".codex/hooks.json", ".codex/prompts/agentbus.md"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			t.Fatal("dry run wrote", p)
		}
	}
	if !strings.Contains(out.String(), "would ") || strings.Contains(out.String(), "registered") {
		t.Fatalf("dry run output:\n%s", out.String())
	}
}

func TestInitInRepoWritesIdentityAndGitignore(t *testing.T) {
	home := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"agentbus":{}}}`), 0o644)
	root := filepath.Join(t.TempDir(), "widgets")
	sub := filepath.Join(root, "pkg", "deep")
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte("bin/"), 0o644) // no trailing newline

	f := &fakeHarness{home: home}
	o := initOpts(t, home, f)
	o.Cwd = sub
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatal("repo init touched harness CLIs:", f.calls)
	}
	id := readJSON(t, filepath.Join(root, ".local", "agentbus.json"))
	if id["identity"] != "widgets" || !strings.Contains(string(mustJSON(t, id["channels"])), `["general","memory","widgets","widgets-memory"]`) {
		t.Fatalf("identity file: %v", id)
	}
	if got := channelKinds(t, o.Config); got["widgets"] != "ordinary" || got["widgets-memory"] != "memory" {
		t.Fatalf("project channels not created on the bus: %v", got)
	}
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if string(gi) != "bin/\n.local/\n" {
		t.Fatalf(".gitignore = %q", gi)
	}
	if !strings.Contains(out.String(), "call the register tool now with the name parameter set to \"widgets\"") {
		t.Fatalf("no registration line:\n%s", out.String())
	}
	if strings.Contains(out.String(), "warning") {
		t.Fatalf("warned although MCP is configured:\n%s", out.String())
	}

	// Rerun keeps the existing identity and does not duplicate the ignore.
	_ = os.WriteFile(filepath.Join(root, ".local", "agentbus.json"), []byte(`{"identity":"Sam"}`+"\n"), 0o644)
	out.Reset()
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	gi, _ = os.ReadFile(filepath.Join(root, ".gitignore"))
	if strings.Count(string(gi), ".local/") != 1 || !strings.Contains(out.String(), "set to \"Sam\"") {
		t.Fatalf("rerun: gitignore=%q out=%s", gi, out.String())
	}
	if got := channelKinds(t, o.Config); got["Sam"] != "ordinary" || got["Sam-memory"] != "memory" {
		t.Fatalf("rerun did not create channels for the existing identity: %v", got)
	}

	// No MCP entry anywhere: warn, but still do the repo work.
	_ = os.Remove(filepath.Join(home, ".claude.json"))
	out.Reset()
	if err := Init(o, &out); err != nil || !strings.Contains(out.String(), "agentbus init --global") {
		t.Fatalf("expected the not-configured warning: %v\n%s", err, out.String())
	}

	// --global inside a repo runs the machine step instead.
	o.Global = true
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	if err := Init(o, &out); err != nil || len(f.calls) == 0 {
		t.Fatalf("--global in repo did not run the global step: %v calls=%v", err, f.calls)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// channelKinds opens the bus at cfg and returns name -> kind for every channel.
func channelKinds(t *testing.T, cfg config.Config) map[string]string {
	t.Helper()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, c := range st.Channels {
		out[c.Name] = c.Kind
	}
	return out
}
