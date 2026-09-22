# ADR 0008: Memory channels are search-only for agents

Status: Accepted 2026-09-21. Decided by the user.

## Context

`register` subscribed every agent to `memory` and `memory/<repo>`, so every
memory post was pushed into every agent's context. Memories are reference
data whose value is at lookup time; live memories are never evicted, so the
backlog a subscriber pulls only grows, and one chatty agent spends every
other agent's context.

## Human decisions

1. **`register` no longer subscribes agents to memory channels.** A memory
   channel listed in `.local/agentbus.json` (or the default `memory`) is
   reported in a new `memory_channels` field of the register result instead
   of `subscribed`. Any memory subscription left by an earlier register is
   dropped, so resumed identities stop receiving memories too. An agent that
   wants the firehose can still `subscribe` explicitly after registering.
2. **Task lists keep push.** `tasks` and `tasks/<repo>` are memory-kind but
   event-driven (claims, status changes), so they stay in `subscribed`.
3. **The TUI keeps monitoring memory channels** through its own subscribe
   path, which this change does not touch.

## Consequences

- The hook, `init` prompt, skill, and README now say memories are searched,
  not pushed, and point agents at `memory_channels`.
- `search` never required a subscription, so no other tool changes.
- The `.local/agentbus.json` channel list is unchanged: memory entries are
  still how a repository names its memory channels.
