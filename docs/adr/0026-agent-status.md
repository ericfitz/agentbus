# ADR 0026: agent status and history (decisions shared with agentbus2)

Status: Accepted (Eric, 2026-10-27), shared with agentbus2 ADR 0014. The
decisions below are Eric's, made on 2026-10-27 for issue #43
(ericfitz/agentbus#43) and ericfitz/agentbus2#10, and copied verbatim from
agentbus2 ADR 0014. They are the part of that design that is not specific to
agentbus2. v1 implements them in issue #43; its own decisions are under "v1
decisions" below.

## Context

Eric asked (issue #43) for one status line per agent: what it is doing now
plus an optional problem, versioned so that earlier statuses become history,
with exactly one tag from a fixed set that the TUI maps to an icon. The
design brief and the implementation are in agentbus2
(`docs/superpowers/specs/2026-10-27-agent-status-design.md`, section 5 holds
Eric's decisions; ADR 0014 records them).

## Decisions (Eric, 2026-10-27)

The numbers are agentbus2 ADR 0014's.

3. **Offline: derived at read** from "no live session"; agents cannot set it
   (option A). Not chosen: stored by the bus at close and by a maintenance
   job at lapse (B), set by the agent (C).
6. **Tag set: the eight tags in the table below**, seven settable and
   `offline` bus-only (option A). Recorded in both repositories' ADRs. Not
   chosen: Eric's seven examples only, without `reviewing` (B); another list
   (C).
8. **Retention: prune to N at write**, N a setting with default 20 (option
   A). Not chosen: a maintenance job (B), a time limit (C).
13. **Push: pull only now**, with wake-on-change a follow-up through issue
    #13 (options A, then C). Not chosen now: wake on change (B). (Issue #13
    is ericfitz/agentbus2#13.)
14. **Repeated identical status: a new revision every time** (option A), so
    "every call is one revision". Not chosen: a no-op when nothing changed
    (B).
15. **`problem` with `blocked` or `needs-human`: optional** (option A), as
    Eric said; the protocol text asks for one. Not chosen: required for those
    two tags (B).

## v1 decisions (Eric, 2026-11-03)

These are human decisions, made by Eric on 2026-11-03 for issue #43 and
recorded in `docs/superpowers/specs/2026-11-03-agent-status-design.md`
(section 3, decisions 1 to 7). Decision 8, on ADR 0024, is recorded there.

1. **Storage: a dedicated `statuses` table** (option A). Not chosen:
   memory-style revisions in `messages` on `status/<name>` channels (B), and
   columns on the `sessions` row (C).
2. **Which agents the TUI shows** (option A): live sessions, plus offline
   identities whose last status was set within `status_offline_hours`
   (default 24). Not chosen: every identity that has ever set a status (B),
   and live sessions only (C).
3. **TUI: a status icon in the rail, plus a board in the main pane**
   (option A). The board has two entry points: the `b` key, and selecting
   the sessions heading with up/down. It sorts by time since last update
   (the default, newest first), by name, or by tag. Not chosen: rail and
   header only (B), and a board only (C).
4. **Agent and CLI surface** (option A): the MCP tools `set_status` and
   `status_history`, status in `discover`/`register` `others`, and the CLI
   commands `set-status` and `statuses`. Not chosen: MCP only (B), and no
   `status_history` over MCP (C).
5. **Truncation and expansion.** One-line cells that do not fit are cut with
   `…`. Left and right expand and collapse a board row's full text, the
   builder's choice, made to match how the stream treats messages.
6. **Stop hook nudge** (option B). When an agent stops with a stale working
   status, or with no status at all, the Stop hook asks it to update the
   status. The nudge quotes the current status so the agent never needs to
   query its own status first. Not chosen: protocol text only (A), and
   protocol text now with the nudge later (C).
7. **API versus prompts.** The API enforces what the bus can check: tag
   membership, the shape of `text` and `problem`, and top-level identities
   only. The skill suggests what only the agent can judge: which tag fits and
   when to change it. Transitions between tags are not enforced, and
   `problem` is accepted with any tag.

## The tag table

| Tag | Settable | Meaning |
| --- | --- | --- |
| `idle` | yes | nothing in hand; parked or between tasks |
| `planning` | yes | reading, designing, writing a plan |
| `implementing` | yes | changing code or docs |
| `reviewing` | yes | reviewing someone's work or its own |
| `waiting` | yes | waiting on another agent, CI or a long run, and expects it to resolve |
| `blocked` | yes | cannot proceed; needs something that will not come by itself |
| `needs-human` | yes | only the human can unblock it |
| `offline` | no (bus only) | no live session |

The icons are a TUI choice; agentbus2's are in its ADR 0014.

## Protocol text

agentbus2's agent-facing text, verbatim from the "Status" section of its
`internal/cli/skills/using-agentbus2/SKILL.md`. v1 implements the decisions
with its own tool and command names; the rules are unchanged.

> Your status is one line other agents and people see in `discover`'s
> `others` and on the board: what you are doing now. `set_status` takes a
> `tag`, a one-line `text` and an optional one-line `problem`.
>
> You cannot set `offline`: the bus shows it for an identity with no live
> session, and keeps the last status you set.
>
> Call `set_status` at the start of each task and when the work changes
> kind (from `planning` to `implementing`). When you are `blocked` or
> `needs-human`, pass `problem` with what is in your way. Set `idle` when
> you finish and have nothing in hand.
>
> A status belongs to the identity, so there is one line per identity, not
> per session. A session registered with a `hint` does not set a status; the
> top-level session's status covers it. The bus keeps the last
> `status_history_count` statuses of each identity (default 20), the current
> one included.

(The tag table that sits between the first and second paragraphs there is the
table above.) The park rule, verbatim from the "Park" section of the same
file: "Before ending a session with a handoff, set `idle` with `set_status`
first, then run `agentbus2 park` as your last agentbus2 action" and "Set
`idle` before `agentbus2 park`, never after, because `set_status` unparks."
Subagents do not set a status.

## What v1 decided for itself

agentbus2's other decisions (storage in a dedicated table, the key per
identity, the `Identity/SetStatus` access action, the three wire operations,
the MCP tool and CLI names, the TUI board layout) are tied to agentbus2's
Store, access and wire design. v1 decided its own storage, access, wire and
TUI when it implemented issue #43.

## References

- agentbus2 ADR 0014:
  https://github.com/ericfitz/agentbus2/blob/main/docs/adr/0014-agent-status.md
- ericfitz/agentbus#43, ericfitz/agentbus2#10.
