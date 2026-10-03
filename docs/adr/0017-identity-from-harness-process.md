# ADR 0017: Hooks and wait find the session's identity by harness process

Status: Accepted 2026-10-02. Decided by the user. Issue #29.

## Context

The Stop hook (ADR 0012), the SubagentStart hook (ADR 0016) and `agentbus
wait` without `-as` all took the identity from the working directory: the
name `agentbus identity` prints. That is the name a session asks for, not
always the one it gets. When two sessions share a repository, the second
registers as `<name>2`, but its hooks and its wait still used `<name>`:

- its Stop hook blocked for the first session's unread messages, its own
  posts included, at every turn end;
- its wait ran as the first session, and because only one wait runs per
  identity, starting it replaced the first session's wait;
- its subagents were told to register under the first session.

The hook stdin carries no registered name, and the MCP server never sees the
harness's session id, so neither can say which session is asking.

## Human decision (2026-10-02)

**Match the harness process.** The `agentbus mcp` server and every command a
harness runs (hooks, background `agentbus wait`) are descendants of the same
harness process. The server finds that process at startup with
`procs.FindHarness` (the first non-shell ancestor, ADR 0014) and records its
pid and start time on each session it registers. A hook or a wait finds its
own harness the same way and uses the live top-level session registered from
it. Without a match (no session registered from this harness, an unsupported
platform, a harness that started the command through a non-shell
intermediary) it falls back to the working-directory name, as before.

Rejected:

- **Text-only workaround** (tell agents to pass `-as` to wait): fixes the
  stolen wait lock but not the Stop hook or the SubagentStart parent.
- **Back off when ambiguous** (the Stop hook does not block, and wait refuses
  to start without `-as`, while more than one live session shares the
  working-directory name): avoids wrong wakes, but a second session loses
  idle wake entirely.

## Consequences

- Schema v9 adds `sessions.harness_pid` and `sessions.harness_start` (0 for
  rows registered before the upgrade, or when the platform cannot tell). As
  with every schema bump, all hosts upgrade and restart together.
- Claude subagents share their parent's MCP process and so its harness. The
  lookup takes only top-level sessions (no `/` in the name); with several, the
  most recently registered wins.
- A start time of 0 (unknown) matches on the pid alone, as `procs.Alive`
  already treats it.
- Only macOS and Linux can read the process table (`procs`); elsewhere the
  lookup finds nothing and the working-directory name is used.
