package mcpserver

import (
	"context"
	"strings"
	"testing"
)

// TestToolDescriptionsHaveNoBannedPhrases guards ADR 0025: no tool
// description models the retired Monitor/watch/-filter advice.
func TestToolDescriptionsHaveNoBannedPhrases(t *testing.T) {
	banned := []string{"Monitor", "agentbus watch -filter", "-filter @", "timeout_ms", "under the Monitor"}
	tools, err := testSession(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tl := range tools.Tools {
		for _, bad := range banned {
			if strings.Contains(tl.Description, bad) {
				t.Errorf("tool %s description mentions %q", tl.Name, bad)
			}
		}
	}
}
