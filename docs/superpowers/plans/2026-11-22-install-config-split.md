# install/config split and uninstall --purge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the location-sensitive `agentbus init` with `agentbus install` (machine) and `agentbus config` (repository), add `config --remove`, add `uninstall --purge`, and make `init` exit 2 naming both replacements.

**Architecture:** The existing `initer` in `internal/cli/init.go` keeps doing the work; `Init` splits into `Install` (today's `global()`) and `Config` (today's `repo()`). Removers sit next to their writers: `config --remove` next to `repo()`, purge in `uninstall.go`. `main.go` dispatches the new commands.

**Tech Stack:** Go (stdlib `flag`), `modernc.org/sqlite` via `internal/bus`, golden-file tests in `internal/cli/testdata`.

**Spec:** `docs/superpowers/specs/2026-11-22-install-config-split-design.md` (read it first; its Behavior section has the exact texts and exit codes).

## Global Constraints

- Done gate: `make verify` (build, vet, vet-cross, gofmt, lint, unit tests). Every task ends with `make verify` green.
- Exit codes: 0 success including nothing to do; 1 a step failed (message names the file or step); 2 usage errors.
- Docs and user-facing text use two-dash flags (`--purge`, `--remove`, `--dry-run`, `--harness`).
- No alias: `init` must not run either job. It is not listed in `usage` or help.
- `init` message (stderr, exit 2, any arguments), exactly:
  ```
  agentbus init was split into two commands:
    agentbus install   configure the harnesses on this machine (was: init --global)
    agentbus config    set up the current repository (was: init inside a repository)
  ```
- `config`/`config --remove` outside a git repository, exactly: `agentbus config: not inside a git repository; to configure the harnesses on this machine, run agentbus install` (exit 2).
- Purge never deletes anything it has not verified idle; never deletes files in the data directory that agentbus did not write; never removes a non-empty directory.
- American English. Historical specs, plans, ADRs, `release/notes-*.md` and `release/winget/**` keep the old `init` wording.
- Work lands on `main`, one commit per task, ending with the session's `Co-Authored-By`/`Claude-Session` trailers.

## Review Focus

1. `uninstall --purge` with stdin not a terminal and empty (agent shells): EOF must cancel (exit 1, nothing deleted), never hang. Test in Task 4.
2. `AGENTBUS_DATA_DIR` pointing at a directory with unrelated files: only agentbus entries go, the rest is listed and kept. Test in Task 4.
3. Purge when the data directory does not exist (already purged or never used): no `bus.Open` (it would create the directory), reports already removed, exit 0. Test in Task 4.
4. `agentbus config` in a subdirectory of a repo: works on the git root, like `init` did. Test in Task 2.
5. Old automation running `agentbus init --global` or `agentbus init -dry-run`: gets the split message and exit 2, not a flag-parse error. Test in Task 1.

---

### Task 1: `install`, `config` and the `init` message

**Files:**
- Modify: `internal/cli/init.go` (split `Init`; update texts that say `init --global`/`agentbus init`)
- Modify: `main.go:34` (usage), `main.go:111-131` (dispatch)
- Modify: `internal/cli/init_golden_test.go`, rename `internal/cli/testdata/init_golden/` to `install_golden/` and regenerate
- Modify: `internal/cli/init_test.go`, `internal/cli/uninstall_test.go` (`installAll`), `internal/cli/harnesses_test.go`, `main_test.go`

**Interfaces:**
- Produces:
  - `type InitOptions` without `Global` (keep the name; other fields unchanged).
  - `func Install(o InitOptions, out io.Writer) error`: defaults, `validHarness`, Windows `requireOnPath`, then `in.global()` regardless of cwd.
  - `func Config(o InitOptions, out io.Writer) error`: defaults, then `in.repo(gitRoot(o.Cwd))`; returns `ErrNotInRepo` when `gitRoot` is `""`. Runs the Windows `requireOnPath` check before that, as `Init` does today for both paths.
  - `var ErrNotInRepo = errors.New("not inside a git repository; to configure the harnesses on this machine, run agentbus install")`
  - `const InitSplitMessage` holding the three-line `init` message from Global Constraints.
- `main.go`: `install` flags `--harness`, `--dry-run` (no `--config`, no `--global`); `config` flags `--dry-run`, `--config`, and `--remove` (wired in Task 2). On `errors.Is(err, cli.ErrNotInRepo)` print `agentbus config: ` + its text to stderr, exit 2. `case "init":` prints `InitSplitMessage` to stderr and returns 2 before any flag parsing.

- [ ] **Step 1: Write failing tests**
  - `main_test.go`: `TestInitPrintsSplitMessage` runs `execBin` with `{"init"}`, `{"init","--global"}`, `{"init","-dry-run"}`, `{"init","extra"}`; each: exit 2, stderr contains `agentbus install` and `agentbus config` and `was split`, stdout empty.
  - `TestUsageListsInstallAndConfig`: `usage` contains `install` and `config`, and does not match `\binit\b`.
  - In `TestSubcommandHelpExitsZero`, `TestUnknownFlagStillFails`, `TestExtraArgumentsRejected`: replace `init` with `install` and `config` (`{"install","extra"}` → 2, `agentbus install: unexpected argument "extra"`; same for `config`).
  - `internal/cli/init_test.go`: `TestConfigOutsideRepo` calls `Config` with `Cwd` = a temp dir with no `.git`; `errors.Is(err, ErrNotInRepo)`. `TestInstallInsideRepo` calls `Install` with `Cwd` inside a temp repo and asserts the harness files are written (global step ran) and no `.local/agentbus.json` was created. `TestConfigFromSubdirectory` calls `Config` with `Cwd` = `<repo>/a/b` and asserts `<repo>/.local/agentbus.json` exists.
  - Golden test renamed `TestInstallGolden`, calling `Install`.

- [ ] **Step 2: Run to verify failure**
  Run: `go test ./... -run 'Init|Install|Config|Usage|Subcommand|UnknownFlag|ExtraArguments' 2>&1 | tail -30`
  Expected: compile errors (`Install`, `Config`, `ErrNotInRepo` undefined).

- [ ] **Step 3: Implement** the interfaces above. Update every string in `init.go` that names `init --global` → `install` and `agentbus init` → `agentbus config` (`global()`'s done line becomes `done: restart the harness, then run agentbus config inside each repository`; the `repo()` warning names `agentbus install`; comments too). Replace `Init(` call sites in tests (`installAll` drops `o.Global = true`).

- [ ] **Step 4: Regenerate goldens** with the golden test's update flag (see `init_golden_test.go` for its name), then `git diff --stat internal/cli/testdata` and read the diff: only `init`→`install`/`config` wording may change.

- [ ] **Step 5: Verify** — Run: `make verify`. Expected: exit 0.

- [ ] **Step 6: Commit** — `feat(cli): split init into install and config; init names both`

### Task 2: `config --remove`

**Files:**
- Modify: `internal/cli/init.go` (add remover next to `repo()`), `main.go` (`--remove` flag)
- Test: `internal/cli/init_test.go`

**Interfaces:**
- Consumes: `Config`, `ErrNotInRepo` (Task 1).
- Produces: `InitOptions.Remove bool`; `Config` with `Remove` set calls `func (in *initer) removeRepo(root string) error` instead of `repo`. `removeRepo` never opens the bus.

Behavior (spec "config --remove"): read identity from `.local/agentbus.json` via `repoconfig.Load` before deleting; on parse failure use `filepath.Base(root)`. Delete with `in.remove` (honors `DryRun`). Print the deleted path, then: `the repository's channels general/<identity> and memory/<identity> stay on the bus; delete them with agentbus delete-channel`. Missing file: print `nothing to remove: <path> does not exist`, return nil. Never touch `.gitignore`, `.local/` or other files in it.

- [ ] **Step 1: Write failing tests** in `init_test.go`:
  - `TestConfigRemoveRoundTrip`: snapshot repo files; `Config` (needs a bus: use the existing test config helper in `init_test.go`); `Config` with `Remove`; `.local/agentbus.json` gone, `.gitignore` still contains `.local/`, a pre-existing `.local/other.json` still present; output contains `general/<name>` and `delete-channel`.
  - `TestConfigRemoveTwice`: second run returns nil, output contains `nothing to remove`.
  - `TestConfigRemoveMalformed`: file content `{` → deleted, hint uses the repo directory name.
  - `TestConfigRemoveDryRun`: file still present afterwards.
  - `TestConfigRemoveOutsideRepo`: `ErrNotInRepo`.
  - `TestConfigRemoveDoesNotOpenBus`: `InitOptions.Config.DataDirectory` set to a path under a temp dir that does not exist; after the run it still does not exist.
- [ ] **Step 2:** `go test ./internal/cli -run ConfigRemove` → FAIL (`Remove` undefined).
- [ ] **Step 3: Implement** `removeRepo` and the `--remove` flag (help: `remove this repository's .local/agentbus.json (channels stay on the bus)`).
- [ ] **Step 4:** `make verify` → exit 0.
- [ ] **Step 5: Commit** — `feat(cli): config --remove deletes the repository's identity file`

### Task 3: uninstall restructure and binary pointer

**Files:**
- Modify: `internal/cli/uninstall.go`, `main.go` (`uninstall` case)
- Test: `internal/cli/uninstall_test.go`

**Interfaces:**
- Produces: `UninstallOptions` gains `Purge bool`, `ConfigPath string`, `In io.Reader` (default `os.Stdin`), `OS string` (default `runtime.GOOS`), `Executable func() (string, error)` (default `os.Executable`). Purge itself is Task 4; this task adds the fields and the flow.
- `Uninstall` flow: harness loop as today; a harness step error returns immediately (purge does not run). When no harness is detected it prints the no-harness message and continues (no early return). If `Purge`, call `in.purge(o)` (stub returning nil in this task). Last, print the binary pointer.
- `func binaryPointer(osName string, exe func() (string, error)) string`: resolved path (`filepath.EvalSymlinks`, falling back to the raw path) followed by `the agentbus binary is still installed; if you installed it with Homebrew (brew uninstall agentbus) or winget (winget uninstall ericfitz.agentbus), use that, otherwise delete the file`; on `windows` one more line: `if install.ps1 put it on your PATH, remove <dir> from your user PATH`. If `exe` errors, omit the path but keep the advice.

- [ ] **Step 1: Write failing tests:** `TestBinaryPointerUnix` / `TestBinaryPointerWindows` (pure function; Windows variant contains the directory and `PATH`); `TestUninstallNoHarnessStillPrintsPointer` (empty home: output has the no-harness message and `brew uninstall agentbus`); existing round-trip test asserts the pointer line appears last.
- [ ] **Step 2:** `go test ./internal/cli -run 'BinaryPointer|Uninstall'` → FAIL.
- [ ] **Step 3: Implement**; `main.go` adds `--purge` (help: `also delete the config file and the bus data (asks for yes; refuses while sessions are live)`) and `--config`, passes `os.Stdin`.
- [ ] **Step 4:** `make verify` → exit 0.
- [ ] **Step 5: Commit** — `feat(cli): uninstall prints where the binary is; prepare --purge`

### Task 4: `uninstall --purge`

**Files:**
- Modify: `internal/cli/uninstall.go` (or new `internal/cli/purge.go` if it passes ~150 lines)
- Test: `internal/cli/purge_test.go`

**Interfaces:**
- Consumes: `UninstallOptions` fields and the `in.purge` hook from Task 3; `config.Load(path) (Config, string, error)`, `config.DefaultPath()`, `config.Default().DataDirectory` (on Unix `~/.local/share/agentbus`, unexpanded: expand a leading `~/` against `o.Home`); `bus.Open`, `(*Bus).LiveSessionCount`, `(*Bus).Close`.
- Produces: `func (in *initer) purge(o UninstallOptions) error`; `var ErrPurgeRefused`, `var ErrPurgeCancelled` (both → exit 1 in `main.go` via the normal error path).

Algorithm (spec steps 1 to 4 and decision 6):
1. `cfg, path, err := config.Load(o.ConfigPath)`. On error: print the refusal (`uninstall: cannot verify that no session is live: <err>`), then `delete by hand after closing every harness:` with the config path (the returned `path`, else `DefaultPath()`) and the default data directory; return `ErrPurgeRefused`.
2. Under `DryRun`: print `would delete:` and each target (step 4's list, only those that exist), return nil.
3. If `<data>/agentbus.db` exists: `bus.Open`; on error refuse as in 1 (with that error, real paths). `LiveSessionCount() > 0` → print `uninstall: N live agentbus session(s); close or restart every harness, then run agentbus uninstall --purge again`, return `ErrPurgeRefused`. Close the bus before deleting. If it does not exist, skip (never call `bus.Open`, it creates the directory).
4. Print the targets, prompt `Type yes to continue: ` on `out`, read one line from `o.In`; not exactly `yes` after trimming (EOF included) → print `purge cancelled`, return `ErrPurgeCancelled`.
5. Delete in the data directory only: `agentbus.db`, `agentbus.db-wal`, `agentbus.db-shm`, `agentbus.db-journal`, `agentbus.log`, `agentbus.log.<n>` (glob `agentbus.log.*`, which also covers `agentbus.log.lock`), `park/`, `wait/` (both recursively). Builder: grep `DataDirectory`/`dataDir` joins once more and add any entry this list misses. Then `os.Remove` the data directory; if not empty, print `kept <dir>: it holds files agentbus did not write:` and the remaining names. Delete the config file; `os.Remove` its directory, ignoring a not-empty error silently. Missing targets print `already removed: <path>`.

- [ ] **Step 1: Write failing tests** in `purge_test.go` (temp home, `ConfigPath` in it, a config whose `data_directory` is a temp dir; seed a bus with `bus.Open` + a registration for the live case; use the existing fake harness):
  - `TestPurgeYesDeletesEverything`: `In` = `"yes\n"`; data dir and config file gone; exit nil.
  - `TestPurgeEOFCancels` (`In` empty) and `TestPurgeOtherAnswerCancels` (`"y\n"`): `ErrPurgeCancelled`, nothing deleted.
  - `TestPurgeRefusesLiveSession`: a session with a fresh heartbeat; `ErrPurgeRefused`, nothing deleted, output contains `1 live agentbus session`.
  - `TestPurgeRefusesMalformedConfig`: config content `{`; `ErrPurgeRefused`; output contains the config path and `delete by hand`; nothing deleted.
  - `TestPurgeRefusesUnopenableDB`: `agentbus.db` containing garbage bytes; `ErrPurgeRefused`; file still present.
  - `TestPurgeKeepsForeignFiles`: `notes.txt` in the data dir; after `yes`, `notes.txt` and the directory remain, output lists `notes.txt`; agentbus files gone.
  - `TestPurgeMissingDataDir`: data dir never created; `yes`; nil; output has `already removed`; the data dir still does not exist afterwards.
  - `TestPurgeDryRun`: nothing deleted, no prompt read (`In` is a reader that fails the test if read).
  - `TestPurgeTwice`: second run after a successful one returns nil.
  - `TestPurgeNoHarnessStillPurges`: empty home (no harness dirs); data deleted.
  - `TestPurgeSkippedAfterHarnessError`: a malformed `~/.claude/settings.json`; `Uninstall` returns that error; data untouched.
- [ ] **Step 2:** `go test ./internal/cli -run Purge` → FAIL.
- [ ] **Step 3: Implement** per the algorithm.
- [ ] **Step 4:** `make verify` → exit 0; also `make test-race` once.
- [ ] **Step 5: Commit** — `feat(cli): uninstall --purge deletes config and bus data when idle`

### Task 5: MCP prompt and in-binary texts

**Files:**
- Modify: `internal/mcpserver/server.go:378` (prompt name `init` → `config`, description `Set up Agentbus for the current repository and register this session`), `internal/mcpserver/server_test.go`
- Modify: `internal/cli/init.go` (`InitPrompt` → `ConfigPrompt`, step 1 runs `agentbus config`, closing line names `agentbus install`; `codexPrompt` says ``For `config`:``; `grokRule` names `agentbus install`)
- Modify: every other live Go text naming `init`: `internal/cli/stophook.go`, `internal/cli/subagenthook.go`, `internal/cli/subscribe.go`, `internal/cli/identity.go` (if it does), `internal/bus/channels.go`, `internal/repoconfig/repoconfig.go`, `internal/cli/skills/using-agentbus/SKILL.md`
- Regenerate goldens touched by the prompt text.

- [ ] **Step 1: Write failing test:** in `server_test.go`, listing prompts returns one named `config` and none named `init`; its text contains `agentbus config`. In `internal/cli`, `TestNoLiveInitReferences` scans the embedded texts (`ConfigPrompt`, `codexPrompt`, `grokRule`, `skillMD`) and fails on the regexp `agentbus init|init --global`.
- [ ] **Step 2:** `go test ./internal/mcpserver ./internal/cli -run 'Prompt|NoLiveInit'` → FAIL.
- [ ] **Step 3: Implement**; then `rg -n 'agentbus init|init --global|init -global' internal main.go` must print only test files asserting the split message.
- [ ] **Step 4:** regenerate goldens, read the diff; `make verify` → exit 0.
- [ ] **Step 5: Commit** — `feat: rename the init MCP prompt to config; texts name install/config`

### Task 6: docs, installers, release templates, ADR

**Files:**
- Modify: `README.md`, `docs/install.md`, `docs/windows-checklist.md`, `install.sh`, `install.ps1`, `release/agentbus.rb.tmpl`, `release/windows-zip-README.txt`, `release/test-install.sh`
- Create: `docs/adr/0029-install-config-split.md`

- [ ] **Step 1:** Replace `agentbus init --global` → `agentbus install`, per-repo `agentbus init` → `agentbus config`; document `config --remove` and `uninstall --purge` (one short paragraph each, from the spec) in `docs/install.md` and the README's install/uninstall section. Leave historical files alone (Global Constraints).
- [ ] **Step 2:** ADR 0029, `Status: Accepted (Eric, 2026-11-22). Human decisions, recorded by Claude.`: context (init ambiguous; agentmesh did the same split), the six decisions verbatim from the spec, consequences (old docs/brew caveats print `init`, answered by the split message; purge semantics).
- [ ] **Step 3: Verify:** `rg -n 'agentbus init|init --global' --glob '!docs/superpowers/**' --glob '!docs/adr/0*' --glob '!release/notes-*' --glob '!release/winget/**' .` prints only test assertions of the split message and ADR 0029. `bash release/test-install.sh` if it runs offline (check its header; otherwise `bash -n` and `shellcheck` it). `make verify` → exit 0.
- [ ] **Step 4: Commit** — `docs: install/config split, config --remove, uninstall --purge; ADR 0029`
