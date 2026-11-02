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

## Key files may contain comments

The embedding key file (`embedding_api_key_file`) parser now ignores blank
lines and `#` comments. Before, a leading comment line became part of the key
and every embedding request failed with `invalid header field value for
"Authorization"`. Malformed files (no key, several key lines, unterminated
quotes, whitespace in the value) are now an error that names the file and
never shows the key (#45). Plain `NAME=value` without `export` is not
supported. A malformed key file fails startup with an error naming the file.

## Background failures log what they need to be diagnosed

A failed embedding pass now writes one `agentbus.log` warning, "embedding
failed", with the endpoint, model, HTTP status, a response body excerpt (at
most 300 bytes), the request's batch size and the affected row ids (#44).
Rows the endpoint rejects on their own are logged once as "embedding rejected
rows". The API key, the Authorization header, endpoint userinfo and endpoint
query values never appear in the log or in the `status` embedding notice; an
echoed key in a response body is removed before the excerpt is cut.

Identical repeated failures (embedding, maintenance steps, wake and session
restore, semantic search fallback) are logged once, then not again for ten
minutes; the next line carries `suppressed=N`, the number held back. Maintenance
step failures also record how long the step ran. Look in `agentbus.log` in the
data directory.

## Breaking changes

- `register` under a second top-level name in one session needs
  `rename: true`; `rename` with `parent` is refused.
- `register` refuses `context: "tui"`, which marks the TUI's own session. An
  agent working in a directory named `tui` now defaults to the context
  `<parent>/tui` (for example `internal/tui`).

## Upgrading

There is no schema change. Upgrade with `brew upgrade agentbus` (or `scoop
update agentbus`, or rerun the install script), then restart harness
sessions so they pick up the new `agentbus mcp`.
