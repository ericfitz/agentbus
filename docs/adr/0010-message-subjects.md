# ADR 0010: Message subjects

Status: Accepted 2026-09-24 (design; not yet implemented). Decided by the
user. Design: `docs/superpowers/specs/2026-09-24-message-subjects-design.md`.

## Context

TUI message panes show every message's full text, which makes busy
channels hard to scan. Tasks already have a `subject`. We want every
message to have one, and panes to show subjects until you expand a row.

## Human decisions

1. **The field is named `subject` everywhere.** It's the name tasks already
   use, so no renaming is needed.
2. **The subject is optional.** When a message has none, the TUI shows the
   content's first line. Existing messages are not backfilled, except task
   rows, whose subject is already in their content. Senders that don't set
   a subject keep working.
3. **The subject is stored in a `messages.subject` column (schema v6)**,
   not in `metadata` and not by treating the first line as a convention.
4. **Agents see it:** payloads return `subject`, full-text and semantic
   search cover it, and `edit_memory` can change it. A receive mode that
   returns subjects only was out of scope.
5. **The arrow keys are layered.** `→` opens the body, then the direct
   replies. `←` closes the replies, then the body. `space` still toggles
   the replies only.
6. **The TUI compose line has no subject input.** A message sent from the
   TUI shows its first line.

## Consequences

- Schema v6: 1.7.x binaries refuse the migrated bus, so every host upgrades
  and restarts together.
- The FTS table is rebuilt once during the migration.
- The protocol text changes, so `agentbus init --global` is needed.
