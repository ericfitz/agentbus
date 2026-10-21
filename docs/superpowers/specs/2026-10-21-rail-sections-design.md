# Rail sections, collapsing, and 14-day task expiry (v1.16.0)

Status: approved design, 2026-10-21. Decisions marked **(Eric)** were made by
the human; decisions marked **(builder's call)** were made by Claude while
writing this and may be revisited.

## Why

The TUI rail mixes chat, memory and task channels in one "channels" list.
With 30 channels on a real bus (6 chat, 19 memory, 4 task), the list is
mostly memory channels, and the `general/`, `memory/` and `tasks/` prefixes
repeat on every row. Eric wants to scan each kind on its own, fold away the
kinds he is not looking at, and see stale task lists disappear sooner.
Two bugs ship in the same release.

## Scope

1. Split the rail's channels list into three sections: **channels**,
   **memories**, **tasks**.
2. Make every rail section (channels, memories, tasks, tags, sessions)
   collapsible.
3. Hide the kind prefix inside each section.
4. Change the default `task_expiry_hours` from 720 (30 days) to 336 (14
   days). **(Eric)**
5. Fix: a selected idle session row (a DM peer shown with the idle icon)
   showed only the icon, without the name and age.
6. Compose: `down` clears the draft **(Eric)**, the counterpart of `up`
   recalling the last sent message into an empty draft. See section 6.
7. Release as **v1.16.0**, together with the already-committed fix that bare
   `agentbus` prints usage and exits 0 (edc2814). **(Eric)**

Non-goals: changing what a channel, memory or task list is; changing the
messages pane; persisting rail state; changing `task_idle_hours` (stays 168,
7 days); changing memory expiry.

## 1. Sections

Order, top to bottom **(builder's call, Eric did not object)**:
channels, memories, tasks, tags, sessions.

Membership (each channel row belongs to exactly one section):

- **tasks**: `bus.IsTaskChannel(name)` (the `tasks` / `tasks/...` channels).
  Note `list_channels` reports these with kind `memory`; classify by
  `IsTaskChannel` first.
- **memories**: memory-kind channels that are not task channels (`memory`,
  `memory/...`).
- **channels**: everything else that is in the rail's channel list today
  (ordinary channels; DMs stay in sessions as now).
- **tags** and **sessions**: unchanged membership.

Each section keeps today's list behavior where it fits: it shows at least
two items when the screen is too short for everything (the existing
`railBudgets` rule: `min(n,4)` rows each, then leftover rows round-robin),
scrolls with the `▲ N more` / `▼ N more` rows, and keeps its own scroll
offset (`railTop` grows from 3 to 5 entries).

Short screens **(Eric, option A)**: five sections at that floor need 25-29
rows, but an 80x24 terminal gives the rail 21. When the floor does not fit,
degrade in this order, and never clip a section off the bottom:

1. Drop the blank separator rows between sections (headings still separate
   them).
2. If still too tight, the per-section floor becomes one item plus one
   combined scroll row (`▲2 ▼5 more`, only the parts that apply) instead of
   two items with separate `▲`/`▼` rows. Leftover rows still go
   round-robin. Five sections then need at most 5 headings + 5 x 2 = 15
   rows (fewer with empty or collapsed sections).
3. Below that (a terminal too short even for step 2), clipping by
   `MaxHeight` remains the last resort, as today.

Layout tests cover 24 lines with Eric's real mix (6 channels, 19 memories,
4 tasks, 0 tags, several sessions) and show every section's heading and at
least one item.

Empty section: only its heading is shown, no item rows **(Eric)**. This
also applies to tags: the `press t to create` hint row goes away
**(builder's call; follows Eric's rule; `?` help still documents `t`)**.

The blank separator row between sections stays.

## 2. Collapsing

**(Eric approved the proposal; builder's call on the details marked.)**

- Each section heading becomes a selectable rail row. Up/down move through
  one sequence: heading, its visible items, next heading, ... across all
  five sections. Today's special cases (down past the last channel enters
  sessions; up from session 0 returns to channels) become the general rule.
- With a heading selected and the rail focused: `left` collapses that
  section, `right` expands it. **(builder's call)** `enter` on a heading
  toggles it.
- With an item row selected, `left` / `right` keep their current meaning
  (they act on the stream: thread or task expand/collapse).
- A collapsed section renders as one row: `▶ memories (19)`, where the
  number is the section's item count; when any item in it has unread
  messages, append the unread total, e.g. `▶ memories (19, 3 unread)`.
  **(builder's call)** An expanded heading renders `▼ memories`.
  Use the existing `markSel` (`▶`) and `markOpen` (`▼`) glyphs and their
  `icon_map` overrides.
- A collapsed section takes one row from the height budget; its other rows
  go to the expanded sections through `railBudgets`.
- Selecting a heading does not change what the stream shows: it keeps the
  last selected channel or session **(builder's call)**. Only the heading
  is highlighted (the "one blue row on screen" rule holds); the header and
  compose target stay on the shown channel; tab from the stream returns
  focus to the heading.
- If the selected item's section is collapsed, the selection moves to that
  section's heading.
- New messages or a new channel do not expand a collapsed section.
- Collapse state lives only in the running TUI; every start shows all
  sections expanded **(Eric)**.
- Tab, shift+tab and home keep their meaning; home selects the first
  heading (channels).

## 3. Prefixes

Within its section, a channel shows its name without the section's prefix
**(Eric)**:

| section | full name | shown |
|---|---|---|
| channels | `general/agentbus` | `agentbus` |
| channels | `general` | `general` |
| channels | `ops/deploys` (not under `general/`) | `ops/deploys` |
| memories | `memory/tmi` | `tmi` |
| memories | `memory` | `memory` |
| tasks | `tasks/aws-eip-logging` | `aws-eip-logging` |
| tasks | `tasks` | `tasks` |

Only the rail rows change. The stream title, compose target, search
results, help and every CLI/MCP surface keep full names.

Collisions: the bus allows unprefixed names alongside prefixed ones (for
example ordinary `agentbus` and `general/agentbus`, or memory-kind `tmi`
and `memory/tmi`). When two rows in one section would show the same short
name, both show their full names **(builder's call)**.

## 4. Task expiry default

`config.Default().TaskExpiryHours` becomes 336. Users who set
`task_expiry_hours` explicitly keep their value. Upgrade consequence to
state in the release notes: on the first maintenance tick after upgrade,
every task list idle for more than 14 days is deleted with its tasks (on
Eric's bus today: `tasks/aws-eip-logging`, `tasks/version-bump-bot`).
Update README.md, docs/install.md, and record the change in ADR 0013 (or a
short new ADR that supersedes its default) as a human decision.

## 5. Idle session row bug

Symptom (Eric, 2026-10-20): a DM peer that used only CLI calls showed in
sessions as idle-icon + name. Selecting it left only the idle icon;
selecting another row restored the name. Eric's `icon_map` overrides the
idle icon with a Nerd Font glyph (`󰒲`).

Known path: unselected rows are `cut(" "+line, ...)`; the selected row is
`th.RailSel(focused, cut(markSel+line, ...), rail)`, and `RailSel` strips
ANSI and renders through lipgloss `Width`, which word-wraps. A wrap after
the icon would push `name age` onto a second line that the rail's
`MaxHeight` clips. Root cause is not yet confirmed: the fix starts with a
failing test that reproduces it (with a Nerd Font idle override and the
default icons), per systematic debugging. Whatever the cause, the selected
row must never wrap: add a regression test that every selected row type
(channel, memory, task, tag, live session, idle session, heading) renders
as exactly one line at narrow and normal rail widths, with default and
Nerd Font icon maps.

## 6. Compose down clears the draft

In the compose pane, `up` on an empty draft fills in the last sent message
(`m.lastSent`), so `enter` resends it. `down` clears the draft (same effect
as `ctrl+u`) **(Eric)**. The compose box is a multi-line textarea
(`alt+enter` inserts a newline), so `down` clears only when the cursor is
on the draft's last line; on an earlier line it moves the cursor down as
today **(builder's call: keeps multi-line editing usable)**. An empty
draft stays empty. Update the help text.

## Docs to update

help.go (rail keys, headings, collapse), docs/install.md rail sections,
README.md rail description and task expiry default, a new ADR recording
the rail sections, collapsing, short-screen rule and 14-day expiry as
Eric's decisions (no existing ADR covers rail layout; ADR 0013 gets a
"default changed by ADR 00NN" note), comments in view.go/model.go that
name "three lists". The README screenshot can be refreshed after the
release.

## Where the work stops

The plan ends with `make verify` green, the version bump and
`release/notes-v1.16.0.md` committed on main, unpushed. Pushing and running
`release/release.sh` (signing, notarization, publishing) are outward-facing
and wait for Eric's explicit go-ahead **(builder's call)**. The winget
pre-flight script and the PR #448498 update are a separate effort after the
release.

## Left to the builder

Internal data structures (how sections, headings and offsets are
represented), refactoring of `railSpans`/`renderRails`, test layout, and
whether `railBudgets` reserves the heading rows as fixed rows or counts them
per section.
