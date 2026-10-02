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

## Investigation findings

Source: https://code.claude.com/docs/en/hooks

- The Claude Code SubagentStart hook's stdin has the common fields
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

## Implementation rulings

- **`agentbus subagent-hook`**, installed by `init --global` as a
  SubagentStart hook in `~/.claude/settings.json` only. Idempotent like the
  other hooks. Codex and Grok get none.
- **Opt-in marker.** The prompt matches, case-insensitively,
  `use agentbus`, `register on agentbus`, or `register with agentbus`
  (an optional "the" before agentbus). A bare mention of agentbus does not
  count: in the agentbus repo every prompt mentions it. A negated "do not use
  agentbus" still matches; dispatchers should not write it.
- **Parent.** `parent=<name>` in the prompt, else the identity of the hook's
  working directory (as `stop-hook` derives it). The hook injects only when
  that parent has a live session (`Bus.SessionLive`).
- **Name.** `name=<name>` from the prompt if present and valid, else a slug of
  `description` (or `agent_type`) plus a 4-hex suffix. Two subagents using the
  same name and parent in one process would share one session row, so names
  must be distinct.
- **Injected text** tells the subagent to `register` with that parent and
  name, pass the returned `as` on every call, follow the protocol, and not
  arm a background `agentbus wait` (it is short-lived; `receive` suffices).
- **Fails open.** Bad JSON, no marker, no bus, or an unregistered parent
  prints nothing and exits 0.
- **Other harnesses.** The using-agentbus skill's Subagents section tells
  dispatchers to write the marker and `register with parent=<your as>,
  name=<distinct>` in the subagent's prompt; there the prompt text is all the
  subagent has.
