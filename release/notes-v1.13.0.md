## Several sessions in one repository

When a second session works in the same repository, it registers as
`<name>2`. Its Stop hook, its SubagentStart hook and `agentbus wait`
without `-as` used to take the first session's name from the working
directory. As a result, the second session was woken by the first session's
messages, and its wait replaced the first session's wait.

Each `agentbus mcp` now records the harness process it serves on the
sessions it registers. The hooks and `agentbus wait` find their own harness
and use the session registered from it, falling back to the
working-directory name. A session that has not registered yet no longer
borrows a name that another live session holds (#29, ADR 0017).

## Embedding failures are visible

`embed ok` in the TUI used to reflect only the TUI's own search queries. The
embedding itself runs in an agentbus process in the background, and its
errors went only to that process's log, so the backlog could stay above zero
next to `ok`.

- Every embedding pass records its result. The status bar shows
  `embed failing`, and the `h` view and `agentbus status` show the error,
  when it happened, and which agentbus process hit it. A 401 says whether
  that process had no API key at all.
- A memory the endpoint rejects on its own, for example one too long for
  the model, used to fail every batch it was in, so the backlog never
  drained. It is now skipped and counted as `rejected`, with the
  endpoint's reason shown. An endpoint that rejects every input is reported
  as a configuration error instead. ADR 0018.

If your backlog stays above zero after upgrading, check the `h` view or
`agentbus status`. A 401 from a harness-started process usually means that
process can't see `embedding_api_key_env`; set `embedding_api_key_file`.

## Claude Code subagents

The SubagentStart hook from 1.12.0 never fired: Claude Code doesn't pass
the dispatch prompt to the hook. When the parent is registered, the hook
now gives every subagent a note: register if your prompt asks you to use
agentbus, otherwise ignore this (#24, ADR 0016).

## TUI

- The memory revision label is on the message's header line (#27).
- The sender icon is drawn in the sender's color everywhere, in every icon
  mode (#28).
- The messages pane title shows the channel type icon, in the channel
  type's color; a DM pane shows the agent or user icon (#30).
- Cut task rows end in `…`, like message rows. Cutting a message header or
  task row no longer leaves an icon's cursor escape behind, which drew the
  line a column wider than the pane.
- `<` and `>` step the saved rail width. Reloading a task list keeps your
  place and follows a growing tree.
- An empty config file can be saved to.

## Fixes

- `before=`/`after=` place a task correctly when its neighbors share a rank.
- An idle maintenance tick no longer takes a write lock for task lists, and
  `task_list` checks liveness in one query.
- Rename and other I/O failures are reported as `internal` errors.

## Upgrading

This release moves the database to **schema v9**. The first 1.13.0 process
to open it migrates it, and older binaries then refuse it.

1. `brew upgrade agentbus`
2. `agentbus init --global` (the skill text changed)
3. Restart every harness session and `agentbus tui` together, so no 1.12.x
   process keeps running against the upgraded database.
