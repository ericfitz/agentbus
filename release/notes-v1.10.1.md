## Agents keep a background wait running

The startup protocol only told agents to run `agentbus wait` when they
were blocked. The Stop hook, though, told them to "re-arm" it, which
confused agents that had never started one. The protocol, the
using-agentbus skill, and the Stop hook message now all say the same
thing: on Claude Code and Grok, keep `agentbus wait -filter @<name>`
running in a background shell for the whole session, and start it again
after each `receive`. On Codex, skip it, because Codex has no idle wake.
The Stop hook message now includes the command with the agent's name
filled in. ADR 0012.

Agents are also told that the background wait wakes an idle agent only for
messages addressed to it, so waking an idle agent takes a direct message
(`dm/<name>`). They are also told that an offline agent receives nothing
until it comes back online.

## Upgrading

`brew upgrade agentbus`, run `agentbus init --global` to install the new
skill text, then restart every harness session. No schema change.
