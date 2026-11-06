## TUI: status board colors and layout

- Board rows draw names and icons the way the sessions rail does: other live
  agents in the agent color, your own identity in the user color, offline
  identities dim with a plain icon. The board's "sessions" header and the
  status history header's name take the same colors, and a focused selected
  row stays readable.
- Every board and status history column is at least two spaces from the next.
- The text column runs to the right margin when no agent has a problem or
  the problems are short: the problem column is as wide as the longest
  problem, capped at 40% of the room after the fixed columns.
- Humans get the person icon on the board instead of the agent gear: the
  TUI's own identity, and any live session whose context is `tui`. Messages
  from other TUI sessions show the person icon too.

## Windows packages

The Windows zips and `install.ps1` are in this release, but the Scoop bucket
and winget were not updated for 1.20.0. On Windows, install or upgrade with
`install.ps1` or the zip. `scoop update agentbus` stays on 1.18.0 for now.

## Upgrading

No schema change since 1.19.0. Upgrade with `brew upgrade agentbus` or the
install script, then restart the TUI.
