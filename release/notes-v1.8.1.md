## TUI tag panes include DMs and memories

A tag set in the TUI rail showed only messages from chat channels, so a tag
used only in direct messages or memories showed an empty pane. A tag pane
now merges the matching messages of every chat, memory, and DM channel in
the rail (task lists are still excluded). Each tagged revision of a memory
shows as its own row.

Tag subscriptions that deliver to agents are unchanged: they still match
chat channels only. ADR 0009, amendment 2026-09-26.

## Upgrading

`brew upgrade agentbus`, then restart the TUI. No schema change; 1.8.0 and
1.8.1 processes can share the bus.
