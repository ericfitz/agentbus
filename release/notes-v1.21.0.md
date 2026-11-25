## Breaking: `init` is split into `install` and `config`

- `agentbus install` replaces `agentbus init --global`: run it once per
  machine to configure Claude Code, Codex and Grok Build.
- `agentbus config` replaces the per-repository `agentbus init`: run it
  inside each repository.
- `agentbus init` now exits 2 with a message naming both replacements.
- The MCP prompt is renamed from `init` to `config`: `/agentbus:config` in
  Claude Code, `/prompts:agentbus config` in Codex, `/agentbus config` in
  Grok Build.
- New: `agentbus config --remove` (also `--dry-run`) deletes the
  repository's identity file, `.local/agentbus.json`, and nothing else.
- New: `agentbus uninstall --purge` also deletes the config file and the
  data directory (the shared bus: every repository's messages, memories and
  task lists). It refuses while any session is live or when it cannot
  verify that none is, and otherwise asks for a typed `yes`. `uninstall`
  ends by printing where the binary is, for you to delete.

## Fixes

- `agentbus wait` no longer exits 4 ("harness gone") after a single failed
  process-table lookup; it keeps watching and exits 4 only when the harness
  process has really exited or been replaced.
- TUI task lists: a pending task is marked ⬜ instead of ❎, which read as
  "failed" beside the green ✅. Nerd Font mode is unchanged.
- TUI task lists: the in-progress ⏱️ mark no longer shifts its row one
  column left in Terminal.app.
- Built with Go 1.27.4 (fixes GO-2026-6859 in `net/http`), with updated
  dependencies, including SQLite (`modernc.org/sqlite` v1.63.0) and the MCP
  Go SDK v1.8.0.

## Windows packages

The Windows zips and `install.ps1` are in this release, and the Scoop
bucket is updated. This version is submitted to winget
(`ericfitz.agentbus`); it reaches `winget upgrade` after Microsoft's
review, which has taken about two weeks.

## Upgrading

No schema change since 1.20.0. Upgrade with `brew upgrade agentbus`,
`winget upgrade ericfitz.agentbus` or the install script, then run
`agentbus install` to refresh the skill, hooks and prompts, and restart
your harnesses and the TUI.
