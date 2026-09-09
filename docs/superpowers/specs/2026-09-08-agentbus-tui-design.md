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
   memory marker; center stream of the selected channel; right rail live
   sessions; compose line; status bar.
5. **Secondary views: search, memory browser, health/config.** Message
   detail deferred.
6. **Framework: Bubble Tea + Lip Gloss.**
7. **Theming: environment variables, ANSI colors only (v1).** Added
   2026-09-08 after the canvas review. One variable per color role; see
   "Theming" below. Richer values (`#RGB`, `#RRGGBB`) may follow in a later
   version, so the parser must reject them with a clear message now rather
   than silently accept them.

## Human-made decisions (2026-09-09, after the first TTY session)

8. **Colors are config settings; environment variables override.** Each
   color role is a `tui_*_color` key in the config file, named and validated
   like every other setting (snake_case, strict validation at load). The
   `AGENTBUS_TUI_*COLOR` variables stay and override the file for one run.
   Supersedes the "no config-file keys for colors" line of decision 7.
9. **`tab` cycles panes; `shift+tab` reverses; `home` returns to the
   channel list.** The panes are the channel list, the message list, and
   the compose line, in that order, wrapping. A pane with nothing to focus
   is skipped (the message list of an empty channel, compose with no
   channel selected). `home` always lands on the channel list so one key
   reaches a known state. Replaces `tab` = next unread channel.
10. **`?` opens a dedicated help overlay; `h` opens health.** Help lists the
    full keymap (including `shift+tab` and `home`, which the status bar
    does not mention); health no longer embeds the keymap.
11. **Key hints and placeholders are marked.** In status bars and overlay
    footers keys render in the agent color and descriptions dim. Text the
    user must supply is written as a placeholder, e.g. `<name> [memory]`.
12. **American English** in all code, comments, and documents.

## Behavior

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

Keymap, color rules, and the required config/bus additions are on the
canvas's "Design notes" artboard. Bus changes needed: none required;
optional `last tick` field on StatusReport.

## Theming

Colors are config settings (decision 8), read once at startup; each
`AGENTBUS_TUI_*COLOR` environment variable overrides its key for one run.
Unset means the design default shown on the canvas.

| Config key              | Environment override        | Role                                                        | Default        |
|-------------------------|-----------------------------|-------------------------------------------------------------|----------------|
| `tui_background_color`  | `AGENTBUS_TUI_BGCOLOR`      | screen background                                           | terminal default |
| `tui_text_color`        | `AGENTBUS_TUI_TEXTCOLOR`    | message content, default foreground                         | terminal default |
| `tui_dim_color`         | `AGENTBUS_TUI_DIMCOLOR`     | timestamps, contexts, dividers, help text, panel borders    | bright black   |
| `tui_agent_color`       | `AGENTBUS_TUI_AGENTCOLOR`   | agent sender names, selected channel, key hints, search border | cyan        |
| `tui_user_color`        | `AGENTBUS_TUI_USERCOLOR`    | the human's own name and messages                            | yellow         |
| `tui_memory_color`      | `AGENTBUS_TUI_MEMCOLOR`     | memory channels (◆), memory hits, memories overlay border   | magenta        |
| `tui_health_color`      | `AGENTBUS_TUI_HEALTHCOLOR`  | live heartbeat dot, ok states, health overlay border        | green          |
| `tui_warn_color`        | `AGENTBUS_TUI_WARNCOLOR`    | warnings: text-only fallback badge, capacity notice, idle dot | yellow       |
| `tui_error_color`       | `AGENTBUS_TUI_ERRORCOLOR`   | errors, error toast, delete confirmation                    | red            |
| `tui_selection_color`   | `AGENTBUS_TUI_SELCOLOR`     | background of the selected row                              | bright black   |

Accepted values in v1: the sixteen ANSI color names (`black`, `red`,
`green`, `yellow`, `blue`, `magenta`, `cyan`, `white`, and their `bright`
forms, e.g. `brightblack`), the integers `0`-`15`, and `default` (the
terminal's own color, useful for `BGCOLOR` on transparent terminals).
Matching is case-insensitive. Anything else, including `#RGB` and
`#RRGGBB`, is rejected: a bad config value fails config load like any
other setting; a bad environment variable makes the TUI print one line to
stderr naming the variable and the accepted forms, then keep the config
value and continue. Lip Gloss takes ANSI indices directly, so the parser is
a name-to-index table plus the integer range check. The Health overlay
lists the resolved theme under a "theme" heading, marking each value an
environment variable overrode, so a user can see what took effect.

## Out of scope for v1

Mouse support, multiple humans per TUI, in-place config editing, reset from
the TUI, truecolor or `#RRGGBB` theme values.
