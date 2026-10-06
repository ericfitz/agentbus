# CLAUDE.md

agentbus: a Go CLI, MCP server and TUI for coordinating agents (see README.md and docs/).

## Commands

- Build: `make build` (`CGO_ENABLED=0 -trimpath` like release.sh, without the version ldflags). Pure Go; SQLite is `modernc.org/sqlite`.
- Tests: `make test`; `make test-race` for the race detector (slower).
- Lint: `make lint` (golangci-lint v2 defaults), `make vet`, `make fmt-check`.
- Done gate: `make verify` (build, vet, gofmt, lint, unit tests). Run it and show the result before claiming done. There is no CI; this gate is the only check before a push. Release signing and notarization (`release/release.sh`) are not part of it.

## Git

- Work lands directly on `main`; no PRs. HANDOFF.md is machine-local and untracked; PROGRESS.md was retired on 2026-10-02 (commit 5dfd359), do not recreate it.
