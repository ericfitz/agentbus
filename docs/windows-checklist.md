# Windows manual checks

Run the general checks in the Windows 11 ARM VM (VMware Fusion) and the
WSL2 shared-database section in a UTM guest with nested virtualization,
before the first Windows release and after any change to harness detection (`internal/procs`,
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
      neither `VISUAL` nor `EDITOR` is set.
- [ ] `install.ps1` from the published URL installs, and upgrades while an
      MCP server is running (the old `agentbus.exe` is renamed `.old-*`
      and removed by the next run).

## Shared database guard (needs WSL2 in the VM)

Nested virtualization: the Mac (M4 Pro, macOS 27) exposes it in
Hypervisor.framework; enable it for the Windows 11 ARM guest in UTM 4.7.4
and confirm `wsl --install` succeeds. If UTM does not expose it, use a physical Windows
machine or a cloud Windows VM with nested virtualization.

- [ ] In WSL: `findmnt -T /mnt/c -o FSTYPE` reports `9p` (or `drvfs`);
      record the value here: ______.
- [ ] In WSL: `AGENTBUS_DATA_DIR=/mnt/c/Users/<you>/agentbus-test agentbus status`
      refuses with the boundary error naming the filesystem.
- [ ] Native Windows: `$env:AGENTBUS_DATA_DIR='\\wsl.localhost\<distro>\home\<you>\agentbus-test'; agentbus status`
      refuses with the boundary error.
- [ ] Native Windows, a drive mapped to `\\wsl.localhost\<distro>` (`net use
      W: \\wsl.localhost\<distro>`): `$env:AGENTBUS_DATA_DIR='W:\agentbus-test'; agentbus status`
      refuses.

## Packaging

- [ ] `scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket && scoop install agentbus`
      installs; `scoop update agentbus` after stopping sessions upgrades.
- [ ] `winget install ericfitz.agentbus` installs once the package is
      published.
- [ ] On a release made with `--windows-signing=on`:
      `Get-AuthenticodeSignature .\agentbus.exe` reports `Valid` with the
      expected signer, for the arm64 binary in the VM and for the amd64
      binary (downloaded zip), and SmartScreen does not warn on first run
      (it may warn until the signing identity has reputation).
