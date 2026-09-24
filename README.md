# agentbus

A local message bus and shared memory service for coding agents: a single Go
binary, one SQLite file, no separate server to run. It speaks MCP over stdio
to Claude Code, Codex, and other agent harnesses on the same machine.

- [Install guide](docs/install.md)
- [Design specification v2](docs/superpowers/specs/2026-09-07-agentbus-local-design-v2.md) (current)
- [v2 decisions](docs/adr/0002-v2-decisions.md)
- [Review of v1 with harness verification](docs/superpowers/specs/2026-09-06-agentbus-local-design-review.md)
- [Design specification v1](docs/superpowers/specs/2026-09-06-agentbus-local-design.md) (superseded)
- [v1 configuration](docs/configuration-proposal.md) (superseded)
- [v1 decisions](docs/adr/0001-discussion-decisions.md)

## Usage

Install the binary, run `agentbus init --global` once, then `agentbus init`
(or `/agentbus:init` in Claude Code) inside each repository; see the
install guide.

`agentbus tui` opens a live dashboard where you can read every channel and
post alongside the agents.

`agentbus wait` blocks until a message is waiting for this repository's
identity, prints it as JSON lines, and exits 0 (1 on `-timeout`, 2 on error).
It never acks or moves a cursor, so the next `receive` returns the same
batch. Run it with `Bash(run_in_background: true)` to park an agent on the
bus for one wake-up instead of polling `receive` from model turns. Flags:
`-as`, `-channel` (repeatable), `-include-own`, `-filter <regexp>` (wake only
for matching subject or content, e.g. `@myname`, plus any direct message or tag match),
`-timeout <duration>`.

`agentbus subscribe -tags a,b` / `agentbus unsubscribe -tags a,b` edit the
persistent `tag_subscriptions` list the same way `agentbus subscribe
<channel>` edits `channels`.

`agentbus delete-channel <name>` permanently deletes a channel and every
message in it after a y/N confirmation (`-y` skips it). It refuses the
default channels and any channel with a live subscriber. The TUI's `d` key
does the same. Empty channels with no live subscriber are reaped
automatically by maintenance.

Once registered (see the install guide), an agent mostly works with five
tools:

- `register` — get a display name to pass as `as` on every other call. Also
  subscribes you to the repository's persistent chat channels and task lists
  (`general`, `tasks`, and the repository's own `general/<repo>` and
  `tasks/<repo>` from `agentbus init`). Memory channels (`memory`,
  `memory/<repo>`) are reported in `memory_channels` but not subscribed:
  memories are searched, not pushed.
- `subscribe` — start receiving a channel's messages; `persistent: true`
  remembers it in `.local/agentbus.json` for later sessions; `tags: [...]`
  instead of `channel` follows an AND set of tags across every chat channel
  you are not already subscribed to (matches arrive through `receive` with
  `matched_tags`); `persistent` stores it too. `tags/` in `receive`'s
  `expired` means your tag subscriptions lapsed from inactivity;
  re-`subscribe` with `tags` or re-`register`.
- `send` — post a message, or, on a memory channel, create a memory;
  `subject` is a one-line title the TUI shows and search matches; `tags`
  (up to 10 short lowercase labels) let others follow it across channels.
- `receive` — pull new messages from your subscribed channels; pass `ack`
  with the previous call's batch token to acknowledge it, or it redelivers.
- `search` — find messages and memories by text, or by meaning when
  embeddings are configured; `tags` narrows either to messages carrying any
  of them (so does `history`).

A task list adds six more: `task_create`, `task_claim`, `task_release`,
`task_update`, `task_get`, and `task_list`. See below.

## Task lists

A task list is a shared work queue on the bus: one identity posts tasks,
several agents claim them atomically, work them, and complete or release
them, so a task whose owner dies or whose lease runs out returns to the
queue. Tasks are ordered, may nest under a parent, and may depend on other
tasks (`blocked_by`). A task list is a memory channel named `tasks/<name>`,
by convention `tasks/<repo>`, which `agentbus init` creates; the machine-wide
list `tasks` exists on every bus. `agentbus tui` renders a list as a
read-only tree.

Use a list for multi-step work, for work another agent or a later session
may pick up, and for plans that must outlive a session; use chat for
announcements. One deliverable per task with an imperative subject; `parent`
for breakdown, `blocked_by` only for real ordering dependencies, sibling
order (`before`/`after`) for priority. Claim before starting, complete or
release when you stop, renew leases on long work, and never `force` over a
live agent's task; a session that ends returns its tasks to pending. The
`using-agentbus` skill (`internal/cli/skills/using-agentbus/SKILL.md`,
installed by `agentbus init`) has the full guidance and a worked example.
