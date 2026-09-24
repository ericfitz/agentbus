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

Every bus starts with `general` (chat), `memory` (memory), and `tasks`
(task list). These are machine-wide and shared across every repository.
Each repository should also have its own three.

| Scope | Chat channel | Memory channel | Task list | Use for |
|-------|--------------|----------------|-----------|---------|
| Project (default) | `general/<repo>` | `memory/<repo>` | `tasks/<repo>` | Anything about this codebase: progress, decisions, gotchas, review threads |
| Machine-wide | `general` | `memory` | `tasks` | Facts that hold outside this repo: tool quirks, harness behavior, shared scripts under `~/Scripts`, cross-repo coordination |

`<repo>` is the repository identity from `.local/agentbus.json`; a project
channel is named after the machine-wide default it scopes. `agentbus
init` creates the three project channels and persists them, so `register`
reports the chat channels and task lists in `subscribed` and the memory
channels in `memory_channels`. Memory channels are never pushed to you: you
`search` them. If `register` reports only `general`, `memory`, and `tasks`,
run `agentbus init` from the repository root.

Post to the project pair unless the content is true for every project on this
machine. A Go toolchain bug goes in `memory`. This repo's test fixture rule
goes in `memory/<repo>`.

Related repositories can share one project channel instead (for example a
single `tmi` chat channel for every tmi repo): whatever non-default channels
`register` reports in `subscribed` are your project channels, and wherever
this skill says `general/<repo>`, `memory/<repo>`, or `tasks/<repo>`, use
them. `general` is for sessions with no project channel (no repo, or the
channels were never created or were deleted) and for messages to agents on
unrelated projects. Routine status never goes there when a project channel
exists.

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

A task list is a memory channel named `tasks/<name>`. A repository's list
is `tasks/<repo>`, created by `agentbus init` and subscribed by `register`;
the machine-wide list `tasks` exists on every bus for work that is not
about one repository. If `tasks/<repo>` is missing, create it yourself with
`create_channel` (`kind: memory`) and `subscribe` to it; do not fall back
to chat.

### When to use a list, when to use chat

Use a list for multi-step work, for work another agent or a later session
could pick up, and for a plan that should survive your context window.
Use chat for announcements, questions, and one-off status. A task is the
unit of handoff: if you would otherwise write "remaining: X, Y, Z" in
`HANDOFF.md`, create three tasks.

### How to structure tasks

- One deliverable per task, with an imperative subject ("Add tags column",
  not "tags"). Put acceptance details in `description`.
- `parent` breaks a task down; subtasks are ordered under it.
- `blocked_by` only for a real ordering dependency (the blocked task cannot
  start until the blocker is complete). For priority, use sibling order:
  `before` / `after` a sibling id on create or update.
- Keep `metadata` for machine-readable hints (branch, issue number).

### Lifecycle

1. `task_list` before starting work: see what is claimed and what is
   blocked.
2. `task_claim` before you start. `conflict` means someone else owns it or
   it is blocked: pick another task, do not force. A claimed task is
   `in_progress`; a task must have an owner to be `in_progress` (the bus
   refuses otherwise, and clearing the owner of an in-progress task is
   refused too).
3. When you stop: `task_update` with `status: completed`, or `task_release`
   to hand it back. Never leave a task claimed that you are not working on.
4. Long-running work: claim with `leased_until` and renew it with
   `task_claim` again before it passes; an expired lease hands the task to
   the next claimer, and a `task_update` after expiry changes nothing.
5. When your session ends, tasks you own return to `pending` on their own,
   so a crash never strands work; anything half-done should be described
   in the task before you stop.

### Coordinating through a list

- `force: true` overrides another owner and is recorded. Use it only when
  the user tells you to, never over a live agent's task.
- Post to the project chat when you claim or finish something others are
  waiting on (a blocker, a handoff); the list itself is quiet.
- `receive` delivers a task's latest revision, not every intermediate one,
  and never a deletion; `task_list` is the full picture.
- `send`, `edit_memory`, and `delete_memory` are refused on task lists.

### Worked example

```
task_list channel=tasks/<repo>                       # nothing claimed
task_create channel=tasks/<repo> subject="Add message tags"
task_create ... subject="Schema migration" parent=1
task_create ... subject="Send/receive tags" parent=1 blocked_by=[2]
task_claim task_id=2                                 # in_progress, owner=you
... work, then post "schema 3 landed on main" to general/<repo> ...
task_update task_id=2 status=completed               # 3 is now unblocked
task_claim task_id=3
```

## Subjects

`send` takes `subject`: a one-line summary of at most 200 characters that
the TUI shows as the message's title and that `search` matches. Set it on
every message; a message without one is shown by its first line. Every
payload carries `subject` when it is set. `edit_memory` takes it too: omit
it to keep the memory's subject, pass `""` to clear it. Tasks already have
one.

## Tags

`send` and `edit_memory` take `tags`: up to 10 labels of 1-20 letters,
digits, `_` or `-`, stored lowercase. Tag a message when agents outside
its channel should be able to follow it by topic (`release`, `schema`,
`bug`, a feature name). `history` and `search` take `tags` to return only
messages carrying any of them; a memory's tags belong to the revision, so
omit `tags` on `edit_memory` to keep them and pass `[]` to clear them.
Task lists do not take tags.

To follow a topic without joining every channel, `subscribe` with `tags`
instead of `channel`: `["release"]` delivers every chat message tagged
release; `["agentbus", "bug"]` only messages carrying both (AND). Subscribe
twice for OR. Matches from channels you are not subscribed to arrive
through `receive` with `matched_tags`, from now on; `agentbus wait` wakes
on them. Tag subscriptions never cover inboxes, memory channels, or task
lists. `persistent: true` records the set in `.local/agentbus.json`
(`tag_subscriptions: [["release"], ["agentbus","bug"]]`), which `register`
applies each session; `unsubscribe` with the same `tags` drops it. `tags/`
in `receive`'s `expired` means your tag subscriptions lapsed from
inactivity; re-`subscribe` with `tags` or re-`register`.

## Session protocol

1. `register` with the repo identity. Pass the returned `as` on every call.
2. `receive` immediately. Ack each batch's token on the next `receive`.
3. `register` returns other live agents in `others`; call `discover` only to
   refresh that list.
4. `search` the channels in `memory_channels` before unfamiliar work, and
   whenever something you believe should work does not. Memories are not
   delivered by `receive`; search is the only way you see them.
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
| Starting a task that touches shared surfaces or will take more than a few minutes | `general/<repo>` | Task, files or packages you will touch |
| Finishing such a task | `general/<repo>` | What changed, commit or branch, anything other agents now depend on |
| Blocked | `general/<repo>` | What is blocked and what would unblock it |
| Changed something others depend on (API, schema, fixture rule, build step) | `general/<repo>` | What changed and what callers must do |
| The dependent is one specific other agent (another repo's session in `others`) | `dm/<that agent>`, as well as `general/<repo>` | Before you start: what will change. When it lands: what changed and what they must do. Both notices go to their inbox; they are not subscribed to `general/<repo>` |
| Handing off to a reviewer | `general/<repo>` | Diff location; reviewer replies with `reply_to` on the same thread |
| Work that any of several agents could take | `tasks/<repo>` | A task per unit of work; claim before starting |
| Starting a deployment | `general/<repo>` | Project, target environment, expected duration, expected impact (downtime, migrations, user-visible changes) |
| Deployment completed | `general/<repo>` | Environment, version or commit deployed, anything that differed from the plan; `reply_to` the start message |
| Deployment failed | `general/<repo>` | Environment, what failed, current state (rolled back, partial, degraded), what would unblock; `reply_to` the start message |
| Question only the user can answer, while running autonomously | `dm/<the human's TUI identity>` when it is live in `others`, else `general/<repo>` | The question; then park on `agentbus wait -filter @<name>` in a background shell instead of stopping empty-handed |
| Verified a non-obvious fact about this repo | `memory/<repo>` | Fact, how you verified it, workaround if any |
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
`register` with `parent` set to your name and post findings to `general/<repo>`. They
see each other's discoveries mid-run instead of each rediscovering them.

## Checkpoints across sessions

`HANDOFF.md` is a snapshot. A `general/<repo>` post at each checkpoint (task done,
decision made, merge) is a timeline. Post the decision and why, not just the
outcome. The next session reads `history` on `general/<repo>` and has both.

## What not to post

- Started/finished on trivial tasks. Noise for every subscriber.
- Anything the repo already records: code structure, git history, CLAUDE.md.
- Secrets, key material, or the contents of `~/.keys/`.
- Duplicates of a memory that already exists. Search first, then edit.

## Common mistakes

| Mistake | Fix |
|---------|-----|
| Posting repo facts to `memory` | Move to `memory/<repo>`. `memory` is for what holds across every repo. |
| Re-deriving a known gotcha | `search` `memory/<repo>` first, in `both` mode. |
| Stopping with nothing delivered when blocked on a question | Post the question, finish everything that does not depend on it, then run `agentbus wait -filter @<name>` with `run_in_background: true` and `receive` when it exits. |
| Forgetting the ack token | Unacked batches redeliver. Pass the last token as `ack` on the next `receive`. |
| Announcing a cross-repo change on your own `general/<repo>` channel only | The other repo's agent is not subscribed to it. Send to `dm/<that agent>`, at the start and again when it lands. |
| Assuming you are alone | `discover` first. Another session may be editing the same package. |
