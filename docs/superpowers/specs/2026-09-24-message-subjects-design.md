# Message subjects: design

Date: 2026-09-24. Status: approved in conversation, spec awaiting review.
Decisions: ADR 0010.

## Goal

Message panes in the TUI are easier to scan. Every row shows a one-line
subject, and the expand key shows the body. Every message kind gets the
field: chat, direct messages, memories and task-list rows. Tasks already
have `subject`, so the name is the same everywhere.

## Non-goals

- A receive mode that returns subjects without bodies. `receive` still
  returns full content.
- A subject input in the TUI compose line.
- A key that expands every row at once.
- Backfilling subjects for existing non-task messages.

## Data and API

### Schema v6

- `messages.subject TEXT NOT NULL DEFAULT ''`, added with `ALTER TABLE`.
- Task rows are backfilled:
  `UPDATE messages SET subject = COALESCE(json_extract(content,'$.subject'),'')`
  for rows in task channels (`channel = 'tasks' OR channel LIKE 'tasks/%'`,
  the same predicate as `bus.IsTaskChannel`).
- After the backfill, `messages_fts` is dropped and recreated as
  `fts5(subject, content, content='messages', content_rowid='seq')`, and
  its two triggers (`messages_ai`, `messages_ad`) are recreated to carry
  both columns. The migration then runs
  `INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`, which also
  indexes the backfilled subjects. No update trigger is needed: message
  rows are never rewritten after the migration (an edit inserts a new
  revision row).
- Other existing rows keep `''`. Existing embeddings are not recomputed.
- The migration test covers v5 to v6 and the v2 to latest chain.

### Validation

`bus.Send` and `bus.EditMemory` normalize the subject:

- Surrounding whitespace is trimmed, and an empty result means no subject.
- A line break (`\n` or `\r`) is an error.
- More than 200 characters (runes) is an error.

Errors use `validation` and name the `subject` field, the same as the
tag checks.

### Payloads

`bus.Message` gets `Subject string` with `json:"subject,omitempty"`.
`messageCols` selects it, so it appears on receive, history, search hits,
`memory_revisions` and `get_memory` with no other changes.

### Writes

- `SendInput.Subject string`. A reply does not inherit its parent's
  subject.
- `EditInput.Subject *string`: nil keeps the current revision's subject,
  and a pointer to `""` clears it. This is the same shape as
  `TaskPatch.Subject`.
- `task_create` and `task_update` keep their current API. Each revision row
  they write also stores the task's subject in the `subject` column.
- Embeddings embed `subject + "\n\n" + content` when there is a subject,
  otherwise `content`.

### MCP

- The `send` schema lists `subject`: an optional one-line summary shown as
  the message's title.
- The `edit_memory` schema lists `subject` as optional. Omitted keeps it,
  and `""` clears it.
- The protocol text (register instructions and the using-agentbus skill)
  adds: set `subject` to a one-line summary of the message.

## TUI display

### Row line

Every message pane collapses by default: channel streams, DM inboxes, the
merged DM pane and tag panes. A collapsed row is:

1. The header line, as it is today.
2. The row line: `subject`, or when that's empty, the content's first line.
   It's cut to the pane width with `…` and never wraps. Tag chips and the
   memory `rN` label follow it, as they do now.
3. The reply summary line (`3 replies · 14:05`), when replies are hidden,
   as it is today.

The task pane is unchanged, since it already shows subjects.

### Has a body

A row has a body when the content holds something the row line doesn't
show. That is when:

- a subject is set and the content isn't empty, or
- there's no subject and the content runs past its first line, or its
  first line was cut to fit.

A one-line message with no subject that fits the width has no body, so it
has nothing to expand.

### Expand state

`bodyOpen map[int64]bool`, keyed by seq, sits beside the existing
`expanded` map for replies. Both are per-session and kept in memory only.
New arrivals, the TUI's own sends included, start collapsed.

### Keys

- `→` opens the body if it's closed and the row has one. Otherwise it opens
  the direct replies, as it does today.
- `←` closes the replies (the whole subtree, as it does today) if any are
  open. Otherwise it closes the body.
- `space` still toggles the replies only.

Reply rows behave the same way.

### Markers

`▶` shows when anything is hidden, body or replies. `▼` shows when the body
is open. These are the same glyphs as today.

### An opened body

An opened body shows the subject line (only when a subject is set), then
the full content, wrapped as it is today.

### Memory versions

`.`/`>` and `,`/`<` show the selected version's subject, and in an open
body its content. A body that's open stays open while you step between
versions.

### Search overlay

Hit rows show the subject when there is one, otherwise the snippet as they
do now.

### Compose

Compose is unchanged. Messages sent from the TUI have no subject and show
their first line.

## Errors

- A bad subject is refused before anything is written, with
  `validation` naming `subject`.
- An `edit_memory` on a memory with no subject leaves it empty unless the
  edit sets one.

## Testing

Bus:

- A subject round-trips through send, receive, history, search,
  `memory_revisions` and `get_memory`.
- FTS finds a word that appears only in the subject.
- `edit_memory` keeps, changes and clears the subject.
- Task create and update write the subject into the row.
- The validation errors: a line break, 201 runes.
- Migration v5 to v6: the column exists, FTS is rebuilt and still finds old
  content, and task rows are backfilled.

MCP:

- The `send` and `edit_memory` schemas list `subject`.
- The protocol text mentions `subject`.

TUI:

- A collapsed row shows the subject, or the first line when there's none.
- `→` opens the body, then the replies. `←` closes the replies, then the
  body.
- No `▶` on a one-line message with no subject.
- Stepping memory versions with the body open.
- The merged DM pane and tag panes collapse too.
- Search rows show the subject.

## Rollout

- Release 1.8.0 with schema v6. The first 1.8.0 process to open the live bus
  migrates it, and 1.7.x binaries then refuse it.
- The release notes say:
  - upgrade and restart every host and the TUI together;
  - run `agentbus init --global`, because the skill text changed;
  - `send` and `edit_memory` take `subject`, and payloads carry it.
