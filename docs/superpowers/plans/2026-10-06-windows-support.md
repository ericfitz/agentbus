# Windows Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run agentbus natively on Windows (amd64 and arm64) and under WSL, refuse a database shared across the Windows–WSL boundary, and ship Windows zips with `install.ps1`, a Scoop bucket and winget, all covered by the signed `SHA256SUMS` from #34.

**Architecture:** Platform-specific behavior moves behind build tags in small files next to the code it serves: a new `internal/filelock` package (flock vs `LockFileEx`), `procs_windows.go` (Toolhelp32 snapshot + `GetProcessTimes`), `terminateWait` (SIGTERM vs `TerminateProcess(h, 3)`), `shellEditorCommand` (`/bin/sh -c` vs `cmd /S /C`), `defaultDataDirectory`/`defaultConfigPath`, and a boundary guard in `bus.Open` whose decision logic is pure and tested with recorded `mountinfo` and paths on every OS. Tests that need a live child process use the re-exec helper pattern instead of `sleep`. Packaging extends `release/release.sh` (#34) with Windows zips, a `file` check, `install.ps1`, a Scoop manifest template and komac; `.github/workflows/windows.yml` runs the Go tests and the `install.ps1` harness on `windows-latest` and `windows-11-arm`.

**Tech Stack:** Go 1.27, `golang.org/x/sys/windows` (v0.47.0, already required), PowerShell 5.1/7, GitHub Actions (SHA-pinned), Scoop, komac, OpenSSL 3, bash release tooling from #34.

**Spec:** `docs/superpowers/specs/2026-10-06-windows-support-design.md`

## Global Constraints

- **Release notes (orchestrator override, 2026-10-07):** release notes files are written in the `chore: release vX.Y.Z` commit at release time (`git log -- release/notes-v1.13.0.md`), never in a feature commit. Wherever a task below says to create, append to or `git add` `release/notes-v1.14.0.md` (or any `release/notes-*.md`), do not touch the file: put that text in the execution's final report for the release author instead, and drop the path from the commit.
- Depends on #34 (`docs/superpowers/plans/2026-10-06-linux-releases.md`) being merged: this plan extends `release/release.sh`'s functions (`require_tools`, `copy_scripts`, `write_sums`, `upload_assets`, `render_template`, `sum_of`, `main`), `release/test-release.sh`, `release/test-render.sh`, `release/embed-key.sh` (key block markers) and `make release-check`. #36 later removes `build_windows` and `check_windows_pe` from this plan, so keep those names.
- Windows paths: config `%AppData%\agentbus\config.json`, data `%LocalAppData%\agentbus`; `AGENTBUS_CONFIG`, `AGENTBUS_DATA_DIR` and `data_directory` override as on other platforms; Unix defaults unchanged.
- Separate buses on Windows and WSL; no new IPC; a data directory across the boundary is refused by `bus.Open` (filesystem `9p`/`drvfs` under WSL; `\\wsl$\`, `\\wsl.localhost\` or a drive mapped to them on Windows).
- Replaced waits keep exit code 3 on Windows (`TerminateProcess(h, 3)`).
- `shells` adds `cmd`, `powershell`, `pwsh`; comparison is case-insensitive without a trailing `.exe`.
- Windows release assets: `agentbus-<tag>-windows-amd64.zip`, `agentbus-<tag>-windows-arm64.zip` (each holding `agentbus.exe`, built `CGO_ENABLED=0 GOOS=windows -trimpath` with the version ldflags) and `install.ps1`, all in `SHA256SUMS`. `release.sh` checks each exe with `file` (`PE32+ executable (console) x86-64, for MS Windows` / `... Aarch64, ...`).
- `install.ps1`: Windows PowerShell 5.1 and PowerShell 7; default install dir `%LocalAppData%\Programs\agentbus`; OpenSSL 3 from `PATH`, then Git for Windows (`usr\bin\openssl.exe`, `mingw64\bin\openssl.exe` under `git --exec-path`'s root, else `%ProgramFiles%\Git`); `AGENTBUS_SKIP_SIGNATURE` accepts only unset or `1`; never runs `agentbus init`.
- Scoop manifest rendered from `release/agentbus.scoop.json.tmpl` into `ericfitz/scoop-bucket` `bucket/agentbus.json`; winget package `ericfitz.agentbus` via `komac update ... --submit` with the token from `~/.keys/KOMAC_GITHUB_KEY` (sourceable file exporting `KOMAC_GITHUB_KEY`), scoped to that one command; komac failure does not fail the release.
- `make verify` stays the done gate and gains `vet-cross` (compile-only checks for `windows/amd64`, `windows/arm64`, `linux/amd64`); the Windows workflow reports and does not gate. Nothing in this plan runs Go tests on Windows locally: the implementer's Windows evidence is `GOOS=windows go vet ./...` (compiles the tests) and the workflow after the user pushes.
- Every GitHub Action is pinned to a full commit SHA with the version in a comment (pins in Task 11).
- No step pushes, creates the `ericfitz/scoop-bucket` repository, runs `komac new`, or publishes; those are user instructions with an `Approve:` line.
- Shell scripts beyond one-liners are bash with `set -euo pipefail` (`install.sh` stays POSIX sh); PowerShell scripts use `Set-StrictMode -Version 2.0` and `$ErrorActionPreference = 'Stop'`. Executor searches use `rg PATTERN <path>`.
- American English in code, comments and docs.

## Review Focus

- A pid reused by another process between a wait's start and its replacement: `StartTime` must differ so `Alive` says no and nothing is terminated. On Windows the creation time comes from `GetProcessTimes`; a terminated process whose handle someone still holds can still be opened, so a non-zero exit time must count as gone. Pinned in Task 2 (`TestSystemGone` runs on every OS via the helper process).
- `%LOCALAPPDATA%` unset (a service account, a stripped environment): `Default().DataDirectory` must still be a usable `~`-relative path, not an empty string that `validate` rejects. Pinned in Task 4.
- An editor setting with spaces in its path *and* arguments on Windows (`"C:\Program Files\Microsoft VS Code\Code.exe" --wait`): `cmd /S /C "<ed> "<path>""` must preserve both quoted parts. Pinned in Task 5 (`CmdLine` test).
- `AGENTBUS_DATA_DIR=/mnt/c/...` under WSL where `/mnt/c` is a nested `ext4` bind: the longest matching mount wins, so a nested Linux-native mount under a 9p mount point is allowed. Pinned in Task 6.
- `install.ps1` run from `irm | iex`: a refusal must `throw`, not `exit`, so the user's shell survives; and a relative `AGENTBUS_INSTALL_DIR` must be rejected because the user-`PATH` edit would store a relative entry. Pinned in Task 9 (harness cases `relative-dir` and the `-File` exit code of 1 for every refusal).

---

### Windows-dependent test inventory

Tests that use Unix-only behavior and what this plan does with each. Every executor task that touches one of these names it again.

| Test | Unix dependency | Action (task) |
|---|---|---|
| `procs.TestSystemGone`, `cli.startSleeper`, `cli.TestAcquireWaitLockStaleNotSignaled` | `exec.Command("sleep", "60")`, `Comm == "sleep"` | helper process (Tasks 2, 3) |
| `cli.TestAcquireWaitLockReplacesLiveWait` | "died from SIGTERM" | asserts exit code 3 on Windows (Task 3) |
| `cli.TestWaitPidfileSanitizesIdentity` | `"/d/wait"` literal | `filepath.Join` (Task 3) |
| `config.TestDataDirEnv...` | `/tmp/override` is not absolute on Windows | absolute temp path (Task 4) |
| `tui.TestEditorCommandPrecedence`, `TestEditorErrTextExplainsMissingCommand`, second half of `TestVisualEditorRunsInBackground` | `/bin/sh`, `true` | moved to `editor_unix_test.go`; Windows counterparts in `editor_windows_test.go` (Task 5) |
| `bus.TestDataDirWithURIMetacharactersOpensAndStaysIsolated` | `?` in a directory name is illegal on Windows | skipped on Windows with that reason (Task 6) |
| `bus.inspect_test.go` fixtures, `mcpserver.TestHookRunsInSendingProcess` | `#!/bin/sh` scripts | skipped on Windows with that reason (Task 7) |
| `mcpserver.TestCleanShutdownFreesNameImmediately/sigterm` | `Process.Signal(SIGTERM)` | subtest skipped on Windows; `disconnect` covers stdin close (Task 7) |
| `mcpserver.TestMain` | builds `agentbus` without `.exe` | `.exe` suffix on Windows (Task 7) |
| `mcpserver` log rotation tests | rename of an open file | runs as is: Go opens files with `FILE_SHARE_DELETE` on Windows (verified in Go 1.27's `internal/syscall/windows/at_windows.go`) |

---

### Task 1: `internal/filelock` and its two users

**Files:**
- Create: `internal/filelock/filelock.go`, `internal/filelock/filelock_unix.go`, `internal/filelock/filelock_windows.go`, `internal/filelock/filelock_test.go`
- Modify: `internal/cli/waitlock.go:26-39` (`flockFile`), `internal/mcpserver/logfile.go:58-70` (`lockAcrossProcesses`)

**Interfaces:**
- Produces: `package filelock`; `func Lock(f *os.File) error` takes an exclusive, blocking, cross-process lock on `f`, released when `f` is closed (or the process exits). Used by Task 3 and by `logfile.go`.

- [ ] **Step 1: Write the failing test** `internal/filelock/filelock_test.go`:

```go
package filelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A second handle blocks in Lock until the first handle is closed; closing
// is the only release.
func TestLockExcludesSecondHandleUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a := open(t, path)
	if err := Lock(a); err != nil {
		t.Fatal(err)
	}
	b := open(t, path)
	defer func() { _ = b.Close() }()
	got := make(chan error, 1)
	go func() { got <- Lock(b) }()
	select {
	case err := <-got:
		t.Fatalf("second lock acquired while the first is held (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("second lock after close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second lock never acquired after the first handle closed")
	}
}

// Locking an empty file works (the lock covers a byte range past EOF on
// Windows; flock does not care).
func TestLockEmptyFile(t *testing.T) {
	f := open(t, filepath.Join(t.TempDir(), "empty.lock"))
	defer func() { _ = f.Close() }()
	if err := Lock(f); err != nil {
		t.Fatal(err)
	}
}

// A closed file cannot be locked: the error must surface, not panic.
func TestLockClosedFileErrors(t *testing.T) {
	f := open(t, filepath.Join(t.TempDir(), "closed.lock"))
	_ = f.Close()
	if err := Lock(f); err == nil {
		t.Fatal("Lock on a closed file must fail")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd <repo root> && go test ./internal/filelock/`
Expected: build failure, `undefined: Lock`.

- [ ] **Step 3: Write the package**

`internal/filelock/filelock.go`:
```go
// Package filelock takes exclusive, blocking, cross-process locks on open
// files: flock(LOCK_EX) on Unix, LockFileEx(LOCKFILE_EXCLUSIVE_LOCK) on
// Windows. A lock is released when the file is closed or the process exits,
// so callers hold it for a short critical section and close the file.
package filelock
```

`internal/filelock/filelock_unix.go`:
```go
//go:build unix

package filelock

import (
	"os"
	"syscall"
)

// Lock takes an exclusive flock on f, blocking until it is free. Closing f
// releases it (the lock belongs to the open file description).
func Lock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}
```

`internal/filelock/filelock_windows.go`:
```go
//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// Lock takes an exclusive LockFileEx lock on the first byte of f (a byte
// range may extend past EOF, so an empty lock file works), blocking until
// it is free. Closing f releases it.
func Lock(f *os.File) error {
	h := windows.Handle(f.Fd())
	if h == windows.InvalidHandle {
		return os.ErrClosed
	}
	return windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}
```

Note: `f.Fd()` on a closed `*os.File` returns `^uintptr(0)` (`InvalidHandle`) on both platforms; on Unix `syscall.Flock(-1, ...)` returns `EBADF`, which satisfies `TestLockClosedFileErrors`.

- [ ] **Step 4: Switch the two callers**

In `internal/cli/waitlock.go`, replace the `syscall.Flock` call in `flockFile` with `filelock.Lock(f)`, import `github.com/ericfitz/agentbus/internal/filelock`, and keep the `syscall` import only if Task 3 has not yet moved the `Kill` call (it will be removed there). In `internal/mcpserver/logfile.go`, replace `syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)` with `filelock.Lock(lf)`, drop the `syscall` import, and change the comment "darwin and linux only, matching this project's targets" to "flock on Unix, LockFileEx on Windows (internal/filelock)".

- [ ] **Step 5: Run the tests**

Run: `cd <repo root> && go test ./internal/filelock/ ./internal/cli/ ./internal/mcpserver/ && GOOS=windows GOARCH=amd64 go vet ./internal/filelock/ ./internal/mcpserver/`
Expected: PASS for the three packages; the Windows vet of `filelock` and `mcpserver` compiles (the `cli` package still fails to compile on Windows until Task 3).

- [ ] **Step 6: Commit**

```bash
git add internal/filelock internal/cli/waitlock.go internal/mcpserver/logfile.go
git commit -m "filelock: exclusive file locks on Unix and Windows"
```

---

### Task 2: Process table on Windows and portable sleeper tests

**Files:**
- Create: `internal/procs/procs_windows.go`, `internal/procs/procs_other_unix.go`, `internal/procs/procs_other_stub.go`, `internal/procs/helper_test.go`
- Modify: `internal/procs/procs_other.go` (build tag, drop `exists`), `internal/procs/harness.go` (`shells`, `isShell`), `internal/procs/procs_test.go` (`TestSystemGone`), `internal/procs/harness_test.go` (cases)

**Interfaces:**
- Produces: `procs.System` works on Windows; `func isShell(comm string) bool` (package-private); test helpers `sleeperCommand() *exec.Cmd` and `helperComm() string` and the entry point `TestHelperSleep` (the same three are duplicated in `internal/cli/helper_test.go` in Task 3; test packages cannot share them).

- [ ] **Step 1: Write the failing tests.** Create `internal/procs/helper_test.go`:

```go
package procs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHelperSleep is not a test: sleeperCommand runs this test binary with
// AGENTBUS_TEST_HELPER=sleep so tests have a live child to inspect on every
// OS (Windows has no `sleep`). It is skipped in a normal run.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("AGENTBUS_TEST_HELPER") != "sleep" {
		t.Skip("helper process entry point")
	}
	time.Sleep(60 * time.Second)
}

// sleeperCommand is a child process that lives for 60 s unless killed.
func sleeperCommand() *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$")
	cmd.Env = append(os.Environ(), "AGENTBUS_TEST_HELPER=sleep")
	return cmd
}

// helperComm is the command name the process table reports for the helper
// (the test binary's base name; Linux truncates comm to 15 bytes, which
// "procs.test" fits).
func helperComm() string {
	return strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
}
```

Replace `TestSystemGone` in `internal/procs/procs_test.go` with:

```go
func TestSystemGone(t *testing.T) {
	cmd := sleeperCommand()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	start, err := System.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := System.Comm(pid); strings.TrimSuffix(c, ".exe") != helperComm() {
		t.Fatalf("Comm = %q, want %s", c, helperComm())
	}
	if ppid, err := System.Parent(pid); err != nil || ppid != os.Getpid() {
		t.Fatalf("Parent(child) = %d, %v; want %d", ppid, err, os.Getpid())
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if Alive(System, Ref{pid, start}) {
		t.Fatal("killed and reaped process must not be alive")
	}
	if _, err := System.Parent(pid); err != ErrGone {
		t.Fatalf("Parent of gone pid = %v, want ErrGone", err)
	}
	if _, err := System.StartTime(pid); err != ErrGone {
		t.Fatalf("StartTime of gone pid = %v, want ErrGone", err)
	}
}
```

Add `"strings"` to that file's imports. In `internal/procs/harness_test.go`, add these cases to the table in `TestFindHarness`:

```go
		{"windows hook via git bash", fakeTable{10: {20, "agentbus.exe", 1}, 20: {30, "bash.exe", 2}, 30: {40, "claude.exe", 3}, 40: {1, "explorer.exe", 4}}, Ref{30, 3}, nil},
		{"windows cmd and powershell", fakeTable{10: {20, "agentbus.exe", 1}, 20: {25, "cmd.exe", 2}, 25: {30, "PowerShell.EXE", 3}, 30: {35, "pwsh.exe", 4}, 35: {1, "codex.exe", 5}}, Ref{35, 5}, nil},
		{"exe suffix is not a shell marker", fakeTable{10: {20, "agentbus.exe", 1}, 20: {1, "claude.exe", 2}}, Ref{20, 2}, nil},
```

and a new test:

```go
func TestIsShell(t *testing.T) {
	for name, want := range map[string]bool{
		"zsh": true, "-zsh": true, "/bin/bash": true, "fish": true,
		"cmd": true, "cmd.exe": true, "CMD.EXE": true, "powershell.exe": true, "PowerShell.EXE": true, "pwsh": true, "pwsh.exe": true, "bash.exe": true,
		"claude": false, "claude.exe": false, "codex": false, "cmdx": false, "cmd.exe.exe": false, "": false,
	} {
		if got := isShell(name); got != want {
			t.Errorf("isShell(%q) = %v, want %v", name, got, want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd <repo root> && go test ./internal/procs/ -run 'TestIsShell|TestFindHarness|TestSystemGone'`
Expected: `undefined: isShell`.

- [ ] **Step 3: Implement.** `internal/procs/harness.go`: replace the `shells` declaration and the comparison with:

```go
// shells are the command names FindHarness walks past: the background shell a
// harness spawns to run a command (`zsh -c ...`, `cmd /c ...`), and the login
// shell a harness itself may have been started from are not the harness.
// Claude Code on Windows runs hooks through Git Bash, so a stop hook's
// ancestry there is claude.exe -> bash.exe -> agentbus.exe.
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"cmd": true, "powershell": true, "pwsh": true,
}

// isShell reports whether a process table command name is a shell: compared
// by basename, with a login shell's leading '-' stripped, case-insensitively
// and without one trailing ".exe" (Windows reports "Bash.EXE", "cmd.exe").
func isShell(comm string) bool {
	name := strings.ToLower(strings.TrimPrefix(filepath.Base(comm), "-"))
	return shells[strings.TrimSuffix(name, ".exe")]
}
```

and in `FindHarness` change `if !shells[strings.TrimPrefix(filepath.Base(comm), "-")] {` to `if !isShell(comm) {`.

`internal/procs/procs_other.go`: change the build tag to `//go:build !darwin && !linux && !windows` and delete the `exists` function and the `syscall` import (keep `errors`). Create `internal/procs/procs_other_unix.go`:

```go
//go:build unix && !darwin && !linux

package procs

import (
	"errors"
	"syscall"
)

// exists reports whether pid is a live process (kill(pid, 0); EPERM means it
// exists but belongs to someone else).
func exists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
```

and `internal/procs/procs_other_stub.go`:

```go
//go:build !unix && !windows

package procs

// exists cannot be answered on this platform; every pid is assumed live, so
// Alive degrades to "unknown" and nothing is ever signaled by mistake.
func exists(int) bool { return true }
```

Create `internal/procs/procs_windows.go`:

```go
//go:build windows

package procs

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type systemTable struct{}

// snapshotEntry finds pid in a Toolhelp32 snapshot of the process table.
func snapshotEntry(pid int) (windows.ProcessEntry32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return windows.ProcessEntry32{}, err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if int(e.ProcessID) == pid {
			return e, nil
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return windows.ProcessEntry32{}, ErrGone
	}
	return windows.ProcessEntry32{}, err
}

func (systemTable) Parent(pid int) (int, error) {
	e, err := snapshotEntry(pid)
	if err != nil {
		return 0, err
	}
	return int(e.ParentProcessID), nil
}

// Comm is the executable file name, with its extension ("claude.exe").
func (systemTable) Comm(pid int) (string, error) {
	e, err := snapshotEntry(pid)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(windows.UTF16ToString(e.ExeFile[:])), nil
}

// StartTime is the creation time from GetProcessTimes, in 100 ns units
// since 1601 (opaque: only equality matters). A pid that does not exist
// returns ErrGone, as does a process that has exited but whose handle
// someone still holds (its exit time is set). A live process this user may
// not open returns 0 (unknown), like procs_other.go.
func (systemTable) StartTime(pid int) (int64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
			return 0, ErrGone
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			if _, serr := snapshotEntry(pid); serr != nil {
				return 0, serr
			}
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	if exit.LowDateTime != 0 || exit.HighDateTime != 0 {
		return 0, ErrGone
	}
	return int64(creation.HighDateTime)<<32 | int64(creation.LowDateTime), nil
}
```

- [ ] **Step 4: Run the tests and the cross-OS compile**

Run: `cd <repo root> && go test ./internal/procs/ -count=1 && GOOS=windows GOARCH=amd64 go vet ./internal/procs/ && GOOS=windows GOARCH=arm64 go vet ./internal/procs/ && GOOS=freebsd go build ./internal/procs/ && GOOS=js GOARCH=wasm go build ./internal/procs/`
Expected: PASS (`TestHelperSleep` shows as SKIP under `-v`); every cross build silent.

- [ ] **Step 5: Commit**

```bash
git add internal/procs
git commit -m "procs: Windows process table, shell names with .exe, portable sleeper tests"
```

---

### Task 3: Replacing a running wait on Windows

**Files:**
- Create: `internal/cli/waitlock_unix.go`, `internal/cli/waitlock_windows.go`, `internal/cli/helper_test.go`
- Modify: `internal/cli/waitlock.go` (drop `syscall`, call `terminateWait`), `internal/cli/waitlock_test.go` (`startSleeper`, `TestAcquireWaitLockStaleNotSignaled`, `TestAcquireWaitLockReplacesLiveWait`, `TestWaitPidfileSanitizesIdentity`), `internal/cli/harness_test.go` (`killRef` unchanged; it uses `os.FindProcess` + `Kill`, which is portable)

**Interfaces:**
- Produces: `func terminateWait(pid int) error` (package `cli`): SIGTERM on Unix; `OpenProcess(PROCESS_TERMINATE)` + `TerminateProcess(h, 3)` on Windows.

- [ ] **Step 1: Write the failing tests.** Create `internal/cli/helper_test.go` with the same content as `internal/procs/helper_test.go` from Task 2 Step 1, with `package cli`. Then in `internal/cli/waitlock_test.go`:

Replace `startSleeper`'s `exec.Command("sleep", "60")` with `sleeperCommand()`, and in `TestAcquireWaitLockStaleNotSignaled` replace the second `exec.Command("sleep", "60")` with `sleeperCommand()`. Replace the body of the `select` in `TestAcquireWaitLockReplacesLiveWait` with:

```go
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old wait should have been terminated")
		}
		if runtime.GOOS == "windows" {
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 3 {
				t.Fatalf("replaced wait must exit 3 on Windows, got %v", err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old wait was not terminated")
	}
```

(add `"errors"` and `"runtime"` imports). Change `TestWaitPidfileSanitizesIdentity` to compare with `filepath.Join("/d", "wait")`:

```go
	if filepath.Dir(got) != filepath.Join("/d", "wait") {
```

Do not add a test that calls `terminateWait` on a reaped pid: Windows reuses pids quickly, and on a busy runner that would terminate an unrelated process. The stale-pid path is pinned by `TestAcquireWaitLockStaleNotSignaled` (start-time mismatch and gone process), which is the only way production reaches `terminateWait`: after `procs.Alive` confirms the incarnation.

- [ ] **Step 2: Run to verify it fails**

Run: `cd <repo root> && go test ./internal/cli/ -run 'TestAcquireWaitLock' -count=1`
Expected: `undefined: terminateWait`.

- [ ] **Step 3: Implement.** `internal/cli/waitlock_unix.go`:

```go
//go:build unix

package cli

import "syscall"

// terminateWait stops the wait process pid the way a replacing wait does on
// Unix: SIGTERM, which the wait's signal handler (installed in main before
// acquireWaitLock) turns into ErrWaitReplaced and exit code 3.
func terminateWait(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
```

`internal/cli/waitlock_windows.go`:

```go
//go:build windows

package cli

import "golang.org/x/sys/windows"

// terminateWait ends the wait process pid with exit code 3, the code a
// replaced wait exits with on Unix (ErrWaitReplaced): no signal except
// Ctrl+C reaches a Windows console process, so the replaced wait is
// terminated outright. Its own cleanup is skipped, which is safe: the
// caller overwrites the pidfile, and SQLite recovers from an abrupt exit.
func terminateWait(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.TerminateProcess(h, 3)
}
```

In `internal/cli/waitlock.go`: replace `_ = syscall.Kill(old.Pid, syscall.SIGTERM) // already gone is fine` with `_ = terminateWait(old.Pid) // already gone is fine`, remove the `syscall` import, and update `acquireWaitLock`'s comment "signals SIGTERM to the wait it replaces" to "stops the wait it replaces (terminateWait: SIGTERM on Unix, TerminateProcess with exit code 3 on Windows)".

- [ ] **Step 4: Run the tests and the cross-OS compile**

Run: `cd <repo root> && go test ./internal/cli/ -count=1 && GOOS=windows GOARCH=amd64 go vet ./internal/cli/ ./internal/procs/ ./internal/filelock/`
Expected: PASS; vet silent.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "cli: replace a running wait with TerminateProcess(3) on Windows"
```

---

### Task 4: Default paths on Windows

**Files:**
- Create: `internal/config/paths_unix.go`, `internal/config/paths_windows.go`, `internal/config/paths_unix_test.go`, `internal/config/paths_windows_test.go`
- Modify: `internal/config/config.go` (`Default`, `DefaultPath`), `internal/config/config_test.go:190-196`

**Interfaces:**
- Produces: `func defaultDataDirectory() string`, `func defaultConfigPath() (string, error)` (package-private, per platform). `config.DefaultPath()` keeps its signature.

- [ ] **Step 1: Write the failing tests.** `internal/config/paths_unix_test.go`:

```go
//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsUnix(t *testing.T) {
	if got := Default().DataDirectory; got != "~/.local/share/agentbus" {
		t.Fatalf("data directory = %q", got)
	}
	home, _ := os.UserHomeDir()
	p, err := DefaultPath()
	if err != nil || p != filepath.Join(home, ".config", "agentbus", "config.json") {
		t.Fatalf("DefaultPath = %q, %v", p, err)
	}
}
```

`internal/config/paths_windows_test.go`:

```go
//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsWindows(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\pat\AppData\Local`)
	t.Setenv("APPDATA", `C:\Users\pat\AppData\Roaming`)
	if got := Default().DataDirectory; got != `C:\Users\pat\AppData\Local\agentbus` {
		t.Fatalf("data directory = %q", got)
	}
	p, err := DefaultPath()
	if err != nil || p != `C:\Users\pat\AppData\Roaming\agentbus\config.json` {
		t.Fatalf("DefaultPath = %q, %v", p, err)
	}
	// Without LOCALAPPDATA the default stays a usable ~-relative path that
	// validate accepts and resolve expands.
	t.Setenv("LOCALAPPDATA", "")
	if got := Default().DataDirectory; got != "~/AppData/Local/agentbus" {
		t.Fatalf("data directory without LOCALAPPDATA = %q", got)
	}
	cfg, _, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if cfg.DataDirectory != filepath.Join(home, "AppData", "Local", "agentbus") {
		t.Fatalf("resolved data directory = %q", cfg.DataDirectory)
	}
}
```

In `internal/config/config_test.go`, replace the `/tmp/override` lines (190-196) with an absolute temp path:

```go
	override := filepath.Join(t.TempDir(), "override")
	t.Setenv("AGENTBUS_DATA_DIR", override)
	c, _, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDirectory != override {
		t.Fatalf("AGENTBUS_DATA_DIR not applied: %s", c.DataDirectory)
	}
```

(keep whatever assertion text the existing lines use for the error branch; only the path changes).

- [ ] **Step 2: Run to verify it fails**

Run: `cd <repo root> && go test ./internal/config/ -run TestDefaultPaths -count=1 && GOOS=windows go vet ./internal/config/`
Expected: the Unix test passes already (behavior unchanged) but the Windows vet reports nothing yet, so the signal is the next step: after Step 3 both must still pass. (TDD here pins the Unix behavior before the refactor.)

- [ ] **Step 3: Implement.** `internal/config/paths_unix.go`:

```go
//go:build !windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultDataDirectory is the data directory before resolve expands "~/".
func defaultDataDirectory() string { return "~/.local/share/agentbus" }

// defaultConfigPath is ~/.config/agentbus/config.json (not XDG_CONFIG_HOME,
// unchanged from before Windows support).
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "agentbus", "config.json"), nil
}
```

`internal/config/paths_windows.go`:

```go
//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultDataDirectory is %LocalAppData%\agentbus: Local, not Roaming,
// because the SQLite database must not roam with a domain profile. Without
// LOCALAPPDATA it falls back to the same place relative to the home
// directory, which resolve expands.
func defaultDataDirectory() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "agentbus")
	}
	return "~/AppData/Local/agentbus"
}

// defaultConfigPath is %AppData%\agentbus\config.json (os.UserConfigDir).
func defaultConfigPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve AppData: %w", err)
	}
	return filepath.Join(d, "agentbus", "config.json"), nil
}
```

In `config.go`: `DataDirectory: "~/.local/share/agentbus",` becomes `DataDirectory: defaultDataDirectory(),`; `DefaultPath`'s body becomes `return defaultConfigPath()` with the doc comment "DefaultPath is the config file used when neither --config nor AGENTBUS_CONFIG is set: ~/.config/agentbus/config.json on Unix, %AppData%\agentbus\config.json on Windows."

- [ ] **Step 4: Run the tests and the cross-OS compile**

Run: `cd <repo root> && go test ./internal/config/ -count=1 && GOOS=windows GOARCH=amd64 go vet ./internal/config/`
Expected: PASS; vet silent.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "config: AppData and LocalAppData defaults on Windows"
```

---

### Task 5: Editor launch on Windows

**Files:**
- Create: `internal/tui/editor_unix.go`, `internal/tui/editor_windows.go`, `internal/tui/editor_unix_test.go`, `internal/tui/editor_windows_test.go`
- Modify: `internal/tui/health.go:185-247` (`editorCommand`, `editorSetting`, `editorErrText`), `internal/tui/health_test.go:108-191` (move three tests)

**Interfaces:**
- Produces: `func shellEditorCommand(ed, path string) *exec.Cmd`, `const defaultEditor`, `const exitCommandNotFound`, `const exitNotExecutable` (package-private, per platform).

- [ ] **Step 1: Move the Unix-only tests.** Cut `TestEditorCommandPrecedence`, `TestEditorErrTextExplainsMissingCommand`, and the second half of `TestVisualEditorRunsInBackground` (from `f := newFixture(t)` to the end of that function) out of `health_test.go` into `internal/tui/editor_unix_test.go` with header `//go:build !windows` and `package tui`; the moved half becomes `TestVisualEditorBackgroundCommandUnix`. Keep the classification loop in `health_test.go`'s `TestVisualEditorRunsInBackground`. Then write `internal/tui/editor_windows_test.go`:

```go
//go:build windows

package tui

import (
	"strings"
	"testing"
)

func TestShellEditorCommandWindows(t *testing.T) {
	cmd := shellEditorCommand(`"C:\Program Files\Microsoft VS Code\Code.exe" --wait`, `C:\Users\pat h\config.json`)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != `cmd.exe /S /C ""C:\Program Files\Microsoft VS Code\Code.exe" --wait "C:\Users\pat h\config.json""` {
		t.Fatalf("CmdLine = %q", cmd.SysProcAttr.CmdLine)
	}
	if defaultEditor != "notepad" {
		t.Fatalf("default editor = %q", defaultEditor)
	}
}

func TestEditorErrTextExplainsMissingCommandWindows(t *testing.T) {
	t.Setenv("VISUAL", "no-such-editor-xyz")
	err := editorCommand("x.md").Run()
	if got := editorErrText(err); !strings.Contains(got, "not found") || !strings.Contains(got, "no-such-editor-xyz") || !strings.Contains(got, "$VISUAL") {
		t.Fatalf("editorErrText(%v) = %q", err, got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd <repo root> && GOOS=windows GOARCH=amd64 go vet ./internal/tui/`
Expected: `undefined: shellEditorCommand`, `defaultEditor`.

- [ ] **Step 3: Implement.** `internal/tui/editor_unix.go`:

```go
//go:build !windows

package tui

import "os/exec"

const defaultEditor = "vi"

// The shell exits 127 when the editor command does not exist and 126 when
// it cannot be run.
const (
	exitCommandNotFound = 127
	exitNotExecutable   = 126
)

// shellEditorCommand runs an editor setting through the shell the way git
// runs GIT_EDITOR, with the file as $1, so arguments and quoting work.
func shellEditorCommand(ed, path string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", ed+` "$1"`, "sh", path)
}
```

`internal/tui/editor_windows.go`:

```go
//go:build windows

package tui

import (
	"os/exec"
	"syscall"
)

const defaultEditor = "notepad"

// cmd.exe exits 9009 when the command is not found; it has no distinct
// "not executable" code, so that constant never matches.
const (
	exitCommandNotFound = 9009
	exitNotExecutable   = -1
)

// shellEditorCommand runs an editor setting through cmd.exe. The command
// line is built by hand: with /S, cmd strips exactly the outer quotes, so a
// quoted program path with spaces and a quoted file path both survive
// (Go's default argument escaping would let cmd mangle them).
func shellEditorCommand(ed, path string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "` + ed + ` "` + path + `""`}
	return cmd
}
```

In `health.go`: `editorCommand`'s last line becomes `return shellEditorCommand(ed, path)`; its comment's "Anything else goes through the shell the way git runs GIT_EDITOR" gains "(cmd.exe on Windows, see editor_windows.go)"; `editorSetting` returns `defaultEditor, "default"` instead of `"vi", "default"`; `editorErrText`'s `case 127:` / `case 126:` become `case exitCommandNotFound:` / `case exitNotExecutable:`, and the comment "The shell exits 127 ... 126 ..." moves to the per-platform files. Update the `hints` text `"open config in $EDITOR"` only if it mentions `vi` (it does not).

- [ ] **Step 4: Run the tests and the cross-OS compile**

Run: `cd <repo root> && go test ./internal/tui/ -count=1 && GOOS=windows GOARCH=amd64 go vet ./internal/tui/`
Expected: PASS; vet silent.

- [ ] **Step 5: Commit**

```bash
git add internal/tui
git commit -m "tui: launch the editor through cmd.exe on Windows, default notepad"
```

---

### Task 6: Shared database guard in `bus.Open`

**Files:**
- Create: `internal/bus/boundary.go`, `internal/bus/boundary_linux.go`, `internal/bus/boundary_windows.go`, `internal/bus/boundary_other.go`, `internal/bus/boundary_test.go`, `internal/bus/boundary_linux_test.go`
- Modify: `internal/bus/bus.go:113-116` (`Open`), `internal/bus/bus_test.go:89` (skip on Windows)

**Interfaces:**
- Produces: `type BoundaryError struct{ Dir, Filesystem, Suggestion string }` (exported, so the CLI can `errors.As` it later); `func checkBoundary(dir string) error`; pure `func mountFSType(mountinfo, dir string) (fstype string, ok bool)` and `func wslPathRefusal(abs string, driveTarget func(drive string) (string, bool)) (fs string, refused bool)`; per-platform `func platformBoundary(dir string) (fs, suggestion string, refused bool)`; Linux `func isWSL(readFile func(string) ([]byte, error)) bool`; Windows `func driveTarget(drive string) (string, bool)`.

- [ ] **Step 1: Write the failing tests.** `internal/bus/boundary_test.go` (no build tag; runs everywhere):

```go
package bus

import (
	"errors"
	"strings"
	"testing"
)

// Recorded from a WSL2 Ubuntu session (abridged): / ext4, /tmp tmpfs,
// /mnt/c 9p (Windows C: drive), /mnt/d drvfs, and an ext4 mount nested
// under /mnt/c. A mount point with a space is escaped as \040.
const recordedMountinfo = `35 25 0:33 / / rw,relatime - ext4 /dev/sdc rw,discard,errors=remount-ro,data=ordered
60 35 0:41 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw
78 35 0:50 / /mnt/c rw,noatime - 9p drvfs rw,dirsync,aname=drvfs;path=C:\;uid=1000;gid=1000;symlinkroot=/mnt/
79 35 0:51 / /mnt/d rw,noatime - drvfs D: rw,dirsync
80 78 0:33 /nested /mnt/c/nested rw,relatime - ext4 /dev/sdc rw
81 35 0:52 / /mnt/with\040space rw - 9p drvfs rw
`

func TestMountFSType(t *testing.T) {
	cases := []struct {
		dir, want string
		ok        bool
	}{
		{"/home/pat/.local/share/agentbus", "ext4", true},
		{"/tmp/agentbus", "tmpfs", true},
		{"/mnt/c/Users/pat/agentbus", "9p", true},
		{"/mnt/c", "9p", true},
		{"/mnt/cx/agentbus", "ext4", true}, // /mnt/cx is not under /mnt/c
		{"/mnt/d/agentbus", "drvfs", true},
		{"/mnt/c/nested/agentbus", "ext4", true}, // nested Linux mount under a 9p mount point
		{"/mnt/with space/x", "9p", true},
		{"relative/dir", "", false},
	}
	for _, c := range cases {
		got, ok := mountFSType(recordedMountinfo, c.dir)
		if got != c.want || ok != c.ok {
			t.Errorf("mountFSType(%q) = %q, %v; want %q, %v", c.dir, got, ok, c.want, c.ok)
		}
	}
	if _, ok := mountFSType("garbage line\n\n- -\n", "/x"); ok {
		t.Fatal("malformed mountinfo must not match")
	}
}

func TestWSLPathRefusal(t *testing.T) {
	drives := func(d string) (string, bool) {
		switch d {
		case "Z:":
			return `\\wsl.localhost\Ubuntu\home\pat`, true
		case "Y:":
			return `\\fileserver\share`, true
		}
		return "", false
	}
	cases := []struct {
		abs     string
		refused bool
	}{
		{`\\wsl$\Ubuntu\home\pat\agentbus`, true},
		{`\\WSL$\Ubuntu\x`, true},
		{`\\wsl.localhost\Ubuntu\home\pat`, true},
		{`Z:\agentbus`, true},
		{`z:\agentbus`, true},
		{`Y:\agentbus`, false},
		{`C:\Users\pat\AppData\Local\agentbus`, false},
		{`\\server\wsl$\x`, false},
		{`\\wslx\Ubuntu`, false},
	}
	for _, c := range cases {
		fs, refused := wslPathRefusal(c.abs, drives)
		if refused != c.refused {
			t.Errorf("wslPathRefusal(%q) = %q, %v; want refused=%v", c.abs, fs, refused, c.refused)
		}
	}
	if _, refused := wslPathRefusal(`Z:\x`, nil); refused {
		t.Fatal("no drive resolver: a drive letter path is allowed")
	}
}

func TestBoundaryErrorText(t *testing.T) {
	err := error(&BoundaryError{Dir: "/mnt/c/x", Filesystem: "9p", Suggestion: "$HOME/.local/share/agentbus"})
	var be *BoundaryError
	if !errors.As(err, &be) {
		t.Fatal("BoundaryError must be matchable with errors.As")
	}
	for _, want := range []string{"/mnt/c/x", "9p", "one side", "$HOME/.local/share/agentbus"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}
```

`internal/bus/boundary_linux_test.go`:

```go
//go:build linux

package bus

import (
	"os"
	"testing"
)

func TestIsWSL(t *testing.T) {
	fake := func(files map[string]string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		}
	}
	if !isWSL(fake(map[string]string{"/proc/sys/fs/binfmt_misc/WSLInterop": "enabled\n"})) {
		t.Fatal("WSLInterop present must mean WSL")
	}
	if !isWSL(fake(map[string]string{"/proc/sys/kernel/osrelease": "5.15.167.4-microsoft-standard-WSL2\n"})) {
		t.Fatal("microsoft in osrelease must mean WSL")
	}
	if isWSL(fake(map[string]string{"/proc/sys/kernel/osrelease": "6.8.0-45-generic\n"})) {
		t.Fatal("a plain kernel is not WSL")
	}
}
```

In `internal/bus/bus_test.go`, at the top of `TestDataDirWithURIMetacharactersOpensAndStaysIsolated` add:

```go
	if runtime.GOOS == "windows" {
		t.Skip("'?' is not a legal file name character on Windows; the DSN escaping is covered on Unix")
	}
```

(add `"runtime"` to its imports).

- [ ] **Step 2: Run to verify it fails**

Run: `cd <repo root> && go test ./internal/bus/ -run 'TestMountFSType|TestWSLPathRefusal|TestBoundaryErrorText' -count=1`
Expected: `undefined: mountFSType` and friends.

- [ ] **Step 3: Implement.** `internal/bus/boundary.go`:

```go
package bus

import (
	"fmt"
	"path"
	"strings"
)

// BoundaryError says the data directory sits across the Windows-WSL
// boundary. SQLite's POSIX/Win32 locks and the WAL shared-memory index
// (agentbus.db-shm) do not work between Windows processes and WSL processes
// on one file, so concurrent use can corrupt the database (#35).
type BoundaryError struct {
	Dir        string
	Filesystem string
	Suggestion string
}

func (e *BoundaryError) Error() string {
	return fmt.Sprintf("data directory %s is on %s, across the Windows-WSL boundary: the database must stay on one side (SQLite locking does not work between Windows and WSL processes on one file); use a directory on this side's own disk, for example %s", e.Dir, e.Filesystem, e.Suggestion)
}

// checkBoundary refuses dir when platformBoundary (per OS) says it is
// across the boundary. Outside WSL, and on other filesystems, it is a no-op.
func checkBoundary(dir string) error {
	fs, suggestion, refused := platformBoundary(dir)
	if !refused {
		return nil
	}
	return &BoundaryError{Dir: dir, Filesystem: fs, Suggestion: suggestion}
}

// mountFSType returns the filesystem type of the mount containing dir
// according to mountinfo (the content of /proc/self/mountinfo): the longest
// mount point that equals dir or is a path prefix of it. dir must be
// absolute and already symlink-resolved; ok is false when nothing matches.
func mountFSType(mountinfo, dir string) (fstype string, ok bool) {
	dir = path.Clean(dir)
	if !path.IsAbs(dir) {
		return "", false
	}
	best := -1
	for _, line := range strings.Split(mountinfo, "\n") {
		// Fields: id parent major:minor root mountpoint options [optional
		// fields...] - fstype source superoptions. The "-" separator ends
		// the variable-length optional fields.
		f := strings.Fields(line)
		sep := -1
		for i, w := range f {
			if w == "-" {
				sep = i
				break
			}
		}
		if sep < 5 || sep+1 >= len(f) {
			continue
		}
		mp := path.Clean(unescapeMount(f[4]))
		if mp != dir && !strings.HasPrefix(dir, strings.TrimSuffix(mp, "/")+"/") {
			continue
		}
		if len(mp) > best {
			best, fstype, ok = len(mp), f[sep+1], true
		}
	}
	return fstype, ok
}

// unescapeMount decodes mountinfo's octal escapes for space, tab, newline
// and backslash.
func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// wslPathRefusal reports whether abs, a Windows path, is inside a WSL
// distribution: under \\wsl$\ or \\wsl.localhost\ directly, or through a
// mapped drive letter that driveTarget (nil: no mapping lookup) resolves
// to such a UNC path. fs names what was found, for the error.
func wslPathRefusal(abs string, driveTarget func(drive string) (string, bool)) (fs string, refused bool) {
	if isWSLUNC(abs) {
		return "a WSL distribution's filesystem (" + abs + ")", true
	}
	if len(abs) >= 2 && abs[1] == ':' && driveTarget != nil {
		drive := strings.ToUpper(abs[:1]) + ":"
		if target, ok := driveTarget(drive); ok && isWSLUNC(target) {
			return "drive " + drive + ", mapped to " + target + " (a WSL distribution's filesystem)", true
		}
	}
	return "", false
}

func isWSLUNC(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
	return strings.HasPrefix(p, "//wsl$/") || strings.HasPrefix(p, "//wsl.localhost/")
}
```

`internal/bus/boundary_linux.go`:

```go
//go:build linux

package bus

import (
	"os"
	"path/filepath"
	"strings"
)

// isWSL detects WSL from /proc: the WSLInterop binfmt entry, or "microsoft"
// in the kernel release. readFile is injected for tests.
func isWSL(readFile func(string) ([]byte, error)) bool {
	if _, err := readFile("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
		return true
	}
	b, err := readFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// platformBoundary refuses, under WSL, a directory whose mount is a Windows
// drive (9p or drvfs). Anything that cannot be determined is allowed.
func platformBoundary(dir string) (fs, suggestion string, refused bool) {
	if !isWSL(os.ReadFile) {
		return "", "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	mi, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", "", false
	}
	t, ok := mountFSType(string(mi), abs)
	if ok && (t == "9p" || t == "drvfs") {
		return t + " (a Windows drive mounted in WSL)", "$HOME/.local/share/agentbus", true
	}
	return "", "", false
}
```

`internal/bus/boundary_windows.go`:

```go
//go:build windows

package bus

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WNetGetConnectionW is not wrapped by x/sys/windows; it lives in mpr.dll.
var (
	modmpr                 = windows.NewLazySystemDLL("mpr.dll")
	procWNetGetConnectionW = modmpr.NewProc("WNetGetConnectionW")
)

// driveTarget resolves a mapped drive ("Z:") to the UNC path it maps to.
// ok is false for a local drive, an unmapped letter, or any error.
func driveTarget(drive string) (string, bool) {
	local, err := windows.UTF16PtrFromString(drive)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	r, _, _ := procWNetGetConnectionW.Call(uintptr(unsafe.Pointer(local)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r != 0 { // NO_ERROR is 0; ERROR_NOT_CONNECTED (2250) for a local drive
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

// platformBoundary refuses a directory inside a WSL distribution, reached
// directly by UNC path or through a mapped drive letter.
func platformBoundary(dir string) (fs, suggestion string, refused bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", false
	}
	fs, refused = wslPathRefusal(abs, driveTarget)
	return fs, `%LocalAppData%\agentbus`, refused
}
```

`internal/bus/boundary_other.go`:

```go
//go:build !linux && !windows

package bus

// platformBoundary: no Windows-WSL boundary exists on this platform.
func platformBoundary(string) (fs, suggestion string, refused bool) { return "", "", false }
```

In `bus.go`'s `Open`, right after the `MkdirAll` block:

```go
	if err := checkBoundary(cfg.DataDirectory); err != nil {
		return nil, err
	}
```

- [ ] **Step 4: Run the tests on macOS, under Linux in Docker, and the cross-OS compile**

Run: `cd <repo root> && go test ./internal/bus/ -count=1 && GOOS=windows GOARCH=amd64 go vet ./internal/bus/ && GOOS=linux GOARCH=arm64 go vet ./internal/bus/ && docker run --rm -v "$PWD:/src:ro" -w /src -e GOFLAGS=-buildvcs=false -e GOCACHE=/tmp/gocache golang:1.27 go test ./internal/bus/ -run 'TestIsWSL|TestMountFSType|TestOpen' -count=1`
Expected: PASS locally; vet silent; the container run passes `TestIsWSL` (Linux-only) and shows that `Open` on a non-WSL Linux host is unaffected.

- [ ] **Step 5: Commit**

```bash
git add internal/bus
git commit -m "bus: refuse a data directory across the Windows-WSL boundary"
```

---

### Task 7: Windows-aware test fixtures, `vet-cross` in the done gate

**Files:**
- Modify: `internal/mcpserver/integration_test.go:48` (`binary`), `:362` (`TestHookRunsInSendingProcess`), `:552-568` (`sigterm` subtest); `internal/bus/inspect_test.go:14-20` (script writer); `Makefile`

- [ ] **Step 1: Edit the fixtures.** In `integration_test.go`'s `TestMain`, after `binary = filepath.Join(dir, "agentbus")` add:

```go
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
```

At the top of `TestHookRunsInSendingProcess`:

```go
	if runtime.GOOS == "windows" {
		t.Skip("the hook fixture is a /bin/sh script; the inspection path is exercised on Unix")
	}
```

In `TestCleanShutdownFreesNameImmediately`, inside the subtest before `dir := t.TempDir()`:

```go
			if how == "sigterm" && runtime.GOOS == "windows" {
				t.Skip("no SIGTERM on Windows; a harness ends the server by closing stdin (the disconnect case)")
			}
```

(add `"runtime"` to the imports). In `internal/bus/inspect_test.go`'s script-writing helper (the function around line 14 that writes `#!/bin/sh`), add as its first statements:

```go
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("inspection command fixtures are /bin/sh scripts")
	}
```

(add `"runtime"`; `t.Skip` from a helper skips the calling test).

- [ ] **Step 2: Makefile.** Add `vet-cross` to `.PHONY` and to `verify` after `vet`:

```make
vet-cross: ## Compile every package and test for the other release targets; lint the Windows build (no cross-OS test run)
	GOOS=linux GOARCH=amd64 go vet ./...
	GOOS=windows GOARCH=amd64 go vet ./...
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build ./...
	GOOS=windows golangci-lint run ./...
```

and in `verify`, insert `$(MAKE) vet-cross` after `$(MAKE) vet`.

- [ ] **Step 3: Run the done gate**

Run: `cd <repo root> && make verify`
Expected: `verify: OK`, including the four `vet-cross` lines. Fix any Windows-only lint finding (unused parameters, missing error checks) in the files from Tasks 1-6.

- [ ] **Step 4: Commit**

```bash
git add internal/mcpserver/integration_test.go internal/bus/inspect_test.go Makefile
git commit -m "test: Windows-aware fixtures; make vet-cross in the done gate"
```

---

### Task 8: `install.ps1`

**Files:**
- Create: `install.ps1` (repository root)

**Interfaces:**
- Produces: `install.ps1` with the key block (`# BEGIN agentbus release public key` / `$PubKeyPem = @'...'@` / `# END ...`, rendered by `release/embed-key.sh`), functions `Get-Arch`, `Find-OpenSsl`, `Resolve-Tag`, `Get-Asset`, `Test-Signature`, `Test-Hash`, `Install-Binary`, `Add-UserPath`, `Main`. Every refusal is a `throw` whose message starts with `agentbus install:`, so `powershell -File install.ps1` exits 1 and `irm | iex` leaves the user's session open. Test hook: `AGENTBUS_TEST_OSARCH` overrides the detected architecture (documented in the header; only Task 9 sets it).

- [ ] **Step 1: Write `install.ps1`**

```powershell
# install.ps1: install agentbus on Windows into %LocalAppData%\Programs\agentbus.
#   irm https://github.com/ericfitz/agentbus/releases/latest/download/install.ps1 | iex
# Windows PowerShell 5.1 and PowerShell 7. Environment:
#   AGENTBUS_VERSION        vX.Y.Z to install (default: the latest release)
#   AGENTBUS_INSTALL_DIR    absolute directory (default: %LocalAppData%\Programs\agentbus)
#   AGENTBUS_BASE_URL       mirror or test server (default: https://github.com/ericfitz/agentbus)
#   AGENTBUS_SKIP_SIGNATURE 1 skips the OpenSSL signature check (the hash is still checked)
#   AGENTBUS_TEST_OSARCH    test hook: pretend the OS architecture is this value
# Downloads the zip, SHA256SUMS and SHA256SUMS.sig, verifies the Ed25519
# signature with OpenSSL 3 (Git for Windows ships one) and the public key
# below, verifies the zip's hash, then swaps agentbus.exe into place (a running
# copy is renamed, not overwritten) and adds the directory to the user PATH.
# Never runs agentbus init. A refusal throws, so a piped `iex` leaves the
# session open; `powershell -File install.ps1` exits 1.
#Requires -Version 5.1
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# BEGIN agentbus release public key
$PubKeyPem = @'
-----BEGIN PUBLIC KEY-----
REPLACED-BY-release/embed-key.sh
-----END PUBLIC KEY-----
'@
# END agentbus release public key

$script:Upgrade = $false

function Get-Arch {
    $a = if ($env:AGENTBUS_TEST_OSARCH) { $env:AGENTBUS_TEST_OSARCH } else { "$([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" }
    switch ($a) {
        'X64'   { return 'amd64' }
        'Arm64' { return 'arm64' }
        default { throw "agentbus install: unsupported architecture: $a (releases cover windows-amd64 and windows-arm64)" }
    }
}

function Get-OpenSslMajor([string]$Exe) {
    try { $v = (& $Exe version 2>$null | Select-Object -First 1) } catch { return $null }
    if ("$v" -match '^OpenSSL (\d+)\.') { return [int]$Matches[1] }
    return $null
}

# Find-OpenSsl returns the first OpenSSL 3+ among: openssl on PATH, then Git
# for Windows' usr\bin\openssl.exe and mingw64\bin\openssl.exe.
function Find-OpenSsl {
    $candidates = @()
    $onPath = Get-Command openssl -ErrorAction SilentlyContinue
    if ($onPath) { $candidates += $onPath.Source }
    $gitRoot = $null
    if (Get-Command git -ErrorAction SilentlyContinue) {
        try {
            $exec = (& git --exec-path 2>$null | Select-Object -First 1)
            if ($exec) { $gitRoot = (Resolve-Path (Join-Path $exec '..\..\..')).Path }
        } catch { $gitRoot = $null }
    }
    if (-not $gitRoot) { $gitRoot = Join-Path $env:ProgramFiles 'Git' }
    foreach ($rel in @('usr\bin\openssl.exe', 'mingw64\bin\openssl.exe')) {
        $p = Join-Path $gitRoot $rel
        if (Test-Path -LiteralPath $p) { $candidates += $p }
    }
    $old = $null
    foreach ($cand in $candidates) {
        $major = Get-OpenSslMajor $cand
        if ($major -ge 3) { return $cand }
        if ($major -and -not $old) { $old = "$cand ($(& $cand version 2>$null | Select-Object -First 1))" }
    }
    if ($old) {
        throw "agentbus install: found $old, but verifying the release signature needs OpenSSL 3. Install Git for Windows (winget install --id Git.Git -e), which ships OpenSSL 3, or rerun with AGENTBUS_SKIP_SIGNATURE=1 to verify the hash only"
    }
    throw "agentbus install: OpenSSL 3 is required to verify the release signature and none was found. Install Git for Windows: winget install --id Git.Git -e (or rerun with AGENTBUS_SKIP_SIGNATURE=1 to verify the hash only)"
}

# Resolve-Tag: AGENTBUS_VERSION, else the tag releases/latest redirects to
# (no API call, so no anonymous rate limit).
function Resolve-Tag([string]$Base) {
    $tag = $env:AGENTBUS_VERSION
    if (-not $tag) {
        $loc = $null
        try {
            $req = [System.Net.HttpWebRequest]::Create("$Base/releases/latest")
            $req.Method = 'HEAD'
            $req.AllowAutoRedirect = $false
            $resp = $req.GetResponse()
            $loc = $resp.Headers['Location']
            $resp.Close()
        } catch { $loc = $null }
        if ($loc) { $tag = ($loc.TrimEnd('/') -split '/')[-1] }
        if (-not $tag) { throw "agentbus install: could not determine the latest release from $Base/releases/latest; set AGENTBUS_VERSION=vX.Y.Z" }
    }
    if ($tag -notmatch '^v\d+\.\d+\.\d+') { throw "agentbus install: version must look like vX.Y.Z, got '$tag'" }
    return $tag
}

function Get-Asset([string]$Url, [string]$Out) {
    try { Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing } catch { throw "agentbus install: download failed: $Url" }
}

function Test-Signature([string]$OpenSsl, [string]$Dir) {
    $key = Join-Path $Dir 'release.pub'
    [IO.File]::WriteAllText($key, ($PubKeyPem -replace "`r", '') + "`n")
    # Windows PowerShell 5.1 turns captured stderr into a terminating error
    # under 'Stop'; openssl writes to stderr on a bad signature, so relax it
    # for this one call and report through the exit code.
    $eap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    & $OpenSsl pkeyutl -verify -rawin -pubin -inkey $key -in (Join-Path $Dir 'SHA256SUMS') -sigfile (Join-Path $Dir 'SHA256SUMS.sig') 2>&1 | Out-Null
    $code = $LASTEXITCODE
    $ErrorActionPreference = $eap
    if ($code -ne 0) { throw "agentbus install: signature check of SHA256SUMS failed: the download is damaged, tampered with, or signed with a key this script does not know" }
}

function Test-Hash([string]$Dir, [string]$Asset) {
    $pattern = '^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($Asset) + '$'
    $line = Get-Content -LiteralPath (Join-Path $Dir 'SHA256SUMS') | Where-Object { $_ -match $pattern } | Select-Object -First 1
    if (-not $line) { throw "agentbus install: SHA256SUMS has no entry for $Asset" }
    $want = ($line -split '\s+')[0].ToLower()
    $got = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Dir $Asset)).Hash.ToLower()
    if ($got -ne $want) { throw "agentbus install: checksum mismatch for $Asset" }
}

# Move-WithRetry: a rename or move racing another installer is retried once.
function Move-WithRetry([string]$From, [string]$To) {
    try { Move-Item -LiteralPath $From -Destination $To -Force }
    catch { Start-Sleep -Milliseconds 500; Move-Item -LiteralPath $From -Destination $To -Force }
}

function Install-Binary([string]$Dir, [string]$Asset, [string]$InstallDir) {
    $extract = Join-Path $Dir 'extract'
    Expand-Archive -LiteralPath (Join-Path $Dir $Asset) -DestinationPath $extract -Force
    $new = Join-Path $extract 'agentbus.exe'
    if (-not (Test-Path -LiteralPath $new)) { throw "agentbus install: $Asset does not contain agentbus.exe" }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path $InstallDir 'agentbus.exe'
    $script:Upgrade = Test-Path -LiteralPath $target
    if ($script:Upgrade) {
        # Windows allows renaming a running executable but not overwriting it.
        Move-WithRetry $target ("$target.old-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
    }
    Move-WithRetry $new $target
    Get-ChildItem -LiteralPath $InstallDir -Filter 'agentbus.exe.old-*' | ForEach-Object {
        try { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction Stop } catch { } # still running: a later run removes it
    }
}

function Add-UserPath([string]$Dir) {
    $user = [Environment]::GetEnvironmentVariable('Path', 'User')
    $parts = @()
    if ($user) { $parts = @($user -split ';' | Where-Object { $_ }) }
    if ($parts -contains $Dir) { return }
    [Environment]::SetEnvironmentVariable('Path', (($parts + $Dir) -join ';'), 'User')
    Write-Host "added $Dir to your user PATH; new terminals pick it up"
}

function Main {
    $base = if ($env:AGENTBUS_BASE_URL) { $env:AGENTBUS_BASE_URL.TrimEnd('/') } else { 'https://github.com/ericfitz/agentbus' }
    $skip = $false
    switch ("$env:AGENTBUS_SKIP_SIGNATURE") {
        ''      { $skip = $false }
        '1'     { $skip = $true; Write-Warning 'AGENTBUS_SKIP_SIGNATURE=1: skipping the signature check; the hash is still verified' }
        default { throw "agentbus install: AGENTBUS_SKIP_SIGNATURE must be 1 or unset, got '$env:AGENTBUS_SKIP_SIGNATURE'" }
    }
    $installDir = if ($env:AGENTBUS_INSTALL_DIR) { $env:AGENTBUS_INSTALL_DIR } else { Join-Path $env:LocalAppData 'Programs\agentbus' }
    if (-not [IO.Path]::IsPathRooted($installDir) -or $installDir -notmatch '^[A-Za-z]:\\|^\\\\') { throw "agentbus install: AGENTBUS_INSTALL_DIR must be an absolute path, got '$installDir'" }
    $arch = Get-Arch
    $openssl = if ($skip) { $null } else { Find-OpenSsl }
    $tag = Resolve-Tag $base
    $asset = "agentbus-$tag-windows-$arch.zip"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('agentbus-install-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $dl = "$base/releases/download/$tag"
        Get-Asset "$dl/$asset" (Join-Path $tmp $asset)
        Get-Asset "$dl/SHA256SUMS" (Join-Path $tmp 'SHA256SUMS')
        if (-not $skip) {
            Get-Asset "$dl/SHA256SUMS.sig" (Join-Path $tmp 'SHA256SUMS.sig')
            Test-Signature $openssl $tmp
        }
        Test-Hash $tmp $asset
        Install-Binary $tmp $asset $installDir
        Add-UserPath $installDir
        $v = (& (Join-Path $installDir 'agentbus.exe') version | Select-Object -First 1)
        Write-Host "installed agentbus $v to $installDir\agentbus.exe"
        Write-Host 'next: agentbus init --global (once per machine), then agentbus init inside each repository'
        if ($script:Upgrade) { Write-Host 'upgraded: restart every harness session and the TUI to pick up the new binary' }
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Main
```

- [ ] **Step 2: Static checks available on macOS**

Run: `cd <repo root> && rg -n 'BEGIN agentbus release public key|^\$PubKeyPem = @' install.ps1 && rg -c 'throw "agentbus install:' install.ps1 && if [[ -r release/agentbus-release-ed25519.pub ]]; then release/embed-key.sh release/agentbus-release-ed25519.pub install.ps1; fi`
Expected: both marker lines found; 12 throw sites; `install.ps1: key embedded` when the `.pub` exists (the ps1 branch of `embed-key.sh` from #34 Task 1 handles the here-string).

- [ ] **Step 3: Commit**

```bash
git add install.ps1
git commit -m "install.ps1: signed Windows installer for PowerShell 5.1 and 7"
```

---

### Task 9: `release/test-install.ps1` and its fixture server

**Files:**
- Create: `release/testdata/fixture-server.ps1`, `release/testdata/stub/main.go`, `release/testdata/stub/go.mod`, `release/test-install.ps1`

**Interfaces:**
- Produces: `release/test-install.ps1 [-SelfTest]`: builds a fake release (stub `agentbus.exe` from `release/testdata/stub`, zips, `SHA256SUMS`, throwaway-key signature, a copy of `install.ps1` with the throwaway key), serves it with `fixture-server.ps1` (`HttpListener` on `127.0.0.1`), runs every case under the PowerShell host that runs the harness, saves and restores the user `PATH`, exits 1 on any failure. Runs on Windows only (Task 11's workflow, Task 12's VM script).

- [ ] **Step 1: Stub binary.** `release/testdata/stub/go.mod`:

```
module stub

go 1.27
```

`release/testdata/stub/main.go`:

```go
// Command stub stands in for agentbus.exe in release/test-install.ps1:
// `stub version` prints the version linked in, `stub sleep` stays alive so a
// test can upgrade while the old binary runs.
package main

import (
	"fmt"
	"os"
	"time"
)

var version = "0.0.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "sleep" {
		time.Sleep(60 * time.Second)
		return
	}
	fmt.Println(version)
}
```

- [ ] **Step 2: Fixture server.** `release/testdata/fixture-server.ps1`:

```powershell
# Serves a fake GitHub releases tree for release/test-install.ps1 (the
# PowerShell twin of fixture-server.py).
#   fixture-server.ps1 -Root <dir> -Port <n>
# <root>/<variant>/latest -> tag; <root>/<variant>/<tag>/<asset> -> files.
# GET|HEAD /<variant>/releases/latest -> 302; /<variant>/releases/download/<tag>/<asset> -> file.
# The "slow" variant waits 3 s before answering a download.
param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][int]$Port)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$listener = New-Object System.Net.HttpListener
$listener.Prefixes.Add("http://127.0.0.1:$Port/")
$listener.Start()
while ($listener.IsListening) {
    $ctx = $listener.GetContext()
    $req = $ctx.Request; $res = $ctx.Response
    try {
        $parts = @($req.Url.AbsolutePath.Trim('/') -split '/')
        if (($parts | Where-Object { $_ -eq '' -or $_ -eq '.' -or $_ -eq '..' }).Count -gt 0) { $res.StatusCode = 400 }
        elseif ($parts.Count -eq 3 -and $parts[1] -eq 'releases' -and $parts[2] -eq 'latest') {
            $latest = Join-Path $Root (Join-Path $parts[0] 'latest')
            if (Test-Path -LiteralPath $latest) {
                $tag = (Get-Content -LiteralPath $latest -Raw).Trim()
                $res.StatusCode = 302
                $res.RedirectLocation = "/$($parts[0])/releases/tag/$tag"
            } else { $res.StatusCode = 404 }
        } elseif ($parts.Count -eq 5 -and $parts[1] -eq 'releases' -and $parts[2] -eq 'download') {
            $file = Join-Path $Root (Join-Path $parts[0] (Join-Path $parts[3] $parts[4]))
            if (Test-Path -LiteralPath $file) {
                if ($parts[0] -eq 'slow') { Start-Sleep -Seconds 3 }
                $bytes = [IO.File]::ReadAllBytes($file)
                $res.ContentLength64 = $bytes.Length
                if ($req.HttpMethod -eq 'GET') { $res.OutputStream.Write($bytes, 0, $bytes.Length) }
            } else { $res.StatusCode = 404 }
        } else { $res.StatusCode = 404 }
    } finally { $res.Close() }
}
```

- [ ] **Step 3: The harness.** `release/test-install.ps1`:

```powershell
# Tests install.ps1 against a fake release served from a local HttpListener.
# Needs Go (for the stub binary) and an OpenSSL 3 (Git for Windows). Signs
# with a throwaway key; install.ps1's key block is rewritten in a copy only.
# Runs the cases under the PowerShell host that runs this script, so the
# workflow runs it once with powershell.exe (5.1) and once with pwsh (7).
#   release\test-install.ps1            all cases
#   release\test-install.ps1 -SelfTest  fixture builder and server checks only
param([switch]$SelfTest)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = (Resolve-Path (Join-Path $Here '..')).Path
$Work = Join-Path ([IO.Path]::GetTempPath()) ('agentbus-itest-' + [guid]::NewGuid().ToString('N'))
$Fx = Join-Path $Work 'fixture'
$Port = Get-Random -Minimum 20000 -Maximum 40000
$Base = "http://127.0.0.1:$Port"
$Server = $null
$HostExe = (Get-Process -Id $PID).Path
$SavedUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$script:Pass = 0; $script:Fail = 0

function Find-OpenSsl3 {
    foreach ($c in @('openssl', "$env:ProgramFiles\Git\usr\bin\openssl.exe", "$env:ProgramFiles\Git\mingw64\bin\openssl.exe")) {
        $cmd = Get-Command $c -ErrorAction SilentlyContinue
        if ($cmd -and ((& $cmd.Source version | Select-Object -First 1) -match '^OpenSSL 3')) { return $cmd.Source }
    }
    throw 'test-install.ps1: no OpenSSL 3 found (install Git for Windows)'
}
$OpenSsl = Find-OpenSsl3

function Cleanup {
    if ($Server) { Stop-Process -Id $Server.Id -Force -ErrorAction SilentlyContinue; $script:Server = $null }
    # Only stubs started from this run's work directory: never the user's real agentbus sessions (the VM runs them).
    Get-Process -Name agentbus -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$Work*" } | Stop-Process -Force -ErrorAction SilentlyContinue
    [Environment]::SetEnvironmentVariable('Path', $SavedUserPath, 'User')
    Remove-Item -LiteralPath $Work -Recurse -Force -ErrorAction SilentlyContinue
}

# Build-Fixture: stub exe for two versions, zips, SHA256SUMS, signature, and
# the variants valid, tampered-zip, tampered-sums, tampered-sig,
# missing-entry and slow.
function Build-Fixture {
    New-Item -ItemType Directory -Force -Path $Fx, (Join-Path $Fx 'fakes') | Out-Null
    # No stderr redirection here: under Windows PowerShell 5.1 with 'Stop', a
    # redirected stderr line becomes a terminating NativeCommandError.
    & $OpenSsl genpkey -algorithm ed25519 -out (Join-Path $Work 'key.pem')
    & $OpenSsl pkey -in (Join-Path $Work 'key.pem') -pubout -out (Join-Path $Work 'key.pub')
    Copy-Item (Join-Path $RepoRoot 'install.ps1') (Join-Path $Fx 'install.ps1')
    Set-KeyBlock (Join-Path $Fx 'install.ps1') (Get-Content -LiteralPath (Join-Path $Work 'key.pub') -Raw)
    Set-Content -LiteralPath (Join-Path $Fx 'fakes\openssl.cmd') -Value "@echo OpenSSL 1.1.1w  11 Sep 2023"
    foreach ($tag in 'v9.0.0', 'v9.0.1') { New-Release 'valid' $tag $tag.Substring(1) }
    Set-Content -LiteralPath (Join-Path $Fx 'valid\latest') -Value 'v9.0.1'
    foreach ($v in 'tampered-zip', 'tampered-sums', 'tampered-sig', 'missing-entry', 'slow') {
        New-Item -ItemType Directory -Force -Path (Join-Path $Fx $v) | Out-Null
        Copy-Item -Recurse (Join-Path $Fx 'valid\v9.0.1') (Join-Path $Fx "$v\v9.0.1")
        Set-Content -LiteralPath (Join-Path $Fx "$v\latest") -Value 'v9.0.1'
    }
    foreach ($a in 'amd64', 'arm64') { Add-Byte (Join-Path $Fx "tampered-zip\v9.0.1\agentbus-v9.0.1-windows-$a.zip") }
    $sums = Join-Path $Fx 'tampered-sums\v9.0.1\SHA256SUMS'
    $lines = Get-Content -LiteralPath $sums
    $lines[0] = $(if ($lines[0][0] -eq 'f') { 'E' } else { 'f' }) + $lines[0].Substring(1)
    [IO.File]::WriteAllText($sums, ($lines -join "`n") + "`n")
    Add-Byte (Join-Path $Fx 'tampered-sig\v9.0.1\SHA256SUMS.sig')
    $me = Join-Path $Fx 'missing-entry\v9.0.1'
    [IO.File]::WriteAllText((Join-Path $me 'SHA256SUMS'), ((Get-Content -LiteralPath (Join-Path $me 'SHA256SUMS') | Where-Object { $_ -match ' install.ps1$' }) -join "`n") + "`n")
    & $OpenSsl pkeyutl -sign -rawin -inkey (Join-Path $Work 'key.pem') -in (Join-Path $me 'SHA256SUMS') -out (Join-Path $me 'SHA256SUMS.sig')
}

# Set-KeyBlock rewrites the $PubKeyPem here-string between the markers (the
# PowerShell twin of release/embed-key.sh).
function Set-KeyBlock([string]$Script, [string]$Pem) {
    $text = Get-Content -LiteralPath $Script -Raw
    $pattern = '(?s)(# BEGIN agentbus release public key\r?\n).*?(# END agentbus release public key)'
    $block = "`$PubKeyPem = @'`n" + $Pem.Trim() + "`n'@`n"
    $new = [regex]::Replace($text, $pattern, { param($m) $m.Groups[1].Value + $block + $m.Groups[2].Value })
    [IO.File]::WriteAllText($Script, $new)
}

function New-Release([string]$Variant, [string]$Tag, [string]$Version) {
    $d = Join-Path $Fx (Join-Path $Variant $Tag)
    New-Item -ItemType Directory -Force -Path $d | Out-Null
    $bin = Join-Path $d 'bin'
    New-Item -ItemType Directory -Force -Path $bin | Out-Null
    Push-Location (Join-Path $Here 'testdata\stub')
    try {
        $env:CGO_ENABLED = '0'
        & go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $bin 'agentbus.exe') .
        if ($LASTEXITCODE -ne 0) { throw 'stub build failed' }
    } finally { Pop-Location }
    foreach ($a in 'amd64', 'arm64') {
        Compress-Archive -LiteralPath (Join-Path $bin 'agentbus.exe') -DestinationPath (Join-Path $d "agentbus-$Tag-windows-$a.zip") -Force
    }
    Remove-Item -Recurse -Force $bin
    Copy-Item (Join-Path $Fx 'install.ps1') (Join-Path $d 'install.ps1')
    $sums = foreach ($f in "agentbus-$Tag-windows-amd64.zip", "agentbus-$Tag-windows-arm64.zip", 'install.ps1') {
        (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $d $f)).Hash.ToLower() + '  ' + $f
    }
    [IO.File]::WriteAllText((Join-Path $d 'SHA256SUMS'), ($sums -join "`n") + "`n")
    & $OpenSsl pkeyutl -sign -rawin -inkey (Join-Path $Work 'key.pem') -in (Join-Path $d 'SHA256SUMS') -out (Join-Path $d 'SHA256SUMS.sig')
}

function Add-Byte([string]$Path) { $s = [IO.File]::Open($Path, 'Append'); $s.WriteByte(0); $s.Close() }

function Start-Server {
    $script:Server = Start-Process -FilePath $HostExe -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $Here 'testdata\fixture-server.ps1'), '-Root', $Fx, '-Port', $Port) -PassThru -WindowStyle Hidden
    foreach ($i in 1..50) {
        try { Invoke-WebRequest -Uri "$Base/" -UseBasicParsing -ErrorAction Stop | Out-Null; return } catch { if ($_.Exception.Response) { return } }
        Start-Sleep -Milliseconds 100
    }
    throw 'fixture server did not start'
}

function Test-Verify([string]$Dir) {
    & $OpenSsl pkeyutl -verify -rawin -pubin -inkey (Join-Path $Work 'key.pub') -in (Join-Path $Dir 'SHA256SUMS') -sigfile (Join-Path $Dir 'SHA256SUMS.sig') 2>&1 | Out-Null
    return ($LASTEXITCODE -eq 0)
}

function Assert([string]$Name, [bool]$Ok) {
    if ($Ok) { $script:Pass++; Write-Host "PASS $Name" } else { $script:Fail++; Write-Host "FAIL $Name" }
}

function Invoke-SelfTest {
    Build-Fixture
    $v = Join-Path $Fx 'valid\v9.0.1'
    Assert 'self: sums list three files' ((Get-Content -LiteralPath (Join-Path $v 'SHA256SUMS')).Count -eq 3)
    Assert 'self: valid signature verifies' (Test-Verify $v)
    Assert 'self: tampered-sig fails' (-not (Test-Verify (Join-Path $Fx 'tampered-sig\v9.0.1')))
    Assert 'self: tampered-sums fails' (-not (Test-Verify (Join-Path $Fx 'tampered-sums\v9.0.1')))
    Assert 'self: tampered-zip keeps sums' ((Get-FileHash -LiteralPath (Join-Path $v 'SHA256SUMS')).Hash -eq (Get-FileHash -LiteralPath (Join-Path $Fx 'tampered-zip\v9.0.1\SHA256SUMS')).Hash)
    Assert 'self: missing-entry verifies and lacks zips' ((Test-Verify (Join-Path $Fx 'missing-entry\v9.0.1')) -and -not ((Get-Content -LiteralPath (Join-Path $Fx 'missing-entry\v9.0.1\SHA256SUMS')) -match 'windows'))
    Assert 'self: install.ps1 copy carries the throwaway key' ((Get-Content -LiteralPath (Join-Path $Fx 'install.ps1') -Raw).Contains((Get-Content -LiteralPath (Join-Path $Work 'key.pub'))[1]))
    Expand-Archive -LiteralPath (Join-Path $Fx 'valid\v9.0.0\agentbus-v9.0.0-windows-amd64.zip') -DestinationPath (Join-Path $Work 'x') -Force
    Assert 'self: stub prints its version' ((& (Join-Path $Work 'x\agentbus.exe') version) -eq '9.0.0')
    Start-Server
    $req = [System.Net.HttpWebRequest]::Create("$Base/valid/releases/latest"); $req.Method = 'HEAD'; $req.AllowAutoRedirect = $false
    $resp = $req.GetResponse(); $loc = $resp.Headers['Location']; $resp.Close()
    Assert 'self: server redirects latest' ($loc -like '*/valid/releases/tag/v9.0.1')
    Assert 'self: server serves SHA256SUMS' ((Invoke-WebRequest -Uri "$Base/valid/releases/download/v9.0.1/SHA256SUMS" -UseBasicParsing).Content -eq (Get-Content -LiteralPath (Join-Path $v 'SHA256SUMS') -Raw))
    Cleanup
    Assert 'self: cleanup removed the work directory' (-not (Test-Path -LiteralPath $Work))
    Assert 'self: user PATH restored' ([Environment]::GetEnvironmentVariable('Path', 'User') -eq $SavedUserPath)
}

# Invoke-Case runs install.ps1 (the fixture copy) in a child host with the
# given environment and compares exit code and output.
function Invoke-Case([string]$Name, [int]$WantExit, [string]$WantOut, [hashtable]$Env, [string]$InstallDir, [scriptblock]$Before, [scriptblock]$After) {
    $saved = @{}
    foreach ($k in 'AGENTBUS_BASE_URL', 'AGENTBUS_VERSION', 'AGENTBUS_INSTALL_DIR', 'AGENTBUS_SKIP_SIGNATURE', 'AGENTBUS_TEST_OSARCH', 'Path', 'ProgramFiles') { $saved[$k] = [Environment]::GetEnvironmentVariable($k) }
    try {
        [Environment]::SetEnvironmentVariable('AGENTBUS_BASE_URL', "$Base/valid")
        [Environment]::SetEnvironmentVariable('AGENTBUS_INSTALL_DIR', $InstallDir)
        foreach ($k in $Env.Keys) { [Environment]::SetEnvironmentVariable($k, $Env[$k]) }
        if ($Before) { & $Before }
        # Windows PowerShell 5.1 raises a terminating NativeCommandError for
        # captured stderr under $ErrorActionPreference = 'Stop'; relax it
        # around the child run only (every refusal writes its throw to stderr).
        $eap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
        $out = & $HostExe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Fx 'install.ps1') 2>&1 | Out-String
        $exit = $LASTEXITCODE
        $ErrorActionPreference = $eap
        $extra = if ($After) { & $After } else { $true }
        if ($exit -eq $WantExit -and $out.Contains($WantOut) -and $extra) { $script:Pass++; Write-Host "PASS $Name" }
        else { $script:Fail++; Write-Host "FAIL $Name (exit=$exit want=$WantExit extra=$extra)"; Write-Host ($out -replace '(?m)^', '    ') }
    } finally {
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
    }
}

function Invoke-Cases {
    $dir = Join-Path $Work 'bin'
    $noGit = @{ 'Path' = "$env:SystemRoot\System32;$env:SystemRoot"; 'ProgramFiles' = (Join-Path $Work 'no-programs') }
    $fakePath = @{ 'Path' = (Join-Path $Fx 'fakes') + ";$env:SystemRoot\System32;$env:SystemRoot"; 'ProgramFiles' = (Join-Path $Work 'no-programs') }
    Invoke-Case 'valid' 0 'installed agentbus 9.0.1' @{} $dir $null { (& (Join-Path $dir 'agentbus.exe') version) -eq '9.0.1' }
    Invoke-Case 'path-added' 0 'added' @{} (Join-Path $Work 'bin2') $null { ([Environment]::GetEnvironmentVariable('Path', 'User') -split ';') -contains (Join-Path $Work 'bin2') }
    Invoke-Case 'pinned-version' 0 'installed agentbus 9.0.0' @{ 'AGENTBUS_VERSION' = 'v9.0.0' } $dir $null $null
    Invoke-Case 'version-no-v' 1 'version must look like vX.Y.Z' @{ 'AGENTBUS_VERSION' = '9.0.0' } $dir $null $null
    Invoke-Case 'latest-unparsable' 1 'could not determine the latest release' @{ 'AGENTBUS_BASE_URL' = "$Base/nope" } $dir $null $null
    Invoke-Case 'tampered-zip' 1 'checksum mismatch' @{ 'AGENTBUS_BASE_URL' = "$Base/tampered-zip" } $dir $null $null
    Invoke-Case 'tampered-sums' 1 'signature check of SHA256SUMS failed' @{ 'AGENTBUS_BASE_URL' = "$Base/tampered-sums" } $dir $null $null
    Invoke-Case 'tampered-sig' 1 'signature check of SHA256SUMS failed' @{ 'AGENTBUS_BASE_URL' = "$Base/tampered-sig" } $dir $null $null
    Invoke-Case 'sums-missing-entry' 1 'SHA256SUMS has no entry for' @{ 'AGENTBUS_BASE_URL' = "$Base/missing-entry" } $dir $null $null
    Invoke-Case 'no-openssl' 1 'winget install --id Git.Git -e' $noGit $dir $null $null
    Invoke-Case 'openssl-too-old' 1 'OpenSSL 1.1.1w' $fakePath $dir $null $null
    Invoke-Case 'skip-signature-valid' 0 'skipping the signature check' ($noGit + @{ 'AGENTBUS_SKIP_SIGNATURE' = '1' }) $dir $null $null
    Invoke-Case 'skip-signature-tampered' 1 'checksum mismatch' @{ 'AGENTBUS_SKIP_SIGNATURE' = '1'; 'AGENTBUS_BASE_URL' = "$Base/tampered-zip" } $dir $null $null
    Invoke-Case 'skip-signature-bad-value' 1 'AGENTBUS_SKIP_SIGNATURE must be 1 or unset' @{ 'AGENTBUS_SKIP_SIGNATURE' = 'yes' } $dir $null $null
    Invoke-Case 'unknown-arch' 1 'unsupported architecture: Riscv64' @{ 'AGENTBUS_TEST_OSARCH' = 'Riscv64' } $dir $null $null
    Invoke-Case 'relative-dir' 1 'must be an absolute path' @{} 'bin' $null $null
    $script:Running = $null
    Invoke-Case 'upgrade-while-running' 0 'upgraded: restart' @{} $dir { $script:Running = Start-Process -FilePath (Join-Path $dir 'agentbus.exe') -ArgumentList 'sleep' -PassThru -WindowStyle Hidden } {
        ((& (Join-Path $dir 'agentbus.exe') version) -eq '9.0.1') -and ((Get-ChildItem -LiteralPath $dir -Filter 'agentbus.exe.old-*').Count -eq 1)
    }
    if ($script:Running) { Stop-Process -Id $script:Running.Id -Force; Start-Sleep -Milliseconds 500 }
    Invoke-Case 'leftover-old-cleaned' 0 'installed agentbus' @{} $dir $null { (Get-ChildItem -LiteralPath $dir -Filter 'agentbus.exe.old-*').Count -eq 0 }
    $c1 = Start-Process -FilePath $HostExe -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $Fx 'install.ps1')) -PassThru -WindowStyle Hidden
    $c2 = Start-Process -FilePath $HostExe -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $Fx 'install.ps1')) -PassThru -WindowStyle Hidden
    $c1.WaitForExit(); $c2.WaitForExit()
    Assert 'concurrent' (($c1.ExitCode -eq 0) -and ($c2.ExitCode -eq 0) -and ((& (Join-Path $dir 'agentbus.exe') version) -eq '9.0.1'))
    Invoke-Case 'failed-run-leaves-no-temp' 1 'checksum mismatch' @{ 'AGENTBUS_BASE_URL' = "$Base/tampered-zip" } $dir $null { (Get-ChildItem ([IO.Path]::GetTempPath()) -Filter 'agentbus-install-*' -Directory).Count -eq 0 }
    $pub = Join-Path $RepoRoot 'release\agentbus-release-ed25519.pub'
    if (Test-Path -LiteralPath $pub) {
        $text = Get-Content -LiteralPath (Join-Path $RepoRoot 'install.ps1') -Raw
        $m = [regex]::Match($text, "(?s)\`$PubKeyPem = @'\r?\n(.*?)\r?\n'@")
        Assert 'key-matches-pub' ($m.Success -and (($m.Groups[1].Value -replace "`r", '').Trim() -eq ((Get-Content -LiteralPath $pub -Raw) -replace "`r", '').Trim()))
    } else { Assert "key-matches-pub ($pub missing: generate it per the #34 plan, Task 1, then run release/embed-key.sh)" $false }
}

try {
    if ($SelfTest) { Invoke-SelfTest } else {
        Build-Fixture
        Start-Server
        Invoke-Cases
    }
} finally { Cleanup }
Write-Host "test-install.ps1 ($($PSVersionTable.PSVersion)): $script:Pass passed, $script:Fail failed"
if ($script:Fail -ne 0) { exit 1 }
```

Notes: `Invoke-Case` sets process environment that the child inherits; `Path`/`ProgramFiles` overrides hide Git so `Find-OpenSsl` finds nothing (or only the fake `openssl.cmd`). The `upgrade-while-running` case starts the installed stub in `sleep` mode so the rename path is exercised; the leftover `.old-*` is removed by the next run once the process is stopped. Every case runs under both hosts because the workflow (Task 11) invokes the harness from `powershell` and from `pwsh`.

- [ ] **Step 4: What can be checked on macOS**

Run: `cd <repo root> && rg -n "Invoke-Case '" release/test-install.ps1 | wc -l && rg -n '^function ' release/test-install.ps1 release/testdata/fixture-server.ps1 && cd release/testdata/stub && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-X main.version=9.0.0" -o /dev/null . && go vet ./...`
Expected: 19 `Invoke-Case` lines; the function list; the stub cross-builds and vets. No `pwsh` is installed on this Mac, so the harness itself first runs in the Windows workflow after the user pushes (Task 11) and in the VM (Task 12); the report must say so.

- [ ] **Step 5: Commit**

```bash
git add release/test-install.ps1 release/testdata/fixture-server.ps1 release/testdata/stub
git commit -m "release: test-install.ps1 exercises install.ps1 against a local fake release"
```

---

### Task 10: `release.sh`: Windows zips, `file` check, Scoop and winget

**Files:**
- Modify: `release/release.sh` (globals, `require_tools`, `copy_scripts`, `write_sums`, `upload_assets`, `main`; add `build_windows`, `check_windows_pe`, `update_scoop`, `submit_winget`)
- Create: `release/agentbus.scoop.json.tmpl`
- Modify: `release/test-release.sh`, `release/test-render.sh`

**Interfaces:**
- Produces: globals `SCOOP_DIR` (default `$HOME/Projects/scoop-bucket`), `KOMAC_KEY_FILE` (`$HOME/.keys/KOMAC_GITHUB_KEY`), `WINGET_ID` (`ericfitz.agentbus`); functions `build_windows`, `check_windows_pe` (both removed by #36), `update_scoop`, `submit_winget` (kept by #36). Scoop placeholders `__VERSION__ __AMD64_URL__ __AMD64_SHA256__ __ARM64_URL__ __ARM64_SHA256__`.

- [ ] **Step 1: Failing tests.** Append to `release/test-release.sh` before the summary:

```bash
# --- Windows packaging (#35) ------------------------------------------------
printf 'aaaa  agentbus-v1.0.0-windows-amd64.zip\nbbbb  agentbus-v1.0.0-windows-arm64.zip\n' > "$WORK/dist/SHA256SUMS"
check scoop-render 0 "" -- bash -c "source '$HERE/release.sh'; render_template '$HERE/agentbus.scoop.json.tmpl' '$WORK/scoop.json' VERSION=1.0.0 AMD64_URL=https://x/a.zip AMD64_SHA256=aaaa ARM64_URL=https://x/r.zip ARM64_SHA256=bbbb && python3 -I -m json.tool '$WORK/scoop.json' >/dev/null"
check scoop-fields 0 "" -- bash -c "python3 -I -c \"import json,sys; m=json.load(open(sys.argv[1])); assert m['version']=='1.0.0' and m['bin']=='agentbus.exe' and m['architecture']['64bit']['hash']=='aaaa' and m['architecture']['arm64']['url']=='https://x/r.zip' and 'checkver' in m and 'autoupdate' in m\" '$WORK/scoop.json'"
mkdir -p "$WORK/pe/windows-amd64" "$WORK/pe/windows-arm64"
for a in amd64 arm64; do (cd "$WORK/repo" && printf 'package main\nfunc main(){}\n' > main.go && printf 'module t\n\ngo 1.27\n' > go.mod && CGO_ENABLED=0 GOOS=windows GOARCH=$a go build -o "$WORK/pe/windows-$a/agentbus.exe" .); done
check pe-ok 0 "" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/pe'; check_windows_pe"
printf 'not a PE\n' > "$WORK/pe/windows-arm64/agentbus.exe"
check pe-wrong 1 "windows/arm64 binary" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/pe'; check_windows_pe"
```

And in `release/test-render.sh`, after the formula block:

```bash
render_check "$HERE/agentbus.scoop.json.tmpl" agentbus.json python3 -I -m json.tool -- \
    VERSION=1.2.3 AMD64_URL=https://example.invalid/a.zip AMD64_SHA256=aaaa ARM64_URL=https://example.invalid/r.zip ARM64_SHA256=bbbb
```

- [ ] **Step 2: Run to verify it fails**

Run: `release/test-release.sh; release/test-render.sh`
Expected: `scoop-*` and `pe-*` FAIL (missing template, undefined `check_windows_pe`); test-render FAILs the scoop line.

- [ ] **Step 3: Scoop template** `release/agentbus.scoop.json.tmpl`:

```json
{
    "version": "__VERSION__",
    "description": "Local message bus and shared memory for coding agents",
    "homepage": "https://github.com/ericfitz/agentbus",
    "license": "Apache-2.0",
    "architecture": {
        "64bit": { "url": "__AMD64_URL__", "hash": "__AMD64_SHA256__" },
        "arm64": { "url": "__ARM64_URL__", "hash": "__ARM64_SHA256__" }
    },
    "bin": "agentbus.exe",
    "checkver": { "github": "https://github.com/ericfitz/agentbus" },
    "autoupdate": {
        "architecture": {
            "64bit": { "url": "https://github.com/ericfitz/agentbus/releases/download/v$version/agentbus-v$version-windows-amd64.zip" },
            "arm64": { "url": "https://github.com/ericfitz/agentbus/releases/download/v$version/agentbus-v$version-windows-arm64.zip" }
        },
        "hash": { "url": "https://github.com/ericfitz/agentbus/releases/download/v$version/SHA256SUMS" }
    },
    "notes": "Stop agentbus sessions (MCP servers, waits, the TUI) before `scoop update agentbus`: Scoop cannot replace the files of a running program."
}
```

- [ ] **Step 4: release.sh changes.** Add after `TAP_DIR=...`:

```bash
SCOOP_DIR="${SCOOP_DIR:-$HOME/Projects/scoop-bucket}" # SSH clone of ericfitz/scoop-bucket
KOMAC_KEY_FILE="$HOME/.keys/KOMAC_GITHUB_KEY"           # `export KOMAC_GITHUB_KEY='<PAT with public_repo>'`
WINGET_ID="ericfitz.agentbus"
```

In `require_tools`, add `file zip komac` to the tool loop and `[[ -r "$KOMAC_KEY_FILE" ]] || missing+=("komac token $KOMAC_KEY_FILE")`. Add after `smoke_linux`:

```bash
# build_windows builds agentbus.exe into $DIST/windows-<arch>/ and packages
# each as agentbus-<tag>-windows-<arch>.zip.
build_windows() {
    local arch
    for arch in amd64 arm64; do
        echo "==> Building windows/$arch"
        mkdir -p "$DIST/windows-$arch"
        (cd "$SRC" && CGO_ENABLED=0 GOOS=windows GOARCH="$arch" go build -trimpath \
            -ldflags "-s -w -X ${VERSION_VAR}=${VERSION}" -o "$DIST/windows-$arch/$BIN_NAME.exe" .)
        local archive="${BIN_NAME}-${TAG}-windows-${arch}.zip"
        (cd "$DIST/windows-$arch" && zip -q "$DIST/$archive" "$BIN_NAME.exe")
        ARCHIVES+=("$archive")
    done
}

# check_windows_pe: this host cannot run Windows binaries, so `file` must
# report a PE32+ console executable for the expected machine. Actions
# (windows.yml) and the VM run them.
check_windows_pe() {
    local arch want got
    for arch in amd64 arm64; do
        case "$arch" in amd64) want="x86-64" ;; arm64) want="Aarch64" ;; esac
        got="$(file -b "$DIST/windows-$arch/$BIN_NAME.exe")"
        [[ "$got" == "PE32+ executable (console) $want, for MS Windows"* ]] \
            || fail "windows/$arch binary: '$got' (want PE32+ executable (console) $want)"
    done
}
```

`copy_scripts` adds `cp "$SRC/install.ps1" "$DIST/install.ps1"`; `write_sums`' `shasum` line lists `"${ARCHIVES[@]}" install.sh install.ps1`; `upload_assets` adds `install.ps1` after `install.sh`. Add after `update_tap`:

```bash
update_scoop() {
    echo "==> Updating Scoop bucket"
    [[ -d "$SCOOP_DIR" ]] || git clone git@github.com:ericfitz/scoop-bucket.git "$SCOOP_DIR"
    git -C "$SCOOP_DIR" pull -q --ff-only
    local base="https://github.com/${GH_REPO}/releases/download/${TAG}"
    local wa="${BIN_NAME}-${TAG}-windows-amd64.zip" wr="${BIN_NAME}-${TAG}-windows-arm64.zip"
    local rendered="$DIST/${BIN_NAME}.scoop.json"
    render_template "$REPO_ROOT/release/${BIN_NAME}.scoop.json.tmpl" "$rendered" \
        "VERSION=$VERSION" "AMD64_URL=$base/$wa" "AMD64_SHA256=$(sum_of "$wa")" \
        "ARM64_URL=$base/$wr" "ARM64_SHA256=$(sum_of "$wr")"
    mkdir -p "$SCOOP_DIR/bucket"
    if cmp -s "$rendered" "$SCOOP_DIR/bucket/${BIN_NAME}.json"; then
        echo "scoop manifest unchanged"; return
    fi
    cp "$rendered" "$SCOOP_DIR/bucket/${BIN_NAME}.json"
    git -C "$SCOOP_DIR" add "bucket/${BIN_NAME}.json"
    git -C "$SCOOP_DIR" commit -m "${BIN_NAME} ${VERSION}"
    git -C "$SCOOP_DIR" push
}

# submit_winget opens the winget-pkgs pull request for this version with
# komac. An open pull request for the version counts as done (rerun). A
# komac failure does not fail the release: the exact rerun command is
# printed. The token is scoped to the one command, because exporting
# GITHUB_TOKEN would override gh's own auth for the rest of the run.
submit_winget() {
    echo "==> Submitting ${WINGET_ID} ${VERSION} to winget"
    if [[ -n "$(gh pr list --repo microsoft/winget-pkgs --state open --search "${WINGET_ID} version ${VERSION} in:title" --json number --jq '.[].number')" ]]; then
        echo "winget: an open pull request for ${VERSION} exists; nothing to do"; return
    fi
    local base="https://github.com/${GH_REPO}/releases/download/${TAG}"
    local cmd=(komac update "$WINGET_ID" --version "$VERSION" --urls "$base/${BIN_NAME}-${TAG}-windows-amd64.zip" "$base/${BIN_NAME}-${TAG}-windows-arm64.zip" --submit)
    if ! (
        # shellcheck disable=SC1090
        source "$KOMAC_KEY_FILE"
        GITHUB_TOKEN="$KOMAC_GITHUB_KEY" "${cmd[@]}"
    ); then
        echo "warning: komac failed; the release stands. Rerun: (source $KOMAC_KEY_FILE && GITHUB_TOKEN=\"\$KOMAC_GITHUB_KEY\" ${cmd[*]})" >&2
    fi
}
```

In `main`, insert `build_windows` and `check_windows_pe` after `smoke_linux`, and `update_scoop` and `submit_winget` after `update_tap`. Update the header comment: "Windows: amd64/arm64 zips built here, checked with `file`, run by the windows.yml workflow and the VM." and the final echo to add `irm https://github.com/${GH_REPO}/releases/latest/download/install.ps1 | iex`.

- [ ] **Step 5: Run the tests**

Run: `cd <repo root> && release/test-release.sh && release/test-render.sh && shellcheck release/*.sh && shellcheck -s sh install.sh`
Expected: `scoop-render`, `scoop-fields`, `pe-ok`, `pe-wrong` PASS (`test-release: 37 passed, 0 failed`); `PASS agentbus.scoop.json.tmpl`, `test-render: OK`; shellcheck silent.

- [ ] **Step 6: Commit**

```bash
git add release/release.sh release/agentbus.scoop.json.tmpl release/test-release.sh release/test-render.sh
git commit -m "release: Windows zips with a PE check, Scoop manifest, winget via komac"
```

---

### Task 11: Windows test workflow, `actionlint`, CLAUDE.md

**Files:**
- Create: `.github/workflows/windows.yml`
- Modify: `Makefile` (`release-check`), `CLAUDE.md`

- [ ] **Step 1: Write the workflow**

```yaml
# Informational: runs the Go tests and the install.ps1 harness on Windows
# amd64 and arm64. It does not gate; `make verify` is the done gate.
name: windows
on:
  push:
    branches: [main]
  workflow_dispatch:
permissions:
  contents: read
jobs:
  test:
    strategy:
      fail-fast: false
      matrix:
        os: [windows-latest, windows-11-arm]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod
      - run: go vet ./...
      - run: go test ./...
      - name: install.ps1 harness self-test (Windows PowerShell 5.1)
        shell: powershell
        run: .\release\test-install.ps1 -SelfTest
      - name: install.ps1 harness (Windows PowerShell 5.1)
        shell: powershell
        run: .\release\test-install.ps1
      - name: install.ps1 harness (PowerShell 7)
        shell: pwsh
        run: ./release/test-install.ps1
```

- [ ] **Step 2: Makefile and CLAUDE.md.** In `release-check`, add as the first line `actionlint .github/workflows/*.yml` and, after the shellcheck lines, `@echo "note: release/test-install.ps1 runs on Windows only (windows.yml, release/test-windows.ps1)"`. In `CLAUDE.md`, replace `There is no CI; this gate is the only check before a push.` with `The Windows workflow (.github/workflows/windows.yml) is informational; this gate is the only check before a push.`

- [ ] **Step 3: Lint and run the release checks**

Run: `cd <repo root> && actionlint .github/workflows/windows.yml && rg -n 'uses: .*@v[0-9]' .github/workflows/ ; make release-check`
Expected: actionlint silent; the `rg` finds nothing (every action is SHA-pinned; `rg` exit 1 is expected there); `make release-check` passes.

- [ ] **Step 4: Commit, and the user step**

```bash
git add .github/workflows/windows.yml Makefile CLAUDE.md
git commit -m "ci: informational Windows test workflow"
```

Report: `Approve: push main to origin so .github/workflows/windows.yml runs on windows-latest and windows-11-arm` and that failures it reports are fixed in a follow-up commit (expected first-run candidates: path-literal assertions not listed in the inventory, `HttpListener` URL ACL on the runner).

---

### Task 12: VM script and manual checklist

**Files:**
- Create: `release/test-windows.ps1`, `docs/windows-checklist.md`

- [ ] **Step 1: `release/test-windows.ps1`**

```powershell
# Runs the Windows checks natively in the user's Windows 11 ARM VM: go vet,
# go test, and the install.ps1 harness under both PowerShell hosts. Prints a
# summary; exits 1 if anything failed. Run from the repository root:
#   pwsh -File release\test-windows.ps1
param()
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Continue'
$root = (Resolve-Path (Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) '..')).Path
Set-Location $root
$results = @()
function Step([string]$Name, [scriptblock]$Body) {
    Write-Host "==> $Name"
    & $Body
    $ok = ($LASTEXITCODE -eq 0)
    $script:results += [pscustomobject]@{ Step = $Name; Result = $(if ($ok) { 'PASS' } else { 'FAIL' }) }
}
Step 'go vet ./...' { go vet ./... }
Step 'go test ./...' { go test ./... }
Step 'test-install.ps1 -SelfTest (powershell 5.1)' { powershell -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 -SelfTest }
Step 'test-install.ps1 (powershell 5.1)' { powershell -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 }
if (Get-Command pwsh -ErrorAction SilentlyContinue) {
    Step 'test-install.ps1 (pwsh 7)' { pwsh -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 }
} else { $results += [pscustomobject]@{ Step = 'test-install.ps1 (pwsh 7)'; Result = 'SKIP (pwsh not installed)' } }
$results | Format-Table -AutoSize
if (($results | Where-Object { $_.Result -eq 'FAIL' }).Count -ne 0) { exit 1 }
```

- [ ] **Step 2: `docs/windows-checklist.md`**

```markdown
# Windows manual checks

Run in the Windows 11 ARM VM (VMware Fusion) before the first Windows
release and after any change to harness detection (`internal/procs`,
`internal/cli/stophook.go`, `internal/cli/wait.go`). The automated part is
`pwsh -File release\test-windows.ps1` from a checkout; the rest is by hand.

## Harness integration

- [ ] `agentbus init --global` writes the Claude Code, Codex and Grok
      configuration under `%USERPROFILE%` (`.claude\settings.json`,
      `.codex\hooks.json`, `.grok\hooks\agentbus.json`) and each harness
      registers on the bus (visible in `agentbus status`).
- [ ] Claude Code's Stop hook fires through Git Bash and reports the right
      identity: with two sessions in one repository the second one's hook
      wakes only for the second session's messages (`claude.exe` ->
      `bash.exe` -> `agentbus.exe` ancestry).
- [ ] A background `agentbus wait` follows the harness: closing the harness
      ends the wait with exit code 4; starting a newer wait for the same
      identity ends the older one with exit code 3.
- [ ] The TUI renders and accepts input in Windows Terminal (`agentbus tui`,
      keys `?`, `h`, `o`, `esc`); `o` opens the config in Notepad when
      `EDITOR` is unset.
- [ ] `install.ps1` from the published URL installs, and upgrades while an
      MCP server is running (the old `agentbus.exe` is renamed `.old-*`
      and removed by the next run).

## Shared database guard (needs WSL2 in the VM)

Nested virtualization: the Mac (M4 Pro, macOS 27) exposes it in
Hypervisor.framework; enable it for the Windows 11 ARM guest in UTM 4.7.4
and confirm `wsl --install` succeeds. If UTM does not expose it, use a
physical Windows machine or a cloud Windows VM with nested virtualization.

- [ ] In WSL: `findmnt -T /mnt/c -o FSTYPE` reports `9p` (or `drvfs`);
      record the value here: ______.
- [ ] In WSL: `AGENTBUS_DATA_DIR=/mnt/c/Users/<you>/agentbus-test agentbus status`
      refuses with the boundary error naming the filesystem.
- [ ] Native Windows: `$env:AGENTBUS_DATA_DIR='\\wsl.localhost\<distro>\home\<you>\agentbus-test'; agentbus status`
      refuses with the boundary error.
- [ ] Native Windows, a drive mapped to `\\wsl.localhost\<distro>` (`net use
      W: \\wsl.localhost\<distro>`): `AGENTBUS_DATA_DIR=W:\agentbus-test`
      refuses.

## Packaging

- [ ] `scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket && scoop install agentbus`
      installs; `scoop update agentbus` after stopping sessions upgrades.
- [ ] `winget install ericfitz.agentbus` installs once the package is
      published.
```

- [ ] **Step 3: Check the file names the checklist references exist**

Run: `cd <repo root> && ls release/test-windows.ps1 release/test-install.ps1 install.ps1 && rg -n 'exit 3|exit 4|return 3|return 4' main.go`
Expected: files listed; `return 3` and `return 4` in `main.go`'s wait case.

- [ ] **Step 4: Commit**

```bash
git add release/test-windows.ps1 docs/windows-checklist.md
git commit -m "docs: Windows VM test script and manual checklist"
```

---

### Task 13: Documentation and release notes

**Files:**
- Modify: `docs/install.md` (after the Linux section from #34; Operating bullet on the default data directory; the ADR 0017 bullet "on a platform other than macOS and Linux"), `README.md` (install section), `release/notes-v1.14.0.md` (or the current next-tag notes file)

- [ ] **Step 1: `docs/install.md`.** After the "Linux: install script" section insert:

```markdown
## Windows: install script, Scoop, winget

```powershell
irm https://github.com/ericfitz/agentbus/releases/latest/download/install.ps1 | iex
```

Installs `agentbus.exe` into `%LocalAppData%\Programs\agentbus` (amd64 or
arm64) and adds that directory to your user `PATH`; open a new terminal
afterwards. The script verifies the release's Ed25519-signed `SHA256SUMS`
with OpenSSL 3 and then the zip's hash. OpenSSL 3 comes with Git for
Windows (`winget install --id Git.Git -e`); without it the script refuses.
Rerunning upgrades: a running `agentbus.exe` is renamed, not overwritten,
so restart harness sessions and the TUI afterwards.

| Variable | Meaning |
|---|---|
| `AGENTBUS_VERSION` | Install this tag (`vX.Y.Z`) instead of the latest release. |
| `AGENTBUS_INSTALL_DIR` | Absolute target directory (default `%LocalAppData%\Programs\agentbus`). |
| `AGENTBUS_BASE_URL` | A mirror of `https://github.com/ericfitz/agentbus`. |
| `AGENTBUS_SKIP_SIGNATURE` | `1` skips only the signature check; the hash is still verified. Any other value is rejected. |

Package managers:

```powershell
scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket
scoop install agentbus
winget install ericfitz.agentbus
```

Stop agentbus sessions (MCP servers, `agentbus wait`, the TUI) before
`scoop update agentbus` or `winget upgrade ericfitz.agentbus`: neither can
replace the files of a running program.

Windows paths: the configuration file is `%AppData%\agentbus\config.json`
and the data directory `%LocalAppData%\agentbus` (Local, so the database
does not roam with a domain profile). `AGENTBUS_CONFIG`, `AGENTBUS_DATA_DIR`
and `data_directory` override them as on other platforms.

## WSL

Inside WSL use the Linux installer above. Windows and WSL each run their
own bus: an agent in a Windows terminal and an agent in a WSL shell do not
see each other. agentbus refuses a data directory that crosses the boundary
(a `/mnt/c/...` path under WSL, or a `\\wsl$\...`, `\\wsl.localhost\...`
or mapped-drive path on Windows), because SQLite's file locks do not work
between Windows and WSL processes on one file and concurrent use could
corrupt the database. Keep each side's database on its own disk.
```

In the Operating section: change "The default data directory is `~/.local/share/agentbus` on macOS and Linux." to "... on macOS and Linux, and `%LocalAppData%\agentbus` on Windows." In the ADR 0017 bullet, replace "(or on a platform other than macOS and Linux)" with "(or on a platform other than macOS, Linux and Windows)".

- [ ] **Step 2: README.** After the Linux block in the Install section add:

```markdown
Windows (PowerShell):

```powershell
irm https://github.com/ericfitz/agentbus/releases/latest/download/install.ps1 | iex
```

Scoop and winget packages, WSL notes and the installers' variables are in
the [install guide](docs/install.md).
```

and change "Both verify what they download" to "All three verify what they download".

- [ ] **Step 3: Release notes.** Append to the next release's notes file (`release/notes-v1.14.0.md` from #34, or the current one):

```markdown
## Windows

agentbus runs natively on Windows 10/11 (amd64 and arm64): `agentbus wait`
follows its harness and is replaced with exit code 3 as on Unix, the TUI
opens the config in Notepad by default, and configuration and data live in
`%AppData%\agentbus` and `%LocalAppData%\agentbus`. Install with

    irm https://github.com/ericfitz/agentbus/releases/latest/download/install.ps1 | iex

or `scoop install agentbus` (bucket `ericfitz`) or `winget install
ericfitz.agentbus`. The release carries `agentbus-<tag>-windows-amd64.zip`
and `-arm64.zip` plus `install.ps1`, all in the signed `SHA256SUMS`.

Under WSL, use the Linux installer; Windows and WSL buses are separate, and
a data directory across the boundary is refused (#35).
```

- [ ] **Step 4: Verify every claim against the code**

Run: `cd <repo root> && rg -n 'notepad' internal/tui/editor_windows.go && rg -n 'Programs' install.ps1 && rg -n 'AppData' internal/config/paths_windows.go && rg -n 'scoop-bucket|ericfitz.agentbus' release/release.sh release/agentbus.scoop.json.tmpl`
Expected: each claim has a source line.

- [ ] **Step 5: Commit**

```bash
git add docs/install.md README.md release/notes-v1.14.0.md
git commit -m "docs: Windows and WSL install, paths, and the boundary guard"
```

---

### Task 14: Done gate, release checks, and user steps

- [ ] **Step 1: Run both gates**

Run: `cd <repo root> && make verify && make release-check`
Expected: `verify: OK` (with `vet-cross`), `test-release: 37 passed, 0 failed`, `test-render: OK`, `test-install: 145 passed, 0 failed` (or only `key-matches-pub` pending), actionlint silent.

- [ ] **Step 2: Self-test commands for the report**

```
release/test-install.sh --self-test
release/test-release.sh
release/test-render.sh
release\test-install.ps1 -SelfTest      (Windows: workflow and VM)
pwsh -File release\test-windows.ps1     (VM)
```

- [ ] **Step 3: User steps (report verbatim; none are implementer actions)**

1. `Approve: push main to origin (runs .github/workflows/windows.yml on windows-latest and windows-11-arm)`; then fix whatever the workflow reports.
2. In the VM: `git pull`, `pwsh -File release\test-windows.ps1`, then `docs/windows-checklist.md`.
3. Create `~/.keys/KOMAC_GITHUB_KEY` (mode 600) containing `export KOMAC_GITHUB_KEY='<GitHub PAT with public_repo scope>'`, and `brew install komac`.
4. `Approve: create the empty public repository ericfitz/scoop-bucket on GitHub (gh repo create ericfitz/scoop-bucket --public)`; `release.sh` clones it over SSH at `$SCOOP_DIR` and pushes `bucket/agentbus.json` on the first release.
5. After the first release with Windows assets: `Approve: run komac new ericfitz.agentbus (first winget submission, reviewed by Microsoft)` with the two zip URLs; later releases go through `release.sh`'s `submit_winget`.
6. Embed the key in `install.ps1` once the `.pub` exists: `release/embed-key.sh release/agentbus-release-ed25519.pub install.sh install.ps1`.

---

## Self-review notes

- Spec coverage: process table (Task 2), shells (Task 2), file locks (Task 1), wait replacement (Task 3), signals (existing behavior; `disconnect` subtest and `watchHarness` test run on Windows), editor (Task 5), default paths (Task 4), Windows test counterparts and skips (Tasks 2-7, inventory table), boundary guard with recorded inputs (Task 6), manual WSL check (Task 12), release assets and `file` check (Task 10), `install.ps1` steps 1-8 and state (Task 8, tested in Task 9), Scoop and winget (Task 10), `windows.yml` (Task 11), `test-install.ps1` cases (Task 9), VM script and checklist (Task 12), docs, README, notes, CLAUDE.md (Tasks 11, 13).
- Resolved: `WNetGetConnection` is not in `x/sys/windows`, so it is loaded from `mpr.dll` (Task 6). `actionlint` enters `make release-check` here (first workflow) rather than in #36. `vet-cross` joins `make verify` so Windows compilation is checked by the done gate.
- Deviation from the spec's "interrupted run" harness case: a hard kill cannot run `finally`; the harness pins the `finally` path with a failing run (`failed-run-leaves-no-temp`) and the Ctrl+C path shares it.
