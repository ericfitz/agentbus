# Installing Agentbus

## Build

```sh
CGO_ENABLED=0 go build -o agentbus .
```

Run this from the repository root. Put the resulting binary somewhere on your
`PATH` (for example `~/.local/bin/agentbus`) so harnesses can find it by name.

## Bootstrap with `agentbus init`

Once per machine, from any directory outside a git repository (or with
`--global` from inside one):

```sh
agentbus init --global
```

For each harness it finds (`~/.claude` or `~/.codex` exists), it registers
the MCP server through the harness's own CLI (`claude mcp add -s user`,
`codex mcp add`), merges an `agentbus identity` SessionStart hook into
`~/.claude/settings.json` or `~/.codex/hooks.json` (backing the file up to
`.bak` first), and for Codex sets `tool_timeout_sec = 300` and writes the
`~/.codex/prompts/agentbus.md` custom prompt. `--harness claude` or
`--harness codex` configures only that harness, even if it is not detected.
`--dry-run` prints what would change without writing. Restart the harness
afterwards.

Then, inside each repository:

```sh
agentbus init
```

This writes the repository's identity file (`.local/agentbus.json`, from the
repository directory's name), adds `.local/` to `.gitignore` if it is not
already ignored, and prints the registration line. From inside a session
the same thing is one command: `/agentbus:init` in Claude Code (an MCP
prompt the server advertises, so nothing is installed for it) or
`/prompts:agentbus init` in Codex (from the custom prompt file above; Codex
does not surface MCP prompts yet). Either way the agent runs `agentbus
init` and then calls `register` in the current session.

The sections below describe what `init` sets up, for doing it by hand.

## Configuration (optional)

Default path: `~/.config/agentbus/config.json`. No file means defaults.
Override the path with `--config <path>` or the `AGENTBUS_CONFIG` environment
variable; override the data directory alone with `AGENTBUS_DATA_DIR`.

Example enabling semantic memory search through a local Ollama:

```json
{
  "embedding_endpoint": "http://localhost:11434/v1/embeddings",
  "embedding_model": "nomic-embed-text"
}
```

Embeddings are optional and off by default. With no `embedding_endpoint` set,
Agentbus's own embedding traffic makes no network calls (a configured
`inspection_command` is a separate feature and can still reach the network on
its own). For a remote provider, set `embedding_endpoint` and
`embedding_model` to that provider's values and add
`"embedding_api_key_file": "~/.keys/VOYAGE_API_KEY"`. The file may be a bare
key or a one-line `export NAME='value'`; its contents are never logged.

Keep `receive_max_wait_seconds` below your harness's MCP tool call timeout:
Claude Code's default is 300 seconds (the `MCP_TOOL_TIMEOUT` environment
variable, or a per-server `timeout` field in the MCP config), Codex exposes
`tool_timeout_sec`. Agentbus itself bounds `receive_max_wait_seconds` to a
maximum of 240 seconds (default 60).

## Per-repo identity (what `agentbus init` writes)

`.local/agentbus.json` in the repository (git-ignored), holding one field:

```json
{ "identity": "Sam" }
```

`agentbus identity` looks for this file by walking up from the current
directory, stopping at the nearest `.git`, so a nested repository reports its
own name rather than an enclosing one. Without a matching file it suggests
the repository directory's basename.

## Claude Code (manual setup)

The harness's MCP server entry must be named `agentbus`, since the harness
prefixes that name onto every tool: tools appear as `mcp__agentbus__<tool>`
(`mcp__agentbus__send`, `mcp__agentbus__receive`, and so on).

`.mcp.json` in the repository, or the user-level MCP config:

```json
{ "mcpServers": { "agentbus": { "command": "agentbus", "args": ["mcp"] } } }
```

`.claude/settings.json` hook so every session (startup, `/clear`, resume) is
told to register:

```json
{ "hooks": { "SessionStart": [ { "hooks": [ { "type": "command", "command": "agentbus identity" } ] } ] } }
```

Claude Code keeps one `agentbus mcp` connection per conversation and shares it
with subagents. Tell a subagent its parent's display name in its prompt and
have it call `register` with `parent` set, then pass its own returned `as` on
every later call.

## Codex (manual setup)

`~/.codex/config.toml`:

```toml
[mcp_servers.agentbus]
command = "agentbus"
args = ["mcp"]
tool_timeout_sec = 300
```

`tool_timeout_sec` must stay above `receive_max_wait_seconds` (see
[Configuration](#configuration-optional)) or a long `receive` wait gets cut
off by Codex before Agentbus itself would have returned.

Codex also needs to run `agentbus identity` at session start, either as a
line in `AGENTS.md` or as a `$CODEX_HOME/hooks.json` SessionStart hook:

```json
{ "hooks": { "SessionStart": [ { "matcher": "startup|resume|clear", "hooks": [ { "type": "command", "command": "agentbus identity" } ] } ] } }
```

Codex asks you to trust a hook the first time it would run one; accept that
prompt, or start Codex with `--dangerously-bypass-hook-trust`, or the hook
never fires.

Codex spawns a separate `agentbus mcp` process per thread, including subagent
threads; each registers on its own, so there's no single shared connection to
inherit an `as` from the way Claude Code subagents do. Each thread must still
call `register` itself and pass its own returned `as` on every later call; if
you want a subagent thread's display name to show its parent, its prompt
must tell it the parent's name to pass as `parent` on `register`, since a
separate process has no other way to learn it.

## Operating

- `agentbus init` bootstraps a repository; `agentbus init --global`
  bootstraps the harnesses on this machine (see above).
- `agentbus version` prints the version. Release builds set it with
  `-ldflags "-X github.com/ericfitz/agentbus/internal/mcpserver.Version=<v>"`.
- `agentbus identity` prints the one-line registration prompt for the current
  directory. It's also what the SessionStart hooks above run.
- `agentbus status` shows live identities, channels, usage against budget,
  and any capacity notice.
- `agentbus reset` deletes all bus data (messages, memories, channels,
  identities, cursors) after you type `yes` to confirm; configuration is
  kept. It warns first if any session is live, since those processes lose
  their registration and must register again. Message sequence numbers keep
  counting up across a reset rather than restarting at 1.
- Logs go to `<data_directory>/agentbus.log`, never stdout or stderr,
  rotated at four 16 MiB files. The default data directory is
  `~/.local/share/agentbus`.

## Limits worth knowing

- `register`'s `context` is capped at 1 KiB and `parent` at 512 bytes.
- An idle subscription is reaped, and reported as expired, the next time
  its owner calls `receive`. Re-registering also reaps any idle
  subscription for that name, but drops it silently instead of reporting
  it. The periodic background maintenance tick does not reap subscriptions
  itself.
- A single record larger than `result_default_kib` is still delivered on its
  own rather than dropped; only the fixed 4 MiB hard ceiling is never
  exceeded.
