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

Amended 2026-09-29 by ADR 0009's amendment of that date: unscoped search also
returns DMs the caller sent; the inbox read guard is unchanged.

## Amendment 2026-10-03: why `wait` wakes but does not deliver

Human decision (user, 2026-10-03): `agentbus wait` stays as it is. It wakes
the agent, and the agent then calls `receive`. Wait does not ack, and it
keeps withholding a direct message's content and subject. Receive does the
delivering because:

1. **The ack makes delivery at-least-once.** Wait never acks or moves a
   cursor, so the receive that follows returns the same messages. A message
   counts as handled only when the agent acks the batch on its next
   receive, after the model has read it. If wait acked, a shell process
   would be acking before the model saw anything, and the messages would
   be lost if the harness truncated the background output, the session
   ended, or the model never read the output file. If wait did not ack, the
   agent would still need receive to ack, or the batch would be redelivered
   and the Stop hook would keep blocking. Either way the receive call does
   not go away.
2. **Wait's output is a partial view.** Wait prints only the messages that
   woke it: those that matched `-filter`, messages in the agent's own
   inbox, and messages that matched a followed tag. Receive returns
   everything pending on every subscribed channel, with `matched_tags` and
   expired-subscription notices, in batches bounded by `count`. An agent
   that acted only on wait's output would miss shared-channel traffic that
   piled up while it was idle, and a large backlog would arrive unbounded
   in a background output file that harnesses truncate.
3. **One delivery path.** Codex has no idle wake and uses receive alone.
   Delivering through wait as well would mean two read paths to keep
   consistent.
4. **Confidentiality, the weakest of the four.** Wait's output lands in
   shell output files (for example a harness's background-task output),
   unlike an MCP tool result, so it withholds a direct message's content
   and subject. This protects little: wait already prints every other
   message in full, the SQLite file holds the same text for the same OS
   user (see Consequences), and MCP results are kept in session
   transcripts. Points 1 to 3 are what keep the design. If DM content is
   ever printed, so an agent can judge urgency before it calls receive,
   only this point changes.
