# Progress

What has been pushed to `origin/main`. Machine-local resume state lives in
the untracked `HANDOFF.md`.

## 2026-09-08: agentbus v2 implementation

Pushed to `main` (fast-forward of `docs/v2-design`, 49 commits):

- **v2 implementation** per
  `docs/superpowers/specs/2026-09-07-agentbus-local-design-v2.md` and the
  plan in `docs/superpowers/plans/2026-09-07-agentbus-v2.md`: SQLite-backed
  bus (`internal/bus`), MCP server (`internal/mcpserver`), CLI
  (`internal/cli`), config (`internal/config`). Direct dependencies are
  `modernc.org/sqlite` and `github.com/modelcontextprotocol/go-sdk` only;
  builds and tests under `CGO_ENABLED=0`, `-race` clean.
- **ADR 0003** (`docs/adr/0003-v2-implementation-deviations.md`): the 15
  implementation rulings, accepted by the user on 2026-09-08; supersedes the
  spec and ADR 0002 where they conflict.
- **Install docs** (`docs/install.md`) for Claude Code and Codex.
- **Tests**: unit suites per package plus multi-process stdio integration
  scenarios, including the spec-required scenarios the plan omitted
  (`internal/mcpserver/integration_more_test.go`).

## 2026-09-08 (later): rename, version, init

Pushed to `main`:

- Repository renamed to `ericfitz/agentbus` (module path
  `github.com/ericfitz/agentbus`); merged branch `docs/v2-design` deleted.
- `agentbus version`, an ldflags-settable version variable, and tag
  `v0.1.0`.
- `agentbus init` (repo identity; `--global` for harness bootstrap via
  `claude mcp add` / `codex mcp add`, hook merge, Codex prompt file) and an
  `init` MCP prompt that Claude Code exposes as `/agentbus:init`.
- Deferred review minors moved to
  `docs/superpowers/plans/2026-09-08-agentbus-v2-deferred-minors.md`.

## 2026-09-08 (later still): release tooling

Pushed to `main` and tagged `v0.1.1`:

- `release/release.sh`: builds a universal macOS binary, codesigns with the
  Developer ID, notarizes, creates the GitHub release, and renders
  `release/agentbus.rb.tmpl` into the `ericfitz/homebrew-tap` formula.
- `docs/install.md` documents `brew install ericfitz/tap/agentbus`.

- **v0.1.1 shipped** (2026-09-08): signed, notarized universal binary on the
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.1.1);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap`. Verified with
  `brew install ericfitz/tap/agentbus` and `brew test agentbus`.

## 2026-09-08 (evening): search fallback docs, query timeout, lint clean

Pushed to `main` (unreleased since v0.1.1):

- `search` tool description documents the text fallback and
  `semantic_unavailable`; new config key `embedding_query_timeout_seconds`
  (default 10, range 0.1-120) replaces the fixed query-embedding timeout.
- Three deferred runtime minors resolved (see the plan doc).
- TUI design spec `docs/superpowers/specs/2026-09-08-agentbus-tui-design.md`
  and canvas sources in `docs/design/tui/`.
- `golangci-lint run ./...` is clean: 227 findings fixed (224 errcheck, mostly
  `_ =` on discarded `Close`/`Rollback`/test setup returns; 3 staticcheck).
  No behavior change.

## 2026-09-09: agentbus tui

Pushed to `main`:

- `agentbus tui`: Bubble Tea dashboard per
  `docs/superpowers/specs/2026-09-08-agentbus-tui-design.md` — channel and
  session rails, stream with new/evicted dividers, compose with reply and
  memory creation, search / memories / health overlays, env-var ANSI theme.
- New config key `tui_name`; new bus read `MemoryRevisions`;
  `mcpserver.StartBackgroundLoops` exported for reuse.

## 2026-09-09: agentbus tui merged; v0.1.2 shipped

- `agentbus tui` (plan `docs/superpowers/plans/2026-09-08-agentbus-tui.md`)
  merged to `main` as ae861aa after subagent-driven implementation, per-task
  reviews, and a whole-branch review; the expired-subscription resubscribe
  defect the final review parked was fixed before merge (221253f).
- **v0.1.2 shipped**: signed, notarized universal binary on the
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.1.2);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (ba5b153).

## 2026-09-09: TUI fixes, idempotent register, default channels

Pushed to `main`:

- TUI: `tab`/`shift+tab` cycle panes (channels, messages, compose), `home`
  returns to channels; `?` opens a help overlay, `h` health; colors are
  `tui_*_color` config keys with `AGENTBUS_TUI_*COLOR` overrides; key hints
  render keys in the agent color. Decisions 8-12 in the TUI spec.
- `register` is idempotent within a process (no new suffix on re-register
  after `/clear`). ADR 0002.
- Default channels `general` and `memory` exist on every bus, recreated
  after `reset`; the identity hook line names them. ADR 0002.
- American spelling throughout.
- **v0.1.3 shipped** (2026-09-09): signed, notarized universal binary on the
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.1.3);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (879038e).
  Verified with `brew upgrade agentbus` (0.1.2 -> 0.1.3) and `brew test`.

## 2026-09-10: persistent subscriptions, JSONL logs, session protocol

Pushed to `main` (merge of `persistent-subscriptions`, 12 commits, 68d6fa6):

- **Spec** `docs/superpowers/specs/2026-09-09-persistent-subscriptions-design.md`
  and plan `docs/superpowers/plans/2026-09-09-persistent-subscriptions.md`.
- **`internal/repoconfig`** owns `.local/agentbus.json`: `identity` plus an
  optional `channels` list (absent = `general`, `memory`; empty = none).
- **`register`** subscribes the session to that list on every register and
  reports `subscribed` / `subscribe_failed`.
- **CLI** `agentbus subscribe <channel>` / `agentbus unsubscribe <channel>`
  edit the file from the repository root; MCP `subscribe` / `unsubscribe`
  gain `persistent: true`.
- **Log** `agentbus.log` is now JSON lines with UTC millisecond `time` and
  `pid`; rotation unchanged.
- **Hook / init prompt** print a prescriptive session protocol from one
  shared constant.
- Deferred minors (final review): `validation` code on repo-file I/O
  errors, 0644 vs 0600 file mode, non-atomic file write.

## 2026-09-10 (later): three follow-up fixes and v0.9.0 release

Pushed to `main`:

- `persistErr` reports repo-file I/O failures as `internal`/retryable
  (7b248f9); `.local/agentbus.json` written as 0644 like `agentbus init`
  (edec245) and atomically via temp file + rename (d4cd64a).
- **v0.9.0 released** (02ebc0b, tag v0.9.0):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.9.0);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (e183965).

## 2026-09-10 (later still): init project channels, TUI tweaks, v0.9.1 release

Pushed to `main`:

- `agentbus init` creates the repository's own `<identity>` and
  `<identity>-memory` channels on the bus (`bus.EnsureChannel`) and adds
  them to `.local/agentbus.json`; `init -config` (46b0d50).
- TUI: arrow keys replace j/k (up/down within the focused pane, right
  expands a channel, left collapses); health `o` fixed for an editor path
  with spaces and overlays now show error toasts; health shows the log
  path; colors are `theme` + `themes[]` in config, env overrides and the
  `tui_*_color` keys removed (19b7d1d).
- `.claude/skills/using-agentbus/SKILL.md`: channel-scope and posting
  guidance for agents (bd7320d).
- **v0.9.1 released** (ce683c0, tag v0.9.1):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.9.1);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (2e67c86).

## 2026-09-17 (night): task lists, TUI rail fix, deferred-minors sweep, v1.3.0 release

Pushed to `main`:

- **Task lists** (ericfitz/agentbus#2) per
  `docs/superpowers/specs/2026-09-17-task-lists-design.md`, plan
  `docs/superpowers/plans/2026-09-17-task-lists.md`, decisions in **ADR 0005**
  (`docs/adr/0005-task-lists.md`). A task list is a memory channel named
  `tasks/<name>`; a task is a memory whose content is JSON; no schema change.
  MCP tools `task_create`, `task_claim`, `task_release`, `task_update`,
  `task_get`, `task_list`. All rules live in `Bus.TaskUpdate`: atomic claim,
  owner guard with a recorded `force`, `blocked_by` dependencies with derived
  "blocked", parent hierarchy (no delete with subtasks), rank-key ordering
  with `before`/`after`, `leased_until` leases. Abandoned tasks (owner's
  session gone or lease expired) return to `pending` from update, get, list,
  and the maintenance tick. `send`, `edit_memory`, and `delete_memory` are
  refused on task lists; the embedder skips them.
- **TUI**: `tasks/` channels show a read-only task tree (indented subtasks,
  `○ ◐ ● ⊘` marks, owner, open blockers, lease time left). Urgent fix:
  channels and sessions are one rail (`↓` past the last channel enters the
  sessions, `↑` from the first session returns) and `tab` goes from the
  highlighted channel or session to its messages, then compose.
- **Guidance**: protocol text, the `using-agentbus` skill, README, and
  install docs cover task lists; `agentbus subscribe tasks/<name>` and
  persistent MCP subscribe accept task lists through one shared
  `bus.ChannelNameRule`.
- **Deferred-minors sweep**: every entry in
  `docs/superpowers/plans/2026-09-08-agentbus-v2-deferred-minors.md` and the
  v1.2.0 minors re-checked; fixes and the six items left for a human decision
  are recorded at the end of that file. `receive` now acks per readable
  subscription row (a batch token spans channels).
- Left open from the task-list reviews (minor): the tick takes the write lock
  once per task channel even when nothing is abandoned; one `sessions` query
  per in-progress task in `task_list`; the TUI reloads unselected task lists
  on every batch and does not scroll a growing tree while following;
  `before=x` with equal neighbor ranks (corrupt writes only) lands after `x`.
- Not yet done: a TTY walkthrough of the rail/tab fix and the task tree.
- **v1.3.0 released** (57d7cf2, tag v1.3.0):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.3.0);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (92ed09a).

## 2026-09-17 (later): direct messages, v1.2.0 release

Pushed to `main` (fast-forward of `direct-messages`, 14 commits) and released:

- **Direct messages** per
  `docs/superpowers/specs/2026-09-17-direct-messages-design.md` and ADR 0004
  (`docs/adr/0004-direct-messages-and-session-end.md`): every identity gets a
  one-way inbox channel `dm/<identity>` at register; `send` to `dm/<name>`;
  only the owner (and the in-process TUI observer) can read it, enforced at
  subscribe, history, search, and delivery; `agentbus wait` wakes on a direct
  message regardless of `-filter` and prints it without content; the name
  `dm` and `dm/...` are rejected for user and persistent channels. No schema
  change.
- **Human decision**: `resume=false` means set the cursor to HEAD: no backlog
  of any kind, inbox included (ADR 0004 human decision 11).
- **TUI**: inboxes are hidden from the channel list; the session list is a
  pane (tab cycle, arrows within), rows show unread direct-message counts,
  selecting a session shows its inbox, compose there sends a direct message,
  reply works only in your own inbox; selection is tracked by name.
- **Skill and hook text** document direct messages; the skill edit was
  retested with paper scenarios (results in ADR 0004).
- The whole-branch review found and fixed one Critical leak before merge:
  delivery paths did not re-check inbox readability, so whoever re-registered
  the TUI's freed name received every inbox.
- Deferred minors (test gaps for rail highlight and colors, `d`/`s` no-ops,
  empty-stream tab skip; comment cleanups; `receive` ack lacks a readability
  filter, affecting only a future TUI unread count).
- Not yet done: a TTY walkthrough of the sessions pane.
- **v1.2.0 released** (e6cd42f, tag v1.2.0):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.2.0);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (5883c83).

## 2026-09-17: channel colors, session end, direct-messages design

Pushed to `main` (9aa09dd, fast-forward):

- **TUI**: channel names carry their kind's color in the channel list and the
  stream header, as in the compose row (`chanStyle`).
- **Session end**: `Bus.EndSessions()`; the MCP server (stdin close or
  SIGTERM/SIGINT/SIGHUP) and the TUI (quit) delete their own session rows, so
  a name is free at once instead of after the 30-second expiry. Human
  decision: no harness hook.
- **Direct messages**: design
  (`docs/superpowers/specs/2026-09-17-direct-messages-design.md`) and plan
  (`docs/superpowers/plans/2026-09-17-direct-messages.md`). The
  implementation followed the same day; see the entry above.
- `using-agentbus` skill baseline test (writing-skills) done: five control
  and five with-skill paper runs; results in ADR 0004.

## 2026-09-16 (night): size display, embedding key env var, gear redraw, v1.1.0 release

Pushed to `main`:

- Database sizes under 1 MiB displayed as "0 MiB": `fmtBytes` gains a KiB
  tier and `agentbus status` prints one decimal. The usage query was correct.
  The gear agent icon now starts with CSI 2X so a gear redrawn onto a line
  that held a wide emoji does not keep the old glyph in the cell CSI 1C
  skips (73fe231).
- New config field `embedding_api_key_env` (human decision 2026-09-16): the
  named environment variable wins when set and non-empty;
  `embedding_api_key_file` is the fallback (a19d325).
- **v1.1.0 released** (2203ef4, tag v1.1.0):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.1.0);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (cccde0d).

## 2026-09-16 (later still): triangle markers, v1.0.5 release

Pushed to `main`:

- ▶ (U+25B6) marks the selected channel, memory, and search result and a
  message with hidden replies; ▼ (U+25BC) marks a message whose replies are
  shown. The ↳ reply prefix, › caret, and ▸ summary bullet are gone; every
  stream row has a two-cell marker column so bodies align (9be14fd).
- **v1.0.5 released** (716799e, tag v1.0.5):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.0.5);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (b024406).

## 2026-09-16 (late night): wrapped reply indentation, v1.0.4 release

Pushed to `main`:

- The reply prefix was part of the text handed to the wrapper, so wrapped
  lines of a reply started at column 0. The message now wraps in the width
  left after the prefix, and every line gets the prefix or a matching pad
  (ad681f2).
- **v1.0.4 released** (c7e22f3, tag v1.0.4):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.0.4);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (f8df08e).

## 2026-09-16 (night): arrows stay in pane, TUI exempt from polling guard, v1.0.3 release

Pushed to `main`:

- TUI normal mode: arrows never change pane. Up/down move within the
  focused pane; right shows the cursor message's direct replies; left hides
  its whole subtree. Enter replies to the cursor message in the stream and
  opens compose from the channel list. Tab/shift+tab/home move panes
  (5274c44).
- The TUI receive loop long-polls forever by design, but the bus counted
  its empty waits as agent polling and errored after three. A
  `ReceiveInput.NoPollGuard` flag, hidden from MCP, exempts it (5274c44).
- **v1.0.3 released** (f9e3b4e, tag v1.0.3):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.0.3);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (8b82728).

## 2026-09-16 (evening): gear icon restored, receive self-heals, v1.0.2 release

Pushed to `main`:

- Robot icon reverted: Source Code Pro draws its own U+1F916 glyph even
  with U+FE0F. The gear stays, followed by CSI 1C (cursor forward one), so
  the terminal's one-cell advance and lipgloss's two-cell count agree and
  the rail border lines up (462b4d7). Verified in Terminal.app.
- Only the MCP server runs `Tick`, so the v1.0.1 orphan sweep never ran in
  a TUI-only process. `receiveOnce` now deletes a subscription whose channel
  no longer exists and continues (cadc354^).
- **v1.0.2 released** (cadc354, tag v1.0.2):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.0.2);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (2f2ac83).

## 2026-09-16 (later again): orphaned subscriptions, robot icon, v1.0.1 release

Pushed to `main`:

- `reapEmptyChannels` left stale sessions' subscription rows behind when it
  dropped an empty channel; a resumed session then failed every receive with
  "internal: sql: no rows in result set" (the TUI toast). The reaper now
  deletes subscriptions to missing channels in the same transaction, so old
  databases heal on the next tick (1ceffcd^).
- Agent session icon is U+1F916 robot (with U+FE0F) instead of U+2699 gear:
  the gear is Neutral width, drawn two cells but advanced one, eating the
  following space and shifting the rail border. Not yet eyeballed in a TTY.
- **v1.0.1 released** (1ceffcd, tag v1.0.1):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.0.1);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (1b7cb9f).

## 2026-09-16 (later): TUI layout, threads, v1.0.0 release

Pushed to `main`:

- TUI layout: sessions under channels in one left column at 20% of the
  screen (16-column floor) with a vertical border; emoji rail icons with
  U+FE0F (chat, memory, agent, user, idle). Robot face was dropped because
  Source Code Pro ships its own glyph at U+1F916 (19d7191).
- Threaded stream: threads sort by newest message, `space` expands the
  direct replies of the cursor message, live replies peek under a collapsed
  thread, root summary line carries the thread time. Whole-row selection
  highlight (`Theme.Highlight`), default selection color blue, TUI starts on
  the channel list (e970462).
- **v1.0.0 released** (206985d, tag v1.0.0):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v1.0.0);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap`. The tagged source
  default of `Version` reads 0.13.0 (the binary reports 1.0.0 via ldflags);
  corrected on main right after.

## 2026-09-16: receive polling guard, register others, v0.12.0 release

Pushed to `main`:

- `receive` rejects `wait_seconds` above `receive_max_wait_seconds` (naming
  `agentbus wait`) instead of clamping, and three consecutive empty waited
  receives from one identity return a `polling` error until a message is
  delivered. `register` returns the other live identities in `others`. Tool
  descriptions, hook text, embedded skill, and ADR 0003 item 9 revised
  (ac18210).
- **v0.12.0 released** (fb99f2e, tag v0.12.0):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.12.0);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (7ac6fc8).

## 2026-09-15: channel deletion, v0.11.0 release

Pushed to `main`:

- Channel deletion (1ae7528): `bus.DeleteChannel` refuses default channels
  and channels with a live subscriber (error names the blockers), otherwise
  drops the channel and all its messages. `agentbus delete-channel [-y]`
  asks a permanent-loss y/N first; the TUI `d` key does the same with a
  confirm line. Maintenance reaps empty channels with no live subscriber.
  Design agreed in chat (no spec file).
- **v0.11.0 released** (0ad07b0, tag v0.11.0):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.11.0);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (a3d24ac).

## 2026-09-14: agentbus wait, v0.10.0 release

Pushed to `main`:

- `agentbus wait`: a read-only blocking CLI wait for new messages, so an
  agent can park a background shell instead of polling `receive` from model
  turns (98d2f3c). Requested by Eric via tmi-ux on the `agentbus` channel.
  Polls the database once per second; skill, protocol text, and the
  `receive` tool description tell agents how to use it (a850486).
- **v0.10.0 released** (2265571, tag v0.10.0):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.10.0);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (85131de).

## 2026-09-10 (evening): skill installed by init --global, v0.9.2 release

Pushed to `main`:

- The `using-agentbus` skill moved to `internal/cli/skills/` and is
  embedded in the binary; `agentbus init --global` installs it to
  `~/.claude/skills/` (Claude Code) and `~/.agents/skills/` (Codex)
  (0a83558).
- **v0.9.2 released** (9861f4a, tag v0.9.2):
  [GitHub release](https://github.com/ericfitz/agentbus/releases/tag/v0.9.2);
  `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap` (1485943).

## 2026-09-17 (late): deferred-minor rulings

Pushed to `main`:

- **ADR 0006** (`docs/adr/0006-deferred-minor-rulings.md`) records the
  user's rulings on the six older deferred minors. Shipped here: item 1
  (`AGENTBUS_DATA_DIR` resolves `~/` and relative paths once at config
  load), item 2 (`go 1.27`), item 3 (owner token stays 12 bytes; ADR 0003
  ruling 16), item 5 (`Send` re-runs the channel and `reply_to` lookups in
  the write transaction, so a send racing a channel delete is `not_found`),
  item 6 (`result()` comment). Item 4 (drop `messages.bytes`, first schema
  migration) follows in the next entry. Shipped together in v1.4.0.

## 2026-09-17 (late): schema version 2, v1.4.0 release

Pushed to `main`:

- **First schema migration** (ADR 0006 item 4, spec
  `docs/superpowers/specs/2026-09-17-schema-migration-design.md`):
  `internal/bus/migrate.go` applies per-version steps on `Open`, each in one
  immediate transaction with foreign keys off, re-checking `user_version`
  under the lock. Step 1 to 2 rebuilds `messages` without the `bytes` column
  and carries the `AUTOINCREMENT` counter over. Tested against a real
  version 1 file. `release/release.sh` now uses `release/notes-<tag>.md`
  for the GitHub release notes when present.
- **v1.4.0 released** (tag v1.4.0): build, sign, notarize, GitHub release,
  and `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap`. Every running old-binary session must be
  restarted after `brew upgrade agentbus`; the release notes say so.

## 2026-09-18: default tasks channel, prefixed project channels, v1.5.0

Pushed to `main`:

- **ADR 0007** (`docs/adr/0007-default-tasks-channel.md`): a machine-wide
  task list `tasks` exists on every bus; project channels are
  `general/<repo>`, `memory/<repo>`, `tasks/<repo>` (prefix implies kind);
  `agentbus init` creates all three and renames a pre-existing `<repo>` or
  `<repo>-memory` in place with history (`bus.RenameChannel`). Task lists
  draw with U+2611 and the new theme color `tasks` (yellow). Skill, hook
  text, README, and install guide updated.
- **v1.5.0 released** (tag v1.5.0): build, sign, notarize, GitHub release,
  and `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap`. After
  upgrading: `agentbus init --global`, `agentbus init` in every repository
  with old-style channels, restart the TUI and harness sessions.

## 2026-09-18: register recreates missing project channels, v1.5.1

Pushed to `main`:

- **Fix** (`internal/mcpserver/server.go`): `register` ensures every
  prefixed channel (`general/`, `memory/`, `tasks/<repo>`) listed in
  `.local/agentbus.json` exists before subscribing. The tick reaps an empty
  channel with no live subscriber, so a fresh `agentbus init`'s channels
  could vanish before the first register; a pre-1.5.0 `agentbus mcp` process
  left running after the upgrade also reaped `tasks` and `tasks/<repo>` on
  every tick. Bare channel names still need `create_channel`.
- **v1.5.1 released** (tag v1.5.1): build, sign, notarize, GitHub release,
  and `Formula/agentbus.rb` pushed to `ericfitz/homebrew-tap`.
