# agentbus tui — design

Status: design approved by the user on 2026-09-08; implementation deferred to
a later session. The visual design lives on a Claude Design canvas:
https://claude.ai/code/artifact/6bcf9b52-d0d3-4727-a373-25ed6621c69f
(sources: `docs/design/tui/*.dc.html`, `canvas.json`).

## Human-made decisions (2026-09-08)

1. **Purpose: observe and participate.** A live dashboard first, with a
   compose line so the human can send messages and create memories.
2. **Data path: in-process bus (Option A).** The TUI opens the SQLite
   database through `internal/bus`, exactly as `status` and `reset` do, and
   is an ordinary bus participant (own owner token, heartbeat, tick).
   Rejected: MCP client over stdio (JSON round trips, no access to status or
   config); hybrid read-direct/write-MCP (two paths for one screen).
3. **Identity: fixed human name.** `tui_name` in config, default the OS user
   name, validated by the register name rule; `--as` overrides.
4. **Layout: chat client.** Left rail channels with unread counts and a
   memory marker; centre stream of the selected channel; right rail live
   sessions; compose line; status bar.
5. **Secondary views: search, memory browser, health/config.** Message
   detail deferred.
6. **Framework: Bubble Tea + Lip Gloss.**
7. **Theming: environment variables, ANSI colours only (v1).** Added
   2026-09-08 after the canvas review. One variable per colour role; see
   "Theming" below. Richer values (`#RGB`, `#RRGGBB`) may follow in a later
   version, so the parser must reject them with a clear message now rather
   than silently accept them.

## Behaviour

- Start: Register(tui_name, resume) → ListChannels → Subscribe to all →
  History(selected, last 200).
- Live: one goroutine long-polls Receive with ack and turns each batch into a
  Bubble Tea message; gaps render as a dim "n evicted" divider; unread is
  seq > last-seen per channel.
- StatusReport every 5 s feeds the status bar and the Health overlay.
- Enter sends to the current channel; on a memory channel Enter creates a
  memory; `r` sets reply_to.
- Search uses the MCP tool's modes; `semantic_unavailable` renders as an
  amber "text only" badge, never an error.
- Memories: list, revision detail, `e` edits via `$EDITOR`, `d` deletes with
  y/n confirm.
- Errors: one-line toast above the status bar for 5 s or until a key.
- Quit: Close() waits for background goroutines, as `mcp` does.

Keymap, colour rules, and the required config/bus additions are on the
canvas's "Design notes" artboard. Bus changes needed: none required;
optional `last tick` field on StatusReport.

## Theming

Colours come from environment variables read once at startup. Each role
maps to one variable; unset means the design default shown on the canvas.

| Variable                    | Role                                                        | Default        |
|-----------------------------|-------------------------------------------------------------|----------------|
| `AGENTBUS_TUI_BGCOLOR`      | screen background                                           | terminal default |
| `AGENTBUS_TUI_TEXTCOLOR`    | message content, default foreground                         | terminal default |
| `AGENTBUS_TUI_DIMCOLOR`     | timestamps, contexts, dividers, help text, panel borders    | bright black   |
| `AGENTBUS_TUI_AGENTCOLOR`   | agent sender names, selected channel, key hints, search border | cyan        |
| `AGENTBUS_TUI_USERCOLOR`    | the human's own name and messages                            | yellow         |
| `AGENTBUS_TUI_MEMCOLOR`     | memory channels (◆), memory hits, memories overlay border   | magenta        |
| `AGENTBUS_TUI_HEALTHCOLOR`  | live heartbeat dot, ok states, health overlay border        | green          |
| `AGENTBUS_TUI_WARNCOLOR`    | warnings: text-only fallback badge, capacity notice, idle dot | yellow       |
| `AGENTBUS_TUI_ERRORCOLOR`   | errors, error toast, delete confirmation                    | red            |
| `AGENTBUS_TUI_SELCOLOR`     | background of the selected row                              | bright black   |

Accepted values in v1: the sixteen ANSI colour names (`black`, `red`,
`green`, `yellow`, `blue`, `magenta`, `cyan`, `white`, and their `bright`
forms, e.g. `brightblack`), the integers `0`-`15`, and `default` (the
terminal's own colour, useful for `BGCOLOR` on transparent terminals).
Matching is case-insensitive. Anything else, including `#RGB` and
`#RRGGBB`, is rejected: the TUI prints one line to stderr naming the
variable and the accepted forms, then uses the default for that role and
continues. Lip Gloss takes ANSI indices directly, so the parser is a
name-to-index table plus the integer range check. No config-file keys for
colours in v1; the Health overlay lists the resolved theme under a
"theme" heading so a user can see what took effect.

## Out of scope for v1

Mouse support, multiple humans per TUI, in-place config editing, reset from
the TUI, truecolour or `#RRGGBB` theme values, per-colour config-file keys.
