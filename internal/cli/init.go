package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
)

// InitPrompt is what the `init` MCP prompt (Claude Code) and the Codex
// custom prompt tell the agent to do. Both harnesses run the same CLI.
const InitPrompt = `Set up Agentbus for this repository:
1. Run ` + "`agentbus init`" + ` in a shell. Inside a git repository it writes the
   repository's identity file and prints a block beginning
   "Agentbus: call the register tool now with the name parameter set to ...".
2. Call the Agentbus register tool with that name, then pass the returned
   "as" value on every later Agentbus call. Register subscribes you to the
   repository's persistent channels (default: general for chat, memory for
   memories).
` + protocol + `If the command reports that the MCP server is not configured yet, tell the
user to run ` + "`agentbus init --global`" + ` from a shell and restart the harness.`

// codexPrompt is written to ~/.codex/prompts/agentbus.md so Codex users get
// /prompts:agentbus init; Codex does not surface MCP prompts (openai/codex
// issue 5059), so this file is the stopgap until it does.
const codexPrompt = "# Agentbus\n\nThe user asked for: agentbus $ARGUMENTS\n\n" +
	"For `init`:\n" + InitPrompt + "\n\n" +
	"For anything else, run `agentbus $ARGUMENTS` in a shell and report the output.\n"

const hookCommand = "agentbus identity"

// InitOptions configures Init. Zero values mean "detect".
type InitOptions struct {
	Global  bool   // force the machine-level bootstrap even inside a repo
	Harness string // "", "claude", or "codex": force one harness
	DryRun  bool   // print actions, write nothing
	Home    string // home directory; defaults to os.UserHomeDir
	Cwd     string // working directory; defaults to os.Getwd
	// LookPath and Run are the harness CLI seams (exec.LookPath / exec.Command).
	LookPath func(string) (string, error)
	Run      func(name string, args ...string) error
}

// Init bootstraps Agentbus for the harnesses on this machine (global) or
// for the repository containing cwd. Inside a git repository the repo step
// runs unless Global is set; outside one the global step runs.
func Init(o InitOptions, out io.Writer) error {
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		o.Home = h
	}
	if o.Cwd == "" {
		c, err := os.Getwd()
		if err != nil {
			return err
		}
		o.Cwd = c
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Run == nil {
		o.Run = func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
			return cmd.Run()
		}
	}
	switch o.Harness {
	case "", "claude", "codex":
	default:
		return fmt.Errorf("--harness must be claude or codex, got %q", o.Harness)
	}
	in := &initer{InitOptions: o, out: out}
	if root := gitRoot(o.Cwd); root != "" && !o.Global {
		return in.repo(root)
	}
	return in.global()
}

type initer struct {
	InitOptions
	out io.Writer
}

func (in *initer) say(format string, a ...any) { _, _ = fmt.Fprintf(in.out, format+"\n", a...) }

// write writes path (creating parents), backing up an existing file first.
func (in *initer) write(path string, data []byte) error {
	_, statErr := os.Stat(path)
	exists := statErr == nil
	if in.DryRun {
		if exists {
			in.say("would update %s (backup at %s.bak)", path, path)
		} else {
			in.say("would write %s", path)
		}
		return nil
	}
	if exists {
		old, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if exists {
		in.say("updated %s (backup at %s.bak)", path, path)
	} else {
		in.say("wrote %s", path)
	}
	return nil
}

// run invokes a harness CLI, or only reports it under DryRun.
func (in *initer) run(name string, args ...string) error {
	if in.DryRun {
		in.say("would have run %s %s", name, strings.Join(args, " "))
		return nil
	}
	return in.Run(name, args...)
}

// wants reports whether harness h should be configured: forced by
// --harness, else detected by its home directory existing.
func (in *initer) wants(h, dir string) bool {
	if in.Harness != "" {
		return in.Harness == h
	}
	_, err := os.Stat(dir)
	return err == nil
}

func (in *initer) global() error {
	claudeDir := filepath.Join(in.Home, ".claude")
	codexDir := filepath.Join(in.Home, ".codex")
	did := false
	if in.wants("claude", claudeDir) {
		did = true
		if err := in.claude(claudeDir); err != nil {
			return err
		}
	}
	if in.wants("codex", codexDir) {
		did = true
		if err := in.codex(codexDir); err != nil {
			return err
		}
	}
	if !did {
		in.say("no harness found (no %s or %s); use --harness claude|codex to force one", claudeDir, codexDir)
		return nil
	}
	in.say("done: restart the harness, then run `agentbus init` inside each repository")
	return nil
}

func (in *initer) claude(dir string) error {
	in.say("Claude Code:")
	if err := in.mcpEntry("claude", []string{"mcp", "remove", "-s", "user", "agentbus"}, []string{"mcp", "add", "-s", "user", "agentbus", "--", "agentbus", "mcp"},
		`{ "mcpServers": { "agentbus": { "command": "agentbus", "args": ["mcp"] } } }`+" in ~/.claude.json"); err != nil {
		return err
	}
	return in.hook(filepath.Join(dir, "settings.json"), "")
}

func (in *initer) codex(dir string) error {
	in.say("Codex:")
	if err := in.mcpEntry("codex", []string{"mcp", "remove", "agentbus"}, []string{"mcp", "add", "agentbus", "--", "agentbus", "mcp"},
		"[mcp_servers.agentbus]\n  command = \"agentbus\"\n  args = [\"mcp\"]\n  tool_timeout_sec = 300\n  in ~/.codex/config.toml"); err != nil {
		return err
	}
	if err := in.codexTimeout(filepath.Join(dir, "config.toml")); err != nil {
		return err
	}
	if err := in.hook(filepath.Join(dir, "hooks.json"), "startup|resume|clear"); err != nil {
		return err
	}
	if err := in.write(filepath.Join(dir, "prompts", "agentbus.md"), []byte(codexPrompt)); err != nil {
		return err
	}
	in.say("  Codex asks you to trust the SessionStart hook the first time it runs; accept it")
	return nil
}

// mcpEntry registers the MCP server through the harness's own CLI so the
// entry lands in the right file and format; without the CLI it prints the
// snippet to add by hand.
func (in *initer) mcpEntry(cli string, remove, add []string, snippet string) error {
	if _, err := in.LookPath(cli); err != nil {
		in.say("  %s CLI not on PATH; add this yourself:\n  %s", cli, snippet)
		return nil
	}
	_ = in.run(cli, remove...) // absent entry is fine
	if err := in.run(cli, add...); err != nil {
		return fmt.Errorf("%s %s: %w", cli, strings.Join(add, " "), err)
	}
	if !in.DryRun {
		in.say("  registered the agentbus MCP server via %s %s", cli, strings.Join(add, " "))
	}
	return nil
}

// codexTimeout ensures tool_timeout_sec = 300 under [mcp_servers.agentbus]
// (a text edit; no TOML parser). A long receive wait must not be cut off by
// Codex's default tool timeout.
func (in *initer) codexTimeout(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // codex mcp add did not run (CLI missing); snippet covers it
		}
		return err
	}
	lines := strings.Split(string(body), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "[mcp_servers.agentbus]" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	for i := start + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "[") {
			break
		}
		if strings.HasPrefix(t, "tool_timeout_sec") {
			return nil
		}
	}
	out := append([]string{}, lines[:start+1]...)
	out = append(out, "tool_timeout_sec = 300")
	out = append(out, lines[start+1:]...)
	return in.write(path, []byte(strings.Join(out, "\n")))
}

// hook merges a SessionStart hook running `agentbus identity` into a
// Claude Code settings.json or Codex hooks.json, leaving everything else
// in the file alone (Go's encoder re-sorts object keys, nothing more).
func (in *initer) hook(path, matcher string) error {
	root := map[string]any{}
	if body, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(body, &root); err != nil {
			return fmt.Errorf("%s: %w (fix or remove it, then rerun)", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	starts, _ := hooks["SessionStart"].([]any)
	for _, s := range starts {
		if hasHookCommand(s, hookCommand) {
			in.say("  SessionStart hook already present in %s", path)
			return nil
		}
	}
	entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": hookCommand}}}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	hooks["SessionStart"] = append(starts, entry)
	root["hooks"] = hooks
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return in.write(path, append(data, '\n'))
}

func hasHookCommand(entry any, cmd string) bool {
	m, _ := entry.(map[string]any)
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		if c, _ := hm["command"].(string); strings.Contains(c, cmd) {
			return true
		}
	}
	return false
}

// repo writes the repository's identity file and makes sure .local/ is
// git-ignored, then prints the registration line.
func (in *initer) repo(root string) error {
	if !in.mcpConfigured() {
		in.say("warning: no agentbus MCP entry found in ~/.claude.json or ~/.codex/config.toml; run `agentbus init --global` first")
	}
	idPath := filepath.Join(root, ".local", "agentbus.json")
	if _, err := os.Stat(idPath); err == nil {
		in.say("identity file already present: %s", idPath)
	} else {
		name := filepath.Base(root)
		if err := bus.NameRule(name); err != nil {
			return fmt.Errorf("repository name %q is not a valid identity (%v); write %s by hand", name, err, idPath)
		}
		data, _ := json.Marshal(map[string]string{"identity": name})
		if err := in.write(idPath, append(data, '\n')); err != nil {
			return err
		}
	}
	if err := in.gitignore(filepath.Join(root, ".gitignore")); err != nil {
		return err
	}
	if in.DryRun {
		return nil
	}
	return identity(in.Cwd, in.out, os.Stderr)
}

func (in *initer) gitignore(path string) error {
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(body), "\n") {
		switch strings.TrimSpace(l) {
		case ".local", ".local/", "/.local", "/.local/":
			return nil
		}
	}
	s := string(body)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return in.write(path, []byte(s+".local/\n"))
}

// mcpConfigured is a cheap check that some harness knows about agentbus.
func (in *initer) mcpConfigured() bool {
	for _, p := range []string{filepath.Join(in.Home, ".claude.json"), filepath.Join(in.Home, ".codex", "config.toml")} {
		if body, err := os.ReadFile(p); err == nil && strings.Contains(string(body), "agentbus") {
			return true
		}
	}
	return false
}

// gitRoot returns the nearest ancestor of dir (inclusive) containing .git,
// or "" when dir is not inside a repository.
func gitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
