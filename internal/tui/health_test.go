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
	for _, want := range []string{"storage", "2.0 GiB", "sessions 2 live", "channels 2 (1 memory)", "embeddings unset", "receive", "theme", "AGENTBUS_TUI_AGENTCOLOR", "cyan", "keys", "q quit", "config ·", filepath.Base(f.c.cfg.Path), "\"tui_name\"", "\"sqlite_budget_mib\": 2048"} {
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
// config JSON near the bottom is only reachable by scrolling. It scrolls
// well past the end (200 downs against a body far shorter than that) to
// confirm the clamp stops at the last line rather than running off into a
// blank body.
func TestHealthOverlayScrollReachesConfigJSON(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.key("esc")
	f.key("h")
	for i := 0; i < 200; i++ {
		f.key("down")
	}
	if want := len(f.m.healthLines()) - 1; f.m.health.scroll != want {
		t.Fatalf("scroll = %d, want clamped at %d", f.m.health.scroll, want)
	}
	v := f.m.View()
	if !strings.Contains(v, "}") {
		t.Fatalf("scrolling to the clamp must still show the config JSON's closing brace, got:\n%s", v)
	}
}

// TestHealthOverlaySurvivesUsageOverBudget covers the reachable state where
// usage has grown past the budget (the bus only starts evicting once it's
// over, and the capacity notice exists for exactly that moment): the
// storage bar's fill must clamp rather than passing strings.Repeat a
// negative count.
func TestHealthOverlaySurvivesUsageOverBudget(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("h") // openHealth also issues its own statusCmd; set the override after it lands
	f.m.status.UsageBytes = 3 << 30
	f.m.status.BudgetBytes = 2 << 30
	v := f.m.View()
	if !strings.Contains(v, "150%") {
		t.Fatalf("over-budget usage must still show its percentage, got:\n%s", v)
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
