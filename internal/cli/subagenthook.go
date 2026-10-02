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
// that SubagentHook reads.
type subagentHookInput struct {
	Cwd         string `json:"cwd"`
	AgentType   string `json:"agent_type"`
	AgentPrompt string `json:"agent_prompt"`
	Description string `json:"description"`
}

var (
	// subagentMarker is the opt-in (ADR 0016): the dispatcher writes one of
	// "use agentbus", "register on agentbus", or "register with agentbus"
	// in the subagent's prompt. A bare mention of agentbus does not count.
	subagentMarker = regexp.MustCompile(`(?i)\b(?:use|register\s+(?:on|with))\s+(?:the\s+)?agentbus\b`)
	// promptParent and promptName read "parent=<name>" and "name=<name>"
	// from the prompt; the value may be quoted or backticked.
	promptParent = regexp.MustCompile("(?i)\\bparent\\s*=\\s*[`'\"<]?([^\\s`'\"<>,;)]+)")
	promptName   = regexp.MustCompile("(?i)\\bname\\s*=\\s*[`'\"<]?([^\\s`'\"<>,;)]+)")
	slugRun      = regexp.MustCompile(`[^a-z0-9]+`)
)

// promptValue is the first capture of re in s, minus trailing punctuation.
func promptValue(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.TrimRight(m[1], ".:")
}

// subagentName is the name the hook suggests for the subagent: the one the
// prompt asks for, else a slug of the description or agent type with a short
// random suffix so two subagents in one parent never share a session row.
func subagentName(hi subagentHookInput) string {
	if n := promptValue(promptName, hi.AgentPrompt); n != "" && n != "<distinct>" && bus.NameRule(n) == nil {
		return n
	}
	base := hi.Description
	if base == "" {
		base = hi.AgentType
	}
	base = strings.Trim(slugRun.ReplaceAllString(strings.ToLower(base), "-"), "-")
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
// process, so without a register of their own they act as the parent. When
// the dispatcher's prompt asks the subagent to use agentbus (see
// subagentMarker) and the parent is registered, it prints
// additionalContext telling the subagent to register with parent set to the
// parent's name and a distinct name of its own. The parent is "parent=<name>"
// from the prompt, else the identity of the hook's working directory. It
// fails open: any error, a missing marker, or an unregistered parent prints
// nothing.
func SubagentHook(cfg config.Config, in io.Reader, out, warn io.Writer) {
	var hi subagentHookInput
	if err := json.NewDecoder(in).Decode(&hi); err != nil {
		return
	}
	if !subagentMarker.MatchString(hi.AgentPrompt) {
		return
	}
	parent := promptValue(promptParent, hi.AgentPrompt)
	if parent == "" || len(parent) > 512 {
		if hi.Cwd == "" {
			hi.Cwd, _ = os.Getwd()
		}
		parent = IdentityName(hi.Cwd, io.Discard)
	}
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_, _ = fmt.Fprintln(warn, "agentbus subagent-hook:", err)
		return
	}
	defer func() { _ = b.Close() }()
	if live, err := b.SessionLive(parent); err != nil || !live {
		return
	}
	ctx := fmt.Sprintf("Agentbus: your dispatcher asked you to use agentbus. Call the "+
		"agentbus register tool with parent=%q and name=%q (pick another name if "+
		"it is taken), then pass the \"as\" it returns on every later agentbus call. "+
		"Follow the using-agentbus protocol, but do not start a background "+
		"`agentbus wait`: you are short-lived, so use receive instead.",
		parent, subagentName(hi))
	_ = json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":     "SubagentStart",
		"additionalContext": ctx,
	}})
}
