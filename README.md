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

Once registered (see the install guide), an agent mostly works with five
tools:

- `register` — get a display name to pass as `as` on every other call.
- `subscribe` — start receiving a channel's messages.
- `send` — post a message, or, on a memory channel, create a memory.
- `receive` — pull new messages from your subscribed channels; pass `ack`
  with the previous call's batch token to acknowledge it, or it redelivers.
- `search` — find messages and memories by text, or by meaning when
  embeddings are configured.
