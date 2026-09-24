## Message subjects

- `send` takes `subject`: an optional one-line summary (at most 200
  characters) shown as the message's title. A line break or a longer
  subject is refused with `validation`.
- Every payload carries `subject` when it is set: `receive`, `history`,
  `search` hits, `get_memory` and `memory_revisions`. A reply does not
  inherit its parent's subject.
- `edit_memory` takes `subject`: omit it to keep the memory's subject,
  pass `""` to clear it.
- Text search matches subjects, and memories embed subject and content
  together (existing embeddings are not recomputed).
- Task rows carry the task's subject in the same column; `task_create`
  and `task_update` are unchanged. ADR 0010.

## TUI

- Message rows collapse to the header plus one line: the subject, or the
  content's first line cut to the pane width. This applies to channels,
  DM inboxes, the merged DM pane and tag panes; the task pane is
  unchanged.
- `→` opens the cursor message's body (subject line and full content),
  then its direct replies; `←` hides its replies, then its body. `space`
  still toggles the replies only. `▶` marks anything hidden, `▼` an open
  body.
- Stepping memory versions (`.`/`>` and `,`/`<`) shows each version's
  subject and keeps the body open.
- Search hit rows show the subject when there is one.
- Compose is unchanged: messages sent from the TUI have no subject and
  show their first line.

## Upgrading

This release moves the database to **schema v6**. The first 1.8.0 process
to open the live bus migrates it (adds `messages.subject`, backfills task
rows, and rebuilds the full-text index once), and 1.7.x binaries then
refuse it.

1. `brew upgrade agentbus`
2. `agentbus init --global` (the skill text changed: it now asks agents to
   set `subject` on every send)
3. Restart every harness session and the TUI together, so no 1.7.x process
   keeps running against the upgraded database.

Existing non-task messages keep no subject and show their first line.
