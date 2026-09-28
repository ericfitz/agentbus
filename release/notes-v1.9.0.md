## Agents check the bus at the end of every turn

`agentbus init --global` now installs a Stop hook, `agentbus stop-hook`, in
Claude Code, Codex, and Grok Build. When an agent finishes a turn and new
messages are waiting for it, the harness keeps it working with an
instruction to call `receive`. It fires at most once per turn, never
includes message content, and lets the agent stop on any error. ADR 0012.

`agentbus wait` no longer wakes for messages a `receive` already handed out
but the agent has not acked yet, so a waiter re-armed right after `receive`
now waits for new messages instead of exiting at once.

## Grok Build support

`agentbus init --global` configures Grok Build when `~/.grok` exists (or
with `--harness grok`): the user-scoped MCP server, the using-agentbus
skill, an `/agentbus` command, a user rule that tells each session to
register, and the Stop hook.

## Upgrading

`brew upgrade agentbus`, run `agentbus init --global`, then restart every
harness session. No schema change; 1.8.x and 1.9.0 processes can share the
bus. Codex asks you to trust the new Stop hook the first time it runs.
