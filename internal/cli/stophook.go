package cli

import (
	"context"
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
// waiting for the session this hook runs for (SessionIdentity, ADR 0017), it prints
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
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_, _ = fmt.Fprintln(warn, "agentbus stop-hook:", err)
		return
	}
	defer func() { _ = b.Close() }()
	as := SessionIdentity(b, hi.Cwd, io.Discard)
	if as == "" {
		return // this session has not registered, and the cwd name is another's
	}
	nudge := statusNudge(b, cfg, as, time.Now())
	// A nanosecond timeout makes Wait check once and return.
	// A parked session keeps only its direct messages (ADR 0025 decision 3).
	msgs, err := b.Wait(context.Background(), as, nil, false, parkedMatch(b, as, nil), time.Nanosecond)
	if err != nil || len(msgs) == 0 {
		// Not subscribed here (not an agentbus session), or nothing new: the
		// nudge, when there is one, is the whole reason.
		if nudge != "" {
			_ = json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": nudge})
		}
		return
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
	head := fmt.Sprintf("Agentbus: %s new message(s) for %s on %s. Call receive "+
		"(ack the previous batch) and handle what concerns you. ", n, as, strings.Join(chans, ", "))
	// ADR 0025: one tail for every harness; Codex has no idle wake.
	tail := fmt.Sprintf("Then, except on Codex, make sure a background `agentbus wait -as %s` is running "+
		"(start one; it replaces any running wait) so the next message wakes you, and stop. If it exits 2, read stderr; do not loop.", as)
	reason := head + tail
	if nudge != "" {
		reason = nudge + " " + reason
	}
	_ = json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": reason})
}

// statusNudge is the nudge for as: "" unless as is a top-level, unparked
// name with a live registered session. A cwd-derived name that never
// registered (an unrelated session on a global hook) gets none, and any read
// error means none (fail open).
func statusNudge(b *bus.Bus, cfg config.Config, as string, now time.Time) string {
	if strings.Contains(as, "/") || b.Parked(as) {
		return ""
	}
	if live, err := b.SessionLive(as); err != nil || !live {
		return ""
	}
	st, err := b.LatestStatus(as)
	if err != nil {
		return ""
	}
	return nudgeLine(st, now, cfg.StatusStaleMinutes)
}

// nudgeLine is the Stop hook's status reminder: for a nil st, that the
// session has none; for a working status (planning, implementing, reviewing)
// at least staleMin minutes old, a prompt to confirm or update it. "" means
// no nudge, including when staleMin is 0 (nudging off).
func nudgeLine(st *bus.AgentStatus, now time.Time, staleMin int) string {
	if staleMin <= 0 {
		return ""
	}
	if st == nil {
		return "Agentbus: you have no status; call set_status."
	}
	switch st.Tag {
	case "planning", "implementing", "reviewing":
	default:
		return ""
	}
	age := now.Sub(time.UnixMilli(st.SetAt))
	if age <= time.Duration(staleMin)*time.Minute {
		return ""
	}
	text := st.Text
	if r := []rune(text); len(r) > nudgeTextRunes {
		text = string(r[:nudgeTextRunes-1]) + "…"
	}
	return fmt.Sprintf("Agentbus: your status is \"%s: %s\" (%s); if that is no longer true, call set_status.",
		st.Tag, text, bus.ShortAge(age))
}

// nudgeTextRunes is the longest status text, in runes, the nudge quotes.
const nudgeTextRunes = 80
