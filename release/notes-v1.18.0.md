## Register no longer renames a session by accident

A subagent shares its parent's MCP process, so a subagent that registered
without `parent` used to take over the parent's name. Now a top-level
`register` under a new name, from a session that already holds a live
top-level name, is refused with a validation error that says to pass
`parent` (a subagent) or `rename: true` (a deliberate rename, which ends the
old name as before). Registering the same name again is unchanged (ADR 0025
decision 8).

## Every running TUI's name is protected

MCP `register` refuses any name a running `agentbus tui` holds, not only the
configured `tui_name`, so a TUI started with `-as` or another `-config` is
protected too (ADR 0025 decision 9).

## Breaking changes

- `register` under a second top-level name in one session needs
  `rename: true`.

## Upgrading

There is no schema change. Upgrade with `brew upgrade agentbus` (or `scoop
update agentbus`, or rerun the install script), then restart harness
sessions so they pick up the new `agentbus mcp`.
