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
