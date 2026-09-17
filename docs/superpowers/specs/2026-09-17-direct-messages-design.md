# Direct messages

Date: 2026-09-17. Status: design approved by the human in session; this
document records it for review before planning.

## Goal

Let one identity send a message to one other identity rather than to a
channel of subscribers. A direct message (DM) is one-way: it lands in the
recipient's inbox. The recipient answers by sending a DM back.

## Human decisions (2026-09-17)

These were decided by the human, not the controller:

1. Every identity automatically gets one agent-specific channel for incoming
   messages only. It is a message channel; there is no memory counterpart.
2. `receive` reads the DM channel like any subscribed channel.
3. Subscribe, unsubscribe, join, and leave are not supported on DM channels.
4. `send` gains no new parameter. A DM is sent by naming the channel
   `dm/<identity>`.
5. The channel name `dm` is prohibited, although `/` already keeps
   `dm/<identity>` from colliding with user channels.
6. Offline delivery is "queue if known": a DM to an identity that has
   registered before succeeds and waits; a DM to a never-seen identity is
   `not_found`.
7. Only the owning identity may read its DM channel (receive, history,
   search). The TUI human may read all of them. Senders cannot read back.
8. DMs are one-way. A reply is a new DM to the original sender, optionally
   carrying `reply_to` with the original DM's seq. No threading surgery.
9. TUI: DM channels do not appear in the channel list. The sessions list
   becomes navigable (focus via the pane cycle, arrows move within it), each
   session row shows a new-message count, and selecting a session shows its
   DM channel.

## Bus

**Naming.** The DM channel of identity `X` is `dm/X`, kind `ordinary`. `X`
is the full display name, so a subagent `Sam/impl` owns `dm/Sam/impl`; the
owner is everything after the first `dm/`. `validateName` already rejects
`/`, so `create_channel` cannot produce such a name. No schema change and
no `user_version` bump: DM-ness is derived from the name by one helper,
`dmOwner(channel) (owner string, ok bool)`.

**Creation.** `Register` creates `dm/<display>` and the owner's subscription
to it (`from` = oldest) in its existing transaction, with `INSERT OR IGNORE`
for both. It runs on every register, so identities that existed before this
release get an inbox at their next register. With `resume=false` the inbox
subscription row is kept, cursor included: queued direct messages still
arrive and acknowledged ones are not replayed.

**Guards.** One helper, `dmReadable(as, channel)`, is true when the channel
is not a DM channel, when `as` owns it, or when `as` is an observer (below).

| Operation | On `dm/*` |
|-----------|-----------|
| `send` to `dm/X` | Allowed for any registered sender. `not_found` if `dm/X` does not exist ("X has never registered"). Sending to your own inbox is allowed. |
| `create_channel`, `EnsureChannel` | `validation` for the name `dm`; `dm/...` already fails the name rule |
| `subscribe`, `unsubscribe` | `validation`: DM channels are managed by register |
| `delete-channel` (CLI and TUI) | `validation` |
| `receive` | Unchanged; the owner's subscription delivers. A `channels` filter naming another identity's DM channel yields nothing, as for any unsubscribed channel |
| `history` | `not_found` unless `dmReadable` (the same error as a missing channel, so existence does not leak) |
| `search` | A `channel` filter on someone else's DM channel is `not_found`; unscoped search excludes DM channels the caller cannot read |
| `list_channels` | Omits all DM channels; the caller's own inbox is implied |
| Empty-channel reaper | Skips DM channels |
| Idle-subscription expiry | Skips the owner's DM subscription; an inbox must not silently detach |

Rate limits, size limits, idempotency keys, eviction, and the storage budget
apply to DM channels unchanged.

**reply_to.** The bus already validates only that the `reply_to` seq exists,
in any channel. A DM may therefore name the DM it answers. Nothing else
changes.

**Register result.** `pending` already lists every subscription, so the DM
channel's count appears there with no new field.

**Observer.** The TUI runs in-process on the bus package. `Bus.SetObserver(as)`
marks one identity of this process as able to read, subscribe to, and
receive from every DM channel. The MCP server never calls it, so no agent
can obtain it through a tool.

## `agentbus wait`

`-filter` is a regular expression on content, and the protocol has agents
park on `-filter @<name>`. A DM rarely contains `@<name>`, so a parked agent
would sleep through it. `wait` treats any message on the waiter's own
`dm/<name>` channel as a match regardless of `-filter`. `-channel` still
narrows which channels are watched.

## MCP surface

No new tools or parameters. Descriptions change:

- `send`: "To message one agent directly, set channel to `dm/<name>` using a
  name from register's `others` or `discover`."
- `subscribe`/`unsubscribe`: DM channels are not accepted.
- `register`: mentions the inbox.

## Hook protocol text and skill

`protocol` in `internal/cli/identity.go` gains one bullet: send to
`dm/<name>` when a message concerns exactly one agent; answer a DM with a DM
to its sender, setting `reply_to`. The embedded `using-agentbus` skill gains
the same guidance plus these rows in "What to post, and where":

- Changed something one specific other agent depends on: `dm/<that agent>`,
  in addition to `<repo>`.
- Question only the user can answer: `dm/<TUI identity>` when the human's
  TUI identity is live in `others`, else `<repo>`.

The first row also closes the gap found in the 2026-09-17 baseline test
(cross-repo completion notices reached the dependent repo in only 2 of 5
runs). Per writing-skills, the skill edit is retested with the same paper
scenario before release.

## TUI

- The rail's channel list omits `dm/*`.
- `paneSessions` joins the pane cycle after `paneChannels`. Up/down move a
  session cursor within it. While it has focus, the stream shows the
  selected session's DM channel, the header reads `@<name> · direct · n
  unread · m messages`, and the compose row sends to `dm/<name>` with the
  label drawn in the agent color (user color for the TUI's own row).
- Idle sessions (seen recently, not live) stay selectable: their inbox still
  exists and queues.
- Each session row shows the unread count of its DM channel in the agent
  color, the same "newer than last seen in this TUI run" rule the channel
  list uses. Viewing the session marks it seen.
- The TUI registers as observer and subscribes to each `dm/*` channel it
  learns of from the status report, so DMs stream in through the existing
  receive loop. `StatusReport` lists DM channels (it is CLI/TUI only).
- Threading: a row whose `reply_to` parent is not in the same channel renders
  as a top-level row. Covered by a test.
- Reply: in the human's own inbox, the reply key on a row sends a DM to that
  row's sender with `reply_to` set. In any other identity's inbox the reply
  key is disabled, because a reply there would land in the inbox being
  viewed, not with the message's sender. Composing a plain DM to the viewed
  identity is always allowed.

## Testing

- Bus: register creates inbox and subscription idempotently; DM delivery;
  queue while offline then resume; every guard in the table; `dm` name
  rejected; reaper and idle expiry skip; observer reads all.
- CLI: `wait -filter @x` wakes on a DM without `@x`.
- MCP integration: two processes, A sends `dm/B`, B receives it, A's
  `history dm/B` is `not_found`.
- TUI: rail omits DM channels; session pane navigation; unread counts;
  compose target; cross-channel reply renders top-level.
- Skill: paper-scenario retest, control vs. with-skill, 5 runs each.

## Out of scope

- Two-way DM conversations in one view, group DMs, DM memory channels.
- Renaming or migrating an existing user channel literally named `dm`; it
  keeps working, only new creation is rejected.
