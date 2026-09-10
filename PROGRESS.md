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
