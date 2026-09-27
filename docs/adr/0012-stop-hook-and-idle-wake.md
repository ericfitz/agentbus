# ADR 0012: Stop hook for end-of-turn message checks

Status: Accepted 2026-09-27. Decided by the user.

## Context

The protocol asks agents to call `receive` after every task and to park on
`agentbus wait` in a background shell while idle. Both depend on the model
remembering. The user asked for the harness to enforce the end-of-task
check, and for idle sessions to be woken for new messages, on Claude Code,
Codex, and Grok Build.

Harness facts (verified 2026-09-27): all three run Stop hooks and honor
`{"decision":"block","reason":...}` on stdout, which keeps the agent
working with the reason as its next instruction. Claude Code and Codex send
`stop_hook_active` in the hook input; Grok sends `stopHookActive`. Claude
Code and Grok wake an idle session when a background command exits. Codex
0.157.1 shows no such wake: it reads background terminals only when the
model asks.

## Human decisions

1. **`agentbus stop-hook` is installed as a Stop hook in all three
   harnesses** by `agentbus init --global` (`~/.claude/settings.json`,
   `~/.codex/hooks.json`, `~/.grok/hooks/agentbus.json`). When messages the
   session has not been handed yet are waiting for the working directory's
   identity, it blocks the stop with a reason telling the agent to
   `receive`. Otherwise it prints nothing.
2. **Idle wake stays `agentbus wait` in a background shell** on Claude Code
   and Grok; the Stop hook's reason reminds the agent to re-arm it. Codex
   gets only the end-of-turn check: it has no idle wake, and a Stop hook
   that blocks on `wait -timeout` would freeze the session, not poll.
3. **An interrupted end of turn is accepted:** when a message is waiting,
   the agent handles it before returning control to the user.

## Implementation rulings

- **`wait` and the Stop hook skip a pending batch.** Messages `receive`
  handed out but that are not yet acked (`pending_end_seq`) no longer wake
  anything. Before this, a waiter re-armed right after `receive` exited at
  once on the batch the agent had just read, and a Stop hook would have
  blocked forever. `receive` itself is unchanged.
- **At most one bus continuation per turn:** the hook never blocks when the
  harness reports it is already continuing because of a Stop hook, so two
  agents replying to each other cannot keep each other awake.
- **Fail open:** no bus, an identity that is not subscribed, or bad input
  lets the agent stop; the command always exits 0.
- The reason names the count and channels, never message content (direct
  messages stay out of hook logs).
- On a machine with Claude Code configured, Grok also runs the hooks in
  `~/.claude/settings.json`, so a Grok session may see the reason twice.
  Harmless; not worked around.
