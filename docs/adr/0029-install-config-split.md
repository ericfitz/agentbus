# ADR 0029: init splits into install and config; uninstall gains --purge

Status: Accepted (Eric, 2026-11-22). Human decisions, recorded by Claude.

## Context

`agentbus init` did two unrelated jobs, chosen by where it ran: inside a git
repository it set up that repository, and outside one (or with `--global`) it
configured the harnesses on the machine. "init" is ambiguous; "install" and
"config" are not. agentmesh made the same split the same day. agentbus
already had `agentbus uninstall` for the harness entries; what was missing
was the renames, `--purge`, and a per-repository inverse.

The design is `docs/superpowers/specs/2026-11-22-install-config-split-design.md`.

## Decisions (Eric, 2026-11-22)

1. `init --global` becomes `agentbus install`. The per-repository `init`
   becomes `agentbus config`. Reason: "init" is ambiguous.
2. `agentbus init` is not kept as an alias. agentbus is released (v1.20.0 on
   Homebrew, winget and the install scripts, all of which print
   `agentbus init`), so `init` exits 2 with a message naming both
   replacements instead of a bare usage error.
3. `agentbus uninstall --purge` also deletes the config file and the data
   directory (the shared bus database: every repository's messages,
   memories and task lists).
4. `--purge` refuses to delete state while any session is live, and
   otherwise asks for a typed `yes`, as `agentbus reset` does.
5. `agentbus config --remove` deletes only `<root>/.local/agentbus.json`. The
   `.gitignore` line and the repository's channels on the bus stay.
6. When `--purge` cannot verify that no session is live (the config does not
   parse, or the bus does not open: a corrupt database, one from a newer
   agentbus, the WSL/Windows boundary check), it refuses: exit 1 naming the
   error, and prints the config file and data directory paths (the defaults
   when the config does not parse) for the user to delete by hand after
   closing the harnesses. It never deletes what it could not verify is idle.

## Consequences

- Documentation and Homebrew caveats written for earlier releases still
  print `agentbus init`; running it exits 2 and prints the split message
  naming `install` and `config`.
- The MCP prompt `init` (`/agentbus:init`) is renamed `config`, and the Codex
  and Grok prompts say "For `config`:". This call was the builder's, not
  Eric's.
- `--purge` deletes only the entries agentbus writes in the data directory,
  then the directory if empty, so a user-chosen data directory holding other
  files is never wiped. It runs when no harness is detected and not after a
  failed harness step. This call was the builder's, not Eric's.
- The liveness check reuses the session heartbeat cutoff; it does not
  inspect processes, so a session whose heartbeat lapsed is not counted.
