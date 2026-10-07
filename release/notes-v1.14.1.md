## Install on Windows

Run this in PowerShell, then open a new terminal:

    irm https://github.com/ericfitz/agentbus/releases/latest/download/install.ps1 | iex

It verifies the download, installs `agentbus.exe` to
`%LocalAppData%\Programs\agentbus` and adds it to your user `PATH`. Unzipping
`agentbus-v1.14.1-windows-<arch>.zip` by hand does not install it (the zip's
`README.txt` has the manual steps), and `agentbus init` refuses to run until
the `agentbus.exe` on `PATH` is the one you ran.

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

## Linux releases

Each release now ships `agentbus-<tag>-linux-amd64.tar.gz` and
`agentbus-<tag>-linux-arm64.tar.gz` (static binaries) next to the macOS
universal tarball, plus `install.sh`:

    curl -fsSL https://github.com/ericfitz/agentbus/releases/latest/download/install.sh | sh

installs to `~/.local/bin` without root and verifies the download. The
Homebrew formula installs on Linux as well.

The per-tarball `.sha256` file is replaced by one `SHA256SUMS` over every
asset, signed with the project's Ed25519 key as `SHA256SUMS.sig`; the public
key is `release/agentbus-release-ed25519.pub` in the repository and is
embedded in `install.sh` and `install.ps1` (#34).

## Build provenance

The Linux and Windows archives are built in GitHub Actions on runners of
their own OS and architecture, run there, and published with build
provenance attestations (`gh attestation verify <file> --repo ericfitz/agentbus
--signer-workflow ericfitz/agentbus/.github/workflows/release-build.yml`)
(#36).

## Structured tags and prefix patterns

A tag may now contain one `:` (`env:prod`, `repo:tmi`, `area:aws`) and be
up to 32 characters. The bus gives the colon no meaning; the using-agentbus
vocabulary keys the dimensions (`env:`, `repo:`, `area:`, `kind:`) and
keeps triage tags flat (`failed`, `blocked`, `breaking`).

Every tag filter accepts a prefix pattern: a tag ending in `*` matches
every tag with that prefix. `history` and `search` `tags`, `subscribe` and
`unsubscribe` `tags`, `.local/agentbus.json` `tag_subscriptions`, and the
TUI follow prompt all take `env:*`. A subscription `["change", "area:*"]`
delivers every change whatever its area, once per message; `matched_tags`
names the message's tags (`area:aws`), not the pattern. Matching stays on
the `message_tags(tag, seq)` index (#20, ADR 0009 amendment 2026-10-06).

**Breaking:** `_` is no longer a tag character. `send`, `edit_memory` and
`.local/agentbus.json` `tags` reject it (a repository `tags` entry with
`_` comes back in `ignored_tags`). Stored tags are not rewritten: a message
tagged with `_` stays readable, but that tag can no longer be named in a
filter. A client built against the old rule keeps working unless it sends
`_`.

## Message refs removed

**Breaking:** `send` and `edit_memory` reject `refs`, payloads no longer
carry it, and existing refs appear as a `Refs:` block at the end of their
message (#33, ADR 0019).

## Upgrading

This release moves the database to **schema v11**. The first 1.14.1 process
to open it migrates it, and older binaries then refuse it.

1. `brew upgrade agentbus`
2. `agentbus init --global` (the skill text changed)
3. Restart every harness session and `agentbus tui` together, so no 1.13.x
   process keeps running against the upgraded database.
