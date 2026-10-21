package mcpserver

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The send tool rejects a DM to the caller's own inbox as a validation
// error (#42): receive and wait skip your own messages, so it would never
// be delivered.
func TestSendToOwnInboxIsAToolError(t *testing.T) {
	cs := testSession(t)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	_, res := call(t, cs, "send", map[string]any{"as": "Sam", "channel": "dm/Sam", "content": "note to self"})
	if !res.IsError {
		t.Fatal("a DM to your own inbox must fail")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "validation") || !strings.Contains(text, "cannot send a direct message to yourself; dm/Sam is your own inbox") {
		t.Fatalf("error=%s", text)
	}
}
