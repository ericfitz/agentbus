package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// ADR 0018: the status bar says "failing" when the last embedding pass (in
// whichever process ran it) failed, not "ok" from the TUI's own queries,
// and the health view shows the error, when it happened, and how many
// memories the endpoint rejected.
func TestEmbeddingFailureShownInStatusBarAndHealth(t *testing.T) {
	f := newFixture(t)
	f.m.c.cfg.EmbeddingEndpoint = "http://embeddings.invalid"
	if bar := ansi.Strip(f.m.renderStatusBar()); !strings.Contains(bar, "embed ok") {
		t.Fatalf("healthy status bar: %q", bar)
	}
	f.m.status.EmbeddingError = "embeddings endpoint returned 401 Unauthorized (no API key in this process)"
	f.m.status.EmbeddingErrorAt = time.Now().UnixMilli()
	f.m.status.EmbeddingRejected = 2
	if bar := ansi.Strip(f.m.renderStatusBar()); !strings.Contains(bar, "embed failing") {
		t.Fatalf("failing status bar: %q", bar)
	}
	health := ansi.Strip(strings.Join(f.m.healthLines(), "\n"))
	for _, want := range []string{"failing", "401 Unauthorized", "no API key", "rejected 2"} {
		if !strings.Contains(strings.Join(strings.Fields(health), " "), want) {
			t.Errorf("health view lacks %q:\n%s", want, health)
		}
	}
}
