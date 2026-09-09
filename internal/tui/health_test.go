package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthOverlayShowsStorageSessionsThemeAndConfig(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.m.height = 64 // tall enough that the whole body fits without scrolling
	f.key("esc")
	f.key("h")
	if f.m.mode != modeHealth {
		t.Fatal("h opens health")
	}
	v := f.m.View()
	for _, want := range []string{"storage", "2.0 GiB", "sessions 2 live", "channels 2 (1 memory)", "embeddings unset", "receive", "theme", "AGENTBUS_TUI_AGENTCOLOR", "cyan", "config ·", filepath.Base(f.c.cfg.Path), "\"tui_name\"", "\"sqlite_budget_mib\": 2048"} {
		if !strings.Contains(v, want) {
			t.Errorf("health lacks %q:\n%s", want, v)
		}
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes")
	}
}

// TestHealthOverlayScrollReachesConfigJSON covers the fixture's default
// (shorter) terminal height, where the body is taller than the box and the
// config JSON near the bottom is only reachable by scrolling.
func TestHealthOverlayScrollReachesConfigJSON(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.key("esc")
	f.key("h")
	for i := 0; i < 30; i++ {
		f.key("down")
	}
	v := f.m.View()
	if !strings.Contains(v, "\"tui_name\"") {
		t.Fatalf("scrolling down must reach the config JSON, got:\n%s", v)
	}
}

func TestConfigEditedReloadsAndReportsErrors(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.c.cfg.Path, []byte(`{"tui_name": ""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.send(configEditedMsg{})
	if !strings.Contains(f.m.toast, "tui_name") {
		t.Fatalf("invalid config must toast the validation error, got %q", f.m.toast)
	}
	if err := os.WriteFile(f.c.cfg.Path, []byte(`{"tui_name": "eric"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.send(configEditedMsg{})
	if !strings.Contains(f.m.toast, "restart") {
		t.Fatalf("valid config must say a restart applies it, got %q", f.m.toast)
	}
}
