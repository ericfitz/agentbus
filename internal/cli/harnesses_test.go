package cli

import "testing"

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
