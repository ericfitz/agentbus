# ADR 0011: TUI task editing and walkthrough fixes

Status: Accepted 2026-09-24 (design; not yet implemented). Decided by the
user. Design: `docs/superpowers/specs/2026-09-24-tui-task-editing-design.md`.

## Context

The user's TTY walkthrough of message subjects found five issues: a
small completed icon, a confusing memory version label, one color used for
both selection and focus, task lists that don't expand like message lists,
and no way to change a task's state from the TUI (task lists were
read-only in the TUI by design).

## Human decisions

1. **Only the TUI's own identity counts as a user.** The bus has no
   user/agent kind. A task owned by anyone else is treated as owned by an
   agent and cannot be changed from the TUI. No `kind` field and no
   configured list of humans.
2. **One draft at a time, discarded on leaving.** `space` changes are a
   draft until `enter`; moving the cursor, leaving the pane or switching
   channels discards the draft with a toast.
3. **Task children start collapsed.** Task lists use the message lists'
   layered `→`/`←` keys over details, then children.
4. **`space` is no longer a show/hide key in message lists.** The arrow
   keys do that everywhere; in task lists `space` cycles the state.
5. **Memory version label only.** The stepper shows the real revision
   number and how many are kept; the tombstone purge of replaced memory
   revisions is unchanged.
6. **The TUI may write to task lists** (reversing the read-only rule),
   through the existing task API, with the bus's rules authoritative.
   Keys: `space` cycle, `enter` save, `esc` cancel, `t` take, `u`
   unassign. `a` (assign to another identity) is deferred to #19.
7. **Rail keys and layout.** `t` follows a tag set only with rail focus;
   a blank line precedes the `tags` label; `d` on a tag set unfollows it
   (no confirmation) and moves the selection to the next tag set, else the
   previous, else the last channel.
