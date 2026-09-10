# Persistent subscriptions, JSONL logs, and the session protocol — design

Status: design approved in chat by the user on 2026-09-09; spec pending user
review before planning.

## Human-made decisions (2026-09-09)

1. **Register subscribes.** The MCP `register` tool reads the repository's
   `.local/agentbus.json` and subscribes the session to its `channels` list.
   Persistence does not depend on the agent following instructions.
2. **Persistence lives in the repo config file**, not the global config and
   not the database. Subscriptions are keyed by identity, and identity is
   per repository.
3. **Both surfaces get a persistent path.** CLI `agentbus subscribe` and
   `agentbus unsubscribe` edit the repo file. The MCP `subscribe` and
   `unsubscribe` tools gain a `persistent` boolean that edits the same file
   in addition to the session action.
4. **Logs become JSONL** with UTC RFC3339 millisecond timestamps and a `pid`
   attribute. The file keeps the name `agentbus.log`.
5. **The hook text is prescriptive.** It tells the agent what to do, not what
   it may do, and the init prompt reuses the same text.

## Repository config file

`.local/agentbus.json` gains one optional key:

```json
{ "identity": "agentbus", "channels": ["general", "memory", "reviews"] }
```

- `channels` absent: the persistent list is `["general", "memory"]`.
- `channels` present and empty: the persistent list is empty.
- Duplicates are removed on write; order is preserved as written.
- Each entry must satisfy the bus's existing name rule (`bus.NameRule`, the
  same rule channels and identities already use). An invalid entry is reported to stderr by `agentbus
  identity`, and by register in `subscribe_failed`, and otherwise ignored.
- Unknown keys are preserved by every writer (read into `map[string]any`,
  modify, write back with two-space indent and a trailing newline).
- The file lookup is the existing upward walk from cwd in `internal/cli`:
  nearest `.local/agentbus.json`, stopping at the nearest `.git`. The walk
  and reader move to a small shared package (`internal/repoconfig`) so the
  CLI and the MCP server use one implementation. `readIdentityFile` in
  `internal/cli/identity.go` is replaced by a call into it.

## Register behavior

`register` resolves the repo file from the MCP server's working directory
(the same `os.Getwd` it already uses for the default context) and, after the
existing session and subscription bookkeeping, calls `Subscribe(as, ch,
"now")` for each channel in the persistent list.

- Applies on every register: fresh, resumed, `resume: false`, and subagent
  (`parent` set). One rule, no exceptions.
- An existing subscription is untouched. `Subscribe` already uses
  `INSERT OR IGNORE`, so the cursor does not move.
- A channel that does not exist is not created; its error message is
  reported and register still succeeds.
- The response gains two fields:

```json
{ "as": "agentbus", "resumed": false, "pending": [],
  "subscribed": ["general", "memory"],
  "subscribe_failed": { "reviews": "channel \"reviews\" does not exist" } }
```

`subscribed` lists every channel now subscribed from the persistent list,
including ones that were already subscribed. `subscribe_failed` is omitted
when empty.

Idle expiry, retention eviction, and the name-suffix collision rule are
unchanged. A `general` subscription that expired after 72 idle hours comes
back at the next register because it is in the persistent list; that is the
intended meaning of persistent.

## CLI commands

```
agentbus subscribe <channel>
agentbus unsubscribe <channel>
```

- Edit `<cwd>/.local/agentbus.json`. cwd must be a repository root (contain
  `.git`); otherwise print `agentbus: run this from the repository root` and
  exit 1. No upward walk: the file being edited should be unambiguous.
- If the file is missing, `subscribe` creates it with the identity
  `agentbus init` would have chosen (basename of cwd, validated by the name
  rule). `unsubscribe` on a missing file is an error.
- No `--persistent` or `--as` flags. The command only edits the file, so it
  is persistent by definition, and the file holds exactly one identity.
  Session-scoped subscriptions belong to the MCP tools; a CLI process cannot
  act on another process's session because the bus's owner check rejects it.
- `subscribe` on a file without a `channels` key writes
  `["general", "memory", "<channel>"]` so the defaults are not silently lost.
- `unsubscribe` on a file without a `channels` key writes the defaults minus
  the channel; unsubscribing a channel that is not listed is a no-op that
  prints the resulting list.
- Both print the resulting list on one line:
  `Agentbus: persistent channels for <identity>: general, memory, reviews`.
- Neither touches the running bus. The change takes effect at the next
  register. The output ends with that sentence so the user knows.
- Validation: the channel name rule; a rejected name is an error, exit 1.

## MCP tool changes

`subscribe` and `unsubscribe` gain `persistent` (boolean, default false).

- `subscribe` with `persistent: true` performs the session subscribe first;
  if that fails (for example the channel does not exist) nothing is written.
  Then it adds the channel to the repo file with the same rules as the CLI.
- `unsubscribe` with `persistent: true` performs the session unsubscribe,
  then removes the channel from the file.
- The repo file is resolved from the server's working directory, the same
  place register reads it.
- The result gains `"persistent": true` when the file was written, so the
  agent can tell the user.
- Tool descriptions are updated to mention the parameter and that persistent
  subscriptions are applied by register in later sessions.

## Logs

`OpenLog` in `internal/mcpserver/logfile.go` switches from the text handler
to `slog.NewJSONHandler` with a `ReplaceAttr` that rewrites the `time`
attribute to `t.UTC().Format("2006-01-02T15:04:05.000Z07:00")`, which
renders as `...Z`. The returned logger is `logger.With("pid", os.Getpid())`.

- File name, location, rotation size, keep count, and locking are unchanged.
- Keys are slog's defaults: `time`, `level`, `msg`, then attributes.
- Every existing log call site works unchanged; nothing else in the code
  formats log lines.
- `status` and `reset` keep their discard logger.

## Hook and init prompt text

`agentbus identity` prints the block below with NAME substituted. The init
prompt (`cli.InitPrompt`, also the `init` MCP prompt) embeds the same
protocol block after its setup steps, so there is one constant for the
protocol and two callers.

```
Agentbus: call the register tool now with the name parameter set to "NAME",
and pass the "as" value it returns on every later Agentbus call. Register
subscribes you to this repository's persistent channels (from
.local/agentbus.json; default: general for chat, memory for memories).
Then follow this protocol:
- Call receive right after registering, whenever you finish a task, and before
  you ask the user a question. Pass each batch's token as ack on your next
  receive.
- Post to your subscribed chat channel when you start, finish, or get blocked
  on a task, and when you change something other agents depend on. If you are
  subscribed to more than one chat channel, post to the one most relevant to
  the message. Reply to messages addressed to you.
- Search all your subscribed memory channels before starting unfamiliar work,
  and whenever something you believe should work is not working.
- Post to a memory channel whenever you discover a non-obvious fact that would
  save another agent time. Examples: "tool X does not honor --y; workaround is
  Z"; "the spec for feature A says B, but I verified with <test> that the
  correct behavior is C"; "to accomplish J, I tried K, L, and M, which failed;
  P worked."
- Call discover before assuming you are the only agent working.
```

The first line keeps the `Agentbus: ` prefix and the name so existing tests
that compare against `identityLine(name)` need only the new expected text.

## Testing

- `internal/repoconfig`: read with and without `channels`, empty list,
  invalid entry, unknown keys preserved across add and remove, dedupe.
- `internal/bus` or `internal/mcpserver` register: file with channels, file
  without the key, empty list, unknown channel reported in
  `subscribe_failed`, subagent gets the list, `resume: false` still applies
  the list, existing cursor untouched.
- MCP `subscribe`/`unsubscribe` with `persistent`: file written only after a
  successful session action; result carries `persistent: true`.
- CLI: `subscribe` creates the file when missing, errors outside a repo root, adds
  without duplicates, `unsubscribe` removes and is a no-op when absent, both
  refuse invalid names.
- Logs: one emitted line parses as JSON; `time` matches
  `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`; `pid` is present.
- Hook: output begins with the register sentence containing the quoted name
  and contains each protocol bullet; init prompt contains the same block.

## Out of scope

- Applying a CLI subscribe to a live session. Add if the next-register delay
  is a problem in practice.
- Auto-creating channels named in the persistent list.
- Global (non-repo) persistent channel lists.
- Live reload of the repo file; it is read at register and at each
  persistent tool call.
