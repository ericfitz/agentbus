# Harness Table and Uninstall Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Drive `agentbus init --global` from a table of harness definitions without changing its behavior, then add `agentbus uninstall [--harness X] [--dry-run]`, which reverses it using the same table.

**Architecture:** `internal/cli/harnesses.go` holds `[]harness`, one entry each for Claude Code, Codex and Grok Build. A generic install (in `init.go`) and a generic uninstall (in the new `uninstall.go`) interpret it. A golden characterization test, recorded before the refactor, shows the refactor changed no output, file or CLI call.

**Tech Stack:** Go (standard library only), the existing `internal/cli` test seams (`LookPath`, `Run`, temporary `Home`).

**Spec:** `docs/superpowers/specs/2026-11-04-harness-table-and-uninstall-design.md`

## Global Constraints

- Work lands on `main`; commit per task. Done gate: `make verify`.
- `init --global` and `init --global --dry-run` print the same lines in the same order, write the same files and run the same CLI commands as at commit 522459d.
- The harness order everywhere is Claude Code, Codex, Grok Build.
- An unknown `--harness` is rejected with the message `--harness must be claude, codex, or grok, got %q`. Build the name list from the table: comma-separated, with "or" before the last name.
- Uninstall never touches the bus database and key, the config file, any repository's `.local/`, running processes, or `.bak` files. It never removes a directory that sits directly under home. There is no confirmation prompt.
- On Windows, uninstall does not run `requireOnPath`.
- Verified on 2026-11-04 with Codex installed: `codex mcp remove agentbus` deletes the whole `[mcp_servers.agentbus]` table, `tool_timeout_sec` included, and exits 0 when the server is absent. Uninstall makes no Codex text edit.
- American English; `rg`, not `grep`.

## Review Focus

1. A hook file that also holds the user's own hooks under the same event, or in the same matcher entry as ours: only our inner hook goes, and theirs stays. Pinned in Task 3.
2. Uninstall run when nothing is installed, or run twice: it prints "not present" lines, writes no file, creates no `.bak`, and exits 0. Pinned in Task 3.
3. An invalid JSON hook file during uninstall: the error names the file, and nothing else in that harness is half-removed before the error. Hooks are read before any file is deleted. Pinned in Task 3.
4. `--dry-run` uninstall on a fully installed home: nothing changes on disk and no CLI runs, but every action is listed. Pinned in Task 3.
5. An emptied skill directory is removed, but `~/.agents`, `~/.claude` and the other harness dirs remain even when they are left empty. Pinned in Task 3.

---

### Task 1: Golden characterization test for `init --global` (before the refactor)

**Files:**
- Create: `internal/cli/init_golden_test.go`
- Create: `internal/cli/testdata/init_golden/{all,claude,codex,grok}-{run,dryrun}.txt` (8 files, generated)

**Interfaces:**
- Produces: `TestInitGlobalGolden`, run with `-update-golden` to regenerate. Task 2 must pass it with no regeneration.

- [ ] **Step 1: Write `TestInitGlobalGolden`.** Use subtests over the scenarios `all` (all three home dirs exist, no `--harness`), `claude`, `codex` and `grok` (each with only that home dir, plus `--harness <name>`). Each scenario runs twice, as `run` and `dryrun`. Each run uses a fresh temp home, the existing `fakeHarness` and `initOpts`, and `Global: true`. Build the golden text in this order:
  1. the `Init` output, with the home path replaced by `~` and passed through `filepath.ToSlash`;
  2. a `--- calls` line, then `fakeHarness.calls`, one per line;
  3. a `--- files` line, then every file under home: its slash-separated relative path, a space, and the first 12 hex digits of the SHA-256 of its contents, sorted.

  Compare the text with `testdata/init_golden/<scenario>-<mode>.txt`. A package-level `var updateGolden = flag.Bool("update-golden", false, "rewrite init golden files")` writes the files instead.
- [ ] **Step 2: Generate the files.** Run `go test ./internal/cli -run TestInitGlobalGolden -update-golden`, then `go test ./internal/cli -run TestInitGlobalGolden -count=1`. Expected: PASS. Open `all-run.txt` and confirm it lists the six CLI calls in `TestInitGlobalConfiguresDetectedHarnessesAndIsIdempotent`'s order, and the files `.claude/settings.json`, `.claude/skills/using-agentbus/SKILL.md`, `.codex/hooks.json`, `.codex/config.toml`, `.codex/prompts/agentbus.md`, `.agents/skills/using-agentbus/SKILL.md`, `.grok/hooks/agentbus.json`, `.grok/skills/using-agentbus/SKILL.md`, `.grok/commands/agentbus.md` and `.grok/rules/agentbus.md`.
- [ ] **Step 3: Commit** `test(init): golden record of init --global before the harness table`.

### Task 2: Harness table drives install

**Files:**
- Create: `internal/cli/harnesses.go`, `internal/cli/harnesses_test.go`
- Modify: `internal/cli/init.go` (`Init` validation, `global`, delete `claude`/`codex`/`grok` methods, `mcpConfigured`), `main.go:115` (flag help)

**Interfaces:**
- Consumes: the existing helpers `write`, `run`, `hook`, `mcpEntry`, `codexTimeout` and `wants`.
- Produces (used by Task 3 and Task 4):
  ```go
  type mcpSpec struct {
      cli          string   // "claude"
      add, remove  []string // args after the CLI name
      snippet      string   // printed when the CLI is not on PATH (today's text, verbatim)
      configFile   string   // relative to home, read by mcpConfigured: ".claude.json", ".codex/config.toml", ".grok/config.toml"
  }
  type hookSpec struct{ event, command, matcher string }
  type fileSpec struct {
      path    string // relative to home
      content []byte
  }
  type harness struct {
      name, title string // "claude"/"Claude Code", "codex"/"Codex", "grok"/"Grok Build"
      dir         string // ".claude", ".codex", ".grok"
      mcp         mcpSpec
      hookFile    string // ".claude/settings.json", ".codex/hooks.json", ".grok/hooks/agentbus.json"
      ownHookFile bool   // true only for Grok
      hooks       []hookSpec
      files       []fileSpec
      extra       func(in *initer, h harness) error // Codex: codexTimeout on ~/.codex/config.toml
      notes       []string // Codex: today's "accept them" line, verbatim
  }
  var harnesses []harness
  func harnessNames() []string          // in table order
  func harnessNameList() string         // "claude, codex, or grok"
  func validHarness(name string) error  // "" is valid; else the Global Constraints message
  func (in *initer) install(h harness) error
  ```
- Hook and file order per harness is today's order:
  - **Claude Code:** hooks SessionStart, Stop, UserPromptSubmit, SubagentStart; then the skill.
  - **Codex:** MCP, `extra`, hooks SessionStart (matcher `startup|resume|clear`), Stop, UserPromptSubmit; then the prompt `.codex/prompts/agentbus.md`, the skill `.agents/skills/using-agentbus/SKILL.md`, and the note.
  - **Grok Build:** hooks Stop, UserPromptSubmit; then the skill, `.grok/commands/agentbus.md` and `.grok/rules/agentbus.md`.
- Keep the existing comments that explain *why* (Grok has no SessionStart hook, Grok's default timeout, Codex prompts and issue 5059) next to the matching table entries.

- [ ] **Step 1: Write the table invariant tests** in `harnesses_test.go`:
  - `TestHarnessTableInvariants` checks that names are unique and non-empty, every entry has a non-empty `dir`, `mcp.cli`, `mcp.add`, `mcp.remove` and `mcp.configFile`, and `hookFile` is set whenever `hooks` is not empty. It also checks that no `fileSpec.path` or `hookFile` appears under two harnesses.
  - `TestHarnessNameList` checks that `harnessNameList() == "claude, codex, or grok"`, that `validHarness("cursor")` returns an error equal to `--harness must be claude, codex, or grok, got "cursor"`, and that `validHarness("")` returns nil.
- [ ] **Step 2: Run** `go test ./internal/cli -run 'TestHarness' -count=1`. Expected: FAIL, because the table is undefined.
- [ ] **Step 3: Implement.** Add `harnesses.go`. Make `global()` loop over `harnesses`: for each wanted harness, print `<title>:`, then call `install`. Build the "no harness found" message from the table, keeping today's text: `no harness found (no <dir1>, <dir2>, or <dir3>); use --harness claude|codex|grok to force one`, with absolute dirs. Make `mcpConfigured` loop over `mcp.configFile`. Make `Init` call `validHarness`. Build `main.go`'s `--harness` help from an exported `cli.HarnessNameList()` that wraps `harnessNameList`. Delete the three per-harness methods.
- [ ] **Step 4: Run** `go test ./internal/cli -count=1`. Expected: PASS, including `TestInitGlobalGolden` with **no** regeneration and every existing `TestInit*`.
- [ ] **Step 5: Commit** `refactor(init): drive init --global from a harness table`.

### Task 3: `Uninstall`

**Files:**
- Create: `internal/cli/uninstall.go`, `internal/cli/uninstall_test.go`

**Interfaces:**
- Consumes: `harnesses`, `validHarness`, `initer.write`, `initer.run`, `initer.say`, `initer.wants`, `hasHookCommand`.
- Produces (used by Task 4):
  ```go
  type UninstallOptions struct {
      Harness  string
      DryRun   bool
      Home     string                                // defaults to os.UserHomeDir
      LookPath func(string) (string, error)          // defaults to exec.LookPath
      Run      func(name string, args ...string) error // defaults as in Init
  }
  func Uninstall(o UninstallOptions, out io.Writer) error
  ```
- Behavior (the spec's Section 2): validate `Harness`, default the seams, and build an `initer`. For each wanted harness in table order:
  1. Print `<title>:`.
  2. **Read and parse `hookFile` first.** An invalid file returns `<path>: <err> (fix or remove it, then rerun)` before anything else in that harness changes.
  3. **MCP.** If the CLI isn't on PATH, print `  <cli> CLI not on PATH; remove this yourself:\n  <snippet>`. Otherwise run `remove`. On nil, print `  unregistered the agentbus MCP server via <cli> <remove args>`, except under DryRun, where `run` already prints `would have run ...`. On error, print `  <cli> <remove args> failed (agentbus may not have been registered): <err>` and continue.
  4. **Hooks**, one write per file. Remove each inner hook for which `hasHookCommand` matches any spec command. Drop any outer entry whose `hooks` list is now empty, any event whose list is now empty, and the `hooks` key if it is now empty. If nothing matched, print `  agentbus hooks not present in <path>`. If `ownHookFile` is set and the `hooks` map is now empty and no other top-level key remains, delete the file, print `  removed <path>`, and remove its emptied parent directories under the same rule as step 5. Otherwise call `write`, which backs the file up and prints the `updated` line.
  5. **Files.** Delete each existing file and print `  removed <path>`. A missing one prints `  <path> not present`. Then remove parent directories that are now empty, going upward but never removing a directory that sits directly under home.
  6. **Deletions in dry-run mode.** Under DryRun every deletion prints `  would remove <path>` and nothing is touched. Add a `remove(path string) error` helper next to `write` for this.

  After the loop, print `done: restart the harness` if any harness was wanted. Otherwise print the same "no harness found" message as `global()`. Return nil.

- [ ] **Step 1: Write the failing tests** in `uninstall_test.go`. For setup, install with `Init` and `Global: true` on a temp home (using `fakeHarness`), then uninstall with the same home and fakes:
  - `TestUninstallReversesInstallForEveryHarness`: table-driven over `harnesses`, each forced with `Harness`. After uninstall, none of that harness's `files` exist, and `hookCommands` finds no agentbus command in its `hookFile` (Grok's file must not exist). `f.calls` ends with exactly the harness's `remove` call. `Init` run again restores every file; compare against the golden list from Task 1.
  - `TestUninstallKeepsUserHooks`: the Claude `settings.json` holds `{"model":"opus","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"},{"type":"command","command":"agentbus identity"}]}],"PreToolUse":[{"hooks":[{"type":"command","command":"lint"}]}]}}`. After uninstall it parses as `{"model":"opus","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"}]}],"PreToolUse":[...unchanged]}}`, with the Stop, UserPromptSubmit and SubagentStart keys absent, and `settings.json.bak` exists.
  - `TestUninstallTwiceIsANoOp`: after one uninstall of all three, a second one returns nil. Its output contains `not present` for every file and `agentbus hooks not present in` for Claude and Codex. No file under home changes (compare a path→sha map before and after), and no new `.bak` appears.
  - `TestUninstallDryRunChangesNothing`: on a fully installed home, the path→sha map is unchanged, `f.calls` gained nothing, and the output contains `would remove` and `would have run claude mcp remove -s user agentbus`.
  - `TestUninstallInvalidHookFileStopsBeforeChanges`: `~/.claude/settings.json` holds `{`. The error message contains the path, and the Claude skill file still exists.
  - `TestUninstallRemovesEmptyDirsButNotHarnessDirs`: after uninstalling everything, `~/.claude/skills`, `~/.agents/skills`, `~/.grok/rules` and `~/.grok/hooks` are gone, while `~/.claude`, `~/.codex`, `~/.grok` and `~/.agents` exist.
  - `TestUninstallWithoutCLIPrintsSnippet`: with a `LookPath` that always errors, the output contains `claude CLI not on PATH; remove this yourself:` and the Claude snippet, and the files are still removed.
  - `TestUninstallRejectsUnknownHarness`: `Harness: "cursor"` returns the Global Constraints error.
  - `TestUninstallNoHarnessFound`: an empty home returns nil and the output contains `no harness found`.
- [ ] **Step 2: Run** `go test ./internal/cli -run TestUninstall -count=1`. Expected: FAIL, because `Uninstall` is undefined.
- [ ] **Step 3: Implement `uninstall.go`** as described above.
- [ ] **Step 4: Run** `go test ./internal/cli -count=1`. Expected: PASS, with the golden test still passing.
- [ ] **Step 5: Commit** `feat(cli): agentbus uninstall removes harness integrations`.

### Task 4: Wire the `uninstall` command, help and docs

**Files:**
- Modify: `main.go` (`usage` const at line 34, a new `case "uninstall"` after `case "init"`), `main_test.go` (the command lists at lines 126 and 144, plus an argument test), `README.md` (the bootstrap section around lines 100-120), `docs/install.md` (a new section after "Before first run: bootstrap with `agentbus init`", ending before "Configuration (optional)")

**Interfaces:**
- Consumes: `cli.Uninstall`, `cli.UninstallOptions`, `cli.HarnessNameList()`.

- [ ] **Step 1: Write the failing tests** in `main_test.go`. Add `uninstall` to the lists of commands that must accept `-h`, as the existing loops do for `init`. Add `{[]string{"uninstall", "extra"}, 2, "agentbus uninstall: unexpected argument \"extra\""}` to the argument table. Assert that `usage` contains `uninstall`.
- [ ] **Step 2: Run** `go test . -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** `case "uninstall"` with `flag.NewFlagSet("agentbus uninstall", flag.ContinueOnError)` and the flags `--harness` ("remove only this harness (<HarnessNameList>), even if it is not detected") and `--dry-run` ("print what would be removed without changing anything"). Parse with `parseFlags`/`parseExit` as `init` does; take no config. Call `cli.Uninstall(o, os.Stdout)`. An error prints `agentbus: <err>` and returns 1. Put `uninstall` after `init` in `usage`.
- [ ] **Step 4: Docs.**
  - `docs/install.md` gets a section `## Removing agentbus from a harness: agentbus uninstall`. It shows the command and the flags, and lists what is removed per harness (the MCP entry through the harness CLI, our hook entries with a `.bak`, Grok's own hook file, the skill, prompt, command and rule files, and emptied directories). It also lists what is kept: the bus database and key, the config file, every repository's `.local/`, `.bak` files and running processes. It ends with "restart the harness". Since the "Before first run" table doesn't list the UserPromptSubmit hook, add it to the hook column there.
  - `README.md` gets one sentence after the `init --global` block pointing at `agentbus uninstall`.
- [ ] **Step 5: Run** `go test . -count=1`, then `make verify`. Expected: PASS for both. Then try it against a scratch home: `HOME=$(mktemp -d) && mkdir $HOME/.claude && go run . init --global --harness claude --dry-run && go run . uninstall --harness claude --dry-run`. Both should print `would ...` lines only.
- [ ] **Step 6: Commit** `feat(cli): agentbus uninstall command, help and docs`.
