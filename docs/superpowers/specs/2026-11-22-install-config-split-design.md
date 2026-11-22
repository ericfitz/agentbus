# `agentbus install`, `config` and `uninstall --purge` design

Date: 2026-11-22. Status: proposed.

## Intent

`agentbus init` does two unrelated jobs, chosen by where it runs: inside a
git repository it sets up that repository, and outside one (or with
`--global`) it configures the harnesses on the machine. Eric's reason for
the change: "init" is ambiguous; "install" and "config" are not. This
splits the two jobs into explicitly named commands and gives each a
complete inverse, matching the split agentmesh made the same day (agentmesh
`docs/superpowers/specs/2026-11-22-uninstall-design.md`, commit 716569b).

agentbus already has `agentbus uninstall` (spec
`2026-11-04-harness-table-and-uninstall-design.md`, `internal/cli/uninstall.go`),
which removes the MCP entries, agentbus's own hooks and agentbus's files from
each harness. This design adds what is missing: the renames, `-purge`, the
per-repository inverse, and a pointer for people who still type `init`.

Goals: command names that say what they do; each command undoes only what
its counterpart did; one command that removes agentbus from a machine
completely, with deletion of the shared bus an explicit opt-in.

Non-goals: finding every repository on the machine; deleting a repository's
channels from the bus (`agentbus delete-channel` already does that);
restoring `.bak` files; deleting the binary.

Anti-goals: deleting the bus database or config without `-purge`; deleting
the bus while a session is using it; touching anything agentbus did not
write (other hooks, other MCP servers, agentmesh files).

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

## Approaches

- A. Mirror each writer with a remover next to it (chosen; Eric approved).
  `uninstall.go` already works this way for the harness entries; `--purge`
  and `config --remove` follow the same pattern, so writers and removers stay
  in step and tests pair them.
- B. Restore the `.bak` files the writers leave. Rejected: a `.bak` is the
  file as it was before the last change, so restoring it discards every
  edit made since.
- C. Record a manifest of what was written and replay it backwards.
  Rejected: a new state file to keep in step, when `uninstall` already
  works without one.

For the command shape, keeping `init`/`init --global` and mirroring them
with `uninstall`/`uninstall --global` was the alternative; Eric chose the
split (decision 1).

## Behavior

All commands keep the existing conventions: Go `flag` parsing (one or two
dashes both work; docs use two), `--dry-run` prints what would change and
changes nothing, exit 0 on success including nothing to do, 1 when a step
fails (the message names the file or step), 2 on usage errors. Every command
is idempotent: a second run reports nothing to do, and a run after a failed
one finishes the remainder.

### `agentbus install [--harness H] [--dry-run]`

Exactly what `init --global` does today (`initer.global`), including the
PATH check, with every text that says `init --global` or `init` changed to
`install` or `config`. It behaves the same inside or outside a git
repository; there is no `--global` flag. It takes no `--config`:
`global()` never reads the bus config.

### `agentbus config [--dry-run] [--config PATH]`

Exactly what `init` does inside a git repository today (`initer.repo`:
identity file, `.gitignore`, `general/<repo>` and `memory/<repo>`, task-list
entry cleanup, registration line), with its texts updated. Outside a git
repository it exits 2: `agentbus config: not inside a git repository; to
configure the harnesses on this machine, run agentbus install`.

### `agentbus config --remove [--dry-run]`

Inside a git repository: deletes `<root>/.local/agentbus.json` and prints
what it deleted, plus one line saying that the repository's channels
(`general/<identity>`, `memory/<identity>`) stay on the bus and how to delete
them (`agentbus delete-channel`). The identity is read from the file before
deleting it; if the file does not exist, it prints that there is nothing to
remove (exit 0). If the file exists but does not parse, it is still deleted,
and the channel hint uses the directory name. It does not remove `.local/`
itself, other files in it, or the `.gitignore` line. Outside a git
repository: exit 2 as for `config`. `--remove` does not open the bus.

### `agentbus init`

Any invocation, with any arguments, exits 2 and prints to stderr:

```
agentbus init was split into two commands:
  agentbus install   configure the harnesses on this machine (was: init --global)
  agentbus config    set up the current repository (was: init inside a repository)
```

`init` is not listed in usage or help.

### `agentbus uninstall [--harness H] [--dry-run] [--purge] [--config PATH]`

Without `--purge`: unchanged from today (per detected or forced harness:
MCP entry through the harness CLI, agentbus hook commands with a `.bak`,
skill/prompt/command/rule files), then the binary pointer below.

With `--purge`, after the harness steps. Purge also runs when no harness is
detected (a machine whose harnesses were already removed can still be
purged); it does not run when a harness step failed (`Uninstall` returns
that error first, so a malformed hook file is fixed before any state is
deleted).

1. Load the config the way every other command does (`--config`,
   `AGENTBUS_CONFIG`, default path; `AGENTBUS_DATA_DIR` applies) to find the
   config file path and the data directory. If loading fails, refuse per
   decision 6.
2. If the data directory exists and holds a bus database, open it and call
   `LiveSessionCount`; if opening fails, refuse per decision 6. If it is above zero, print
   `uninstall: N live agentbus session(s); close or restart every harness, then run agentbus uninstall --purge again`
   and exit 1 without deleting anything. The harness steps above have
   already run, so a restarted harness no longer starts agentbus and the
   second run finds no live session.
3. Print the paths that will be deleted and ask `Type yes to continue:` on
   stdin. Only an exact `yes` line proceeds; anything else (including EOF)
   prints `purge cancelled` and exits 1. Under `--dry-run` it prints the
   paths and stops, without the liveness check or the prompt.
4. Delete only the entries agentbus writes in the data directory:
   `agentbus.db` and its `-wal`/`-shm` files, `agentbus.log` with its
   rotations and `.lock`, `park/`, and the wait lock files agentbus keeps
   there (the builder enumerates them from the code). Then remove the data
   directory if it is empty; if anything else is left, list it and leave
   it (the directory may be a user-chosen path holding other files). Delete
   the config file, and remove its directory if left empty; never remove a
   non-empty one. A path that is already gone is reported as already
   removed.

Last, with or without `--purge`, it prints `os.Executable()`'s resolved path
with: `the agentbus binary is still installed; if you installed it with
Homebrew (brew uninstall agentbus) or winget (winget uninstall ericfitz.agentbus),
use that, otherwise delete the file`. On Windows it also names the binary's
directory as the one to take off PATH if `install.ps1` added it (agentbus
cannot tell whether it did).

## Calls made in the design (not Eric's)

- `install` ignores whether it runs inside a repository; `config` refuses to
  run outside one.
- The MCP prompt `init` (`/agentbus:init` in Claude Code) is renamed `config`,
  and the Codex prompt (`/prompts:agentbus`) and Grok command
  (`/agentbus`) say "For `config`:". The prompt runs `agentbus config` and
  registers, so the ambiguity Eric named applies to it too. (agentmesh kept
  its `init` prompt because there it only registers.)
- The `init` message goes to stderr with exit 2, like other usage errors.
- `--purge` deletes only the entries agentbus writes in the data directory,
  then the directory if empty, so a user-chosen `data_directory` or
  `AGENTBUS_DATA_DIR` holding other files is never wiped.
- `--purge` runs when no harness is detected and does not run after a failed
  harness step.
- The purge liveness check reuses `LiveSessionCount` (heartbeat cutoff);
  it does not inspect processes.
- An ADR (`docs/adr/0029-install-config-split.md`) records decisions 1 to 5
  as human decisions.

## Left to the builder

- File layout: `install`/`config` may stay in `init.go` (renamed functions)
  or move; removers sit next to their writers.
- Every live reference: `README.md`, `docs/install.md`,
  `docs/windows-checklist.md`, `install.sh`, `install.ps1`,
  `release/agentbus.rb.tmpl`, `release/windows-zip-README.txt`,
  `release/test-install.sh`, and the texts in `internal/cli/` (init prompt,
  Codex/Grok prompt, Grok rule, `identity`, stop and subagent hooks,
  `subscribe`), `internal/bus/channels.go`, `internal/repoconfig`,
  `internal/mcpserver/server.go`, and the embedded using-agentbus skill if it
  names `init`. Historical specs, plans, ADRs, release notes and the
  published winget manifests keep the old name.
- Tests: golden files for `install` (rename the `init_golden` set);
  `install` then `uninstall` against a temporary home with fake harness CLIs;
  `config` then `config --remove`; `config` and `config --remove` outside a
  repository (exit 2); `init` with and without arguments (exit 2, message);
  `--purge` with a live session (refuses, nothing deleted), with a malformed
  config and with an unopenable database (refuses, prints paths), with no
  harness detected (still purges), with a foreign file in the data
  directory (left in place and listed), with `yes`, with
  any other answer and with EOF, under `--dry-run`, with the data directory
  already gone, and on a second run; usage no longer lists `init`.
- Second runs and failed-run leftovers: covered by idempotency above; a
  purge interrupted midway is finished by running it again.
