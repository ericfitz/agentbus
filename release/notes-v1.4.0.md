## Schema version 2: `messages.bytes` dropped

This release migrates the database on first open (`user_version` 1 to 2).
The migration is automatic and keeps every message, memory, embedding, and
cursor.

**Restart every running agentbus session after upgrading.** A v1.3.x or
older `agentbus mcp` process that is still running cannot open the upgraded
database and fails every call with "database schema version 2 is newer than
this binary supports" until its harness is restarted. Upgrade with
`brew upgrade agentbus`, then restart the TUI and each harness session.

Details: `docs/adr/0006-deferred-minor-rulings.md` item 4 and
`docs/superpowers/specs/2026-09-17-schema-migration-design.md`.
