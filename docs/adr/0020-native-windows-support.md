# ADR 0020: Native Windows support

Status: Accepted 2026-10-07. Human decision by Eric. Tracked in #35. Design:
[docs/superpowers/specs/2026-10-06-windows-support-design.md](../superpowers/specs/2026-10-06-windows-support-design.md).

Supersedes in part [ADR 0001](0001-discussion-decisions.md) (native Windows
runtime out of scope) and [ADR 0017](0017-identity-from-harness-process.md)
(process table read only on macOS and Linux).

## Context

ADR 0001 kept a native Windows runtime out of the initial implementation, and
ADR 0017 made the process table (`procs`) available only on macOS and Linux,
so on other platforms identity fell back to the working-directory name.
Agents now run on Windows, both natively and under WSL.

## Human decisions (user, 2026-10-07)

1. agentbus runs natively on Windows, amd64 and arm64. WSL keeps using the
   Linux build.
2. Windows has its own process table in `internal/procs/procs_windows.go`:
   a Toolhelp snapshot for parent and name, and `GetProcessTimes` for the
   start time, so identity from the harness process works as on macOS and
   Linux.
3. Native Windows and WSL keep separate buses. agentbus adds no IPC between
   them, and `bus.Open` refuses a database across the Windows-WSL boundary.
4. Paths: config in `%AppData%\agentbus`, data in `%LocalAppData%\agentbus`.
5. Packaging: `install.ps1`, a Scoop bucket and winget.

Rejected: WSL only; one shared bus over a new IPC mechanism; a warning
instead of a refusal for a cross-boundary database. See the design for the
full list.
