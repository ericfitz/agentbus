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

// TestMemoryToolDescriptionsExplainFeedback pins the memory feedback copy in
// the tool descriptions (ADR 0033) and keeps counters out of agent text.
func TestMemoryToolDescriptionsExplainFeedback(t *testing.T) {
	tools, err := testSession(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	desc := map[string]string{}
	for _, tl := range tools.Tools {
		desc[tl.Name] = tl.Description
		for _, bad := range []string{"served_count", "used_count"} {
			if strings.Contains(tl.Description, bad) {
				t.Errorf("tool %s description mentions %q", tl.Name, bad)
			}
		}
	}
	for name, wants := range map[string][]string{
		"search":      {"incorrect=true", "memory_feedback"},
		"get_memory":  {"incorrect=true", "memory_feedback"},
		"register":    {"memory_feedback"},
		"edit_memory": {"clears an INCORRECT flag"},
	} {
		for _, want := range wants {
			if !strings.Contains(desc[name], want) {
				t.Errorf("%s description lacks %q", name, want)
			}
		}
	}
}
