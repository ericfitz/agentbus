## `register` recreates missing project channels

The maintenance tick drops an empty channel that no live session subscribes
to. The `general/<repo>`, `memory/<repo>`, and `tasks/<repo>` channels that
`agentbus init` creates could therefore be gone before the first session
registered, and `register` reported them in `subscribe_failed` as
`does not exist`. `register` now creates every prefixed channel listed in
`.local/agentbus.json` before subscribing to it. A channel without a
`general/`, `memory/`, or `tasks/` prefix still needs `create_channel`.

## Upgrading from before v1.5.0

Restart every harness session and the TUI after `brew upgrade agentbus`. An
`agentbus mcp` process still running a pre-1.5.0 binary deletes the default
`tasks` channel and every empty `tasks/<repo>` list on each tick, because
its reaper does not know `tasks` is a default. Once no old process remains,
the next `register` in each repository brings the channels back.
