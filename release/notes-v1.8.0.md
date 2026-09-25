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
  DM inboxes, the merged DM pane and tag panes.
- `→` opens the cursor message's body (subject line and full content),
  then its direct replies; `←` hides its replies, then its body. `▶`
  marks anything hidden, `▼` an open body. `space` no longer shows or
  hides replies; the arrow keys do that everywhere.
- Stepping memory versions (`.`/`>` and `,`/`<`) shows each version's
  subject and keeps the body open. The label is now `r<revision> · <n>
  kept`: the real revision number and how many revisions survive the
  tombstone purge.
- Search hit rows show the subject when there is one.
- Compose is unchanged: messages sent from the TUI have no subject and
  show their first line.
- Task lists nest like threads: subtasks start hidden under their parent
  with an `N subtasks` line; `→` opens a task's details, then its
  subtasks; `←` hides its subtasks, then its details. A search jump
  expands the target's ancestors.
- Task lists are no longer read-only. On a task that is unassigned or
  yours (the TUI's identity), `space` cycles the state (not started, in
  progress, completed) as a draft shown in the new `unsaved` color,
  `enter` saves it (into in progress claims the task, so the lease follows
  the TUI's heartbeat), `esc` cancels it; moving the cursor, leaving the
  pane, switching channels, or a new revision of the task on the bus
  discards it. `t` takes an unassigned task; `u` unassigns your
  not-started task. The bus's rules are authoritative: a refusal shows
  its message and keeps the draft. Tasks owned by agents cannot be changed
  from the TUI (ADR 0011).
- `t` follows a tag set only from the channel or session list. A blank
  line separates the channels from the `tags` section. `d` on a tag set
  unfollows it like `s`, with no confirmation, and the selection moves to
  the next set, else the previous, else the last channel.
- The rail's selected channel or session row uses the new theme key
  `selection_inactive` (default `brightblack`) while the messages or
  compose pane has focus, so only the focused pane's row is `selection`
  blue. New theme key `unsaved` (default `yellow`) colors a drafted task
  row.
- The `nerdfont` icon set's completed task glyph is U+F14A
  (`fa-square-check`), the same size as the pending and in-progress ones.

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
