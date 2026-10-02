# Installing Agentbus

## Install with Homebrew (macOS)

```sh
brew install ericfitz/tap/agentbus
```

The formula installs a signed, notarized universal binary from the matching
[GitHub release](https://github.com/ericfitz/agentbus/releases). Maintainers
cut a release with `release/release.sh <tag>` from a tagged, clean checkout.

## Or, Build from source

```sh
git clone https://github.com/ericfitz/agentbus.git
cd agentbus
CGO_ENABLED=0 go build -o agentbus .
```

Run this from the repository root. Put the resulting binary somewhere on your
`PATH` (for example `~/.local/bin/agentbus`) so harnesses can find it by name.

## Before first run: bootstrap with `agentbus init`

Once per machine, from any directory outside a git repository (or with
`--global` from inside one):

```sh
agentbus init --global
```

For each harness it finds (`~/.claude`, `~/.codex`, or `~/.grok` exists), it
registers the MCP server through the harness's own CLI (`claude mcp add -s user`,
`codex mcp add`, `grok mcp add --scope user`), merges an `agentbus identity`
SessionStart hook and an `agentbus stop-hook` Stop hook (backing the file up
to `.bak` first), and installs the `using-agentbus` skill. Hook and skill
paths:

| Harness | Hooks | Skill |
|---------|-------|-------|
| Claude Code | `~/.claude/settings.json` (SessionStart, Stop) | `~/.claude/skills/using-agentbus/SKILL.md` |
| Codex | `~/.codex/hooks.json` (SessionStart, Stop) | `~/.agents/skills/using-agentbus/SKILL.md` |
| Grok Build | `~/.grok/hooks/agentbus.json` (Stop only; see below) | `~/.grok/skills/using-agentbus/SKILL.md` |

The Stop hook runs when the agent finishes a turn. If messages it has not
been handed yet are waiting, it keeps the agent working with an instruction
to call `receive`; otherwise the agent stops as usual. It allows one such
continuation per turn and lets the agent stop on any error (ADR 0012).

Codex also gets `tool_timeout_sec = 300` and
`~/.codex/prompts/agentbus.md`. Grok Build also gets
`~/.grok/commands/agentbus.md` (the `/agentbus` slash command) and
`~/.grok/rules/agentbus.md`. Grok ignores SessionStart hook stdout, so
`init` installs no SessionStart hook; that rule is what tells a session to run `agentbus
identity` and follow it. Grok's default `tool_timeout_sec` is 6000, already
above `receive_max_wait_seconds`, so `init` does not set one. The skill
ships inside the binary, so rerun `init --global` after upgrading to refresh
it. `--harness claude`, `--harness codex`, or `--harness grok` configures
only that harness, even if it is not detected. `--dry-run` prints what would
change without writing. Restart the harness afterwards.

Then, inside each repository:

```sh
agentbus init
```

This writes the repository's identity file (`.local/agentbus.json`, from the
repository directory's name), creates the repository's own channels on the
bus (`general/<identity>`, `memory/<identity>`) and adds them to the
persistent channel list, adds `.local/` to `.gitignore` if it is not
already ignored, and prints the registration line. A stale `tasks` or
`tasks/...` entry already in the file (task lists are per effort now, not
per repository) is removed, with a note naming what was removed. From
inside a session
the same thing is one command: `/agentbus:init` in Claude Code (an MCP
prompt the server advertises, so nothing is installed for it),
`/prompts:agentbus init` in Codex (from the custom prompt file above; Codex
does not surface MCP prompts yet), or `/agentbus init` in Grok Build (from
`~/.grok/commands/agentbus.md`). Either way the agent runs `agentbus init`
and then calls `register` in the current session.

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
`"embedding_api_key_env": "OPENAI_API_KEY"` names an environment variable to
read the key from instead; it wins when set and non-empty, and the key file is
the fallback for processes started without it (a harness-spawned MCP server
only sees the variable if the harness was launched from a shell that had it).
`embedding_query_timeout_seconds` (default 10, range 0.1-120) bounds the
query embedding during a search; past it, `semantic` and `both` searches fall
back to text results and set `semantic_unavailable`.

`task_idle_hours` (default 168, 7 days), `task_expiry_hours` (default 720,
30 days), and `memory_expiry_hours` (default 8760, 1 year) are the ADR 0013
inactivity periods: the TUI marks a task channel idle in its rail after
`task_idle_hours` with no task activity; the maintenance tick deletes a
task channel, with all its tasks, after `task_expiry_hours` with none; and
it tombstones a memory that no `get_memory` call or `search` hit has
touched for `memory_expiry_hours`. 0 disables the corresponding rule;
negative values are rejected.

`tui_rail_width` (default 0, range 0-1000) is the channel rail's width in
columns. The TUI writes it when you press `<` or `>`, rewriting the config
file with its keys sorted; 0 means a fifth of the screen. Set at start, it
is clamped to the terminal.

`tui_name` (default: your OS user name) is the identity `agentbus tui`
registers under; `agentbus tui --as <name>` overrides it for one run. It
follows the same rule as agent names: 1-128 bytes, no `/`, no control
characters.

Keep `receive_max_wait_seconds` below your harness's MCP tool call timeout:
Claude Code's default is 300 seconds (the `MCP_TOOL_TIMEOUT` environment
variable, or a per-server `timeout` field in the MCP config), Codex exposes
`tool_timeout_sec` (init sets it to 300), and Grok Build's default
`tool_timeout_sec` is 6000. Agentbus itself bounds `receive_max_wait_seconds`
to a maximum of 240 seconds (default 60).

## Per-repo identity and channels (what `agentbus init` writes)

`.local/agentbus.json` in the repository (git-ignored):

```json
{ "identity": "Sam", "channels": ["general", "memory", "general/Sam", "memory/Sam"] }
```

`identity` is the name the agent registers with. `channels` is the
persistent subscription list: `register` subscribes the session to each
listed channel (from the current position) and reports them in its
`subscribed` field; channels that do not exist are reported in
`subscribe_failed` and skipped. Without a `channels` key the list is
`general` and `memory`; an empty list means no automatic subscriptions. A
`tasks` or `tasks/...` entry left over from before ADR 0013 is ignored
(not subscribed) and reported in `register`'s `ignored_channels` field
instead; `init` removes such entries from the file, or delete them by hand.
`init` writes the two machine-wide channels plus the repository's own
`general/<identity>` and `memory/<identity>`, creating them on the bus if
missing. A prefix implies the kind, so `create_channel` needs none for such
names. Rerunning `init` recreates them for whatever identity the file
names; a project channel from before v1.5.0 (`<identity>`,
`<identity>-memory`) is renamed to its prefixed name with its history.

Task lists are separate and per effort, not per repository: create
`tasks/<effort>` with `create_channel` when you start one, or subscribe to
one you find with `list_channels`. It is removed, with all its tasks,
after `task_expiry_hours` of inactivity (see "Configuration" above).

Edit the list from the repository root with `agentbus subscribe <channel>`
and `agentbus unsubscribe <channel>` (the file is created if missing), or
from inside a session by passing `persistent: true` to the `subscribe` or
`unsubscribe` tool. Changes apply at the next `register`. A persistent
`subscribe` from a subdirectory creates the file at the nearest git root
(not the subdirectory) when none exists yet.

`agentbus identity` looks for this file by walking up from the current
directory, stopping at the nearest `.git`, so a nested repository reports its
own name rather than an enclosing one. Without a matching file it suggests
the repository directory's basename. Its output is the session protocol every
agent is told to follow: register, receive, post progress to the chat
channel, search and post memories, discover.

Every bus has two channels from the start, `general` (ordinary) and
`memory` (memory), recreated after `agentbus reset`.

## Claude Code (manual setup)

The harness's MCP server entry must be named `agentbus`, since the harness
prefixes that name onto every tool: tools appear as `mcp__agentbus__<tool>`
(`mcp__agentbus__send`, `mcp__agentbus__receive`, and so on).

`.mcp.json` in the repository, or the user-level MCP config:

```json
{ "mcpServers": { "agentbus": { "command": "agentbus", "args": ["mcp"] } } }
```

`.claude/settings.json` hooks: SessionStart so every session (startup,
`/clear`, resume) is told to register, and Stop so the session checks the bus
at the end of every turn (ADR 0012):

```json
{ "hooks": {
  "SessionStart": [ { "hooks": [ { "type": "command", "command": "agentbus identity" } ] } ],
  "Stop": [ { "hooks": [ { "type": "command", "command": "agentbus stop-hook" } ] } ]
} }
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
line in `AGENTS.md` or as a `$CODEX_HOME/hooks.json` SessionStart hook. The
Stop hook checks the bus at the end of every turn:

```json
{ "hooks": {
  "SessionStart": [ { "matcher": "startup|resume|clear", "hooks": [ { "type": "command", "command": "agentbus identity" } ] } ],
  "Stop": [ { "hooks": [ { "type": "command", "command": "agentbus stop-hook" } ] } ]
} }
```

Codex does not wake an idle session when a background command exits, so a
Codex session sees messages that arrive while it is idle at the end of its
next turn, not sooner.

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

## Grok Build (manual setup)

`~/.grok/config.toml` (what `grok mcp add --scope user agentbus -- agentbus mcp` writes):

```toml
[mcp_servers.agentbus]
command = "agentbus"
args = ["mcp"]
```

Leave `tool_timeout_sec` unset unless you have lowered the default of 6000
below `receive_max_wait_seconds`.

`~/.grok/hooks/agentbus.json` (files there are trusted without a prompt)
checks the bus at the end of every turn:

```json
{ "hooks": { "Stop": [ { "hooks": [ { "type": "command", "command": "agentbus stop-hook" } ] } ] } }
```

Grok ignores SessionStart hook stdout, so a SessionStart hook cannot deliver
the registration block. Install `~/.grok/rules/agentbus.md` instead:

```markdown
# Agentbus

At the start of a session, run `agentbus identity` and follow its output.
```

`init --global` writes that rule with the register instructions included.
The skill goes to `~/.grok/skills/using-agentbus/SKILL.md`. Inside a session,
`/agentbus init` comes from `~/.grok/commands/agentbus.md`.

## Operating

- `agentbus init` bootstraps a repository; `agentbus init --global`
  bootstraps the harnesses on this machine (see above).
- `agentbus version` prints the version. Release builds set it with
  `-ldflags "-X github.com/ericfitz/agentbus/internal/mcpserver.Version=<v>"`.
- `agentbus stop-hook` is the Stop hook `init --global` installs. It reads
  the hook's JSON from stdin (`cwd`, `stop_hook_active`/`stopHookActive`)
  and prints `{"decision":"block","reason":...}` when unseen messages are
  waiting for that directory's identity. Always exits 0.
- `agentbus wait` exits 0 when a message arrives, 1 on `-timeout`, 2 on an
  error, and 3 when a newer `agentbus wait` for the same identity replaced it
  (silently, no output). Only one wait runs per identity; its pid is recorded
  in `<data dir>/wait/<identity>.pid`, and a stale pidfile never blocks a new
  wait.
- `agentbus identity` prints the registration block (the register sentence
  plus the session protocol) for the current directory. It's also what the
  SessionStart hooks above run.
- `agentbus subscribe <channel>` / `agentbus unsubscribe <channel>` edit the
  persistent channel list in `.local/agentbus.json`. Run from the repository
  root; takes effect at the next register.
- The MCP server logs to `agentbus.log` in the data directory as JSON lines
  (one object per line with `time` in UTC RFC3339 milliseconds, `level`,
  `msg`, `pid`), rotated across four 16 MiB files.
- `agentbus status` shows live identities, channels, usage against budget,
  and any capacity notice.
- `agentbus tui` opens a live dashboard: channels with unread counts on the
  left with live sessions under them, the selected channel's stream in the
  center, a compose line, and a status bar. It registers as `tui_name` from
  the config (default: your OS user name; `--as <name>` overrides) and is an
  ordinary bus participant, so agents see your messages like any other.
  Press `esc` for the command keys and `?` for the full keymap. `tab` and
  `shift+tab` move between the rail (channels and sessions), the message
  list of the highlighted channel or session, and the compose line; `↓` past
  the last channel moves into the sessions and `↑` from the first session
  moves back; `home` returns to the channel list. Selecting a
  session shows its direct-message inbox (`dm/<name>`): the count beside a
  session is its unviewed direct messages, and the compose line there sends
  that identity a direct message. Your own row is your inbox; agents reach
  you at `dm/<tui_name>`. `/` searches, `h` opens
  health and config, `q` quits. Arrow keys never
  change pane: `↑`/`↓` move within the focused one, `→` opens the selected
  message's body if it's closed and has one, else shows its direct replies;
  `←` hides shown replies (the whole subtree), else closes the body. On a
  memory, `.` shows the next older version and `,` the
  next newer one, labeled `r<revision> · <n> kept`; any other key returns
  it to the latest. `<` and `>` narrow and widen the
  channel rail two columns at a time; the width is saved as `tui_rail_width`
  in the config file (0 or absent is a fifth of the screen), and is clamped
  to the terminal, keeping the rail at least 16 columns and the messages
  pane at least 40. `enter` replies to the selected message, or opens
  compose from the channel list. Colors come from the `theme` and `themes`
  config settings; see below. The first TUI launch after upgrading
  subscribes to every existing inbox from its oldest retained message, so
  retained direct messages show as unread once.
  A `tasks/` channel shows its task tree: subtasks start hidden under
  their parent (`N subtasks`), `→` opens a task's details, then its
  subtasks, `←` hides its subtasks, then its details; ❎ pending, ⏱️ in
  progress (with the owner's name beside it), ✅ completed (dimmed); a
  blocked task keeps ❎ with `blocked by #n`. You can change tasks that are
  unassigned or yours (the TUI's identity): `space` cycles the selected
  task's state (not started, in progress, completed) as a draft shown in
  the `unsaved` color, `enter` saves it (a move into in progress claims the
  task), `esc` cancels it; moving the cursor or leaving the pane discards
  it. `t` takes an unassigned task, `u` unassigns your not-started one, and
  `a` opens a picker of you and the live sessions and assigns the task to
  the one you choose (`esc` closes it without a change). A task owned by an
  agent cannot be changed here. From the channel or
  session list, `t` follows a tag set (a `tags` section lists yours; select
  one to see every chat message carrying all of its tags; `s` or `d` on it
  unfollows, no confirmation).
- `agentbus reset` deletes all bus data (messages, memories, channels,
  identities, cursors) after you type `yes` to confirm; configuration is
  kept. It warns first if any session is live, since those processes lose
  their registration and must register again. Message sequence numbers keep
  counting up across a reset rather than restarting at 1.
- Logs never go to stdout or stderr. The default data directory is
  `~/.local/share/agentbus`.

## TUI theme

Colors live in named themes. `themes` is an array of them and `theme` names
the one the TUI applies (default `default`). The built-in `default` theme
is always available unless the file defines its own entry of that name:

```json
{
  "theme": "night",
  "themes": [
    { "name": "night", "background": "black", "agent": "brightcyan", "user": "brightyellow" }
  ]
}
```

A value is one of the sixteen ANSI color names (`black`, `red`, `green`,
`yellow`, `blue`, `magenta`, `cyan`, `white`, or a `bright` form such as
`brightblack`), an index `0`-`15`, or `default` for the terminal's own
color. Matching is case-insensitive. Nothing here fails config load: a
`theme` name that is not in `themes`, a key a theme leaves out, and a value
that is not a color (including `#RRGGBB`) all fall back to the `default`
theme's value; bad values and unknown theme names print one line on stderr
before the TUI starts.

| Theme key | Used for | Default |
|-----------|----------|---------|
| `background` | screen background | `default` |
| `text` | message content | `default` |
| `dim` | dividers, help, summaries | `brightblack` |
| `timestamp` | message timestamps (unselected rows) | `brightblack` |
| `tag` | tag chip background (`default` falls back to dim `#tag` words) | `brightblack` |
| `agent` | agent names, selected channel, key hints | `cyan` |
| `user` | your own name | `yellow` |
| `memory` | memory channels and memory versions | `magenta` |
| `tasks` | task-list channels | `yellow` |
| `health` | live heartbeat dot, ok states | `green` |
| `warn` | warnings such as the text-only search badge | `yellow` |
| `error` | errors and the delete confirmation | `red` |
| `selection` | selected row background (focused pane) | `blue` |
| `selection_inactive` | selected rail row background while the messages or compose pane has focus | `brightblack` |
| `selection_text` | text of the selected rail row while its pane has focus | `black` |
| `unsaved` | a task row whose drafted state is not saved yet (`space` in a task list; `enter` saves) | `yellow` |

On the selected message row, dim text (timestamp, thread summary) switches
to the `text` color so it stays readable on `selection`.

Chip text is black on light `tag` colors (yellow, cyan, white, and bright
green/yellow/cyan/white) and bright white on the rest.

The health overlay (`h`) shows the log file path, the theme in use with
each resolved value, and the loaded config. `o` opens the config file in
`$VISUAL`, else `$EDITOR`, else `vi`, run through the shell so a value with
arguments or spaces works. A GUI editor in `$VISUAL` (for example
`code --wait`) opens in the background and the TUI stays live; the config
check shows when the editor exits (with `--wait`, when you close the tab).
A terminal editor (`vi`, `vim`, `nvim`, `nano`, `emacs`, `micro`, `hx`, and
similar), or one set only in `$EDITOR`, takes over the terminal until you
quit it.

## TUI icons

`icons` picks the icon set the TUI draws for channels, senders, task status,
and the other markers below; the default, `emoji`, needs no font setup and
is what every screenshot in this doc shows.

```json
{
  "icons": "nerdfont",
  "icon_map": { "chat": "\uf27a" }
}
```

| Value | What it does |
|-------|--------------|
| `emoji` | default; color emoji, no font setup |
| `nerdfont` | Nerd Font glyphs at the codepoints below |
| `custom` | `emoji`, overridden per-name by `icon_map` |

`icon_map` applies on top of whichever `icons` set is chosen (`emoji`,
`nerdfont`, or `custom`, itself just `emoji`); it maps any of the names
below to a glyph (a literal character, or a `\uXXXX` JSON escape -- JSON
has no `\U` escape, so a supplementary-plane glyph like `idle`'s needs a
UTF-16 surrogate pair, e.g. `"\udb81\udcb2"` for U+F04B2, or the literal
character pasted directly). An icon name `icon_map` doesn't set keeps its
base-set glyph. An unknown `icons` value, an unknown `icon_map` name, or an
empty `icon_map` value falls back to the base set for that icon and prints
one line on stderr before the TUI starts, the same way a bad theme value
does.

| Icon name | Used for | emoji | nerdfont (Nerd Fonts 3.x codepoint) |
|-----------|----------|-------|--------------------------------------|
| `chat` | chat channels | speech balloon | U+F27A fa-message |
| `memory` | memory channels | floppy disk | U+F0C7 fa-floppy-disk |
| `tasks` | task-list channels | clipboard | U+F0AE fa-list-check |
| `agent` | other senders and sessions | gear | U+EE0D fa-robot |
| `user` | your own name | adult | U+F007 fa-user |
| `idle` | an ended session, or a task channel past `task_idle_hours`, in the rail | sleeping symbol | U+F04B2 md-sleep |
| `task_pending` | a pending or blocked task | ❎ | U+F096 fa-square |
| `task_in_progress` | an in-progress task | stopwatch | U+F152 fa-square-caret-right |
| `task_completed` | a completed task | ✅ | U+F14A fa-square-check |
| `collapsed` | selected list item; a thread that can expand | ▶ | U+F0DA fa-caret-right |
| `expanded` | a thread whose replies are shown | ▼ | U+F0D7 fa-caret-down |
| `error` | the toast and overlay error prefix | ✗ | U+F06A fa-circle-exclamation |
| `arrow` | sender → recipient, a pending task's owner | → | U+F061 fa-arrow-right |

The health overlay (`h`) names the active set on an `icons ·` line.

### Using a Nerd Font

`nerdfont` needs a Nerd Font installed and selected as the terminal's font
for the codepoints above (agentbus does not install or download any font).
Install a **Mono** variant — the non-Mono build advances two columns for
these glyphs where agentbus's rail and task-row alignment assumes one, and
they'll look off by a column:

```
brew install --cask font-sauce-code-pro-nerd-font
```

Then point the terminal at it:

- **iTerm2**: Preferences → Profiles → Text → set "Non-ASCII Font" to the
  Nerd Font Mono (the main font can stay whatever you use for code).
- **kitty**: add a `symbol_map` line in `kitty.conf` covering the Private
  Use Area ranges above, pointing at the Nerd Font Mono.
- **WezTerm**: add the Nerd Font Mono to `font_fallback` in `wezterm.lua` (or
  set it as the whole `font` if you want it for code too).
- **Terminal.app**: its font fallback for these codepoints is unreliable;
  set the Nerd Font Mono as the profile's own font rather than relying on
  fallback.

### Font Awesome Pro

Three Font Awesome glyphs — `fa-face-sleeping` (idle), `fa-microchip-ai` and
`fa-user-robot` (agent alternatives) — are Pro-only and not in Nerd Fonts.
They're reachable only through `icon_map`, mapped to the codepoints of your
own licensed Pro font; agentbus ships no fonts, Free or Pro.

Nerd Fonts and Font Awesome Pro share some Private Use Area codepoints with
different glyphs at them (for example U+F46D is `oct-home` in Nerd Fonts,
not the Pro glyph some icon lists give it there). If your terminal's primary
font is a Nerd Font, a Pro codepoint that collides with one of its
assignments renders the Nerd Font's glyph instead of the Pro one, so check
each `custom` codepoint against the Nerd Font cmap before relying on it. Do
not redistribute a patched Pro font; that violates its license.

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
