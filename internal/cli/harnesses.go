package cli

import (
	"fmt"
	"path/filepath"
	"strings"
)

// mcpSpec describes how a harness's CLI registers the agentbus MCP server.
type mcpSpec struct {
	cli         string   // harness CLI name, e.g. "claude"
	add, remove []string // args after the CLI name
	snippet     string   // printed when the CLI is not on PATH
	configFile  string   // relative to home; mcpConfigured reads it
}

// hookSpec is one event hook that runs command.
type hookSpec struct{ event, command, matcher string }

// fileSpec is a file init writes under the home directory.
type fileSpec struct {
	path    string // relative to home
	content []byte
}

// harness is everything init knows about one agent harness. install, and the
// uninstall that mirrors it, are driven entirely by this table.
type harness struct {
	name, title string // "claude"/"Claude Code"
	dir         string // ".claude", relative to home; its existence detects the harness
	mcp         mcpSpec
	hookFile    string // relative to home
	ownHookFile bool   // true when the file is agentbus's alone (Grok), so uninstall may delete it
	hooks       []hookSpec
	files       []fileSpec
	extra       func(in *initer, h harness) error // runs after the MCP step
	notes       []string                          // printed last, each indented
}

var harnesses = []harness{
	{
		name: "claude", title: "Claude Code", dir: ".claude",
		mcp: mcpSpec{
			cli:        "claude",
			remove:     []string{"mcp", "remove", "-s", "user", "agentbus"},
			add:        []string{"mcp", "add", "-s", "user", "agentbus", "--", "agentbus", "mcp"},
			snippet:    `{ "mcpServers": { "agentbus": { "command": "agentbus", "args": ["mcp"] } } }` + " in ~/.claude.json",
			configFile: ".claude.json",
		},
		hookFile: ".claude/settings.json",
		hooks: []hookSpec{
			{"SessionStart", hookCommand, ""},
			{"Stop", stopHookCommand, ""},
			{"UserPromptSubmit", unparkHookCommand, ""},
			{"SubagentStart", subagentHookCommand, ""},
		},
		files: []fileSpec{{filepath.Join(".claude", skillPath), skillMD}},
	},
	{
		name: "codex", title: "Codex", dir: ".codex",
		mcp: mcpSpec{
			cli:        "codex",
			remove:     []string{"mcp", "remove", "agentbus"},
			add:        []string{"mcp", "add", "agentbus", "--", "agentbus", "mcp"},
			snippet:    "[mcp_servers.agentbus]\n  command = \"agentbus\"\n  args = [\"mcp\"]\n  tool_timeout_sec = 300\n  in ~/.codex/config.toml",
			configFile: ".codex/config.toml",
		},
		// A long receive wait must not be cut off by Codex's default tool timeout.
		extra: func(in *initer, h harness) error {
			return in.codexTimeout(filepath.Join(in.Home, ".codex", "config.toml"))
		},
		hookFile: ".codex/hooks.json",
		hooks: []hookSpec{
			{"SessionStart", hookCommand, "startup|resume|clear"},
			{"Stop", stopHookCommand, ""},
			{"UserPromptSubmit", unparkHookCommand, ""},
		},
		files: []fileSpec{
			// Codex does not surface MCP prompts (openai/codex issue 5059), so the
			// in-session command is a custom prompt file (/prompts:agentbus).
			{filepath.Join(".codex", "prompts", "agentbus.md"), []byte(codexPrompt)},
			{filepath.Join(".agents", skillPath), skillMD},
		},
		notes: []string{"Codex asks you to trust the SessionStart, Stop and UserPromptSubmit hooks the first time they run; accept them"},
	},
	{
		name: "grok", title: "Grok Build", dir: ".grok",
		mcp: mcpSpec{
			cli: "grok",
			// Default tool_timeout_sec is 6000, already above receive_max_wait_seconds.
			remove:     []string{"mcp", "remove", "--scope", "user", "agentbus"},
			add:        []string{"mcp", "add", "--scope", "user", "agentbus", "--", "agentbus", "mcp"},
			snippet:    "[mcp_servers.agentbus]\n  command = \"agentbus\"\n  args = [\"mcp\"]\n  in ~/.grok/config.toml",
			configFile: ".grok/config.toml",
		},
		// No SessionStart hook: Grok ignores its stdout, so grokRule does that job.
		// Grok does read Stop hook stdout, and ~/.grok/hooks/*.json is trusted.
		hookFile:    ".grok/hooks/agentbus.json",
		ownHookFile: true,
		hooks: []hookSpec{
			{"Stop", stopHookCommand, ""},
			{"UserPromptSubmit", unparkHookCommand, ""},
		},
		files: []fileSpec{
			{filepath.Join(".grok", skillPath), skillMD},
			{filepath.Join(".grok", "commands", "agentbus.md"), []byte(codexPrompt)},
			{filepath.Join(".grok", "rules", "agentbus.md"), []byte(grokRule)},
		},
	},
}

// harnessNames returns the harness names in table order.
func harnessNames() []string {
	names := make([]string, len(harnesses))
	for i, h := range harnesses {
		names[i] = h.name
	}
	return names
}

// harnessNameList renders the names for messages: "claude, codex, or grok".
func harnessNameList() string {
	n := harnessNames()
	if len(n) < 2 {
		return strings.Join(n, "")
	}
	return strings.Join(n[:len(n)-1], ", ") + ", or " + n[len(n)-1]
}

// HarnessNameList is harnessNameList for the command's flag help.
func HarnessNameList() string { return harnessNameList() }

// validHarness accepts "" (detect) or a harness name.
func validHarness(name string) error {
	if name == "" {
		return nil
	}
	for _, h := range harnesses {
		if h.name == name {
			return nil
		}
	}
	return fmt.Errorf("--harness must be %s, got %q", harnessNameList(), name)
}

// home resolves a table path (slash-separated, relative to home).
func (in *initer) home(rel string) string {
	return filepath.Join(in.Home, filepath.FromSlash(rel))
}

// install configures one harness: MCP entry, extra step, hooks, files, notes.
func (in *initer) install(h harness) error {
	in.say("%s:", h.title)
	if err := in.mcpEntry(h.mcp.cli, h.mcp.remove, h.mcp.add, h.mcp.snippet); err != nil {
		return err
	}
	if h.extra != nil {
		if err := h.extra(in, h); err != nil {
			return err
		}
	}
	for _, hk := range h.hooks {
		if err := in.hook(in.home(h.hookFile), hk.event, hk.command, hk.matcher); err != nil {
			return err
		}
	}
	for _, f := range h.files {
		if err := in.write(in.home(f.path), f.content); err != nil {
			return err
		}
	}
	for _, n := range h.notes {
		in.say("  %s", n)
	}
	return nil
}

// noHarnessMessage is the line printed when no harness directory exists.
func (in *initer) noHarnessMessage() string {
	dirs := make([]string, len(harnesses))
	for i, h := range harnesses {
		dirs[i] = in.home(h.dir)
	}
	last := len(dirs) - 1
	return fmt.Sprintf("no harness found (no %s, or %s); use --harness %s to force one",
		strings.Join(dirs[:last], ", "), dirs[last], strings.Join(harnessNames(), "|"))
}
