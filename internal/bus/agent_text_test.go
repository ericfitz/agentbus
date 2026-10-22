package bus

import (
	"errors"
	"strings"
	"testing"
)

// bannedAgentText lists phrases that must never reach an agent (ADR 0025:
// one wake text, plain `agentbus wait`). The same list guards the protocol,
// skill, hooks (internal/cli) and MCP tool descriptions (internal/mcpserver).
var bannedAgentText = []string{"Monitor", "agentbus watch -filter", "-filter @", "timeout_ms", "under the Monitor"}

// TestBusErrorsCarryNoBannedAgentText exercises the two errors that tell an
// agent how to wait.
func TestBusErrorsCarryNoBannedAgentText(t *testing.T) {
	b, _, kim := setupTwo(t)
	b.cfg.ReceiveMaxWaitSeconds = 1
	var msgs []string
	_, err := b.Receive(kim, ReceiveInput{WaitSeconds: 2})
	msgs = append(msgs, errText(t, err, "validation"))
	for i := 0; i < pollingGuardEmptyWaits-1; i++ {
		b.notePoll(kim, false)
	}
	_, err = b.Receive(kim, ReceiveInput{WaitSeconds: 1})
	msgs = append(msgs, errText(t, err, "polling"))
	for _, m := range msgs {
		if !strings.Contains(m, "`agentbus wait`") {
			t.Errorf("error must name plain `agentbus wait`: %s", m)
		}
		for _, bad := range bannedAgentText {
			if strings.Contains(m, bad) {
				t.Errorf("error mentions %q: %s", bad, m)
			}
		}
	}
}

func errText(t *testing.T, err error, code string) string {
	t.Helper()
	var be *Error
	if !errors.As(err, &be) || be.Code != code {
		t.Fatalf("want %s error, got %v", code, err)
	}
	return be.Message
}
