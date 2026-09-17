# ADR 0004: Direct messages and session end

Status: Accepted 2026-09-17. The human decisions below were made by the user
in session on 2026-09-17; the controller decisions were made while planning
and implementing and are open to veto. Where this ADR conflicts with the v2
spec or earlier ADRs, this ADR supersedes; the v2 spec is not edited. Design:
`docs/superpowers/specs/2026-09-17-direct-messages-design.md`.

## Human decisions

1. Every identity automatically gets one agent-specific channel for incoming
   messages only. It is a message channel; there is no memory counterpart.
2. `receive` reads the direct-message channel like any subscribed channel.
3. Subscribe, unsubscribe, join, and leave are not supported on
   direct-message channels.
4. `send` gains no new parameter. A direct message is sent by naming the
   channel `dm/<identity>`.
5. The channel name `dm` is prohibited, although `/` already keeps
   `dm/<identity>` from colliding with user channels.
6. Offline delivery is "queue if known": a direct message to an identity
   that has registered before succeeds and waits; one to a never-seen
   identity is `not_found`.
7. Only the owning identity may read its inbox (receive, history, search).
   The TUI human may read all of them. Senders cannot read back.
8. Direct messages are one-way. A reply is a new direct message to the
   original sender, optionally carrying `reply_to`. No threading changes.
9. TUI: inboxes do not appear in the channel list. The session list is
   navigable (pane cycle for focus, arrows within it), each session row
   shows a new-message count, and selecting a session shows its inbox.
10. Session end needs no harness hook. The process that holds a session
    announces its own departure: the MCP server on shutdown (stdin close, or
    SIGTERM, SIGINT, SIGHUP) and the TUI on quit delete their session rows
    (`Bus.EndSessions`). The agent does nothing and waits on nothing. The
    30-second attachment expiry remains the fallback for a killed process.
    Subscriptions and cursors survive, so the identity resumes.
11. `resume=false` means no backlog of any kind, inbox included: not from
    the cursor and not from the beginning; only messages that arrive after
    the register. (Decided 2026-09-17, after the final review found two
    earlier designs — recreate the inbox at cursor 0, and keep its old
    cursor — both misread the flag as being about subscriptions rather than
    about backlog.)

## Controller decisions

1. No schema change. An inbox is an `ordinary` channel named `dm/<identity>`;
   whether a channel is an inbox is derived from its name.
2. The TUI reads every inbox through `Bus.SetObserver`, which only an
   in-process caller can reach. The MCP server never calls it, so no agent
   can obtain it through a tool. The delivery paths (`receive`, `wait`,
   `register`'s pending list) re-check `dmReadable` too, not just subscribe,
   history, and search, so inheriting the TUI's name after its session ends
   does not also inherit its view of other identities' inboxes.
3. Reading or searching another identity's inbox returns `not_found`, the
   same as a name that never registered, so existence does not leak.
4. `Registration.Resumed` ignores the inbox subscription created by the same
   register call, so a brand-new identity still reports `resumed: false`.
5. The owner's inbox subscription never idle-expires, and the empty-channel
   reaper skips inboxes.
6. `agentbus wait` wakes on a message in the waiter's own inbox even when
   its text does not match `-filter`; `-channel` still narrows.
7. In the TUI, reply works only in the human's own inbox, where it sends a
   direct message to the row's sender with `reply_to` set. In another
   identity's inbox it is refused, because the reply would land in the inbox
   being viewed.
8. The `using-agentbus` skill routes "a question only the user can answer"
   to the human's TUI inbox when that identity is live, and tells agents to
   send a dependent agent both the start and the completion notice directly.
   Tested 2026-09-17 with paper scenarios, five runs per arm: completion
   notices reached the dependent agent in 5 of 5 runs with the skill (2 of 5
   before the edit); user questions went to the human's inbox in 5 of 5 (3 of
   5 with the hook text alone).

## Consequences

- An existing user channel literally named `dm` keeps working; only creating
  one is rejected.
- A conversation between two identities is split across their two inboxes.
  One view of both directions is out of scope.
- Harness sessions keep running the old MCP binary until restarted, and the
  installed skill is refreshed only by `agentbus init --global`.
- The inbox read guard bounds the MCP tool surface; it is not a security
  boundary against a process with shell access as the same OS user. The
  SQLite file, `agentbus status`, and `agentbus wait -as <name>` are all
  reachable there (`wait` withholds a direct message's content but still
  reveals its sender and timing). Names are not credentials: an agent that
  registers a name whose session has already ended becomes that identity,
  inbox included.
