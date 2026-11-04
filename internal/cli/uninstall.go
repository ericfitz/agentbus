package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UninstallOptions configures Uninstall. Zero values mean "detect".
type UninstallOptions struct {
	Harness  string // "", "claude", "codex", or "grok": force one harness
	DryRun   bool   // print actions, change nothing
	Home     string // home directory; defaults to os.UserHomeDir
	LookPath func(string) (string, error)
	Run      func(name string, args ...string) error
}

// Uninstall reverses `init --global` for the harnesses on this machine: the
// MCP entry, the hooks and the files. Per-repository state (the identity
// file, the bus and its data) is left alone.
func Uninstall(o UninstallOptions, out io.Writer) error {
	if err := validHarness(o.Harness); err != nil {
		return err
	}
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		o.Home = h
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Run == nil {
		o.Run = execRun
	}
	in := &initer{
		InitOptions: InitOptions{Harness: o.Harness, DryRun: o.DryRun, Home: o.Home, LookPath: o.LookPath, Run: o.Run},
		out:         out,
	}
	did := false
	for _, h := range harnesses {
		if !in.wants(h.name, in.home(h.dir)) {
			continue
		}
		did = true
		if err := in.uninstall(h); err != nil {
			return err
		}
	}
	if !did {
		in.say("%s", in.noHarnessMessage())
		return nil
	}
	in.say("done: restart the harness")
	return nil
}

// uninstall reverses install for one harness. The hook file is parsed before
// anything changes, so an invalid file leaves the harness untouched.
func (in *initer) uninstall(h harness) error {
	in.say("%s:", h.title)
	hookPath := in.home(h.hookFile)
	root, present, err := readHookFile(hookPath)
	if err != nil {
		return err
	}
	in.removeMCP(h.mcp)
	if err := in.removeHooks(h, hookPath, root, present); err != nil {
		return err
	}
	for _, f := range h.files {
		p := in.home(f.path)
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				in.say("  %s not present", p)
				in.removeEmptyParents(p) // an interrupted run may have left it empty
				continue
			}
			return err
		}
		if err := in.remove(p); err != nil {
			return err
		}
		in.removeEmptyParents(p)
	}
	return nil
}

// readHookFile parses a hooks file; a missing one is an empty, absent root.
func readHookFile(path string) (root map[string]any, present bool, err error) {
	root = map[string]any{}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return root, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false, fmt.Errorf("%s: %w (fix or remove it, then rerun)", path, err)
	}
	if root == nil { // the file held "null"
		root = map[string]any{}
	}
	return root, true, nil
}

// removeMCP unregisters the MCP server through the harness CLI, or prints
// what to remove by hand when the CLI is missing. A failure is reported, not
// fatal: agentbus may never have been registered.
func (in *initer) removeMCP(m mcpSpec) {
	if _, err := in.LookPath(m.cli); err != nil {
		in.say("  %s CLI not on PATH; remove this yourself:\n  %s", m.cli, m.snippet)
		return
	}
	args := strings.Join(m.remove, " ")
	if err := in.run(m.cli, m.remove...); err != nil {
		in.say("  %s %s failed (agentbus may not have been registered): %v", m.cli, args, err)
		return
	}
	if !in.DryRun {
		in.say("  unregistered the agentbus MCP server via %s %s", m.cli, args)
	}
}

// removeHooks strips h's agentbus hooks from the parsed hook file in one
// write, deleting the file when it is agentbus's alone and nothing is left.
func (in *initer) removeHooks(h harness, path string, root map[string]any, present bool) error {
	hooks, _ := root["hooks"].(map[string]any)
	changed := false
	for _, hk := range h.hooks {
		entries, ok := hooks[hk.event].([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, e := range entries {
			left, removed, drop := stripHookCommands(e, h.hooks)
			changed = changed || removed
			if !drop {
				kept = append(kept, left)
			}
		}
		if len(kept) == 0 {
			delete(hooks, hk.event)
		} else {
			hooks[hk.event] = kept
		}
	}
	if !present || !changed {
		in.say("  agentbus hooks not present in %s", path)
		return nil
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	}
	if h.ownHookFile && len(root) == 0 {
		if err := in.remove(path); err != nil {
			return err
		}
		in.removeEmptyParents(path)
		return nil
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return in.write(path, append(data, '\n'))
}

// stripHookCommands removes from one outer hook entry every inner hook that
// runs one of specs' commands. It returns the entry to keep, whether anything
// was removed, and whether to drop the entry (no inner hook is left). An entry
// that is not a hook object is kept unchanged.
func stripHookCommands(entry any, specs []hookSpec) (kept any, removed, drop bool) {
	m, ok := entry.(map[string]any)
	if !ok {
		return entry, false, false
	}
	inner, ok := m["hooks"].([]any)
	if !ok {
		return entry, false, false
	}
	var left []any
	for _, ih := range inner {
		match := false
		for _, s := range specs {
			if hasHookCommand(map[string]any{"hooks": []any{ih}}, s.command) {
				match = true
				break
			}
		}
		if match {
			removed = true
		} else {
			left = append(left, ih)
		}
	}
	if !removed {
		return entry, false, false
	}
	if len(left) == 0 {
		return nil, true, true
	}
	m["hooks"] = left
	return m, true, false
}

// removeEmptyParents removes the now-empty directories above path, going up
// and stopping at the first one that is not empty or that sits directly under
// home (the harness's own directory, which agentbus did not create).
func (in *initer) removeEmptyParents(path string) {
	if in.DryRun {
		return
	}
	home := filepath.Clean(in.Home)
	for dir := filepath.Dir(path); dir != home && filepath.Dir(dir) != home && filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil { // not empty, or already gone
			return
		}
	}
}
