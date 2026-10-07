# Linux releases: tarballs, a signed install script, and Homebrew on Linux

Status: Approved 2026-10-06 (design). Tracked in #34. Follow-up: #36
(GitHub artifact attestations).

## Goal

Make agentbus installable on Linux cloud servers (amd64 and arm64, where
agents may run autonomously) and Linux desktops (where developers run
coding-agent CLIs), with every download verifiable.

The code already runs on Linux: it builds with `CGO_ENABLED=0 GOOS=linux`
for amd64 and arm64, `internal/procs/procs_linux.go` covers process checks,
and `go test ./...` passed in a `golang:1.27` linux/arm64 container on
2026-10-06. Only packaging is missing: `release/release.sh` builds, signs
and notarizes darwin binaries alone, and the formula declares
`depends_on :macos`.

## Human decisions (user, 2026-10-06)

1. **Targets:** cloud servers (amd64, arm64) and Linux desktops.
2. **Distribution:** release tarballs plus an `install.sh` that installs to
   `~/.local/bin` without root (the pattern Claude Code's and Codex's
   Linux installers use), with Homebrew `on_linux` blocks as a secondary
   path. No .deb or .rpm, and no apt or dnf repository (superseded by decision 10).
3. **Verification:** one `SHA256SUMS` over every tarball, signed with an
   Ed25519 key through OpenSSL 3, verified by `install.sh` with the public
   key built in. Linux binaries are built locally by `release.sh`.
4. **Attestations later:** building Linux in GitHub Actions with artifact
   attestations is #36, added on top of the signed `SHA256SUMS`.
5. **Missing OpenSSL 3:** `install.sh` refuses and suggests a
   platform-appropriate way to get it.

Rejected: .deb/.rpm and hosted repositories (signing keys, hosting and
metadata to maintain, which none of the agent CLIs take on; a bare package
needs root and does not update itself); minisign (rarely preinstalled, so
`install.sh` would usually fall back to a checksum, while OpenSSL 3 ships
with Ubuntu 22.04+, Debian 12+ and RHEL 9+); checksums alone (whoever can
replace a release asset can replace its checksum); attestations now (they
need a CI release build, and verifying them needs `gh` or `cosign`, which
servers rarely have).

## Human decisions (user, 2026-10-07)

Approved by Eric on 2026-10-07.

6. **Stable tags only.** `release.sh` accepts only `vX.Y.Z` tags: a
   release candidate would become GitHub's latest release and `install.sh`
   would reject it.
7. **`install.sh` refusals** beyond the original plan: it refuses a copy
   without an embedded release key, requires `AGENTBUS_VERSION` to be exactly
   `vX.Y.Z` (no suffix, no newline), rejects command-line arguments, refuses
   when `<install dir>/agentbus` is a directory, and strips trailing slashes
   from `AGENTBUS_BASE_URL` and `AGENTBUS_INSTALL_DIR`.
8. **`release.sh` preflight**, before any build: the tag must be on origin,
   the draft is created with `gh release create --verify-tag`, and
   `install.sh` and `install.ps1` at the tag must have the release key
   embedded.
9. **`AGENTBUS_SIGNING_KEY`** overrides the default signing key path
   `~/.keys/agentbus-release-ed25519.pem`. The path is passed only to
   `openssl`.
10. **Formula layout A.** The formula uses top-level
    `OS`/`Hardware::CPU` conditionals (not `on_macos`/`on_linux` blocks) so it
    passes `brew style` and `brew audit --strict`. Trade-off: `brew fetch --os
    linux` on a Mac fetches the macOS tarball, because the conditionals read
    the host rather than Homebrew's simulated system; `brew install` on a real
    host is correct. `release/test-render.sh` runs `brew style` and a
    stubbed-host URL-selection check when brew is installed.

## Release assets

Each release carries:

- `agentbus-<tag>-macos-universal.tar.gz` (unchanged build, signing and
  notarization)
- `agentbus-<tag>-linux-amd64.tar.gz`, `agentbus-<tag>-linux-arm64.tar.gz`
  (each holds one `agentbus` binary, like the macOS tarball)
- `install.sh`
- `SHA256SUMS`: one `shasum -a 256` line per file above, in the format
  `sha256sum -c` reads
- `SHA256SUMS.sig`: an Ed25519 signature over `SHA256SUMS`

`SHA256SUMS` replaces the per-tarball `.sha256` file.

## release.sh

Additions, in order:

1. **Preflight**, before any build. Fail with a message naming what is
   missing:
   - Homebrew `openssl@3` (`$(brew --prefix openssl@3)/bin/openssl`; the
     system `/usr/bin/openssl` is LibreSSL 3.3.6 and is not used),
   - `docker` with a running daemon,
   - `~/.keys/agentbus-release-ed25519.pem`,
   - the committed public key `release/agentbus-release-ed25519.pub`, and
     that the private key matches it (sign a fixed string, verify with the
     public key).
2. **Linux builds** from the tag's worktree, for `amd64` and `arm64`:
   `CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build -trimpath -ldflags "-s
   -w -X <version var>=<version>"`. The binaries are static.
3. **Smoke test** each Linux binary: `docker run --rm --platform
   linux/<arch> -v <dist>:/d:ro alpine /d/agentbus-linux-<arch> version`
   must print the version (Docker emulates the other architecture).
4. **Package** all three tarballs and `install.sh` (the repository-root
   script, copied from the tag's worktree), write `SHA256SUMS`, and sign it:
   `openssl pkeyutl -sign -rawin -inkey ~/.keys/agentbus-release-ed25519.pem
   -in SHA256SUMS -out SHA256SUMS.sig`. Verify the signature with the
   committed public key before going on.
5. **Publish:** create the release if it does not exist; upload every asset
   with `gh release upload --clobber`.
6. **Formula:** render the template (below); fail if any `__[A-Z0-9_]+__`
   remains; commit and push to the tap only if the rendered file differs
   from the tap's copy.

State:

- Rerun for the same tag after a failure at any step: the release is
  reused, every asset is re-uploaded with `--clobber`, and the formula is
  re-rendered from the same run's `SHA256SUMS`, so assets, checksums and
  formula always come from one run. (A rebuilt macOS binary has a new
  signing timestamp and so a new checksum; that is consistent because
  everything is replaced together.)
- Two concurrent runs for one tag: not supported; the second can clobber
  the first's assets. The release is run by one person from one machine.
- Leftovers: `dist/` is recreated at the start of each run, and the tag
  worktree is removed on exit, as today.

### Key setup (once, by the user)

```
umask 077
"$(brew --prefix openssl@3)/bin/openssl" genpkey -algorithm ed25519 -out ~/.keys/agentbus-release-ed25519.pem
"$(brew --prefix openssl@3)/bin/openssl" pkey -in ~/.keys/agentbus-release-ed25519.pem -pubout -out release/agentbus-release-ed25519.pub
```

The public key is committed and embedded in `install.sh`. Agents never read
the private key; only `release.sh` passes its path to openssl. Losing it
means generating a new pair and shipping the new public key in the next
`install.sh`; earlier releases stay verifiable with the old key from their
own `install.sh`.

## install.sh

Served as a release asset, so the stable URL
`https://github.com/ericfitz/agentbus/releases/latest/download/install.sh`
always gives the newest release's copy, and the script and the binaries
come from the same signed release. Usage:

```
curl -fsSL https://github.com/ericfitz/agentbus/releases/latest/download/install.sh | sh
```

POSIX `sh`, `set -eu`. Steps:

1. **Platform:** `uname -s` must be `Linux`; on `Darwin` it prints `brew
   install ericfitz/tap/agentbus` and exits non-zero. `uname -m`:
   `x86_64` → `amd64`, `aarch64` or `arm64` → `arm64`; anything else fails
   naming the architecture.
2. **Tools:** `curl` and `tar`; `sha256sum`, or `shasum -a 256`; an OpenSSL
   3 command (below). Each missing tool fails with its name.
3. **Version:** `AGENTBUS_VERSION` (`vX.Y.Z`) if set; otherwise the tag
   from the `Location` header of `<base>/releases/latest` (no API call, so
   no anonymous rate limit). An unparsable answer fails.
4. **Download** the tarball, `SHA256SUMS` and `SHA256SUMS.sig` into a
   `mktemp -d` directory. `AGENTBUS_BASE_URL` (default
   `https://github.com/ericfitz/agentbus`) points at a mirror or, in tests,
   a local server.
5. **Verify the signature** with the embedded public key: `<openssl>
   pkeyutl -verify -rawin -pubin -inkey <key file> -in SHA256SUMS -sigfile
   SHA256SUMS.sig`. Failure stops the install.
6. **Verify the checksum** of the tarball against its `SHA256SUMS` line. A
   missing line or a mismatch stops the install.
7. **Install** to `${AGENTBUS_INSTALL_DIR:-$HOME/.local/bin}`: create the
   directory if needed; if it is not writable, fail with how to rerun
   (another `AGENTBUS_INSTALL_DIR`, or `sudo env AGENTBUS_INSTALL_DIR=...
   sh`). Extract into the temp directory, copy to
   `<dir>/.agentbus.tmp.<pid>`, `chmod 755`, and `mv` over
   `<dir>/agentbus`. The script never runs `sudo` itself.
8. **Report** the installed version (`agentbus version`), warn if the
   directory is not on `PATH`, and print the next step: `agentbus init
   --global`, and after an upgrade, restart every harness session and the
   TUI. It does not run `init`.

### Finding OpenSSL 3

The script uses the first of `openssl` and `openssl3` (EPEL's name) whose
`version` reports 3 or newer.

If there is none, it refuses and prints a suggestion chosen from
`/etc/os-release` (`ID`, then each word of `ID_LIKE`):

| Distribution | Suggestion |
|---|---|
| debian, ubuntu | `sudo apt-get install openssl` |
| fedora, rhel, centos, rocky, almalinux, amzn (2023) | `sudo dnf install openssl` |
| alpine | `apk add openssl` (with `sudo` unless running as root) |
| arch | `sudo pacman -S openssl` |
| opensuse*, sles | `sudo zypper install openssl-3` |
| anything else | install OpenSSL 3 with the system package manager |

If an older OpenSSL is found (1.1 on Ubuntu 20.04, Debian 11, RHEL 8,
Amazon Linux 2), installing the package will not help, so the message
names the version found and offers: EPEL's `openssl3` package on RHEL 8
and its rebuilds, upgrading the distribution, or
`AGENTBUS_SKIP_SIGNATURE=1`.

`AGENTBUS_SKIP_SIGNATURE=1` skips only step 5, says so on stderr, and still
runs step 6. Any other value of the variable is rejected.

### State

- Second run: downloads and verifies again, then replaces the binary
  (upgrade or reinstall).
- Upgrading while agentbus runs: `mv` replaces the directory entry; a
  running process keeps its open file.
- Two runs at once: each has its own temp directory and
  `.agentbus.tmp.<pid>`; the last `mv` wins, and both leave a complete,
  verified binary.
- Interrupted run: an `EXIT` trap removes the temp directory and the
  `.agentbus.tmp.<pid>` file. A leftover from a killed run
  (`.agentbus.tmp.*`) is never executed and is overwritten or ignored by
  later runs.

## Homebrew formula

`release/agentbus.rb.tmpl`:

```ruby
if OS.mac?
  url "__MACOS_URL__"
  sha256 "__MACOS_SHA256__"
elsif OS.linux? && Hardware::CPU.intel?
  url "__LINUX_AMD64_URL__"
  sha256 "__LINUX_AMD64_SHA256__"
elsif OS.linux? && Hardware::CPU.arm?
  url "__LINUX_ARM64_URL__"
  sha256 "__LINUX_ARM64_SHA256__"
end
```

(`on_macos`/`on_linux` blocks cannot hold `url`/`sha256`: `brew style` and
`brew audit --strict` reject them. See decision 10.)

`depends_on :macos` is removed, and the header comment no longer says
macOS only. `release.sh` fills the placeholders from `SHA256SUMS`.

`release/check-linux-brew.sh`, run after a release, runs `brew install
ericfitz/tap/agentbus && brew test agentbus` in the `homebrew/brew` Docker
image (`--platform linux/amd64`; arm64 where the image supports it) and
prints the result.

## Documentation

- `docs/install.md`: a Linux section with the `install.sh` command; its
  environment variables (`AGENTBUS_VERSION`, `AGENTBUS_INSTALL_DIR`,
  `AGENTBUS_BASE_URL`, `AGENTBUS_SKIP_SIGNATURE`); verifying a download by
  hand (`openssl pkeyutl -verify ...`, then `grep <tarball> SHA256SUMS |
  sha256sum -c`, which also works with BusyBox `sha256sum`); Homebrew on
  Linux; and `CGO_ENABLED=0 go install -trimpath
  github.com/ericfitz/agentbus@latest` as a fallback (no version string).
- README install section mentions Linux.
- Release notes for the first release with Linux assets describe the new
  assets and the `SHA256SUMS` change.

## Testing

Test harnesses are code and are tested before any manual release run.

- `release/test-install.sh` builds a fake release in a temp directory (a
  stub `agentbus` that prints a version, tarballs, `SHA256SUMS`, and a
  signature from a throwaway key, with `install.sh`'s embedded key swapped
  for the throwaway public key in the test copy only), serves it with a
  local HTTP server, and runs `install.sh` with `AGENTBUS_BASE_URL` in
  `ubuntu`, `debian` and `alpine` containers on amd64 and arm64. Cases:
  - valid release installs; `agentbus version` prints the fake version;
  - tampered tarball, tampered `SHA256SUMS`, and tampered signature each
    refuse;
  - no OpenSSL: refuses, and the suggestion matches the container's
    distribution; OpenSSL 1.1 (a fake `openssl` printing `OpenSSL 1.1.1`):
    refuses with the too-old message;
  - `openssl3` command name accepted;
  - `AGENTBUS_SKIP_SIGNATURE=1` installs with a valid checksum and refuses
    a tampered tarball; `AGENTBUS_SKIP_SIGNATURE=yes` is rejected;
  - unknown architecture (a fake `uname`) refuses, naming it;
  - unwritable install directory refuses with the rerun advice;
  - second run upgrades from one fake version to another;
  - two concurrent runs both succeed and leave a working binary;
  - a leftover `.agentbus.tmp.*` file does not break a run, and a run
    killed mid-install leaves no temp directory;
  - the public key embedded in the real `install.sh` equals
    `release/agentbus-release-ed25519.pub`.
- `release/test-render.sh` renders the formula template with fake values,
  asserts that no `__...__` remains, and runs `ruby -c` on the result.
- `make release-check` runs `shellcheck` on `release/*.sh` and
  `install.sh`, then both test scripts. It needs Docker and stays out of
  `make verify`.
- First real release: `release/check-linux-brew.sh`, and `install.sh` run
  from the published URL on one amd64 and one arm64 Linux host.
