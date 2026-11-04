package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessTableInvariants(t *testing.T) {
	names := map[string]bool{}
	paths := map[string]string{}
	claim := func(h harness, p string) {
		t.Helper()
		if p == "" {
			t.Errorf("%s: empty path", h.name)
			return
		}
		if other, ok := paths[p]; ok {
			t.Errorf("path %s used by both %s and %s", p, other, h.name)
		}
		paths[p] = h.name
	}
	for _, h := range harnesses {
		if h.name == "" || names[h.name] {
			t.Errorf("harness name %q is empty or duplicated", h.name)
		}
		names[h.name] = true
		if h.title == "" || h.dir == "" {
			t.Errorf("%s: title and dir must be set", h.name)
		}
		if h.mcp.cli == "" || len(h.mcp.add) == 0 || len(h.mcp.remove) == 0 || h.mcp.configFile == "" {
			t.Errorf("%s: incomplete mcp spec %+v", h.name, h.mcp)
		}
		if len(h.hooks) > 0 {
			claim(h, h.hookFile)
		}
		for _, f := range h.files {
			claim(h, f.path)
		}
	}
	if len(harnesses) == 0 {
		t.Fatal("no harnesses")
	}
}

func TestHarnessNameList(t *testing.T) {
	if got := harnessNameList(); got != "claude, codex, or grok" {
		t.Errorf("harnessNameList() = %q", got)
	}
	if err := validHarness("cursor"); err == nil || err.Error() != `--harness must be claude, codex, or grok, got "cursor"` {
		t.Errorf("validHarness(cursor) = %v", err)
	}
	if err := validHarness(""); err != nil {
		t.Errorf("validHarness(\"\") = %v", err)
	}
	if err := validHarness("grok"); err != nil {
		t.Errorf("validHarness(grok) = %v", err)
	}
}

func TestHarnessTablePathsAreRelativeSlashForm(t *testing.T) {
	check := func(h harness, what, p string) {
		t.Helper()
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
			t.Errorf("%s: %s %q must be relative and slash-form", h.name, what, p)
		}
		for _, part := range strings.Split(p, "/") {
			if part == ".." {
				t.Errorf("%s: %s %q must not contain ..", h.name, what, p)
			}
		}
	}
	for _, h := range harnesses {
		check(h, "dir", h.dir)
		check(h, "hookFile", h.hookFile)
		check(h, "mcp.configFile", h.mcp.configFile)
		for _, f := range h.files {
			check(h, "file path", f.path)
		}
	}
	if got := strings.Join(harnessNames(), ","); got != "claude,codex,grok" {
		t.Errorf("harnessNames() = %s", got)
	}
}

func TestNoHarnessMessage(t *testing.T) {
	home := t.TempDir()
	in := &initer{InitOptions: InitOptions{Home: home}}
	want := "no harness found (no " + filepath.Join(home, ".claude") + ", " + filepath.Join(home, ".codex") +
		", or " + filepath.Join(home, ".grok") + "); use --harness claude|codex|grok to force one"
	if got := in.noHarnessMessage(); got != want {
		t.Errorf("noHarnessMessage() = %q, want %q", got, want)
	}
}

func TestRepoWarningListsMCPConfigFiles(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeHarness{home: home}
	o := initOpts(t, home, f)
	o.Cwd, o.DryRun = cwd, true
	var out bytes.Buffer
	_ = Init(o, &out) // only the warning line matters here
	want := "warning: no agentbus MCP entry found in ~/.claude.json, ~/.codex/config.toml, or ~/.grok/config.toml; run `agentbus init --global` first\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
}
