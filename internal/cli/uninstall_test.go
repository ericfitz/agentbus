package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func uninstallOpts(home string, f *fakeHarness) UninstallOptions {
	return UninstallOptions{Home: home, LookPath: f.lookPath, Run: f.run}
}

// installAll runs init --global for every harness on a fresh home.
func installAll(t *testing.T) (string, *fakeHarness) {
	t.Helper()
	home := t.TempDir()
	f := &fakeHarness{home: home}
	for _, h := range harnesses {
		if err := os.MkdirAll(filepath.Join(home, h.dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	o := initOpts(t, home, f)
	o.Global = true
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatal(err, out.String())
	}
	return home, f
}

// snapshot maps every file under home to its content hash.
func snapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		m[p] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestUninstallReversesInstallForEveryHarness(t *testing.T) {
	for _, h := range harnesses {
		t.Run(h.name, func(t *testing.T) {
			home := t.TempDir()
			f := &fakeHarness{home: home}
			io2 := initOpts(t, home, f)
			io2.Global, io2.Harness = true, h.name
			var out bytes.Buffer
			if err := Init(io2, &out); err != nil {
				t.Fatal(err, out.String())
			}
			uo := uninstallOpts(home, f)
			uo.Harness = h.name
			out.Reset()
			if err := Uninstall(uo, &out); err != nil {
				t.Fatal(err, out.String())
			}
			for _, fl := range h.files {
				if p := filepath.Join(home, filepath.FromSlash(fl.path)); exists(p) {
					t.Errorf("%s still exists", p)
				}
			}
			hookPath := filepath.Join(home, filepath.FromSlash(h.hookFile))
			if h.ownHookFile {
				if exists(hookPath) {
					t.Errorf("%s still exists", hookPath)
				}
			} else {
				m := readJSON(t, hookPath)
				for _, hk := range h.hooks {
					for _, c := range hookCommands(m, hk.event) {
						if strings.Contains(c, hk.command) {
							t.Errorf("%s still has %q under %s", hookPath, hk.command, hk.event)
						}
					}
				}
			}
			if want := h.mcp.cli + " " + strings.Join(h.mcp.remove, " "); f.calls[len(f.calls)-1] != want {
				t.Errorf("last call = %q, want %q", f.calls[len(f.calls)-1], want)
			}
			// Reinstalling restores everything.
			out.Reset()
			if err := Init(io2, &out); err != nil {
				t.Fatal(err, out.String())
			}
			for _, fl := range h.files {
				if p := filepath.Join(home, filepath.FromSlash(fl.path)); !exists(p) {
					t.Errorf("%s not restored", p)
				}
			}
			m := readJSON(t, hookPath)
			for _, hk := range h.hooks {
				found := false
				for _, c := range hookCommands(m, hk.event) {
					found = found || strings.Contains(c, hk.command)
				}
				if !found {
					t.Errorf("%s lacks %q under %s after reinstall", hookPath, hk.command, hk.event)
				}
			}
		})
	}
}

func TestUninstallKeepsUserHooks(t *testing.T) {
	home, f := installAll(t)
	p := filepath.Join(home, ".claude", "settings.json")
	const before = `{"model":"opus","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"},{"type":"command","command":"agentbus identity"}]}],"PreToolUse":[{"hooks":[{"type":"command","command":"lint"}]}],"Stop":[{"hooks":[{"type":"command","command":"agentbus stop-hook"}]}],"UserPromptSubmit":[{"hooks":[{"type":"command","command":"agentbus unpark"}]}],"SubagentStart":[{"hooks":[{"type":"command","command":"agentbus subagent-hook"}]}]}}`
	if err := os.WriteFile(p, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	uo := uninstallOpts(home, f)
	uo.Harness = "claude"
	var out bytes.Buffer
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	got := readJSON(t, p)
	want := map[string]any{"model": "opus", "hooks": map[string]any{
		"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo hi"}}}},
		"PreToolUse":   []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "lint"}}}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings.json = %v, want %v", got, want)
	}
	if !exists(p + ".bak") {
		t.Error("settings.json.bak missing")
	}
}

func TestUninstallTwiceIsANoOp(t *testing.T) {
	home, f := installAll(t)
	var out bytes.Buffer
	if err := Uninstall(uninstallOpts(home, f), &out); err != nil {
		t.Fatal(err, out.String())
	}
	before := snapshot(t, home)
	out.Reset()
	if err := Uninstall(uninstallOpts(home, f), &out); err != nil {
		t.Fatal(err, out.String())
	}
	if after := snapshot(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("second uninstall changed files:\nbefore %v\nafter  %v", before, after)
	}
	s := out.String()
	for _, h := range harnesses {
		for _, fl := range h.files {
			if want := filepath.Join(home, filepath.FromSlash(fl.path)) + " not present"; !strings.Contains(s, want) {
				t.Errorf("output lacks %q:\n%s", want, s)
			}
		}
	}
	for _, name := range []string{".claude/settings.json", ".codex/hooks.json"} {
		want := "agentbus hooks not present in " + filepath.Join(home, filepath.FromSlash(name))
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}

func TestUninstallDryRunChangesNothing(t *testing.T) {
	home, f := installAll(t)
	before, calls := snapshot(t, home), len(f.calls)
	uo := uninstallOpts(home, f)
	uo.DryRun = true
	var out bytes.Buffer
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if after := snapshot(t, home); !reflect.DeepEqual(before, after) {
		t.Error("dry run changed files")
	}
	if len(f.calls) != calls {
		t.Errorf("dry run ran %v", f.calls[calls:])
	}
	for _, want := range []string{"would remove ", "would have run claude mcp remove -s user agentbus"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestUninstallInvalidHookFileStopsBeforeChanges(t *testing.T) {
	home, f := installAll(t)
	p := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(p, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := len(f.calls)
	uo := uninstallOpts(home, f)
	uo.Harness = "claude"
	var out bytes.Buffer
	err := Uninstall(uo, &out)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v, want one naming %s", err, p)
	}
	if !exists(filepath.Join(home, ".claude", skillPath)) {
		t.Error("skill file removed despite the invalid hook file")
	}
	if len(f.calls) != calls {
		t.Errorf("ran %v before failing", f.calls[calls:])
	}
}

func TestUninstallRemovesEmptyDirsButNotHarnessDirs(t *testing.T) {
	home, f := installAll(t)
	var out bytes.Buffer
	if err := Uninstall(uninstallOpts(home, f), &out); err != nil {
		t.Fatal(err, out.String())
	}
	for _, d := range []string{".claude/skills", ".agents/skills", ".grok/rules"} {
		if p := filepath.Join(home, filepath.FromSlash(d)); exists(p) {
			t.Errorf("%s should be gone", p)
		}
	}
	// Uninstall never touches .bak files, so the Grok hooks directory stays.
	if exists(filepath.Join(home, ".grok", "hooks", "agentbus.json")) {
		t.Error("grok hook file should be gone")
	}
	if !exists(filepath.Join(home, ".grok", "hooks", "agentbus.json.bak")) {
		t.Error("agentbus.json.bak should remain")
	}
	for _, d := range []string{".claude", ".codex", ".grok", ".agents"} {
		if p := filepath.Join(home, d); !exists(p) {
			t.Errorf("%s should remain", p)
		}
	}
}

func TestUninstallWithoutCLIPrintsSnippet(t *testing.T) {
	home, f := installAll(t)
	calls := len(f.calls)
	uo := uninstallOpts(home, f)
	uo.Harness = "claude"
	uo.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	var out bytes.Buffer
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if want := "claude CLI not on PATH; remove this yourself:\n  " + harnesses[0].mcp.snippet; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
	if exists(filepath.Join(home, ".claude", skillPath)) {
		t.Error("skill file still exists")
	}
	if len(f.calls) != calls {
		t.Error("ran a CLI that is not on PATH")
	}
}

func TestUninstallRejectsUnknownHarness(t *testing.T) {
	err := Uninstall(UninstallOptions{Harness: "cursor", Home: t.TempDir()}, &bytes.Buffer{})
	if err == nil || err.Error() != `--harness must be claude, codex, or grok, got "cursor"` {
		t.Errorf("err = %v", err)
	}
}

func TestUninstallNoHarnessFound(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	if err := Uninstall(uninstallOpts(home, &fakeHarness{home: home}), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no harness found") {
		t.Errorf("output = %q", out.String())
	}
}

func TestUninstallKeepsNonObjectHookEntries(t *testing.T) {
	home, f := installAll(t)
	p := filepath.Join(home, ".claude", "settings.json")
	const before = `{"hooks":{"Stop":[null,{"hooks":[{"type":"command","command":"agentbus stop-hook"}]}]}}`
	if err := os.WriteFile(p, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	uo := uninstallOpts(home, f)
	uo.Harness = "claude"
	var out bytes.Buffer
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	got := readJSON(t, p)
	want := map[string]any{"hooks": map[string]any{"Stop": []any{nil}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings.json = %v, want %v", got, want)
	}
}

func TestUninstallCleansDirLeftEmptyByInterruptedRun(t *testing.T) {
	home, f := installAll(t)
	skill := filepath.Join(home, ".claude", filepath.FromSlash(skillPath))
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	uo := uninstallOpts(home, f)
	uo.Harness = "claude"
	var out bytes.Buffer
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if d := filepath.Join(home, ".claude", "skills"); exists(d) {
		t.Errorf("%s should be gone", d)
	}
}

func TestUninstallContinuesWhenMCPRemoveFails(t *testing.T) {
	home, f := installAll(t)
	uo := uninstallOpts(home, f)
	uo.Harness = "claude"
	uo.Run = func(string, ...string) error { return os.ErrPermission }
	var out bytes.Buffer
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "failed (agentbus may not have been registered)") {
		t.Errorf("output lacks failure line:\n%s", out.String())
	}
	if exists(filepath.Join(home, ".claude", filepath.FromSlash(skillPath))) {
		t.Error("skill file still exists")
	}
}

func TestUninstallKeepsGrokHookFileWithUserKey(t *testing.T) {
	home, f := installAll(t)
	p := filepath.Join(home, ".grok", "hooks", "agentbus.json")
	const before = `{"note":"mine","hooks":{"Stop":[{"hooks":[{"type":"command","command":"agentbus stop-hook"}]}]}}`
	if err := os.WriteFile(p, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	uo := uninstallOpts(home, f)
	uo.Harness = "grok"
	var out bytes.Buffer
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if got, want := readJSON(t, p), map[string]any{"note": "mine"}; !reflect.DeepEqual(got, want) {
		t.Errorf("hook file = %v, want %v", got, want)
	}
}

func TestUninstallKeepsSymlinkedParentDir(t *testing.T) {
	home := t.TempDir()
	f := &fakeHarness{home: home}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(target, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "other", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".claude", "skills")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	o := initOpts(t, home, f)
	o.Global = true
	o.Harness = "claude"
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if !exists(filepath.Join(target, "using-agentbus", "SKILL.md")) {
		t.Fatal("install did not write through the symlink")
	}
	uo := uninstallOpts(home, f)
	uo.Harness = "claude"
	out.Reset()
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink should remain: %v %v", fi, err)
	}
	if !exists(filepath.Join(target, "other", "SKILL.md")) {
		t.Error("other/SKILL.md should remain")
	}
	if exists(filepath.Join(target, "using-agentbus", "SKILL.md")) {
		t.Error("using-agentbus/SKILL.md should be gone")
	}
}

func TestUninstallKeepsPreExistingEmptyEvent(t *testing.T) {
	home, f := installAll(t)
	p := filepath.Join(home, ".claude", "settings.json")
	const before = `{"hooks":{"PreToolUse":[],"Stop":[],"SessionStart":[{"hooks":[{"type":"command","command":"agentbus identity"}]}]}}`
	if err := os.WriteFile(p, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	uo := uninstallOpts(home, f)
	uo.Harness = "claude"
	var out bytes.Buffer
	if err := Uninstall(uo, &out); err != nil {
		t.Fatal(err, out.String())
	}
	want := map[string]any{"hooks": map[string]any{"PreToolUse": []any{}, "Stop": []any{}}}
	if got := readJSON(t, p); !reflect.DeepEqual(got, want) {
		t.Errorf("settings.json = %v, want %v", got, want)
	}
}
