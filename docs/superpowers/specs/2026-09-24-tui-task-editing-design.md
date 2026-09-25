# TUI task editing and walkthrough fixes: design

Date: 2026-09-24. Status: approved in conversation (sections 1-3); written
spec awaiting review. Human decisions: `docs/adr/0011-tui-task-editing.md`.
Follow-up: ericfitz/agentbus#19 (`a` assigns to another identity).

Five issues from the user's TTY walkthrough of message subjects. All
changes are TUI-only; the bus API, schema and task rules do not change.

## 1. Completed icon size

The `nerdfont` icon set's completed glyph U+F046 (`fa-check_square_o`) is
drawn at 480x480 units in the Mono-patched font, against 600x600 for the
pending (U+F096) and in-progress (U+F152) glyphs, so it looks smaller.

- The `nerdfont` set's completed icon becomes U+F14A (`fa-square_check`).
- The emoji set is unchanged. `icon_map` overrides still apply on top.

## 2. Memory version label

A memory's replaced revisions are purged by the routine tombstone purge
(72 h default), so the list of surviving revisions can start above r1.
The version stepper labels a version by its position in that list, so a
memory whose only surviving row is revision 2 reads `r1 of 1`, while the
row's own label correctly reads `r2`.

- While stepping, the indicator shows the real revision number and how
  many revisions are kept: `r<revision> · <n> kept` (e.g. `r2 · 1 kept`).
- The row's permanent `r<N>` label is unchanged.
- The purge is unchanged (human decision 5).

## 3. Selection versus focus color

One theme color, `selection`, marks the rail's selected channel, the
sessions pane's selected session, the stream cursor and the task cursor,
and none checks focus. With focus in the stream, the rail row and the
cursor row are both blue.

- New theme key `selection_inactive`, default `brightblack`.
- The channel rail's and the sessions pane's selected row use
  `selection_inactive` when that pane does not have focus (focus as
  `pane()` reports it), and `selection` when it does.
- The stream and task cursors are unchanged (they only show while focused).
- Documented with the other theme keys.

## 4. Arrow keys in message and task lists

### Message lists

`space` no longer shows or hides replies (human decision 4). `→` and `←`
keep their layered behavior: `→` opens the body, then shows direct replies;
`←` hides shown replies (the whole subtree), then closes the body. Help
text and `docs/install.md` drop `space` for message lists.

### Task lists

Task lists nest tasks under their `parent`. They get the same keys, with a
task's details (description and metadata) in place of the body and its
child tasks in place of replies:

- `→`: opens the details if closed and the task has any; otherwise shows
  the task's direct children.
- `←`: hides shown children (the whole subtree); otherwise closes the
  details.
- Children start collapsed: only top-level tasks show (human decision 3).
  A collapsed task with children shows its hidden-child count the way a
  collapsed thread shows hidden replies.
- Markers as in message lists: `▶` when anything is hidden (details or
  children), `▼` when something is open and nothing is hidden, blank
  otherwise.
- A jump into a task list (search result, link) expands the target's
  ancestors so the target row is visible.
- `space` is the state toggle (section 5), not a show/hide key.

`TaskSummary` already carries `Parent` and `Depth`; no bus change.

## 5. Changing task state from the TUI

Until now task lists were read-only in the TUI by design. The TUI now
edits task state and ownership through the existing task API as its own
identity (`cfg.TUIName`). The bus's rules stay authoritative.

### Who can change what

The bus does not record whether an identity is a person. The TUI treats
its own identity as "the user" and every other owner as an agent (human
decision 1). Only tasks that are unassigned or owned by the TUI identity
can be changed. On any other task, `space`, `t` and `u` show an error
toast: `owned by <name>; only unassigned tasks or yours can be changed
here`.

### `space`: cycle the state

- Cycles the cursor task's shown state: not started (`pending`) → in
  progress (`in_progress`) → completed (`completed`) → not started. It
  starts from whatever the row currently shows, including a draft.
- The change is a draft held only in the TUI. At most one task holds a
  draft at a time.
- While the draft differs from the saved task, the row's text uses the new
  theme color `unsaved` (default `yellow`), and a hint toast shows
  `enter saves · esc cancels`.
- On an unassigned task, the draft assigns the task to the TUI identity as
  soon as its state leaves not started; cycling back to not started drops
  that assignment again.
- A draft that cycles back to exactly the saved state (status and owner)
  is cleared, and the color returns to normal.

### `enter`: save

- Saves the draft, then clears it.
- A change to in progress is a claim (`TaskClaim`, as agents do, so the
  lease follows the TUI's heartbeat). Other changes are a `TaskUpdate`
  with the new status and, when it changed, the owner.
- If the bus refuses (for example open blockers), an error toast shows the
  bus's message and the draft stays, so `esc` can cancel it.

### `esc`: cancel

Clears the draft and restores the normal color. With no draft, `esc`
behaves as today.

### Losing a draft

A draft is discarded, with the hint toast `change discarded`, when:

- the cursor moves to another row, focus leaves the task pane, or the
  selected channel changes (human decision 2);
- a task-list refresh shows the drafted task's revision changed on the
  bus.

### `t`: take

On an unassigned task, assigns it to the TUI identity immediately (no
draft), without changing its state. In a task list, `t` takes over from
tag-following; `t` keeps its tag meaning in every other pane.

### `u`: unassign

On a not-started task owned by the TUI identity, clears the owner
immediately. On a task in any other state: error toast
`only a not-started task can be unassigned`.

While a draft is pending, `t` and `u` show the `enter saves · esc cancels`
hint instead of acting.

### Toasts

Today every toast is an error (red). Add a hint style using the normal
status-bar text color; errors stay red. Both keep the current lifetime and
dismissal.

### Removed

The `task lists are read-only` toast (and whatever keys raised it that now
act) goes away.

## Out of scope

- `a` (assign to another identity): #19.
- A user/agent kind on identities, or a configured list of humans.
- Keeping memory history longer than the purge window.

## Testing

TUI tests follow the fixture rules: no `tea.Sequence` in production code,
timers only via the package `tick` var, static cursors on text inputs.

- Icons: the `nerdfont` set's completed glyph is U+F14A.
- Memory label: with revision 1 purged, stepping shows `r2 · 1 kept`.
- Colors: rail selection renders with `selection_inactive` while the stream
  is focused and `selection` while the rail is focused.
- Message lists: `space` no longer changes reply visibility.
- Task tree: collapsed by default; `→`/`←` layering over details and
  children; markers; hidden-child count; a search jump expands ancestors.
- Task editing: the full cycle from each starting state; auto-assign on an
  unassigned task and its removal on cycling back; `unsaved` color;
  `enter` saves (claim for in progress); bus refusal keeps the draft; `esc`
  reverts; cursor move, focus change and a revision change discard; agent-
  owned tasks refused with the toast; `t` and `u` rules; `t` and `u`
  blocked while a draft exists.

## Rollout

TUI-only; no schema change. Ships in the next release with message
subjects (1.8.0). The release notes list the new keys, the `space` change
in message lists, and the new theme keys `selection_inactive` and
`unsaved`.
