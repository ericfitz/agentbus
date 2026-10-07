# Windows support: native port, WSL, packaging

Status: Approved 2026-10-06 (design). Tracked in #35. Builds on #34
(`docs/superpowers/specs/2026-10-06-linux-releases-design.md`): its signed
`SHA256SUMS`, release-asset layout and `install.sh` conventions apply here.
Follow-up: #37 (Authenticode).

## Goal

Run agentbus for agents on native Windows (PowerShell or cmd) and in WSL,
installed and upgraded the way Windows developers expect, with every
download verifiable.

Checked 2026-10-06 at e159e8c: with three Unix-only calls stubbed in a
scratch copy, `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build` and `go vet
./...` (which compiles the tests) passed. The work is behavior, packaging
and testing.

## Human decisions (user, 2026-10-06)

1. **Both native Windows and WSL.** WSL uses the Linux build and
   `install.sh` from #34.
2. **Separate buses; no new IPC.** Native Windows and WSL each have their
   own bus. Users may point both at one directory themselves; agentbus does
   not configure that, and does not add a network transport.
3. **Refuse a database shared across the Windows–WSL boundary** (see Shared
   database guard).
4. **Windows paths:** config in `%AppData%\agentbus`, data in
   `%LocalAppData%\agentbus`.
5. **Packaging:** `install.ps1`, a Scoop bucket, and winget.
6. **Verification:** `install.ps1` verifies the Ed25519-signed `SHA256SUMS`
   with OpenSSL 3, normally the one shipped with Git for Windows, and
   refuses without it. Authenticode signing is deferred to #37.
7. **Testing:** a GitHub Actions Windows workflow (amd64 and arm64) plus
   the user's Windows 11 ARM VM in VMware Fusion for harness checks.

## Human decisions (user, 2026-10-07)

Proposed in the implementation plan; approved by Eric on 2026-10-07.

8. **`make verify` gains `vet-cross`:** compile-only `go vet` for
   `windows/amd64`, `windows/arm64` and `linux/amd64`, plus golangci-lint on
   the Windows build. The done gate grows; it still runs no tests on
   another OS.
9. **`install.ps1` reads `AGENTBUS_TEST_OSARCH`,** a test-only override of
   the detected architecture, documented in the script header and set only
   by `release/test-install.ps1` to exercise the unknown-architecture
   refusal.
10. **`install.ps1` edits the user Path in the registry.** It reads
    `HKCU:\Environment` `Path` unexpanded and writes it back as
    `ExpandString`, which preserves `%VAR%` entries, instead of
    `[Environment]::GetEnvironmentVariable` and `SetEnvironmentVariable`
    with `'Path','User'`. It then broadcasts the setting change.
11. **`install.ps1` refusals** beyond the original plan: it refuses a copy
    without an embedded release key, requires `AGENTBUS_VERSION` to be
    exactly `vX.Y.Z` (no suffix, no newline), rejects command-line arguments,
    refuses when `<install dir>\agentbus.exe` is a directory, and strips
    trailing slashes from `AGENTBUS_BASE_URL` and `AGENTBUS_INSTALL_DIR`.
12. **Test machines:** the Windows 11 ARM VM for harness checks is VMware
    Fusion; the WSL2 shared-database checks use a UTM 4.7.4 guest with
    nested virtualization.

Rejected: WSL only (no native port); one shared bus over a new IPC
mechanism; allowing a cross-boundary database with a warning or silently;
keeping `~/.config` and `~/.local/share` on Windows; `install.ps1` alone
(no upgrade path); Authenticode now (certificate cost before a need is
shown); a VM-only test setup.

## Porting the code

### Process table

`internal/procs/procs_windows.go` (`golang.org/x/sys/windows`):

- `Parent(pid)` and `Comm(pid)` from a `CreateToolhelp32Snapshot` process
  walk (`th32ParentProcessID`, `szExeFile`).
- `StartTime(pid)` from `GetProcessTimes` creation time, so `Alive` rejects a
  reused pid as on darwin and linux. A pid that cannot be opened or no
  longer exists returns `ErrGone`; access denied for a live process returns
  its start time as unknown (0), matching `procs_other.go`.

`procs_other.go` stays the fallback for other platforms; its `syscall.Kill`
liveness check moves behind a `unix` build tag.

### Harness detection shells

`shells` in `internal/procs/harness.go` adds `cmd`, `powershell`, `pwsh`
and `bash`, compared case-insensitively after removing a trailing `.exe`.
Claude Code on Windows runs hooks through Git Bash, so a stop hook's
ancestry is `claude.exe` → `bash.exe` → `agentbus.exe`.

### File locks

`flockFile` (`internal/cli/waitlock.go`) and the MCP log lock
(`internal/mcpserver/logfile.go`) get per-platform implementations by build
tag: `flock(LOCK_EX)` on Unix, `LockFileEx(LOCKFILE_EXCLUSIVE_LOCK)` on
Windows. Behavior (exclusive, blocking, released on close or exit) is
unchanged.

### Replacing a running wait

`acquireWaitLock` stops the wait it replaces with SIGTERM on Unix and, on
Windows, `OpenProcess(PROCESS_TERMINATE)` plus `TerminateProcess(h, 3)`, so
the replaced wait's exit code stays 3 as agents see today. The replaced
wait's own cleanup (removing a pidfile that no longer names it, closing the
bus) is a no-op or safe to skip: SQLite recovers from an abrupt exit.

### Signals and shutdown

Only Ctrl+C reaches a Windows console process. `mcp` already returns when
stdin closes, and `wait` already exits when its harness process is gone
(ADR 0014). Windows tests cover both.

### Editor launch

`internal/tui/health.go` runs `%EDITOR%` (default `notepad`) through `cmd
/c` on Windows instead of `/bin/sh -c`.

### Default paths

On Windows, `config.DefaultPath` returns `%AppData%\agentbus\config.json`
(`os.UserConfigDir`) and the default data directory is
`%LocalAppData%\agentbus` (Local, because the database must not roam with a
domain profile). `AGENTBUS_CONFIG`, `AGENTBUS_DATA_DIR` and
`data_directory` override them as on other platforms. Unix defaults are
unchanged.

### Tests

Tests that depend on Unix behavior (process trees, signals, `flock`,
`/bin/sh`) get Windows counterparts where the behavior exists and are
skipped with a stated reason where it does not. `go test ./...` passes on
`windows/amd64` and `windows/arm64`.

## Shared database guard

Opening the bus (`bus.Open`, used by the CLI, the MCP server and the TUI)
refuses a data directory that sits across the Windows–WSL boundary. SQLite's
POSIX/Win32 locks and the WAL shared-memory index (`agentbus.db-shm`) do not
work between Windows processes and WSL processes on one file, so concurrent
use can corrupt the database.

- **Linux under WSL** (`/proc/sys/fs/binfmt_misc/WSLInterop` exists, or
  `/proc/sys/kernel/osrelease` contains `microsoft`, case-insensitive): find
  the mount containing the data directory in `/proc/self/mountinfo` (the
  longest mount point that is a path prefix, after resolving symlinks) and
  refuse if its filesystem type is `9p` or `drvfs`.
- **Windows:** refuse a data directory whose absolute path is under
  `\\wsl$\` or `\\wsl.localhost\`, directly or through a mapped drive letter
  (resolved with `WNetGetConnection`).
- The error names the directory and filesystem, says the database must stay
  on one side, and suggests a path on that side's own disk.
- Outside WSL, and on other filesystems, nothing changes.

Unit tests use recorded `mountinfo` content and paths: `9p` and `drvfs`
refused; `ext4`, `tmpfs` and a nested ext4 mount under a 9p mount point
allowed; WSL not detected → allowed; `\\wsl$\Ubuntu\...`,
`\\wsl.localhost\...`, and a mapped drive resolving to one refused; a
local `C:\...` path allowed.

Manual check before the first release with this guard: in a Windows 11
ARM VM with WSL2, confirm the filesystem type `/mnt/c` reports in current
WSL, that agentbus refuses `AGENTBUS_DATA_DIR=/mnt/c/...`, and that native
Windows agentbus refuses `AGENTBUS_DATA_DIR=\\wsl.localhost\<distro>\...`.
WSL2 needs nested virtualization. The user's Mac (M4 Pro, macOS 27) has
it in Hypervisor.framework (M3 and later, macOS 15 and later), so the first
step is to enable it for a Windows 11 ARM guest in UTM (4.7.4 installed)
and confirm `wsl --install` succeeds; GitHub's hosted runners cannot run
WSL2. If UTM does not expose it, use a physical Windows machine or a cloud
Windows VM with nested virtualization.

## Packaging

### Release assets

`release.sh` adds `agentbus-<tag>-windows-amd64.zip` and
`agentbus-<tag>-windows-arm64.zip` (each holding `agentbus.exe`, built with
`CGO_ENABLED=0 GOOS=windows -trimpath` and the version ldflags) and
`install.ps1` (repository root, copied from the tag's worktree), all listed
in the signed `SHA256SUMS`. It cannot run the Windows binaries, so it checks
with `file` that each is a PE32+ executable for the expected machine;
Actions and the VM run them.

### install.ps1

Compatible with Windows PowerShell 5.1 and PowerShell 7. Usage:

```
irm https://github.com/ericfitz/agentbus/releases/latest/download/install.ps1 | iex
```

Steps:

1. **Architecture** from
   `[System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture`
   (`X64` → amd64, `Arm64` → arm64; anything else fails, naming it). This
   reports ARM64 even when PowerShell itself runs as emulated x64.
2. **Version:** `AGENTBUS_VERSION`, else the tag from the
   `releases/latest` redirect, as in `install.sh`. `AGENTBUS_BASE_URL`
   points at a mirror or a test server.
3. **Download** the zip, `SHA256SUMS` and `SHA256SUMS.sig` to a new temp
   directory.
4. **Find OpenSSL 3:** `openssl` on `PATH`, then Git for Windows'
   `usr\bin\openssl.exe` and `mingw64\bin\openssl.exe` under the Git root
   (from `git --exec-path`, else `%ProgramFiles%\Git`); the first reporting
   version 3 or newer is used. None: refuse, suggesting `winget install --id
   Git.Git -e`; only older versions found: refuse, naming the version.
5. **Verify** the signature with the embedded public key (`openssl pkeyutl
   -verify -rawin -pubin`), then the zip's hash with `Get-FileHash` against
   its `SHA256SUMS` line. `AGENTBUS_SKIP_SIGNATURE=1` skips only the
   signature, says so, and still checks the hash; any other value is
   rejected.
6. **Install** to `AGENTBUS_INSTALL_DIR`, default
   `%LocalAppData%\Programs\agentbus`. Extract to the temp directory. If
   `agentbus.exe` exists, rename it to `agentbus.exe.old-<random>` (Windows
   allows renaming a running executable but not overwriting it), move the
   new one into place, then try to delete every `agentbus.exe.old-*`,
   ignoring those still in use.
7. **PATH:** if the directory is not on the user `PATH`, append it at user
   scope (`[Environment]::SetEnvironmentVariable(..., 'User')`) and say that
   new terminals pick it up.
   (Decision 10 supersedes this: the Path is edited in the registry; see
   Human decisions, 2026-10-07.)
8. **Report** `agentbus version`, and print `agentbus init --global` and the
   restart-after-upgrade reminder. It does not run `init`.

State:

- Second run: upgrades or reinstalls; old binaries in use are renamed and
  removed by a later run.
- Two runs at once: separate temp directories; a failed rename or move is
  retried once; both end with a verified `agentbus.exe`.
- Interrupted run: a `finally` block removes the temp directory; a
  leftover `.old-*` file is removed by the next run once unused.

### Scoop

A new repository `ericfitz/scoop-bucket` holds `bucket/agentbus.json`:
`version`, `architecture.64bit` and `architecture.arm64` (`url`, `hash`),
`bin: agentbus.exe`, and `checkver`/`autoupdate` against GitHub releases.
`release.sh` renders it from `release/agentbus.scoop.json.tmpl`, fails on
any unfilled placeholder, and commits and pushes only when it changed.
Users install with `scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket`
and `scoop install agentbus`. Scoop cannot replace files of a running app;
the docs say to stop agentbus sessions before `scoop update agentbus`.

### winget

Package `ericfitz.agentbus`: `InstallerType: zip`, `NestedInstallerType:
portable`, `PortableCommandAlias: agentbus`, one installer per
architecture.

- First submission: the user runs `komac new` once (manual, reviewed by
  Microsoft).
- Each release: `release.sh` runs `komac update ericfitz.agentbus --version
  <v> --urls <amd64 zip> <arm64 zip> --submit` with a GitHub token sourced
  from `~/.keys/`. Komac runs on macOS (`brew install komac`).
- If komac fails, the rest of the release stands; `release.sh` prints the
  exact command to rerun. On a rerun, an open winget-pkgs pull request for
  that version counts as done.
- Upgrading with winget while agentbus runs can fail; the docs say to stop
  sessions first.

## Testing

### GitHub Actions

`.github/workflows/windows.yml`, on push to `main` and
`workflow_dispatch`, `permissions: contents: read`, matrix `windows-latest`
(amd64) and `windows-11-arm`:

- `actions/setup-go` with `go-version-file: go.mod`, then `go vet ./...` and
  `go test ./...`.
- `release/test-install.ps1`: builds a fake release (a stub `agentbus.exe`
  built from a tiny Go program, zips, `SHA256SUMS`, a signature from a
  throwaway key, and a copy of `install.ps1` with the throwaway public key),
  serves it from a local `HttpListener`, and runs `install.ps1` with
  `AGENTBUS_BASE_URL`. Cases: valid install; tampered zip, `SHA256SUMS`, and
  signature refused; no OpenSSL (refuses with the Git suggestion); OpenSSL
  1.1 stub (refuses, naming the version); `AGENTBUS_SKIP_SIGNATURE=1` (hash
  still checked) and an invalid value rejected; unknown architecture
  (injected) refused; upgrade while the old binary runs; two concurrent
  runs; leftover `.old-*` cleaned once unused; interrupted run leaves no
  temp directory; the embedded public key equals
  `release/agentbus-release-ed25519.pub`.

The workflow reports; it does not gate. `make verify` remains the done
gate, and CLAUDE.md's "There is no CI" line is updated to say the Windows
workflow is informational.

### Windows VM

`release/test-windows.ps1`, run in the user's Windows 11 ARM VM, runs `go
test ./...` and `release/test-install.ps1` natively on arm64 and prints a
summary. `docs/windows-checklist.md` lists the manual harness checks, done
before the first Windows release and after any change to harness
detection:

- `agentbus init --global` writes Claude Code, Codex and Grok config under
  `%USERPROFILE%`, and each harness registers on the bus.
- Claude Code's stop hook fires through Git Bash and reports the right
  identity.
- A background `agentbus wait` follows the harness, exits when it closes,
  and is replaced (exit 3) by a newer wait.
- The TUI renders and accepts input in Windows Terminal.
- `install.ps1` from the published URL installs and upgrades while an MCP
  server is running.

## Documentation

- `docs/install.md`: a Windows section (`install.ps1` with its environment
  variables, Scoop, winget, the OpenSSL 3 / Git for Windows requirement,
  stopping sessions before Scoop or winget upgrades, config and data paths)
  and a WSL section (install with `install.sh`; Windows and WSL buses are
  separate; why a data directory across the boundary is refused).
- README install section mentions Windows.
- `docs/windows-checklist.md` as above.
- Release notes for the first release with Windows assets.
