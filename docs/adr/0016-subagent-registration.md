# ADR 0016: Claude subagents register through an opt-in SubagentStart hook

Status: Accepted 2026-10-02. Decided by the user. Issue #24.

## Context

Codex subagents get their own session on the bus; Claude Code subagents show
up as the parent. The bus already supports a second `register(name, parent=P)`
from the parent's MCP process (a distinct row; `as` is checked per identity),
so the gap is that a Claude subagent never calls `register`.

## Human decisions (2026-10-02)

1. **Opt-in.** Only a subagent whose dispatcher asks it to use agentbus
   registers, with `parent` set to the dispatcher's name. Short Explore or
   lookup subagents stay off the bus.
2. **Investigate first**, and choose between a hook (if Claude Code's
   SubagentStart hook can see the dispatch prompt) and skill text alone.

3. **Conditional injection (2026-10-02, after the live test below).** The
   hook cannot see the prompt, so it tells every subagent of a registered
   parent to register only if its own prompt asks it to use agentbus. The
   user chose this over polling the parent transcript for the dispatch.

## Investigation findings

Source: https://code.claude.com/docs/en/hooks

- Per the docs at the time (the live capture below found `agent_prompt`,
  `task`, and `description` absent), the Claude Code SubagentStart hook's
  stdin has the common fields
  (`session_id`, `transcript_path`, `cwd`, `permission_mode`,
  `hook_event_name`) plus `agent_type`, `agent_prompt` (the prompt sent to the
  subagent), `task`, and `description`. Plain stdout, or
  `{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"..."}}`,
  is added to the subagent's context. It cannot block. The matcher filters
  only on `agent_type`, so the prompt test happens inside the command.
- A Codex subagent gets its own `agentbus mcp` process, because it inherits
  the parent's `mcp_servers` config. Codex SessionStart does not run for
  subagents, and Codex SubagentStart carries no prompt. A Claude subagent
  shares the parent's MCP process.

The hook branch of decision 2 therefore applies, for Claude Code only.

### Live capture (2026-10-02) overturns the prompt field

v1.12.0's hook keyed on `agent_prompt` and never fired. A capture of the real
SubagentStart stdin showed only `session_id`, `transcript_path`, `cwd`,
`scratchpad_dir`, `prompt_id` (the user turn, shared by every dispatch in
it), `agent_id`, `agent_type`, and `hook_event_name`: no `agent_prompt`, no
`description`. While the hook runs the subagent's own transcript does not
exist yet, and the parent's Agent tool_use reaches `transcript_path` about
100ms after the hook starts. A background agent's launch result (which names
`agent_id`) also lands during the hook; a foreground agent's lands only after
it finishes, so matching a dispatch to its hook would be a guess there.

Options weighed: (1) inject a conditional note and let the subagent judge its
own prompt; (2) poll the parent transcript for 1-2 seconds for the dispatch.
Option 2 depends on an undocumented file format, is ambiguous for foreground
and parallel same-type dispatches, and delays every subagent. The user chose
option 1 (decision 3). Live test 2026-10-02 with option 1: a subagent whose
prompt said "use agentbus" registered as `agentbus/hooktest`; one whose
prompt did not ignored the note and made no agentbus calls.

## Implementation rulings

- **`agentbus subagent-hook`**, installed by `init --global` as a
  SubagentStart hook in `~/.claude/settings.json` only. Idempotent like the
  other hooks. Codex and Grok get none.
- **Opt-in.** The hook does not see the prompt. When the parent is live it
  injects a conditional note; the subagent registers only if its prompt asks
  it to use agentbus. A subagent that was not asked ignores the note. Every
  subagent of a registered parent pays for the note (about 80 tokens).
- **Parent.** The identity of the hook's working directory (as `stop-hook`
  derives it; since ADR 0017, the session registered from the hook's own
  harness process, falling back to that directory identity). The hook injects only when that parent has a live session
  (`Bus.SessionLive`). A `parent=<name>` in the prompt overrides it, but the
  hook cannot check that name.
- **Name.** A slug of `agent_type` plus a 4-hex suffix, unless the prompt
  names one with `name=<name>`. Two subagents using the same name and parent
  in one process would share one session row, so names must be distinct.
- **Injected text** tells the subagent to `register` with that parent and
  name, pass the returned `as` on every call, follow the protocol, and not
  arm a background `agentbus wait` (it is short-lived; `receive` suffices).
- **Fails open.** Bad JSON, no bus, or an unregistered parent
  prints nothing and exits 0.
- **Other harnesses.** The using-agentbus skill's Subagents section tells
  dispatchers to write the marker and `register with parent=<your as>,
  name=<distinct>` in the subagent's prompt; there the prompt text is all the
  subagent has.
