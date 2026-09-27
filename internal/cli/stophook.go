package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// stopHookInput is the part of a Stop hook's stdin that StopHook reads.
// Claude Code and Codex send snake_case; Grok sends camelCase.
type stopHookInput struct {
	Cwd                string `json:"cwd"`
	StopHookActive     bool   `json:"stop_hook_active"`
	StopHookActiveGrok bool   `json:"stopHookActive"`
}

// StopHook is the Stop hook `agentbus init --global` installs in every
// harness (ADR 0012). When messages the session has not been handed yet are
// waiting for the identity of the hook's working directory, it prints
// {"decision":"block","reason":...} so the harness keeps the agent working;
// otherwise it prints nothing and the agent stops. It fails open: any error
// (no bus, not registered here, bad input) lets the agent stop. It never
// blocks a stop the harness is already continuing because of a Stop hook,
// so two agents cannot keep each other awake at turn end.
func StopHook(cfg config.Config, in io.Reader, out, warn io.Writer) {
	var hi stopHookInput
	_ = json.NewDecoder(in).Decode(&hi) // missing or odd input: use defaults
	if hi.StopHookActive || hi.StopHookActiveGrok {
		return
	}
	if hi.Cwd == "" {
		hi.Cwd, _ = os.Getwd()
	}
	as := IdentityName(hi.Cwd, io.Discard)
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_, _ = fmt.Fprintln(warn, "agentbus stop-hook:", err)
		return
	}
	defer func() { _ = b.Close() }()
	// A nanosecond timeout makes Wait check once and return.
	msgs, err := b.Wait(as, nil, false, nil, time.Nanosecond)
	if err != nil || len(msgs) == 0 {
		return // not subscribed here (not an agentbus session), or nothing new
	}
	seen := map[string]bool{}
	var chans []string
	for _, m := range msgs {
		if !seen[m.Channel] {
			seen[m.Channel] = true
			chans = append(chans, m.Channel)
		}
	}
	sort.Strings(chans)
	n := fmt.Sprint(len(msgs))
	if len(msgs) >= cfg.ReceiveMaxCount {
		n += "+"
	}
	reason := fmt.Sprintf("Agentbus: %s new message(s) for %s on %s. Call receive "+
		"(ack the previous batch), handle what concerns you, then stop. If you "+
		"keep a background `agentbus wait` running, re-arm it first.",
		n, as, strings.Join(chans, ", "))
	_ = json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": reason})
}
