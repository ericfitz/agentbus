# Harness table and `agentbus uninstall`

Date: 2026-11-04. Status: approved design.

## Human decisions (Eric, 2026-11-04)

1. **Refactor `init` so that adding a harness is easy.** Each harness becomes
   one entry in a table of harness definitions. A generic install and a
   generic uninstall read that table. Rejected: a per-harness interface with
   hand-written Install/Uninstall methods, because each harness's reverse
   steps could drift from its install. Also rejected: a pure data table with
   no per-harness extra step, which would force odd cases such as the Codex
   timeout edit into the table shape.
2. **Add a command that removes integrations:**
   `agentbus uninstall [--harness X] [--dry-run]`. It works at machine level
   and per harness. It undoes `init --global` and leaves the per-repository
   setup and the bus alone. Rejected: a `--remove` flag on `init`, and
   removing repository files as well.
3. **No Cursor support.** A spike on Cursor 3.28.7 (agentbus memory 1548)
   found three problems:
   - every chat in a window shares one `agentbus mcp` connection;
   - hooks run under a different process than the MCP server;
   - neither `sessionStart` `additional_context` nor MCP server
     `instructions` reaches the model.

   Eric dropped Cursor rather than change the register rule. The table makes
   a later attempt cheaper, but this work adds no Cursor entry.

## Section 1: the harness table

The new file `internal/cli/harnesses.go` holds one definition per harness:

```go
type harness struct {
    name, title string     // "codex", "Codex"
    dir         string     // relative to home; its existence means "detected"
    mcp         mcpSpec    // CLI name, add and remove args, the snippet to print
                           // when the CLI is not on PATH, and the config file
                           // (relative to home) that mcpConfigured reads
    hookFile    string     // relative to home
    ownHookFile bool       // agentbus owns the whole file (Grok's hooks/agentbus.json)
    hooks       []hookSpec // event, command, matcher
    files       []fileSpec // path relative to home, content
    extra       func(in *initer, h harness) error // optional install-only step
    notes       []string   // printed after install, for example the Codex trust line
}

var harnesses = []harness{claudeHarness, codexHarness, grokHarness}
```

These parts of the code read the table and keep no list of their own:

- the check on `InitOptions.Harness` and the `--harness` flag help in
  `main.go`, both of which build the name list from the table;
- the loop in `global()`, which keeps today's order (Claude Code, Codex,
  Grok);
- the "no harness found" message, which names each `dir`;
- `mcpConfigured()`, which reads each `mcp` config path;
- install (`init --global`) and uninstall.

The only Codex-specific step that stays is `extra` for Codex:
`codexTimeout`, which runs after the MCP entry exists. The existing helpers
(`write`, `run`, `hook`, `mcpEntry`, `hasHookCommand`) stay, now called from
the generic install.

**Behavior is unchanged.** `init --global` and `init --global --dry-run`
write the same files and run the same commands as today. They also print
the same lines in the same order. The existing `init_test.go` tests pass
with no more than mechanical edits. Two other files keep harness-specific
knowledge by design:

- the stop hook's stdin parsing, where Grok sends camelCase;
- docs/install.md's manual-setup sections.

**What a new harness touches:** a table entry, its docs section, and any
change in how a hook must reply under it.

## Section 2: `agentbus uninstall`

`agentbus uninstall [--harness X] [--dry-run]` works at
machine level wherever it runs. Inside a repository it does nothing to that
repository. Detection is the same as for `init --global`. An unknown
`--harness` value is rejected with the same message `init` uses. With no
harness detected, it prints the "no harness found" message and exits 0. On
Windows it does not require agentbus on PATH, because nothing it does
starts agentbus.

For each harness to act on, it works in this order:

1. **MCP entry.** If the harness CLI is on PATH, it runs the harness's
   remove command (`claude mcp remove -s user agentbus`,
   `codex mcp remove agentbus`, `grok mcp remove --scope user agentbus`).
   If the remove fails (Claude Code and Grok exit nonzero when the server
   is absent), it prints `<cli> ... failed (agentbus may not have been
   registered)` and the uninstall continues. If the CLI is not
   on PATH, it prints the entry to delete by hand: the same snippet that
   install prints.
2. **Hooks.** For each `hookSpec` it removes every inner hook whose command
   contains the spec's command, using the same `hasHookCommand` match that
   install uses. It then drops any matcher entry whose `hooks` list is now
   empty, and any event whose list is now empty. Other keys, events and
   hooks are kept. A changed file gets a `.bak` backup first, as `write`
   does. When `ownHookFile` is set and no hooks remain, the file is
   deleted. A missing or already-clean hook file needs no change. Invalid
   JSON is an error that names the file, as in install.
3. **Files.** It deletes each `files` path that exists, then removes
   parent directories left empty, stopping at the harness directory. It
   never removes `dir` itself, or `~/.agents` for the Codex skill. No file
   is listed by more than one harness, so there is no shared-ownership
   rule. A test enforces that.

The Codex `tool_timeout_sec` line lives inside the `[mcp_servers.agentbus]`
table, which `codex mcp remove` deletes. The plan checks this against the
installed Codex. If the line survives, uninstall also removes that table's
lines by a text edit, the reverse of `codexTimeout`.

**Output.** One line per change: `removed <path>`,
`updated <path> (backup at <path>.bak)`, or
`unregistered the agentbus MCP server via <cli> ...`. For an item already
absent it prints `<item> not present`. Under `--dry-run` each line reads
`would ...`, and nothing is written or run. The run ends with
`done: restart the harness`.

**Never touched:** the bus database and key, the config file, any
repository's `.local/`, and running agentbus processes. It never deletes
a `.bak` file; each hook file it edits is backed up to `<file>.bak` first,
which replaces an older backup.
There is no confirmation prompt; `--dry-run` and the backups cover that.

**Second runs and partial failures:**

- **A second run** finds nothing to remove, prints "not present" lines, and
  changes nothing on disk. The MCP step runs each harness's remove command
  again: Claude Code and Grok then print `<cli> ... failed (agentbus may not
  have been registered)` because their CLIs exit nonzero when the server is
  absent; Codex exits 0.
- **A failed run** stops at the first error. Each step only removes what it
  finds, so the next run continues from the leftovers.
- **Overlapping `init` and `uninstall` runs** take no lock, the same as
  `init` today. The last writer wins for each file.

`usage`, `agentbus help`, README.md and docs/install.md gain the command.
The section on bootstrapping with `agentbus init` in docs/install.md gets a
matching "Removing agentbus from a harness" section.

## Testing

All tests run against a temporary home and fake `LookPath`/`Run` seams, as
`init_test.go` does today.

- **Golden output of the refactor:** before the refactor, record the
  `--dry-run` output, the written files and the CLI calls for each harness
  and for all three together. After it, compare against the recordings.
- **Install, uninstall, install again,** table-driven over every harness:
  - after uninstall no agentbus hook, file or MCP call remains;
  - a hook file holding the user's own hooks keeps them unchanged
    when compared after parsing;
  - a second uninstall reports "not present" and writes nothing;
  - a reinstall restores the full setup.
- **Edge cases:**
  - invalid hook-file JSON is an error naming the file;
  - with the CLI missing, the snippet is printed;
  - `--dry-run` writes nothing and runs nothing;
  - an unknown `--harness` is rejected;
  - no harness detected exits 0 with the message;
  - Grok's owned hook file is deleted;
  - an empty skill directory is removed, and the harness dir and
    `~/.agents` remain.
- **Table invariants:**
  - names are unique;
  - no file path appears under two harnesses;
  - every harness has an MCP spec and a detection dir.
- **Manual check during implementation:** whether `codex mcp remove agentbus`
  removes `tool_timeout_sec` on this Mac (Codex is installed).
