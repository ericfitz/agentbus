package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// subagentHookInput is the part of a Claude Code SubagentStart hook's stdin
// that SubagentHook reads. The real stdin carries no prompt or description
// (captured 2026-10-02; ADR 0016).
type subagentHookInput struct {
	Cwd       string `json:"cwd"`
	AgentType string `json:"agent_type"`
}

var slugRun = regexp.MustCompile(`[^a-z0-9]+`)

// subagentName is the name the hook suggests for the subagent: a slug of its
// agent type with a short random suffix so two subagents in one parent never
// share a session row.
func subagentName(agentType string) string {
	base := strings.Trim(slugRun.ReplaceAllString(strings.ToLower(agentType), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "subagent"
	}
	var b [2]byte
	_, _ = rand.Read(b[:])
	return base + "-" + hex.EncodeToString(b[:])
}

// SubagentHook is the SubagentStart hook `agentbus init --global` installs
// in Claude Code (ADR 0016). Claude subagents share the parent's MCP
// process, so without a register of their own they act as the parent. The
// hook cannot see the dispatch prompt, so when the identity of its working
// directory is registered it prints additionalContext telling the subagent
// to register (with that parent and a distinct name) only if its own prompt
// asks it to use agentbus. It fails open: any error or an unregistered
// parent prints nothing.
func SubagentHook(cfg config.Config, in io.Reader, out, warn io.Writer) {
	var hi subagentHookInput
	if err := json.NewDecoder(in).Decode(&hi); err != nil {
		return
	}
	if hi.Cwd == "" {
		hi.Cwd, _ = os.Getwd()
	}
	parent := IdentityName(hi.Cwd, io.Discard)
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_, _ = fmt.Fprintln(warn, "agentbus subagent-hook:", err)
		return
	}
	defer func() { _ = b.Close() }()
	if live, err := b.SessionLive(parent); err != nil || !live {
		return
	}
	ctx := fmt.Sprintf("Agentbus: If your dispatcher's prompt asks you to use agentbus "+
		"(for example \"use agentbus\" or \"register with agentbus\"), call the agentbus "+
		"register tool with parent=%q and name=%q, or the parent= or name= your prompt "+
		"names (pick another name if it is taken), then pass the \"as\" it returns on "+
		"every later agentbus call. Follow the using-agentbus protocol, but do not start "+
		"a background `agentbus wait`: you are short-lived, so use receive instead. "+
		"Otherwise ignore this note: you do not need to register.",
		parent, subagentName(hi.AgentType))
	_ = json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":     "SubagentStart",
		"additionalContext": ctx,
	}})
}
