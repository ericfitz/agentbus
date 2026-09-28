## TUI: → scrolls what it reveals into view

Pressing → on a collapsed message whose header was the last row of the
messages pane opened the message's body or replies below the visible
window, leaving only the header on screen. The pane now scrolls up just
enough to show the whole opened block, keeping the header visible when the
block is taller than the pane. Task lists get the same fix for subtasks and
task details.

## Upgrading

`brew upgrade agentbus`, then restart `agentbus tui`. No schema change.
