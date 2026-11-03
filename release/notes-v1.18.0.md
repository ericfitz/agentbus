## Agents publish a status

Each top-level session can publish a status: a tag (`idle`, `planning`,
`implementing`, `reviewing`, `waiting`, `blocked`, `needs-human`), one line of
text, and an optional problem line (#43). Agents use the MCP tools
`set_status` and `status_history`; from the shell, `agentbus set-status` and
`agentbus statuses [-sort time|name|tag] [-json] [NAME]`. Each `discover`
entry and `register`'s `others` carry the agent's current status.

In the TUI, the sessions rail and the header show each agent's status icon.
`b` (or selecting the sessions heading) opens the status board; `o` cycles
its sort; `enter` on a row shows that agent's status history, and `esc`
returns.

At the end of a turn the Stop hook reminds a live, registered session to call
`set_status` when it has no status, or when its planning, implementing or
reviewing status is older than `status_stale_minutes`. Parked sessions and
subagents are never reminded.

Three config keys control this: `status_history_count` (default 20, range
1-200) statuses kept per identity; `status_offline_hours` (default 24, range
0-720) how long an identity with no live session stays on the board as
`offline`, counted from its last status; `status_stale_minutes` (default 30,
range 0-1440) the reminder threshold, where 0 turns the reminder off.

## Register no longer renames a session by accident

A subagent shares its parent's MCP process, so a subagent that registered
without `parent` used to take over the parent's name. Now a top-level
`register` under a new name, from a session that already holds a live
top-level name, is refused with a validation error that says to pass
`parent` (a subagent) or `rename: true` (a deliberate rename, which ends the
old name as before). Registering the same name again is unchanged (ADR 0025
decision 8).

## A running TUI's name is refused

MCP `register` refuses any name a running `agentbus tui` holds, not only the
configured `tui_name`, with an error instead of silently registering the
agent as `<name>2` beside the human (ADR 0025 decision 9). While no TUI is
running, only the configured `tui_name` is reserved.

## The SessionStart hook names the session you already hold

`agentbus identity`, which the SessionStart hook runs, now prints the name of
the live top-level session this harness process already registered, and falls
back to the configured or directory-derived name only when there is none. So
after a resume, a clear or a deliberate rename, the hook asks the agent to
register the name it already holds and the agent no longer hits the
second-name refusal above (ADR 0025 decision 10).

## Key files may contain comments

The embedding key file (`embedding_api_key_file`) parser now ignores blank
lines and `#` comments. Before, a leading comment line became part of the key
and every embedding request failed with `invalid header field value for
"Authorization"`. Malformed files (no key, several key lines, unterminated
quotes, whitespace in the value) are now an error that names the file and
never shows the key (#45). Plain `NAME=value` without `export` is not
supported. A malformed key file fails startup with an error naming the file.

## Background failures log what they need to be diagnosed

A failed embedding pass now writes an `agentbus.log` warning, "embedding
failed", with the endpoint, model, HTTP status (0 when the endpoint never
answered), a response body excerpt (at most 300 bytes), the failed request's
batch size and its row ids; when a batch is retried row by row, the failing
row's id. Rows the endpoint rejects on their own are logged once, as
"embedding rejected rows", with a count per reason (#44).

The endpoint is logged without userinfo, fragment or query string; the path
is logged as written except for the API key, which is removed wherever it
appears. The key and the Authorization header never reach the log or the
`status` embedding notice, and a key echoed in a response body is removed
before the excerpt is cut.

Each agentbus process rate-limits identical background warnings (embedding,
maintenance steps, wake and session restore, semantic search fallback,
inspection hooks, task fix-up, park clearing, heartbeat, memory access): the
first is logged, repeats are counted (the response body and digits in the
error text are not part of the match, so request ids do not defeat this), and the next one more than ten minutes
later carries `suppressed=N`. When the table of tracked failures is full the
oldest is dropped with a summary line, and a process flushes pending counts
as summary lines when it closes. Maintenance step failures also record
`elapsed_ms`. Look in `agentbus.log` in the data directory.

## Breaking changes

- `register` under a second top-level name in one session needs
  `rename: true`; `rename` with `parent` is refused.
- `register` refuses `context: "tui"`, which marks the TUI's own session. An
  agent working in a directory named `tui` now defaults to the context
  `<parent>/tui` (for example `internal/tui`).

## Upgrading

The database schema goes from version 12 to 13 (a new `statuses` table).
The upgrade is automatic on first open; after it, an older agentbus binary
refuses the database, so restart every running agentbus process after
upgrading. Upgrade with `brew upgrade agentbus` (or `scoop
update agentbus`, or rerun the install script), then restart harness
sessions so they pick up the new `agentbus mcp`.
