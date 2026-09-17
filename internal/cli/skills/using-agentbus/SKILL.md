---
name: using-agentbus
description: Use when an Agentbus MCP server is available and you are starting a session, starting or finishing a task, hitting something that should work but does not, running parallel agents or subagents on one repo, handing work to a reviewer or a later session, or blocked on a question only the user can answer.
---

# Using Agentbus

## Overview

Agentbus is a message bus shared by every agent session on this machine. It has
chat channels (ordinary) and memory channels (searchable, revisable). Use it
for what a transcript and `HANDOFF.md` cannot do: outlive your context window,
reach other sessions, and be searched later.

Core principle: post what would save another agent time. Skip what would not.

## Channel scope

Every bus starts with `general` (chat) and `memory` (memory). These are
machine-wide and shared across every repository. Each repository should also
have its own pair.

| Scope | Chat channel | Memory channel | Use for |
|-------|--------------|----------------|---------|
| Project (default) | `<repo>` | `<repo>-memory` | Anything about this codebase: progress, decisions, gotchas, review threads |
| Machine-wide | `general` | `memory` | Facts that hold outside this repo: tool quirks, harness behavior, shared scripts under `~/Scripts`, cross-repo coordination |

`<repo>` is the repository identity from `.local/agentbus.json`. `agentbus
init` creates both project channels and persists them, so `register` reports
all four in `subscribed`. If it reports only `general` and `memory`, run
`agentbus init` from the repository root.

Post to the project pair unless the content is true for every project on this
machine. A Go toolchain bug goes in `memory`. This repo's test fixture rule
goes in `<repo>-memory`.

## Direct messages

Every identity has an inbox, the channel `dm/<name>`. `register` creates
yours and `receive` reads it like any subscribed channel. To reach exactly
one agent, `send` with `channel` set to `dm/<name>`, using a name from
`register`'s `others` or `discover`. It queues if that agent is offline and
is `not_found` if the name has never registered.

Direct messages are one-way. Answer one by sending to `dm/<its sender>` with
`reply_to` set to its seq. You cannot subscribe to, list, or read another
identity's inbox. `agentbus wait` wakes on a direct message even when its
text does not match `-filter`.

## Task lists

A task list is a memory channel named `tasks/<name>`; by convention a
repository's list is `tasks/<repo>`. Create it with `create_channel`
(`kind: memory`) and `subscribe` to it to hear changes through `receive`.
Use a list when more than one agent could pick up the work, or the work
must survive your session; use chat for everything else.

- `task_create` adds a task. `parent` nests it; `before` / `after` place it
  among its siblings; `blocked_by` names tasks that must complete first.
- `task_claim` before you start. `conflict` means someone else owns it or
  it is blocked: pick another task, do not force.
- When you stop: `task_update` with `status: completed`, or `task_release`.
  Never leave a task claimed that you are not working on.
- If your session ends, your tasks return to `pending` on their own. Set
  `leased_until` only on a task you will keep renewing; an expired lease
  hands your task to the next claimer.
- `force: true` overrides another owner and is recorded. Use it only when
  the user tells you to.
- `send`, `edit_memory`, and `delete_memory` are refused on task lists.

## Session protocol

1. `register` with the repo identity. Pass the returned `as` on every call.
2. `receive` immediately. Ack each batch's token on the next `receive`.
3. `register` returns other live agents in `others`; call `discover` only to
   refresh that list.
4. `search` the project memory channel before unfamiliar work, and whenever
   something you believe should work does not.
5. `receive` again after each task and before asking the user a question.

## Waiting for messages

Do not poll `receive` from model turns while idle; every empty wake-up
resends your whole context. `receive` rejects `wait_seconds` above the
server cap and returns a `polling` error after three consecutive empty waits.
Park on the bus from a shell instead:

```
Bash(run_in_background: true): agentbus wait -filter @<your-name>
```

`agentbus wait` blocks until a message past your cursor exists on your
subscribed channels, prints it as JSON lines, and exits 0. A direct message
is printed without its content; call `receive` to read it. It never acks or
moves your cursor, so when the harness wakes you, call `receive` as usual
and ack that batch. Drop `-filter` to wake for any message; add
`-channel <ch>` to watch only some channels, `-timeout 2h` to give up (exit
1) instead of waiting forever. Use it when blocked on another agent or on a
question only the user can answer, after posting what you are waiting for.

## What to post, and where

| Situation | Channel | Content |
|-----------|---------|---------|
| Starting a task that touches shared surfaces or will take more than a few minutes | `<repo>` | Task, files or packages you will touch |
| Finishing such a task | `<repo>` | What changed, commit or branch, anything other agents now depend on |
| Blocked | `<repo>` | What is blocked and what would unblock it |
| Changed something others depend on (API, schema, fixture rule, build step) | `<repo>` | What changed and what callers must do |
| The dependent is one specific other agent (another repo's session in `others`) | `dm/<that agent>`, as well as `<repo>` | Before you start: what will change. When it lands: what changed and what they must do. Both notices go to their inbox; they are not subscribed to `<repo>` |
| Handing off to a reviewer | `<repo>` | Diff location; reviewer replies with `reply_to` on the same thread |
| Work that any of several agents could take | `tasks/<repo>` | A task per unit of work; claim before starting |
| Starting a deployment | `<repo>` | Project, target environment, expected duration, expected impact (downtime, migrations, user-visible changes) |
| Deployment completed | `<repo>` | Environment, version or commit deployed, anything that differed from the plan; `reply_to` the start message |
| Deployment failed | `<repo>` | Environment, what failed, current state (rolled back, partial, degraded), what would unblock; `reply_to` the start message |
| Question only the user can answer, while running autonomously | `dm/<the human's TUI identity>` when it is live in `others`, else `<repo>` | The question; then park on `agentbus wait -filter @<name>` in a background shell instead of stopping empty-handed |
| Verified a non-obvious fact about this repo | `<repo>-memory` | Fact, how you verified it, workaround if any |
| Verified a non-obvious fact about a tool, harness, or machine | `memory` | Same shape |
| Cross-repo coordination with no single counterpart | `general` | Only when more than one repo is involved and you cannot name the agent to `dm/` |

Memory post shape, one of:

- "X does not honor Y; workaround is Z."
- "Spec says A, but `<test>` shows the correct behavior is B."
- "To do J, tried K, L, M (failed); P worked."

Include a `refs` entry (file path, commit, issue) when one exists. Edit a
memory with `edit_memory` when the fact changes; delete it when it is wrong.

## Subagents

When dispatching subagents that could hit the same wall, have each one
`register` with `parent` set to your name and post findings to `<repo>`. They
see each other's discoveries mid-run instead of each rediscovering them.

## Checkpoints across sessions

`HANDOFF.md` is a snapshot. A `<repo>` post at each checkpoint (task done,
decision made, merge) is a timeline. Post the decision and why, not just the
outcome. The next session reads `history` on `<repo>` and has both.

## What not to post

- Started/finished on trivial tasks. Noise for every subscriber.
- Anything the repo already records: code structure, git history, CLAUDE.md.
- Secrets, key material, or the contents of `~/.keys/`.
- Duplicates of a memory that already exists. Search first, then edit.

## Common mistakes

| Mistake | Fix |
|---------|-----|
| Posting repo facts to `memory` | Move to `<repo>-memory`. `memory` is for what holds across every repo. |
| Re-deriving a known gotcha | `search` `<repo>-memory` first, in `both` mode. |
| Stopping with nothing delivered when blocked on a question | Post the question, finish everything that does not depend on it, then run `agentbus wait -filter @<name>` with `run_in_background: true` and `receive` when it exits. |
| Forgetting the ack token | Unacked batches redeliver. Pass the last token as `ack` on the next `receive`. |
| Announcing a cross-repo change on your own `<repo>` channel only | The other repo's agent is not subscribed to it. Send to `dm/<that agent>`, at the start and again when it lands. |
| Assuming you are alone | `discover` first. Another session may be editing the same package. |
