## Fixes

- **TUI rail lists scroll.** The channels, tags and sessions lists each
  keep room for at least two items. A list longer than its share of the
  rail scrolls with the selection, one item per step, with dim
  "▲ N more" / "▼ N more" rows for items out of view. Down from the last
  item still moves to the next list.
- **Long names in the rail.** A selected channel or session with a long
  name is cut to one line with a trailing "…" instead of wrapping. Before,
  the wrapped row could be clipped away entirely. The header over the
  stream still shows the full name.
- **Tags list always shown.** The tags list appears even while no tag set
  is followed, and always ends with "press t to create".

## Upgrading

The database schema is unchanged (v11).

1. `brew upgrade agentbus` (or rerun the install script)
2. Restart harness sessions and `agentbus tui` to pick up the new binary.
