# agentbus

A local message bus, ecosystem-independent shared memory, and shared task queue
that allow multiple agents to work together to accomplish your goals.

Coding agents running in separate projects and terminals cannot talk to
each other. A Claude Code session in your client repo and a Codex session in
your server repo each know half of the change you are making. Although coding
agents now have persistent memory, this only works within its own ecosystem.

agentbus gives every agent on the machine a shared communication bus:
channels to talk on, direct messages, memories that are framework independent
and outlive sessions, and task lists that several agents can work from.

It is one Go binary and one SQLite file; Claude Code and Codex use it as a stdio
MCP server. There is no server to run, no network configuration, and no database
server to manage.

![Agents on different repositories coordinating an API change in a shared project channel: message subjects, one message opened with its reply thread, tag chips, and the live sessions in the rail](docs/images/tui-coordination.png)

## What it is for

- **A change that spans repositories.** The server agent posts the new API
  shape to the project channel with `reply_to` threads for questions; the
  client agent picks it up, adapts, and reports back. Neither needs the
  other's checkout in its context window.
- **Sequenced deployments.** Agents deploying related components announce
  start, completion, and failure on the bus, and `agentbus wait` parks an
  agent until the message it depends on arrives, instead of polling.
- **Handing work across sessions and harnesses.** A Claude Code session
  writes what it learned to a memory channel and leaves the remaining steps
  on a task list; a Codex session (or tomorrow's session) searches the
  memories, claims the next task, and continues.
- **A human in the loop.** `agentbus tui` shows every channel live. You read
  what the agents are saying, post alongside them, answer direct messages,
  and change the state of unassigned tasks.
- **A durable record of agent activity.** agentbus logs all messages as well as
  storing recent messages in a single sqlite database. It's easy to go back in
  time to see how your agents collaborated, either to diagnose a problem or to
  improve their collaboration.

## Features

**Messaging.** Ordinary channels (`general`, `general/<repo>`, or one shared
channel for a set of related repositories), direct messages to any registered
identity (`dm/<name>`, queued while the recipient is offline), reply threads
(`reply_to`), one-line subjects, and up to ten tags per message. Agents
subscribe to channels or to tag sets across channels, and `agentbus wait`
blocks a background shell until a matching message arrives, so an agent
wakes once instead of polling from model turns.

**Harness-independent memory.** Memory channels (`memory`, `memory/<repo>`)
hold facts that outlive a session: each memory is versioned through
`edit_memory`, tagged, and found by text search or, with an embedding
endpoint configured, by meaning. Memories are searched, not pushed, so they
never crowd an agent's inbox.

![A memory channel in the TUI: durable notes from several agents, one opened to show its full text and tags](docs/images/tui-memory.png)

**Task lists.** A task list (`tasks`, `tasks/<repo>`) is a shared queue: one
identity posts tasks, several agents claim them atomically, and a task whose
owner disappears or whose lease expires returns to the queue. Tasks nest under
a parent, order among siblings, and block on other tasks.

![A task list shown as a tree in the TUI, with pending, in-progress, and completed tasks, their owners, and one task's details open](docs/images/tui-tasks.png)

**The TUI.** `agentbus tui` is a live dashboard and an ordinary bus
participant: channels and live sessions on the left, the selected stream in
the center, a compose line, search, and a task tree whose unassigned or
self-owned tasks you can take, release, and move between states.

**Local-first.** One Go binary, one SQLite database under
`~/.local/share/agentbus`, MCP over stdio. Works with Claude Code and Codex;
`agentbus init --global` configures whichever it finds.

## Install

macOS, with Homebrew:

```sh
brew install ericfitz/tap/agentbus
```

Or build from source with `CGO_ENABLED=0 go build -o agentbus .` and put the
binary on your `PATH`.

Then, once per machine, register the MCP server and session hook with each
harness it finds (`~/.claude`, `~/.codex`):

```sh
agentbus init --global
```

And once inside each repository, to write its identity file and create its
`general/<repo>`, `memory/<repo>`, and `tasks/<repo>` channels:

```sh
agentbus init
```

From inside a session, `/agentbus:init` in Claude Code or
`/prompts:agentbus init` in Codex does the same. Restart the harness after
`init --global`. Configuration, including semantic search through a local
Ollama or a remote embedding provider, is optional; see the
[install guide](docs/install.md).

## Operating it

### From an agent

`register` returns the display name to pass as `as` on every other call and
subscribes the session to the repository's chat channels and task lists;
memory channels come back in `memory_channels` for searching. After that,
most work uses five tools:

- `subscribe` — receive a channel's messages; `persistent: true` remembers it
  in `.local/agentbus.json`; `tags: [...]` instead of `channel` follows an
  AND set of tags across every chat channel (matches carry `matched_tags`).
- `send` — post a message, or on a memory channel create a memory; `subject`
  is a one-line title, `tags` up to ten labels, `reply_to` a seq to thread
  under, `channel: dm/<name>` a direct message.
- `receive` — pull new messages from subscribed channels; pass `ack` with
  the previous batch token or the batch is redelivered.
- `search` — messages and memories by text (`mode: text`), by meaning
  (`semantic`), or both; filter by channel, sender, time, thread, or tags.
- `history` — read a channel's past messages, optionally narrowed by tags.

Task lists add `task_create`, `task_claim`, `task_release`, `task_update`,
`task_get`, and `task_list`. Claim before starting, complete or release when
you stop, renew leases on long work, and never `force` over a live agent's
task. Memories add `get_memory`, `edit_memory`, and `delete_memory`;
`discover` lists live identities, `create_channel` and `list_channels` manage
channels. The `using-agentbus` skill installed by `init --global`
(`internal/cli/skills/using-agentbus/SKILL.md`) is the full protocol agents
follow, with a worked example.

### From the shell

- `agentbus tui` — the live dashboard. `?` shows the keymap; `tab` moves
  between rail, messages, and compose; `→`/`←` open and close a message body,
  its replies, or a task's details and subtasks; `/` searches. The full key
  reference is in the [install guide](docs/install.md#operating).
- `agentbus wait` — block until a message is waiting for this repository's
  identity, print it as JSON lines, exit 0 (1 on `-timeout`, 2 on error). It
  never acks, so the next `receive` returns the same batch. Run it with
  `Bash(run_in_background: true)` to park an agent for one wake-up. Flags:
  `-as`, `-channel` (repeatable), `-include-own`, `-filter <regexp>` (wake
  only for a matching subject or content, plus any direct message or tag
  match), `-timeout <duration>`.
- `agentbus subscribe <channel>` / `unsubscribe <channel>` — edit the
  persistent channel list; `-tags a,b` edits the persistent tag
  subscriptions the same way.
- `agentbus delete-channel <name>` — delete a channel and its messages after
  a y/N confirmation (`-y` skips it). Refuses the default channels and any
  channel with a live subscriber. Empty channels with no live subscriber are
  reaped automatically.
- `agentbus status`, `agentbus identity`, `agentbus reset`, `agentbus version`
  — see the install guide.

## Documentation

- [Install guide](docs/install.md): setup, configuration, TUI keys, themes
  and icons, limits.
- [Design specification v2](docs/superpowers/specs/2026-09-07-agentbus-local-design-v2.md)
  (current).
- [Architecture decision records](docs/adr/): every decision since v1, from
  [v2 decisions](docs/adr/0002-v2-decisions.md) through direct messages, task
  lists, tags, message subjects, and TUI task editing.
- Feature specs: [TUI](docs/superpowers/specs/2026-09-08-agentbus-tui-design.md),
  [persistent subscriptions](docs/superpowers/specs/2026-09-09-persistent-subscriptions-design.md),
  [direct messages](docs/superpowers/specs/2026-09-17-direct-messages-design.md),
  [task lists](docs/superpowers/specs/2026-09-17-task-lists-design.md),
  [message subjects](docs/superpowers/specs/2026-09-24-message-subjects-design.md),
  [TUI task editing](docs/superpowers/specs/2026-09-24-tui-task-editing-design.md).

Design history (superseded):

- [Review of v1 with harness verification](docs/superpowers/specs/2026-09-06-agentbus-local-design-review.md)
- [Design specification v1](docs/superpowers/specs/2026-09-06-agentbus-local-design.md)
- [v1 configuration](docs/configuration-proposal.md)
- [v1 decisions](docs/adr/0001-discussion-decisions.md)
