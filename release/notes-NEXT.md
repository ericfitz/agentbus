## Rename this file before releasing

`release.sh` reads `release/notes-<tag>.md` (the tag includes the `v`, for
example `notes-v1.17.0.md`) and falls back to GitHub's generated notes when
that file is missing. The release cutter must `git mv release/notes-NEXT.md
release/notes-vX.Y.Z.md` and delete this section.

## Park a session so only direct messages wake it

`agentbus park` marks a session parked: until it is unparked, `wait`,
`watch` and the Stop hook wake it only for direct messages; every direct
message wakes it, whatever `-filter` says (ADR 0025). Run it as the last
agentbus action of a handoff, after the final send or memory write. A new
prompt (the `UserPromptSubmit` hook runs `agentbus unpark`), `register`, or
any MCP tool call except `receive` ends the park, and so does
`agentbus unpark`. `park -as <name>` refuses a name with no live session
(exit 2). `agentbus reset` clears every park.

## Sessions in a home directory are named `<user>-home`

A session started in the user's home directory registers as `<user>-home`
instead of the bare user name, so it can no longer take the name of the TUI
(`tui_name`).

## One background wait on every harness but Codex

Claude Code now runs a background `agentbus wait` like Grok does, instead of
`agentbus watch` under Monitor. The protocol text, the Stop hook and the
subagent hook all say so. `agentbus watch` is deprecated but still works.

## Breaking changes

- Commands reject stray positional arguments (`agentbus identity Sam`, `wait
  Pat`) with `unexpected argument` and exit 2 instead of ignoring them. The
  hooks (`stop-hook`, `subagent-hook`, `unpark`) report it but still exit 0.
- `subscribe` (CLI and MCP) refuses memory channels. Agents never receive
  memory channels; `register` returns them in `memory_channels` for
  `search` and drops any memory subscription the identity still holds.
- A new top-level `register` ends the process's previous top-level name (a
  rename). Its subscriptions stay, so registering the old name again
  resumes it, and tasks it claimed are reclaimed. A subagent must pass
  `parent`.
- `register` refuses `tui_name` over MCP; pick another name (the error
  suggests `<name>-home`).
- `-filter @<name>` is gone from the agent protocol. Plain `wait` with no
  filter is the protocol.

## Fixes

- The TUI rail keeps one highlighted row when the last session ages out
  while the sessions pane is selected and the channel the selection returns
  to sits in a collapsed section.

## Upgrading

There is no schema change. Upgrade with `brew upgrade agentbus` (or `scoop
update agentbus`, or rerun the install script).

**Run `agentbus init --global` again on every host.** It installs the
`UserPromptSubmit` hook that runs `agentbus unpark`. Without it, a parked
session stays parked until it registers or makes an MCP tool call, and
`agentbus status` does not tell you the hook is missing. Restart running
sessions so they pick up the new `agentbus mcp`.
