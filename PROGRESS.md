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
