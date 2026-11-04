## `agentbus uninstall`

`agentbus uninstall [--harness claude|codex|grok] [--dry-run]` undoes
`agentbus init --global`: for each harness it finds (or only the one named)
it removes the agentbus MCP entry through the harness's own CLI, the
agentbus hook commands (each edited hook file is backed up to `.bak` first),
the skill, prompt, command and rule files `init` wrote, and directories
those deletions leave empty. It leaves the bus database, key, config file,
every repository's `.local/` and running processes alone. `--dry-run` shows
what would be removed. See "Removing agentbus from a harness" in
docs/install.md.

## One character rule for one-line text

Every one-line text field now must be valid UTF-8 without control
characters, the line and paragraph separators U+2028 and U+2029, or bidi
formatting characters (U+202A-U+202E, U+2066-U+2069, U+200E, U+200F,
U+061C), which can make text display differently from what was sent. This
covers identity and channel names, `register`'s `parent` and `context`,
status text and problem, and message, memory and task subjects. Emoji
sequences joined with a zero-width joiner are still accepted. Harness
client names and versions are cleaned of these characters instead of
refused. See ADR 0027.

## Status follow-ups (#46)

- `agentbus statuses <name>` prints each history entry's revision and local
  time under a `REV TIME TAG TEXT PROBLEM` header.
- In the TUI, `o` no longer re-sorts the board while a status history is
  open, and the sessions rail shows every age in one format (m, h or d).

## TUI: selected agent rows stay aligned

In Terminal.app, a selected agent's session row, with the sessions pane
focused, drew its name one column left of the other rows: the selection
highlight dropped the escape sequences that keep the gear icon's width
right. They are kept now.

## Breaking changes

- A message subject containing a tab or any other control character is
  refused (before, only line breaks were).
- A name, channel name, `parent`, `context`, status or subject containing a
  bidi formatting character or U+2028/U+2029, or invalid UTF-8, is refused
  with a validation error.

## Windows packages

The Windows zips and `install.ps1` are in this release, but the Scoop bucket
and winget were not updated for 1.19.0. On Windows, install or upgrade with
`install.ps1` or the zip. `scoop update agentbus` stays on 1.18.0 for now.

## Upgrading

No schema change since 1.18.0. Upgrade with `brew upgrade agentbus` or the
install script, then restart harness sessions so they pick up the new
`agentbus mcp`.
