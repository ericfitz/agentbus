# Linux Releases Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship Linux amd64 and arm64 tarballs with one Ed25519-signed `SHA256SUMS`, a root-less `install.sh` that verifies them, and a Homebrew formula that installs on Linux too.

**Architecture:** `release/release.sh` is restructured into named bash functions with a `main` guard so its pieces can be unit-tested by sourcing; it gains a preflight, Linux builds, a Docker smoke test, `SHA256SUMS` signing and idempotent publishing. `install.sh` (POSIX sh, repository root) carries the public key in a delimited block that `release/embed-key.sh` renders from the committed `.pub`. Three test harnesses (`test-release.sh`, `test-render.sh`, `test-install.sh` with Docker containers and a local fixture server) run from `make release-check`.

**Tech Stack:** bash 3.2+ (macOS) for release tooling, POSIX sh for `install.sh`, OpenSSL 3 (`brew --prefix openssl@3`), Docker Desktop, python3 (fixture server), shellcheck, ruby (`ruby -c`), `gh`.

**Spec:** `docs/superpowers/specs/2026-10-06-linux-releases-design.md`

## Global Constraints

- **Release notes (orchestrator override, 2026-10-07):** release notes files are written in the `chore: release vX.Y.Z` commit at release time (`git log -- release/notes-v1.13.0.md`), never in a feature commit. Wherever a task below says to create, append to or `git add` `release/notes-v1.14.0.md` (or any `release/notes-*.md`), do not touch the file: put that text in the execution's final report for the release author instead, and drop the path from the commit.
- Depends on nothing else; #35, #36 and #37 build on this plan's file, function and variable names (see each task's Interfaces block). Do not rename anything listed there.
- Release assets per tag, exactly: `agentbus-<tag>-macos-universal.tar.gz`, `agentbus-<tag>-linux-amd64.tar.gz`, `agentbus-<tag>-linux-arm64.tar.gz`, `install.sh`, `SHA256SUMS`, `SHA256SUMS.sig`. Each tarball holds one file named `agentbus`. `SHA256SUMS` replaces the per-tarball `.sha256` file.
- Linux build command, verbatim: `CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build -trimpath -ldflags "-s -w -X github.com/ericfitz/agentbus/internal/mcpserver.Version=<version>"`.
- Signing: `openssl pkeyutl -sign -rawin -inkey ~/.keys/agentbus-release-ed25519.pem -in SHA256SUMS -out SHA256SUMS.sig` with Homebrew `openssl@3` only (`/usr/bin/openssl` is LibreSSL and is never used). Verification: `openssl pkeyutl -verify -rawin -pubin -inkey <pub> -in SHA256SUMS -sigfile SHA256SUMS.sig`.
- The private key lives at `~/.keys/agentbus-release-ed25519.pem`; the public key at `release/agentbus-release-ed25519.pub` (committed). No task reads, prints or copies the private key; scripts only pass its path to `openssl`. The user generates both (Task 1).
- `install.sh` is POSIX `sh` with `set -eu`, no bashisms (shellcheck `-s sh`). Every other script is bash with `set -euo pipefail` and absolute paths derived from `${BASH_SOURCE[0]}`.
- Scripts that run on target hosts (`install.sh`, container commands) use `grep`/`sed`; the executor's own searches in plan steps use `rg PATTERN <path>`.
- `install.sh` defaults: `AGENTBUS_BASE_URL=https://github.com/ericfitz/agentbus`, `AGENTBUS_INSTALL_DIR=$HOME/.local/bin`; `AGENTBUS_SKIP_SIGNATURE` accepts only unset/empty or `1`; `AGENTBUS_VERSION` must match `vX.Y.Z`. The script never runs `sudo`, never runs `agentbus init`.
- OpenSSL command search order in `install.sh`: `openssl`, then `openssl3`; the first whose `version` reports major 3 or newer wins.
- Homebrew formula: `on_macos`/`on_linux` + `on_intel`/`on_arm` blocks from `release/agentbus.rb.tmpl`; `depends_on :macos` removed; any leftover `__[A-Z0-9_]+__` after rendering is a failure; tap commit and push only when the rendered file differs.
- `make release-check` (shellcheck, the three test scripts) needs Docker and stays out of `make verify`. `make verify` remains the done gate.
- No step pushes, tags, creates a GitHub release, creates repositories or sets GitHub variables. Those are written as user instructions with an `Approve:` line.
- American English throughout.

## Review Focus

- `HOME` unset (systemd unit, minimal container): `$HOME/.local/bin` under `set -u` would abort with a shell error; the script must fail naming `AGENTBUS_INSTALL_DIR` as the fix. Pinned in Task 6 (`home-unset`).
- `AGENTBUS_VERSION=1.2.3` (no `v`): must be rejected naming the expected form, not turned into a 404 on download. Pinned in Task 6 (`version-no-v`).
- Relative `AGENTBUS_INSTALL_DIR=bin`: the `PATH` warning, the `.agentbus.tmp.<pid>` cleanup trap and `sudo env` advice all assume an absolute path; reject relative values. Pinned in Task 6 (`install-dir-relative`).
- `release.sh <tag>` for a tag that does not exist: `git worktree add` would fail with git's message after preflight passed; preflight must say "tag vX not found" first. Pinned in Task 2 (`check_tag_exists`).
- A `SHA256SUMS` that verifies but lists no line for this architecture's tarball (a partial upload after a failed rerun): install must stop with "no entry for <file>", not install an unverified file. Pinned in Task 6 (`sums-missing-entry`).

---

### Task 1: Release signing key pair (user step) and `embed-key.sh`

**Files:**
- Create: `release/embed-key.sh`
- Create: `release/agentbus-release-ed25519.pub` (by the user, not the implementer)
- Test: `release/test-release.sh` (created here, extended in Tasks 2 and 4)

**Interfaces:**
- Produces: `release/embed-key.sh <pubkey-file> <script>...` rewrites the text between `# BEGIN agentbus release public key` and `# END agentbus release public key` in each script so that it reads `PUBKEY_PEM='<pem>'` (sh) or `$PubKeyPem = @'`…`'@` (ps1, by extension, used by #35). Exit 1 naming the script when a marker is missing.
- Produces: `release/test-release.sh`, a bash test runner with `check <name> <expected-exit> <substring> -- <command...>` and a final PASS/FAIL summary; later tasks append cases.

- [ ] **Step 1: User generates the key pair (no implementer action).** The implementer does not run these; the orchestrator relays them to the user and continues with the rest of the plan. Until the `.pub` exists, Task 6's `key-matches-pub` case and `release.sh` preflight fail with a message pointing here.

```
umask 077
"$(brew --prefix openssl@3)/bin/openssl" genpkey -algorithm ed25519 -out ~/.keys/agentbus-release-ed25519.pem
"$(brew --prefix openssl@3)/bin/openssl" pkey -in ~/.keys/agentbus-release-ed25519.pem -pubout -out /Users/efitz/Projects/agentbus/release/agentbus-release-ed25519.pub
chmod 644 /Users/efitz/Projects/agentbus/release/agentbus-release-ed25519.pub
```

Then: `git -C /Users/efitz/Projects/agentbus add release/agentbus-release-ed25519.pub && git -C /Users/efitz/Projects/agentbus commit -m "release: add Ed25519 release public key"`.

- [ ] **Step 2: Write the failing test runner with the embed cases.** Create `release/test-release.sh`:

```bash
#!/usr/bin/env bash
# Unit tests for the release tooling: embed-key.sh and release.sh's pure
# functions (sourced, never run). No network, no Docker, no keys in ~/.keys.
#   release/test-release.sh
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PASS=0; FAIL=0

# check <name> <expected-exit> <required-substring> -- <command...>
# Runs the command, compares its exit code and looks for the substring in its
# combined output. An empty substring skips the output check.
check() {
    local name="$1" want_exit="$2" want_out="$3"; shift 3
    [[ "$1" == "--" ]] && shift
    local out exit=0
    out="$("$@" 2>&1)" || exit=$?
    if [[ "$exit" == "$want_exit" && ( -z "$want_out" || "$out" == *"$want_out"* ) ]]; then
        PASS=$((PASS + 1)); echo "PASS $name"
    else
        FAIL=$((FAIL + 1)); echo "FAIL $name: exit=$exit want=$want_exit; output:"; echo "$out" | sed 's/^/    /'
    fi
}

# --- embed-key.sh -----------------------------------------------------------
openssl_bin="$(brew --prefix openssl@3)/bin/openssl"
"$openssl_bin" genpkey -algorithm ed25519 -out "$WORK/throwaway.pem" 2>/dev/null
"$openssl_bin" pkey -in "$WORK/throwaway.pem" -pubout -out "$WORK/throwaway.pub" 2>/dev/null
cat > "$WORK/script.sh" <<'EOF'
#!/bin/sh
echo before
# BEGIN agentbus release public key
PUBKEY_PEM='placeholder'
# END agentbus release public key
echo after
EOF
cat > "$WORK/script.ps1" <<'EOF'
# BEGIN agentbus release public key
$PubKeyPem = @'
placeholder
'@
# END agentbus release public key
Write-Host after
EOF
check embed-sh 0 "" -- "$HERE/embed-key.sh" "$WORK/throwaway.pub" "$WORK/script.sh"
check embed-sh-has-key 0 "" -- grep -q "$(sed -n 2p "$WORK/throwaway.pub")" "$WORK/script.sh"
check embed-sh-keeps-rest 0 "" -- bash -c "sed -n '1,2p;\$p' '$WORK/script.sh' | diff - <(printf '#!/bin/sh\necho before\necho after\n')"
check embed-sh-valid-sh 0 "" -- sh -n "$WORK/script.sh"
check embed-sh-key-roundtrip 0 "" -- bash -c "sh -c '. \"$WORK/script.sh\" >/dev/null; printf \"%s\\n\" \"\$PUBKEY_PEM\"' | diff - '$WORK/throwaway.pub'"
check embed-ps1 0 "" -- "$HERE/embed-key.sh" "$WORK/throwaway.pub" "$WORK/script.ps1"
check embed-ps1-has-key 0 "" -- grep -q "$(sed -n 2p "$WORK/throwaway.pub")" "$WORK/script.ps1"
check embed-idempotent 0 "" -- bash -c "cp '$WORK/script.sh' '$WORK/again.sh' && '$HERE/embed-key.sh' '$WORK/throwaway.pub' '$WORK/again.sh' && cmp '$WORK/script.sh' '$WORK/again.sh'"
printf 'no markers\n' > "$WORK/bare.sh"
check embed-missing-marker 1 "bare.sh" -- "$HERE/embed-key.sh" "$WORK/throwaway.pub" "$WORK/bare.sh"
check embed-missing-pub 1 "no such public key" -- "$HERE/embed-key.sh" "$WORK/nope.pub" "$WORK/script.sh"
check embed-bad-pub 1 "not a PEM public key" -- bash -c "printf 'junk\n' > '$WORK/junk.pub'; '$HERE/embed-key.sh' '$WORK/junk.pub' '$WORK/script.sh'"
check embed-usage 2 "usage" -- "$HERE/embed-key.sh"

echo "test-release: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
```

- [ ] **Step 3: Run it to verify it fails**

Run: `chmod +x /Users/efitz/Projects/agentbus/release/test-release.sh && /Users/efitz/Projects/agentbus/release/test-release.sh`
Expected: `FAIL embed-sh` (embed-key.sh does not exist), summary with failures, exit 1.

- [ ] **Step 4: Write `release/embed-key.sh`**

```bash
#!/usr/bin/env bash
# Embed a PEM public key into install scripts, between the markers
#   # BEGIN agentbus release public key
#   # END agentbus release public key
# A .sh script gets  PUBKEY_PEM='<pem>'  and a .ps1 script gets
#   $PubKeyPem = @'
#   <pem>
#   '@
# Idempotent: running it again with the same key changes nothing.
#   release/embed-key.sh release/agentbus-release-ed25519.pub install.sh install.ps1
set -euo pipefail

BEGIN='# BEGIN agentbus release public key'
END='# END agentbus release public key'

[[ $# -ge 2 ]] || { echo "usage: embed-key.sh <pubkey.pem> <script>..." >&2; exit 2; }
PUB="$1"; shift
[[ -r "$PUB" ]] || { echo "error: no such public key: $PUB" >&2; exit 1; }
grep -q '^-----BEGIN PUBLIC KEY-----$' "$PUB" && grep -q '^-----END PUBLIC KEY-----$' "$PUB" \
    || { echo "error: $PUB is not a PEM public key" >&2; exit 1; }
PEM="$(cat "$PUB")"

for script in "$@"; do
    [[ -f "$script" ]] || { echo "error: no such script: $script" >&2; exit 1; }
    grep -qF "$BEGIN" "$script" && grep -qF "$END" "$script" \
        || { echo "error: $script has no '$BEGIN' / '$END' markers" >&2; exit 1; }
    case "$script" in
        *.ps1) block="\$PubKeyPem = @'"$'\n'"$PEM"$'\n'"'@" ;;
        *)     block="PUBKEY_PEM='$PEM'" ;;
    esac
    tmp="$(mktemp)"
    # The block goes through the environment, not -v: awk -v would interpret
    # backslashes, and ENVIRON keeps the embedded newlines verbatim.
    BLOCK="$block" awk -v begin="$BEGIN" -v end="$END" '
        $0 == begin { print; print ENVIRON["BLOCK"]; skipping = 1; next }
        $0 == end   { skipping = 0 }
        !skipping   { print }
    ' "$script" > "$tmp"
    if cmp -s "$tmp" "$script"; then rm -f "$tmp"; echo "$script: unchanged"; else
        chmod "$(stat -f '%Lp' "$script")" "$tmp"   # BSD stat: this tooling runs on macOS
        mv "$tmp" "$script"; echo "$script: key embedded"
    fi
done
```

- [ ] **Step 5: Run the tests**

Run: `chmod +x /Users/efitz/Projects/agentbus/release/embed-key.sh && /Users/efitz/Projects/agentbus/release/test-release.sh && shellcheck /Users/efitz/Projects/agentbus/release/embed-key.sh /Users/efitz/Projects/agentbus/release/test-release.sh`
Expected: every `embed-*` case PASS, `test-release: 12 passed, 0 failed`, shellcheck silent.

- [ ] **Step 6: Commit**

```bash
git add release/embed-key.sh release/test-release.sh
git commit -m "release: embed-key.sh renders the public key into install scripts"
```

---

### Task 2: Restructure `release.sh` into functions with a preflight

**Files:**
- Modify: `release/release.sh` (whole file)
- Modify: `release/test-release.sh` (append cases)

**Interfaces:**
- Produces (used by #35, #36, #37): globals `BIN_NAME GH_REPO TAP_DIR SIGN_IDENTITY NOTARY_PROFILE VERSION_VAR SIGNING_KEY REPO_ROOT PUBKEY DIST OPENSSL TAG VERSION SRC ARCHIVES`; functions `usage`, `fail`, `parse_args`, `require_tools`, `check_signing_key`, `check_tag_exists`, `checkout_tag`, `build_macos`, `build_linux`, `smoke_linux`, `copy_scripts`, `write_sums`, `sign_sums`, `verify_sums`, `release_notes_args`, `ensure_release`, `upload_assets`, `render_template`, `sum_of`, `update_tap`, `main`; guard `if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi` so tests can `source` it.
- `render_template <tmpl> <out> KEY=VALUE...` substitutes `__KEY__`; fails (exit 1, naming them) if any `__[A-Z0-9_]+__` remains.
- `sum_of <filename>` prints the hash recorded for that file in `$DIST/SHA256SUMS`, exit 1 if absent.

- [ ] **Step 1: Append failing tests to `release/test-release.sh`** (before the summary lines):

```bash
# --- release.sh pure functions ----------------------------------------------
# shellcheck source=release.sh
source "$HERE/release.sh"   # the main guard keeps it from running

check args-none 2 "usage" -- parse_args
check args-bad-tag 2 "tag must look like v1.2.3" -- parse_args 1.2.3
check args-two 2 "usage" -- parse_args v1.2.3 extra
check args-ok 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3-rc.1 && [[ \$VERSION == 1.2.3-rc.1 ]]"

git init -q "$WORK/repo" && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init && git -C "$WORK/repo" tag v0.0.1
check tag-exists 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; check_tag_exists"
check tag-missing 1 "tag v0.0.2 not found" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.2; check_tag_exists"

printf 'url "__URL__"\nsha "__SHA__"\n' > "$WORK/t.tmpl"
check render-ok 0 "" -- bash -c "source '$HERE/release.sh'; render_template '$WORK/t.tmpl' '$WORK/t.out' URL=https://x/y SHA=abc && diff '$WORK/t.out' <(printf 'url \"https://x/y\"\nsha \"abc\"\n')"
check render-leftover 1 "__SHA__" -- bash -c "source '$HERE/release.sh'; render_template '$WORK/t.tmpl' '$WORK/t.out' URL=https://x/y"
check render-bad-arg 1 "expected KEY=VALUE" -- bash -c "source '$HERE/release.sh'; render_template '$WORK/t.tmpl' '$WORK/t.out' URL"

mkdir -p "$WORK/dist"
printf 'aaaa  agentbus-v1.0.0-linux-amd64.tar.gz\nbbbb  install.sh\n' > "$WORK/dist/SHA256SUMS"
check sum-of-ok 0 "aaaa" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/dist'; sum_of agentbus-v1.0.0-linux-amd64.tar.gz"
check sum-of-exact 0 "bbbb" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/dist'; [[ \$(sum_of install.sh) == bbbb ]] && echo bbbb"
check sum-of-missing 1 "no SHA256SUMS entry for nope.tar.gz" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/dist'; sum_of nope.tar.gz"

check notes-generated 0 "--generate-notes" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; release_notes_args"
mkdir -p "$WORK/repo/release" && printf 'notes\n' > "$WORK/repo/release/notes-v0.0.1.md"
check notes-file 0 "--notes-file" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; release_notes_args"
```

- [ ] **Step 2: Run to verify it fails**

Run: `/Users/efitz/Projects/agentbus/release/test-release.sh`
Expected: sourcing the current `release.sh` runs it (no guard) and fails on `usage: release.sh <tag>`; exit non-zero.

- [ ] **Step 3: Rewrite `release/release.sh`**

```bash
#!/usr/bin/env bash
# Build, sign and publish a release of agentbus, then update the Homebrew tap.
# macOS: universal binary, codesigned and notarized here. Linux: static
# amd64/arm64 binaries built here and smoke-tested in Docker. Every archive
# is listed in SHA256SUMS, signed with the Ed25519 release key.
#   ./release/release.sh v0.1.1
# Builds from the tag, so HEAD may be anywhere. Reruns for the same tag reuse
# the release and replace every asset (--clobber).
# One-time setup: a notarytool keychain profile named $NOTARY_PROFILE
# (`xcrun notarytool store-credentials`), and the release key pair (see
# docs/superpowers/specs/2026-10-06-linux-releases-design.md, "Key setup").
set -euo pipefail

BIN_NAME="agentbus"
GH_REPO="ericfitz/agentbus"
TAP_DIR="${TAP_DIR:-$HOME/Projects/homebrew-tap}" # SSH clone of ericfitz/homebrew-tap
SIGN_IDENTITY="Developer ID Application: Robert Fitzgerald (796T45968D)"
NOTARY_PROFILE="sqdist-notary" # team-level credential, shared across projects
VERSION_VAR="github.com/ericfitz/agentbus/internal/mcpserver.Version"
SIGNING_KEY="${AGENTBUS_SIGNING_KEY:-$HOME/.keys/agentbus-release-ed25519.pem}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PUBKEY="${REPO_ROOT}/release/agentbus-release-ed25519.pub"
DIST="${REPO_ROOT}/dist"
OPENSSL=""  # Homebrew openssl@3, set by require_tools
TAG="" VERSION="" SRC=""
ARCHIVES=() # archive file names under $DIST, in SHA256SUMS order

usage() { echo "usage: release.sh <tag>" >&2; exit 2; }
fail() { echo "error: $*" >&2; exit 1; }

parse_args() {
    [[ $# -eq 1 ]] || usage
    TAG="$1"
    [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] \
        || { echo "error: tag must look like v1.2.3 (or v1.2.3-rc.1), got '$TAG'" >&2; exit 2; }
    VERSION="${TAG#v}"
}

# require_tools fails once, naming everything that is missing.
require_tools() {
    local missing=() t prefix
    for t in gh git go lipo codesign xcrun shasum docker; do
        command -v "$t" >/dev/null 2>&1 || missing+=("$t")
    done
    prefix="$(brew --prefix openssl@3 2>/dev/null || true)"
    if [[ -x "$prefix/bin/openssl" ]]; then OPENSSL="$prefix/bin/openssl"; else missing+=("openssl@3 (brew install openssl@3)"); fi
    docker info >/dev/null 2>&1 || missing+=("a running Docker daemon")
    [[ -r "$SIGNING_KEY" ]] || missing+=("signing key $SIGNING_KEY (see the spec's Key setup)")
    [[ -r "$PUBKEY" ]] || missing+=("public key $PUBKEY (see the spec's Key setup)")
    (( ${#missing[@]} == 0 )) || fail "missing: $(printf '%s; ' "${missing[@]}")"
}

# check_signing_key signs a fixed string and verifies it with the committed
# public key, so a wrong or regenerated private key is caught before building.
check_signing_key() {
    local d; d="$(mktemp -d)"
    printf 'agentbus release key check\n' > "$d/msg"
    "$OPENSSL" pkeyutl -sign -rawin -inkey "$SIGNING_KEY" -in "$d/msg" -out "$d/sig"
    if ! "$OPENSSL" pkeyutl -verify -rawin -pubin -inkey "$PUBKEY" -in "$d/msg" -sigfile "$d/sig" >/dev/null 2>&1; then
        rm -rf "$d"; fail "signing key $SIGNING_KEY does not match $PUBKEY"
    fi
    rm -rf "$d"
}

check_tag_exists() {
    git -C "$REPO_ROOT" rev-parse -q --verify "refs/tags/$TAG" >/dev/null || fail "tag $TAG not found in $REPO_ROOT"
}

# checkout_tag builds from the tagged source in a throwaway worktree, whatever HEAD is.
checkout_tag() {
    SRC="$(mktemp -d)/src"
    git -C "$REPO_ROOT" worktree add -q "$SRC" "$TAG"
    trap 'git -C "$REPO_ROOT" worktree remove -f "$SRC"' EXIT
    rm -rf "$DIST" && mkdir -p "$DIST"
}

build_macos() {
    local bin="$DIST/macos/$BIN_NAME" arch
    mkdir -p "$DIST/macos"
    echo "==> Building universal binary $VERSION from $TAG"
    for arch in arm64 amd64; do
        (cd "$SRC" && CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath \
            -ldflags "-s -w -X ${VERSION_VAR}=${VERSION}" -o "${bin}-${arch}" .)
    done
    lipo -create -output "$bin" "${bin}-arm64" "${bin}-amd64"
    rm "${bin}-arm64" "${bin}-amd64"
    lipo -info "$bin"
    [[ "$("$bin" version)" == "$VERSION" ]] || fail "version mismatch: $("$bin" version) != $VERSION"

    echo "==> Codesigning with hardened runtime"
    codesign --force --timestamp --options runtime --sign "$SIGN_IDENTITY" "$bin"
    codesign --verify --strict --verbose=2 "$bin"

    echo "==> Notarizing (bare CLI binaries cannot be stapled; Gatekeeper checks online)"
    local zip="$DIST/notarize.zip"
    ditto -c -k --keepParent "$bin" "$zip"
    xcrun notarytool submit "$zip" --keychain-profile "$NOTARY_PROFILE" --wait | tee "$DIST/notary.log"
    grep -q 'status: Accepted' "$DIST/notary.log" || fail "notarization not accepted"
    rm -f "$zip"

    local archive="${BIN_NAME}-${TAG}-macos-universal.tar.gz"
    tar -C "$DIST/macos" -czf "$DIST/$archive" "$BIN_NAME"
    ARCHIVES+=("$archive")
}

# build_linux builds static linux binaries into $DIST/linux-<arch>/agentbus
# and packages each as agentbus-<tag>-linux-<arch>.tar.gz.
build_linux() {
    local arch
    for arch in amd64 arm64; do
        echo "==> Building linux/$arch"
        mkdir -p "$DIST/linux-$arch"
        (cd "$SRC" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
            -ldflags "-s -w -X ${VERSION_VAR}=${VERSION}" -o "$DIST/linux-$arch/$BIN_NAME" .)
        local archive="${BIN_NAME}-${TAG}-linux-${arch}.tar.gz"
        tar -C "$DIST/linux-$arch" -czf "$DIST/$archive" "$BIN_NAME"
        ARCHIVES+=("$archive")
    done
}

# smoke_linux runs each linux binary in Alpine (Docker emulates the other
# architecture) and requires `agentbus version` to print the version.
smoke_linux() {
    local arch got
    for arch in amd64 arm64; do
        echo "==> Smoke test linux/$arch"
        got="$(docker run --rm --platform "linux/$arch" -v "$DIST/linux-$arch:/d:ro" alpine /d/$BIN_NAME version)"
        [[ "$got" == "$VERSION" ]] || fail "linux/$arch binary printed '$got', want $VERSION"
    done
}

# copy_scripts puts the installer(s) from the tag's worktree into $DIST so
# they are listed in SHA256SUMS and published with the archives.
copy_scripts() {
    cp "$SRC/install.sh" "$DIST/install.sh"
}

write_sums() {
    echo "==> Writing SHA256SUMS"
    (cd "$DIST" && shasum -a 256 "${ARCHIVES[@]}" install.sh > SHA256SUMS)
    cat "$DIST/SHA256SUMS"
}

sign_sums() {
    echo "==> Signing SHA256SUMS"
    "$OPENSSL" pkeyutl -sign -rawin -inkey "$SIGNING_KEY" -in "$DIST/SHA256SUMS" -out "$DIST/SHA256SUMS.sig"
    verify_sums
}

verify_sums() {
    "$OPENSSL" pkeyutl -verify -rawin -pubin -inkey "$PUBKEY" -in "$DIST/SHA256SUMS" -sigfile "$DIST/SHA256SUMS.sig" >/dev/null \
        || fail "SHA256SUMS.sig does not verify with $PUBKEY"
}

# release_notes_args prints the gh flags for the notes: release/notes-<tag>.md
# when present, else generated notes.
release_notes_args() {
    if [[ -f "$REPO_ROOT/release/notes-${TAG}.md" ]]; then
        printf '%s\n' --notes-file "$REPO_ROOT/release/notes-${TAG}.md"
    else
        printf '%s\n' --generate-notes
    fi
}

# ensure_release creates the GitHub release for $TAG unless it already exists
# (a rerun after a failure reuses it).
ensure_release() {
    if gh release view "$TAG" --repo "$GH_REPO" >/dev/null 2>&1; then
        echo "==> Release $TAG exists; replacing its assets"
    else
        echo "==> Creating GitHub release $TAG"
        local notes=(); while IFS= read -r line; do notes+=("$line"); done < <(release_notes_args)
        gh release create "$TAG" --repo "$GH_REPO" --title "$TAG" "${notes[@]}"
    fi
}

upload_assets() {
    echo "==> Uploading assets"
    (cd "$DIST" && gh release upload "$TAG" --repo "$GH_REPO" --clobber "${ARCHIVES[@]}" install.sh SHA256SUMS SHA256SUMS.sig)
}

# render_template <tmpl> <out> KEY=VALUE... replaces every __KEY__ and fails
# if any __PLACEHOLDER__ is left.
render_template() {
    local tmpl="$1" out="$2"; shift 2
    local args=() kv
    for kv in "$@"; do
        [[ "$kv" == *=* ]] || fail "render_template: expected KEY=VALUE, got '$kv'"
        args+=(-e "s|__${kv%%=*}__|${kv#*=}|g")
    done
    sed "${args[@]}" "$tmpl" > "$out"
    local left; left="$(grep -Eo '__[A-Z0-9_]+__' "$out" | sort -u | tr '\n' ' ' || true)"
    [[ -z "$left" ]] || fail "unfilled placeholders in $out: $left"
}

# sum_of <file> prints the hash SHA256SUMS records for <file>.
sum_of() {
    local h; h="$(awk -v f="$1" '$2 == f { print $1 }' "$DIST/SHA256SUMS")"
    [[ -n "$h" ]] || fail "no SHA256SUMS entry for $1"
    printf '%s\n' "$h"
}

update_tap() {
    echo "==> Updating Homebrew tap"
    [[ -d "$TAP_DIR" ]] || git clone git@github.com:ericfitz/homebrew-tap.git "$TAP_DIR"
    git -C "$TAP_DIR" pull -q --ff-only
    local base="https://github.com/${GH_REPO}/releases/download/${TAG}"
    local m="${BIN_NAME}-${TAG}-macos-universal.tar.gz" la="${BIN_NAME}-${TAG}-linux-amd64.tar.gz" lr="${BIN_NAME}-${TAG}-linux-arm64.tar.gz"
    local rendered="$DIST/${BIN_NAME}.rb"
    render_template "$REPO_ROOT/release/${BIN_NAME}.rb.tmpl" "$rendered" \
        "MACOS_URL=$base/$m" "MACOS_SHA256=$(sum_of "$m")" \
        "LINUX_AMD64_URL=$base/$la" "LINUX_AMD64_SHA256=$(sum_of "$la")" \
        "LINUX_ARM64_URL=$base/$lr" "LINUX_ARM64_SHA256=$(sum_of "$lr")"
    if cmp -s "$rendered" "$TAP_DIR/Formula/${BIN_NAME}.rb"; then
        echo "formula unchanged"; return
    fi
    cp "$rendered" "$TAP_DIR/Formula/${BIN_NAME}.rb"
    git -C "$TAP_DIR" add "Formula/${BIN_NAME}.rb"
    git -C "$TAP_DIR" commit -m "${BIN_NAME} ${VERSION}"
    git -C "$TAP_DIR" push
}

main() {
    parse_args "$@"
    require_tools
    check_signing_key
    check_tag_exists
    checkout_tag
    build_macos
    build_linux
    smoke_linux
    copy_scripts
    write_sums
    sign_sums
    ensure_release
    upload_assets
    update_tap
    echo "==> Released ${TAG}: brew install ericfitz/tap/${BIN_NAME}; curl -fsSL https://github.com/${GH_REPO}/releases/latest/download/install.sh | sh"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi
```

State (spec): rerun for the same tag reuses the release and clobbers every asset from this run's `SHA256SUMS`; two concurrent runs for one tag are unsupported; `dist/` is recreated and the worktree removed on exit.

- [ ] **Step 4: Run the tests and shellcheck**

Run: `/Users/efitz/Projects/agentbus/release/test-release.sh && shellcheck /Users/efitz/Projects/agentbus/release/release.sh /Users/efitz/Projects/agentbus/release/test-release.sh`
Expected: all `args-*`, `tag-*`, `render-*`, `sum-of-*`, `notes-*` cases PASS; `test-release: 26 passed, 0 failed`; shellcheck silent.

- [ ] **Step 5: Commit**

```bash
git add release/release.sh release/test-release.sh
git commit -m "release: restructure release.sh into functions with preflight, Linux builds and signed SHA256SUMS"
```

---

### Task 3: `install.sh`

**Files:**
- Create: `install.sh` (repository root)

**Interfaces:**
- Produces: `install.sh` with the key block markers from Task 1 and the functions `die`, `need_tool`, `detect_platform`, `find_openssl`, `suggest_openssl`, `resolve_version`, `download`, `verify_signature`, `verify_checksum`, `install_binary`, `report`, `main`. Environment contract as in Global Constraints. Exit 1 on every refusal with `agentbus install: <reason>` on stderr.
- Consumed by: Task 6's harness (which swaps the key block), `release.sh` `copy_scripts`, and #35 (`install.ps1` mirrors its structure).

- [ ] **Step 1: Write `install.sh`**

```sh
#!/bin/sh
# install.sh: install agentbus on Linux into ~/.local/bin without root.
#   curl -fsSL https://github.com/ericfitz/agentbus/releases/latest/download/install.sh | sh
# Environment:
#   AGENTBUS_VERSION        vX.Y.Z to install (default: the latest release)
#   AGENTBUS_INSTALL_DIR    absolute directory (default: $HOME/.local/bin)
#   AGENTBUS_BASE_URL       mirror or test server (default: https://github.com/ericfitz/agentbus)
#   AGENTBUS_SKIP_SIGNATURE 1 skips the OpenSSL signature check (the checksum is still checked)
# Downloads the tarball, SHA256SUMS and SHA256SUMS.sig, verifies the Ed25519
# signature with the public key below and the tarball's checksum, then
# replaces <dir>/agentbus atomically. Never runs sudo or agentbus init.
set -eu

# BEGIN agentbus release public key
PUBKEY_PEM='-----BEGIN PUBLIC KEY-----
REPLACED-BY-release/embed-key.sh
-----END PUBLIC KEY-----'
# END agentbus release public key

BASE_URL="${AGENTBUS_BASE_URL:-https://github.com/ericfitz/agentbus}"
ARCH="" TAG="" ASSET="" OPENSSL="" OLD_OPENSSL="" SHA_CMD="" SKIP_SIG=0 INSTALL_DIR="" TMP="" UPGRADE=0

die() { printf 'agentbus install: %s\n' "$*" >&2; exit 1; }

cleanup() {
    [ -n "$TMP" ] && rm -rf "$TMP"
    [ -n "$INSTALL_DIR" ] && rm -f "$INSTALL_DIR/.agentbus.tmp.$$"
    return 0
}
trap cleanup EXIT
trap 'exit 1' INT TERM

need_tool() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

detect_platform() {
    case "$(uname -s)" in
        Linux) ;;
        Darwin) die "this script is for Linux; on macOS run: brew install ericfitz/tap/agentbus" ;;
        *) die "unsupported operating system: $(uname -s)" ;;
    esac
    case "$(uname -m)" in
        x86_64) ARCH=amd64 ;;
        aarch64 | arm64) ARCH=arm64 ;;
        *) die "unsupported architecture: $(uname -m) (releases cover linux-amd64 and linux-arm64)" ;;
    esac
    need_tool curl
    need_tool tar
    if command -v sha256sum >/dev/null 2>&1; then SHA_CMD="sha256sum"
    elif command -v shasum >/dev/null 2>&1; then SHA_CMD="shasum -a 256"
    else die "sha256sum (or shasum) is required but not installed"; fi
    case "${AGENTBUS_SKIP_SIGNATURE:-}" in
        "") SKIP_SIG=0 ;;
        1) SKIP_SIG=1; printf 'agentbus install: AGENTBUS_SKIP_SIGNATURE=1: skipping the signature check; the checksum is still verified\n' >&2 ;;
        *) die "AGENTBUS_SKIP_SIGNATURE must be 1 or unset, got '${AGENTBUS_SKIP_SIGNATURE}'" ;;
    esac
    if [ -n "${AGENTBUS_INSTALL_DIR:-}" ]; then
        INSTALL_DIR="$AGENTBUS_INSTALL_DIR"
        case "$INSTALL_DIR" in /*) ;; *) die "AGENTBUS_INSTALL_DIR must be an absolute path, got '$INSTALL_DIR'" ;; esac
    else
        [ -n "${HOME:-}" ] || die "HOME is not set; set AGENTBUS_INSTALL_DIR to an absolute directory"
        INSTALL_DIR="$HOME/.local/bin"
    fi
}

# openssl_major prints the major version of an OpenSSL command, or nothing
# for anything that is not OpenSSL (LibreSSL, a broken wrapper).
openssl_major() { "$1" version 2>/dev/null | sed -n 's/^OpenSSL \([0-9][0-9]*\)\..*/\1/p'; }

find_openssl() {
    for c in openssl openssl3; do
        command -v "$c" >/dev/null 2>&1 || continue
        v="$(openssl_major "$c")"
        if [ -n "$v" ] && [ "$v" -ge 3 ]; then OPENSSL="$c"; return 0; fi
        [ -n "$OLD_OPENSSL" ] || OLD_OPENSSL="$c ($("$c" version 2>/dev/null || echo unknown version))"
    done
    if [ -n "$OLD_OPENSSL" ]; then
        die "found $OLD_OPENSSL, but verifying the release signature needs OpenSSL 3. Options: on RHEL 8 and its rebuilds install EPEL's openssl3 package (sudo dnf install openssl3); upgrade the distribution; or rerun with AGENTBUS_SKIP_SIGNATURE=1 to verify the checksum only"
    fi
    die "OpenSSL 3 is required to verify the release signature. $(suggest_openssl). Or rerun with AGENTBUS_SKIP_SIGNATURE=1 to verify the checksum only"
}

# suggest_openssl prints a package-manager suggestion from /etc/os-release
# (ID, then each word of ID_LIKE; first match wins).
suggest_openssl() {
    id="" like=""
    if [ -r /etc/os-release ]; then
        id="$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')"
        like="$(sed -n 's/^ID_LIKE=//p' /etc/os-release | tr -d '"')"
    fi
    sudo="sudo "; [ "$(id -u)" = 0 ] && sudo=""
    for d in $id $like; do
        case "$d" in
            debian | ubuntu) echo "Install it with: sudo apt-get install openssl"; return ;;
            fedora | rhel | centos | rocky | almalinux | amzn) echo "Install it with: sudo dnf install openssl"; return ;;
            alpine) echo "Install it with: ${sudo}apk add openssl"; return ;;
            arch) echo "Install it with: sudo pacman -S openssl"; return ;;
            opensuse* | sles) echo "Install it with: sudo zypper install openssl-3"; return ;;
        esac
    done
    echo "Install OpenSSL 3 with the system package manager"
}

resolve_version() {
    if [ -n "${AGENTBUS_VERSION:-}" ]; then
        TAG="$AGENTBUS_VERSION"
    else
        loc="$(curl -fsSI -o /dev/null -w '%{redirect_url}' "$BASE_URL/releases/latest" 2>/dev/null || true)"
        TAG="${loc##*/}"
        [ -n "$TAG" ] || die "could not determine the latest release from $BASE_URL/releases/latest; set AGENTBUS_VERSION=vX.Y.Z"
    fi
    case "$TAG" in
        v[0-9]*.[0-9]*.[0-9]*) ;;
        *) die "version must look like vX.Y.Z, got '$TAG'" ;;
    esac
    ASSET="agentbus-$TAG-linux-$ARCH.tar.gz"
}

fetch() { curl -fsSL -o "$TMP/$2" "$BASE_URL/releases/download/$TAG/$1" || die "download failed: $BASE_URL/releases/download/$TAG/$1"; }

download() {
    TMP="$(mktemp -d)"
    fetch "$ASSET" "$ASSET"
    fetch SHA256SUMS SHA256SUMS
    [ "$SKIP_SIG" = 1 ] || fetch SHA256SUMS.sig SHA256SUMS.sig
}

verify_signature() {
    [ "$SKIP_SIG" = 1 ] && return 0
    printf '%s\n' "$PUBKEY_PEM" > "$TMP/release.pub"
    "$OPENSSL" pkeyutl -verify -rawin -pubin -inkey "$TMP/release.pub" -in "$TMP/SHA256SUMS" -sigfile "$TMP/SHA256SUMS.sig" >/dev/null 2>&1 \
        || die "signature check of SHA256SUMS failed: the download is damaged, tampered with, or signed with a key this script does not know"
}

verify_checksum() {
    line="$(grep "[ *]$ASSET\$" "$TMP/SHA256SUMS" || true)"
    [ -n "$line" ] || die "SHA256SUMS has no entry for $ASSET"
    (cd "$TMP" && printf '%s\n' "$line" | $SHA_CMD -c - >/dev/null 2>&1) || die "checksum mismatch for $ASSET"
}

install_binary() {
    mkdir -p "$INSTALL_DIR" 2>/dev/null || true
    [ -d "$INSTALL_DIR" ] && [ -w "$INSTALL_DIR" ] \
        || die "cannot write to $INSTALL_DIR. Rerun with AGENTBUS_INSTALL_DIR=<a writable directory>, or for a system-wide install: curl -fsSL $BASE_URL/releases/latest/download/install.sh | sudo env AGENTBUS_INSTALL_DIR=/usr/local/bin sh"
    [ -x "$INSTALL_DIR/agentbus" ] && UPGRADE=1
    tar -xzf "$TMP/$ASSET" -C "$TMP" agentbus || die "could not extract $ASSET"
    cp "$TMP/agentbus" "$INSTALL_DIR/.agentbus.tmp.$$"
    chmod 755 "$INSTALL_DIR/.agentbus.tmp.$$"
    mv -f "$INSTALL_DIR/.agentbus.tmp.$$" "$INSTALL_DIR/agentbus"
}

report() {
    v="$("$INSTALL_DIR/agentbus" version)" || die "$INSTALL_DIR/agentbus does not run"
    echo "installed agentbus $v to $INSTALL_DIR/agentbus"
    case ":${PATH:-}:" in
        *":$INSTALL_DIR:"*) ;;
        *) echo "note: $INSTALL_DIR is not on your PATH; add this to your shell profile: export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
    esac
    echo "next: agentbus init --global (once per machine), then agentbus init inside each repository"
    [ "$UPGRADE" = 1 ] && echo "upgraded: restart every harness session and the TUI to pick up the new binary"
    return 0
}

main() {
    detect_platform
    [ "$SKIP_SIG" = 1 ] || find_openssl
    resolve_version
    download
    verify_signature
    verify_checksum
    install_binary
    report
}

main "$@"
```

- [ ] **Step 2: Static checks**

Run: `chmod +x /Users/efitz/Projects/agentbus/install.sh && shellcheck -s sh /Users/efitz/Projects/agentbus/install.sh && sh -n /Users/efitz/Projects/agentbus/install.sh && rg -n 'BEGIN agentbus release public key' /Users/efitz/Projects/agentbus/install.sh`
Expected: shellcheck silent, `sh -n` silent, the marker line printed.

- [ ] **Step 3: Quick local refusal checks (macOS)**

Run: `cd /Users/efitz/Projects/agentbus && sh install.sh; echo "exit=$?"; AGENTBUS_SKIP_SIGNATURE=yes sh install.sh; echo "exit=$?"`
Expected: `agentbus install: this script is for Linux; on macOS run: brew install ericfitz/tap/agentbus`, `exit=1`, twice (the Darwin check comes first).

- [ ] **Step 4: Commit**

```bash
git add install.sh
git commit -m "install.sh: signed, root-less Linux installer"
```

---

### Task 4: Embed the real key, formula template, `test-render.sh`

**Files:**
- Modify: `install.sh` (key block, via `release/embed-key.sh`; only when `release/agentbus-release-ed25519.pub` exists)
- Modify: `release/agentbus.rb.tmpl`
- Create: `release/test-render.sh`

**Interfaces:**
- Consumes: `render_template` from `release.sh` (Task 2).
- Produces: `release/test-render.sh` with `render_check <tmpl> <syntax-check-command> KEY=VALUE...`; #35 adds the Scoop template to it.

- [ ] **Step 1: Write the failing `release/test-render.sh`**

```bash
#!/usr/bin/env bash
# Renders every release template with fake values, asserts no __PLACEHOLDER__
# survives, and syntax-checks the result. No network.
#   release/test-render.sh
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# shellcheck source=release.sh
source "$HERE/release.sh"
FAIL=0

# render_check <template> <out-name> <syntax-check command...> -- KEY=VALUE...
render_check() {
    local tmpl="$1" out="$WORK/$2"; shift 2
    local syntax=(); while [[ "$1" != "--" ]]; do syntax+=("$1"); shift; done; shift
    if render_template "$tmpl" "$out" "$@" && "${syntax[@]}" "$out" >/dev/null 2>&1; then
        echo "PASS $(basename "$tmpl")"
    else
        echo "FAIL $(basename "$tmpl")"; FAIL=1
    fi
}

render_check "$HERE/agentbus.rb.tmpl" agentbus.rb ruby -c -- \
    MACOS_URL=https://example.invalid/m.tar.gz MACOS_SHA256=1111 \
    LINUX_AMD64_URL=https://example.invalid/a.tar.gz LINUX_AMD64_SHA256=2222 \
    LINUX_ARM64_URL=https://example.invalid/r.tar.gz LINUX_ARM64_SHA256=3333
grep -q 'on_linux' "$WORK/agentbus.rb" || { echo "FAIL formula lacks on_linux"; FAIL=1; }
grep -q 'depends_on :macos' "$WORK/agentbus.rb" && { echo "FAIL formula still macOS only"; FAIL=1; }

# A template with an extra placeholder must fail (render_template's guard).
printf 'x __UNFILLED__\n' > "$WORK/bad.tmpl"
if (render_template "$WORK/bad.tmpl" "$WORK/bad.out" A=1) 2>/dev/null; then echo "FAIL unfilled placeholder accepted"; FAIL=1; else echo "PASS unfilled placeholder rejected"; fi

[[ "$FAIL" -eq 0 ]] && echo "test-render: OK"
exit "$FAIL"
```

- [ ] **Step 2: Run to verify it fails**

Run: `chmod +x /Users/efitz/Projects/agentbus/release/test-render.sh && /Users/efitz/Projects/agentbus/release/test-render.sh`
Expected: `FAIL agentbus.rb.tmpl` (the template still has `__URL__`/`__SHA256__`), exit 1.

- [ ] **Step 3: Rewrite `release/agentbus.rb.tmpl`**

```ruby
# Formula for agentbus — installs a prebuilt binary: a signed, notarized
# universal binary on macOS, a static binary on Linux (amd64 and arm64).
# Rendered from release/agentbus.rb.tmpl by release/release.sh; do not edit
# the copy in the tap by hand.
class Agentbus < Formula
  desc "Local message bus and shared memory for coding agents"
  homepage "https://github.com/ericfitz/agentbus"
  license "Apache-2.0"

  on_macos do
    url "__MACOS_URL__"
    sha256 "__MACOS_SHA256__"
  end
  on_linux do
    on_intel do
      url "__LINUX_AMD64_URL__"
      sha256 "__LINUX_AMD64_SHA256__"
    end
    on_arm do
      url "__LINUX_ARM64_URL__"
      sha256 "__LINUX_ARM64_SHA256__"
    end
  end

  def install
    bin.install "agentbus"
  end

  def caveats
    <<~EOS
      Register agentbus with your coding harnesses once per machine:
        agentbus init --global
      Then run `agentbus init` inside each repository.
    EOS
  end

  test do
    assert_equal version.to_s, shell_output("#{bin}/agentbus version").strip
  end
end
```

- [ ] **Step 4: Embed the committed public key (skip if Task 1 Step 1 has not happened yet)**

Run: `cd /Users/efitz/Projects/agentbus && if [[ -r release/agentbus-release-ed25519.pub ]]; then release/embed-key.sh release/agentbus-release-ed25519.pub install.sh; else echo "SKIP: release/agentbus-release-ed25519.pub missing (Task 1 Step 1); rerun this step after the user generates it"; fi`
Expected: `install.sh: key embedded`, or the SKIP line.

- [ ] **Step 5: Run the tests**

Run: `/Users/efitz/Projects/agentbus/release/test-render.sh && shellcheck /Users/efitz/Projects/agentbus/release/test-render.sh && shellcheck -s sh /Users/efitz/Projects/agentbus/install.sh`
Expected: `PASS agentbus.rb.tmpl`, `PASS unfilled placeholder rejected`, `test-render: OK`; shellcheck silent.

- [ ] **Step 6: Commit**

```bash
git add release/agentbus.rb.tmpl release/test-render.sh install.sh
git commit -m "release: Homebrew formula for macOS and Linux, test-render.sh"
```

---

### Task 5: Fixture server and container images for the install harness

**Files:**
- Create: `release/testdata/fixture-server.py`
- Create: `release/testdata/Dockerfile.ubuntu`, `release/testdata/Dockerfile.debian`, `release/testdata/Dockerfile.alpine`
- Test: `release/test-release.sh` (append server cases)

**Interfaces:**
- Produces: `fixture-server.py <root> <port>`: `GET|HEAD /<variant>/releases/latest` → 302 to `/<variant>/releases/tag/<tag in root/<variant>/latest>`; `GET|HEAD /<variant>/releases/download/<tag>/<asset>` → `root/<variant>/<tag>/<asset>`; everything else 404; path parts containing `..` → 400. Listens on `0.0.0.0` so containers reach it via `host.docker.internal`.
- Produces: images tagged `agentbus-itest-<distro>-<arch>` with `curl`, `tar`, `sha256sum` and OpenSSL 3 installed.

- [ ] **Step 1: Append failing server tests to `release/test-release.sh`** (before the summary):

```bash
# --- fixture-server.py ------------------------------------------------------
mkdir -p "$WORK/fx/valid/v9.0.1"
printf 'v9.0.1\n' > "$WORK/fx/valid/latest"
printf 'hello\n' > "$WORK/fx/valid/v9.0.1/asset.txt"
port=$(( 20000 + RANDOM % 20000 ))
python3 -I "$HERE/testdata/fixture-server.py" "$WORK/fx" "$port" & server_pid=$!
trap 'kill "$server_pid" 2>/dev/null; rm -rf "$WORK"' EXIT
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$port/" && break; sleep 0.1; done
check server-latest-redirect 0 "/valid/releases/tag/v9.0.1" -- curl -sI -o /dev/null -w '%{redirect_url}' "http://127.0.0.1:$port/valid/releases/latest"
check server-download 0 "hello" -- curl -fsS "http://127.0.0.1:$port/valid/releases/download/v9.0.1/asset.txt"
check server-head 0 "200" -- curl -sI -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/valid/releases/download/v9.0.1/asset.txt"
check server-404-asset 0 "404" -- curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/valid/releases/download/v9.0.1/nope"
check server-404-variant 0 "404" -- curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/other/releases/latest"
check server-traversal 0 "400" -- curl -s -o /dev/null -w '%{http_code}' --path-as-is "http://127.0.0.1:$port/valid/releases/download/../latest"
kill "$server_pid"; wait "$server_pid" 2>/dev/null || true
check server-stopped 1 "" -- kill -0 "$server_pid"
```

- [ ] **Step 2: Run to verify it fails**

Run: `/Users/efitz/Projects/agentbus/release/test-release.sh`
Expected: python3 cannot open `fixture-server.py`; the `server-*` cases FAIL.

- [ ] **Step 3: Write `release/testdata/fixture-server.py`**

```python
#!/usr/bin/env python3
"""Serve a fake GitHub releases tree for release/test-install.sh.

usage: fixture-server.py <root> <port>

<root>/<variant>/latest          one line: the tag `releases/latest` redirects to
<root>/<variant>/<tag>/<asset>   release assets

GET|HEAD /<variant>/releases/latest                  -> 302 /<variant>/releases/tag/<tag>
GET|HEAD /<variant>/releases/download/<tag>/<asset>  -> the file
anything else -> 404; a path part of ".." -> 400
"""

import http.server
import os
import sys

ROOT = os.path.abspath(sys.argv[1])
PORT = int(sys.argv[2])


class Handler(http.server.BaseHTTPRequestHandler):
    def route(self):
        parts = self.path.split("?", 1)[0].strip("/").split("/")
        if any(p in ("", ".", "..") for p in parts):
            return self.status(400)
        if len(parts) == 3 and parts[1:] == ["releases", "latest"]:
            latest = os.path.join(ROOT, parts[0], "latest")
            if not os.path.isfile(latest):
                return self.status(404)
            with open(latest, encoding="ascii") as f:
                tag = f.read().strip()
            self.send_response(302)
            self.send_header("Location", f"/{parts[0]}/releases/tag/{tag}")
            self.end_headers()
            return None
        if len(parts) == 5 and parts[1:3] == ["releases", "download"]:
            path = os.path.join(ROOT, parts[0], parts[3], parts[4])
            if not os.path.isfile(path):
                return self.status(404)
            with open(path, "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            if self.command == "GET":
                self.wfile.write(data)
            return None
        return self.status(404)

    def status(self, code):
        self.send_response(code)
        self.send_header("Content-Length", "0")
        self.end_headers()

    do_GET = route
    do_HEAD = route

    def log_message(self, *args):  # quiet
        pass


http.server.ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
```

- [ ] **Step 4: Write the three Dockerfiles**

`release/testdata/Dockerfile.ubuntu`:
```dockerfile
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates openssl && rm -rf /var/lib/apt/lists/*
```
`release/testdata/Dockerfile.debian`:
```dockerfile
FROM debian:12
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates openssl && rm -rf /var/lib/apt/lists/*
```
`release/testdata/Dockerfile.alpine`:
```dockerfile
FROM alpine:3.20
RUN apk add --no-cache curl openssl
```

- [ ] **Step 5: Run the tests and build one image by hand to confirm the Dockerfiles**

Run: `/Users/efitz/Projects/agentbus/release/test-release.sh && docker build --platform linux/arm64 -t agentbus-itest-alpine-arm64 -f /Users/efitz/Projects/agentbus/release/testdata/Dockerfile.alpine /Users/efitz/Projects/agentbus/release/testdata && docker run --rm --platform linux/arm64 agentbus-itest-alpine-arm64 sh -c 'openssl version && curl --version | head -1 && sha256sum --version | head -1'`
Expected: all `server-*` cases PASS (`test-release: 33 passed, 0 failed`); the container prints `OpenSSL 3.x`, a curl line and a BusyBox line.

- [ ] **Step 6: Commit**

```bash
git add release/testdata/fixture-server.py release/testdata/Dockerfile.ubuntu release/testdata/Dockerfile.debian release/testdata/Dockerfile.alpine release/test-release.sh
git commit -m "release: fixture server and container images for the install harness"
```

---

### Task 6: `release/test-install.sh` (container matrix with a self-test)

**Files:**
- Create: `release/test-install.sh`

**Interfaces:**
- Consumes: `release/testdata/fixture-server.py`, the Dockerfiles, `install.sh`, `release/embed-key.sh`, Homebrew `openssl@3`.
- Produces: `release/test-install.sh [--quick] [--self-test]`. `--self-test` checks the fixture builder and server without Docker and exits. `--quick` runs the case list on `ubuntu`/`amd64` only; the default matrix is `ubuntu debian alpine` × `amd64 arm64`. Exit 1 on any failed case; prints a PASS/FAIL line per case and a summary. Cleans its temp directory and stops the server on exit.

State of the harness: a second run rebuilds the fixture in a fresh `mktemp -d` and reuses the Docker image cache; two overlapping runs use different ports and temp directories; a killed run leaves at most a Docker image and a temp directory that `mktemp` names uniquely (nothing a later run reads).

- [ ] **Step 1: Write `release/test-install.sh`**

```bash
#!/usr/bin/env bash
# Tests install.sh against a fake release served locally, inside Linux
# containers. Needs Docker (Desktop, for host.docker.internal), python3 and
# Homebrew openssl@3. Never touches ~/.keys: it signs with a throwaway key
# and rewrites install.sh's key block in a test copy only.
#   release/test-install.sh             full matrix (ubuntu, debian, alpine x amd64, arm64)
#   release/test-install.sh --quick     ubuntu/amd64 only
#   release/test-install.sh --self-test fixture builder and server checks, no Docker
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
OPENSSL="$(brew --prefix openssl@3)/bin/openssl"
WORK="$(mktemp -d)"
FX="$WORK/fixture"
PORT=$(( 20000 + RANDOM % 20000 ))
SERVER_PID=""
PASS=0; FAIL=0
DISTROS=(ubuntu debian alpine); ARCHES=(amd64 arm64)
MODE=matrix
for a in "$@"; do
    case "$a" in
        --quick) DISTROS=(ubuntu); ARCHES=(amd64) ;;
        --self-test) MODE=self ;;
        *) echo "usage: test-install.sh [--quick] [--self-test]" >&2; exit 2 ;;
    esac
done
cleanup() { [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null; rm -rf "$WORK"; }
trap cleanup EXIT
[[ -x "$OPENSSL" ]] || { echo "error: Homebrew openssl@3 missing (brew install openssl@3)" >&2; exit 1; }

# --- fixture ------------------------------------------------------------------
# build_fixture lays out $FX/<variant>/{latest,<tag>/...} for the variants
# valid (v9.0.0 and v9.0.1), tampered-tarball, tampered-sums, tampered-sig
# and missing-entry, plus $FX/install.sh with the throwaway key embedded.
build_fixture() {
    "$OPENSSL" genpkey -algorithm ed25519 -out "$WORK/key.pem" 2>/dev/null
    "$OPENSSL" pkey -in "$WORK/key.pem" -pubout -out "$WORK/key.pub" 2>/dev/null
    mkdir -p "$FX/fakes"
    cp "$REPO_ROOT/install.sh" "$FX/install.sh"
    "$HERE/embed-key.sh" "$WORK/key.pub" "$FX/install.sh" >/dev/null
    # Fake commands the cases copy onto the container's PATH.
    printf '#!/bin/sh\nif [ "$1" = -m ]; then echo riscv64; else exec /bin/uname "$@"; fi\n' > "$FX/fakes/uname"
    printf '#!/bin/sh\necho "OpenSSL 1.1.1w  11 Sep 2023"\n' > "$FX/fakes/openssl"
    printf '#!/bin/sh\nsleep 5\nexec /bin/tar "$@"\n' > "$FX/fakes/tar"
    chmod 755 "$FX"/fakes/*
    local tag
    for tag in v9.0.0 v9.0.1; do
        make_release valid "$tag" "${tag#v}"
    done
    printf 'v9.0.1\n' > "$FX/valid/latest"
    local v
    for v in tampered-tarball tampered-sums tampered-sig missing-entry; do
        mkdir -p "$FX/$v"; cp -R "$FX/valid/v9.0.1" "$FX/$v/v9.0.1"; printf 'v9.0.1\n' > "$FX/$v/latest"
    done
    append_byte "$FX/tampered-tarball/v9.0.1/agentbus-v9.0.1-linux-amd64.tar.gz"
    append_byte "$FX/tampered-tarball/v9.0.1/agentbus-v9.0.1-linux-arm64.tar.gz"
    # First hex digit of the first hash changed (f -> E, anything else -> f), so it always differs.
    sed -i '' '1s/^f/E/; 1s/^[0-9a-e]/f/' "$FX/tampered-sums/v9.0.1/SHA256SUMS"
    append_byte "$FX/tampered-sig/v9.0.1/SHA256SUMS.sig"
    # missing-entry: SHA256SUMS lists only install.sh, re-signed so the signature is valid.
    grep ' install.sh$' "$FX/missing-entry/v9.0.1/SHA256SUMS" > "$FX/missing-entry/v9.0.1/SHA256SUMS.new"
    mv "$FX/missing-entry/v9.0.1/SHA256SUMS.new" "$FX/missing-entry/v9.0.1/SHA256SUMS"
    "$OPENSSL" pkeyutl -sign -rawin -inkey "$WORK/key.pem" -in "$FX/missing-entry/v9.0.1/SHA256SUMS" -out "$FX/missing-entry/v9.0.1/SHA256SUMS.sig"
}

# make_release <variant> <tag> <version>: stub agentbus (a sh script printing
# the version), both tarballs, install.sh, SHA256SUMS and its signature.
make_release() {
    local variant="$1" tag="$2" version="$3" d="$FX/$1/$2" arch
    mkdir -p "$d/bin"
    printf '#!/bin/sh\necho %s\n' "$version" > "$d/bin/agentbus"; chmod 755 "$d/bin/agentbus"
    for arch in amd64 arm64; do
        tar -C "$d/bin" -czf "$d/agentbus-$tag-linux-$arch.tar.gz" agentbus
    done
    rm -r "$d/bin"
    cp "$FX/install.sh" "$d/install.sh"
    (cd "$d" && shasum -a 256 "agentbus-$tag-linux-amd64.tar.gz" "agentbus-$tag-linux-arm64.tar.gz" install.sh > SHA256SUMS)
    "$OPENSSL" pkeyutl -sign -rawin -inkey "$WORK/key.pem" -in "$d/SHA256SUMS" -out "$d/SHA256SUMS.sig"
}

# append_byte makes a file differ from its recorded checksum (or makes a
# signature the wrong length) without depending on the file's content.
append_byte() { printf '\0' >> "$1"; }

start_server() {
    python3 -I "$HERE/testdata/fixture-server.py" "$FX" "$PORT" & SERVER_PID=$!
    local i
    for i in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$PORT/" && return 0; sleep 0.1; done
    echo "error: fixture server did not start" >&2; exit 1
}

# --- self-test ----------------------------------------------------------------
self_test() {
    local ok=1
    build_fixture
    assert() { if eval "$2"; then echo "PASS self: $1"; else echo "FAIL self: $1"; ok=0; fi; }
    assert "sums list three files" "[[ \$(wc -l < '$FX/valid/v9.0.1/SHA256SUMS') -eq 3 ]]"
    assert "sums name both tarballs" "grep -q ' agentbus-v9.0.1-linux-amd64.tar.gz$' '$FX/valid/v9.0.1/SHA256SUMS' && grep -q ' agentbus-v9.0.1-linux-arm64.tar.gz$' '$FX/valid/v9.0.1/SHA256SUMS'"
    assert "valid signature verifies" "'$OPENSSL' pkeyutl -verify -rawin -pubin -inkey '$WORK/key.pub' -in '$FX/valid/v9.0.1/SHA256SUMS' -sigfile '$FX/valid/v9.0.1/SHA256SUMS.sig' >/dev/null 2>&1"
    assert "tampered-sig does not verify" "! '$OPENSSL' pkeyutl -verify -rawin -pubin -inkey '$WORK/key.pub' -in '$FX/tampered-sig/v9.0.1/SHA256SUMS' -sigfile '$FX/tampered-sig/v9.0.1/SHA256SUMS.sig' >/dev/null 2>&1"
    assert "tampered-sums does not verify" "! '$OPENSSL' pkeyutl -verify -rawin -pubin -inkey '$WORK/key.pub' -in '$FX/tampered-sums/v9.0.1/SHA256SUMS' -sigfile '$FX/tampered-sums/v9.0.1/SHA256SUMS.sig' >/dev/null 2>&1"
    assert "tampered-tarball differs only in the tarballs" "cmp -s '$FX/valid/v9.0.1/SHA256SUMS' '$FX/tampered-tarball/v9.0.1/SHA256SUMS' && ! cmp -s '$FX/valid/v9.0.1/agentbus-v9.0.1-linux-amd64.tar.gz' '$FX/tampered-tarball/v9.0.1/agentbus-v9.0.1-linux-amd64.tar.gz'"
    assert "missing-entry verifies but lacks tarballs" "'$OPENSSL' pkeyutl -verify -rawin -pubin -inkey '$WORK/key.pub' -in '$FX/missing-entry/v9.0.1/SHA256SUMS' -sigfile '$FX/missing-entry/v9.0.1/SHA256SUMS.sig' >/dev/null 2>&1 && ! grep -q linux '$FX/missing-entry/v9.0.1/SHA256SUMS'"
    assert "stub prints its version" "[[ \$(tar -xzOf '$FX/valid/v9.0.0/agentbus-v9.0.0-linux-amd64.tar.gz' agentbus | sh) == 9.0.0 ]]"
    assert "install.sh copy carries the throwaway key" "grep -q \"\$(sed -n 2p '$WORK/key.pub')\" '$FX/install.sh'"
    assert "install.sh copy differs from the repo only in the key block" "diff <(sed '/BEGIN agentbus release public key/,/END agentbus release public key/d' '$REPO_ROOT/install.sh') <(sed '/BEGIN agentbus release public key/,/END agentbus release public key/d' '$FX/install.sh') >/dev/null"
    start_server
    assert "server redirects latest" "[[ \$(curl -sI -o /dev/null -w '%{redirect_url}' http://127.0.0.1:$PORT/valid/releases/latest) == */valid/releases/tag/v9.0.1 ]]"
    assert "server serves SHA256SUMS" "curl -fsS http://127.0.0.1:$PORT/valid/releases/download/v9.0.1/SHA256SUMS | cmp -s - '$FX/valid/v9.0.1/SHA256SUMS'"
    kill "$SERVER_PID"; wait "$SERVER_PID" 2>/dev/null || true
    assert "server stopped" "! kill -0 $SERVER_PID 2>/dev/null"
    SERVER_PID=""
    local w="$WORK"; cleanup; trap - EXIT
    [[ -d "$w" ]] && { echo "FAIL self: cleanup left $w"; ok=0; } || echo "PASS self: cleanup removed the work directory"
    [[ "$ok" -eq 1 ]] && echo "test-install self-test: OK"
    [[ "$ok" -eq 1 ]]
}

# --- container cases ----------------------------------------------------------
BASE="http://host.docker.internal:$PORT"

# run_case <name> <distro> <arch> <expected-exit> <required-substring> <shell script>
# The script runs as root in the container with /fixture mounted read-only and
# AGENTBUS_BASE_URL pointing at the valid variant (cases override it).
run_case() {
    local name="$1" distro="$2" arch="$3" want_exit="$4" want_out="$5" script="$6"
    local out exit=0
    out="$(docker run --rm --platform "linux/$arch" -v "$FX:/fixture:ro" \
        -e "AGENTBUS_BASE_URL=$BASE/valid" -e "BASE=$BASE" -e HOME=/root \
        "agentbus-itest-$distro-$arch" sh -c "$script" 2>&1)" || exit=$?
    if [[ "$exit" == "$want_exit" && "$out" == *"$want_out"* ]]; then
        PASS=$((PASS + 1)); echo "PASS $distro/$arch $name"
    else
        FAIL=$((FAIL + 1)); echo "FAIL $distro/$arch $name: exit=$exit want=$want_exit; output:"; echo "$out" | sed 's/^/    /'
    fi
}

# fake <name>: shell text that puts /fixture/fakes/<name> first on PATH.
fake() { printf 'cp /fixture/fakes/%s /usr/local/bin/%s && chmod 755 /usr/local/bin/%s && ' "$1" "$1" "$1"; }

cases() {
    local d="$1" a="$2" rm_openssl
    rm_openssl='rm -f "$(command -v openssl)"'
    run_case valid-piped "$d" "$a" 0 "installed agentbus 9.0.1" 'cat /fixture/install.sh | sh && ~/.local/bin/agentbus version'
    run_case valid-prints-next-step "$d" "$a" 0 "agentbus init --global" 'sh /fixture/install.sh'
    run_case path-warning "$d" "$a" 0 "not on your PATH" 'sh /fixture/install.sh'
    run_case pinned-version "$d" "$a" 0 "installed agentbus 9.0.0" 'AGENTBUS_VERSION=v9.0.0 sh /fixture/install.sh'
    run_case version-no-v "$d" "$a" 1 "version must look like vX.Y.Z" 'AGENTBUS_VERSION=9.0.0 sh /fixture/install.sh'
    run_case latest-unparsable "$d" "$a" 1 "could not determine the latest release" 'AGENTBUS_BASE_URL=$BASE/nope sh /fixture/install.sh'
    run_case tampered-tarball "$d" "$a" 1 "checksum mismatch" 'AGENTBUS_BASE_URL=$BASE/tampered-tarball sh /fixture/install.sh'
    run_case tampered-sums "$d" "$a" 1 "signature check of SHA256SUMS failed" 'AGENTBUS_BASE_URL=$BASE/tampered-sums sh /fixture/install.sh'
    run_case tampered-sig "$d" "$a" 1 "signature check of SHA256SUMS failed" 'AGENTBUS_BASE_URL=$BASE/tampered-sig sh /fixture/install.sh'
    run_case sums-missing-entry "$d" "$a" 1 "SHA256SUMS has no entry for" 'AGENTBUS_BASE_URL=$BASE/missing-entry sh /fixture/install.sh'
    case "$d" in
        ubuntu | debian) run_case no-openssl-suggestion "$d" "$a" 1 "sudo apt-get install openssl" "$rm_openssl; sh /fixture/install.sh" ;;
        alpine) run_case no-openssl-suggestion "$d" "$a" 1 "apk add openssl" "$rm_openssl; sh /fixture/install.sh" ;;
    esac
    run_case openssl-too-old "$d" "$a" 1 "found openssl (OpenSSL 1.1.1w" "$(fake openssl)sh /fixture/install.sh"
    run_case openssl3-name "$d" "$a" 0 "installed agentbus 9.0.1" 'mv "$(command -v openssl)" /usr/local/bin/openssl3 && sh /fixture/install.sh'
    run_case skip-signature-valid "$d" "$a" 0 "skipping the signature check" "$rm_openssl; AGENTBUS_SKIP_SIGNATURE=1 sh /fixture/install.sh"
    run_case skip-signature-tampered "$d" "$a" 1 "checksum mismatch" 'AGENTBUS_SKIP_SIGNATURE=1 AGENTBUS_BASE_URL=$BASE/tampered-tarball sh /fixture/install.sh'
    run_case skip-signature-bad-value "$d" "$a" 1 "AGENTBUS_SKIP_SIGNATURE must be 1 or unset" 'AGENTBUS_SKIP_SIGNATURE=yes sh /fixture/install.sh'
    run_case unknown-arch "$d" "$a" 1 "unsupported architecture: riscv64" "$(fake uname)sh /fixture/install.sh"
    run_case unwritable-dir "$d" "$a" 1 "Rerun with AGENTBUS_INSTALL_DIR" 'AGENTBUS_INSTALL_DIR=/proc/agentbus sh /fixture/install.sh'
    run_case install-dir-relative "$d" "$a" 1 "must be an absolute path" 'AGENTBUS_INSTALL_DIR=bin sh /fixture/install.sh'
    run_case home-unset "$d" "$a" 1 "HOME is not set" 'env -u HOME sh /fixture/install.sh'
    run_case upgrade "$d" "$a" 0 "upgraded: restart" 'AGENTBUS_VERSION=v9.0.0 sh /fixture/install.sh >/dev/null && sh /fixture/install.sh && [ "$(~/.local/bin/agentbus version)" = 9.0.1 ]'
    run_case concurrent "$d" "$a" 0 "9.0.1" 'sh /fixture/install.sh >/dev/null & p1=$!; sh /fixture/install.sh >/dev/null & p2=$!; wait $p1 && wait $p2 && ~/.local/bin/agentbus version'
    run_case leftover-tmp "$d" "$a" 0 "installed agentbus 9.0.1" 'mkdir -p ~/.local/bin && echo junk > ~/.local/bin/.agentbus.tmp.12345 && sh /fixture/install.sh && [ -f ~/.local/bin/.agentbus.tmp.12345 ]'
    # The fake tar is installed in its own statement so that `&` backgrounds
    # install.sh itself and $! is its pid (not a wrapper list's).
    run_case killed-mid-install "$d" "$a" 0 "clean" "mkdir -p /tmp/t && $(fake tar)true; TMPDIR=/tmp/t sh /fixture/install.sh & p=\$!; sleep 2; kill -TERM \$p; wait \$p; [ -z \"\$(ls -A /tmp/t)\" ] && [ -z \"\$(ls -A ~/.local/bin 2>/dev/null)\" ] && echo clean"
}

key_matches_pub() {
    local pub="$REPO_ROOT/release/agentbus-release-ed25519.pub"
    if [[ ! -r "$pub" ]]; then
        FAIL=$((FAIL + 1)); echo "FAIL key-matches-pub: $pub missing; the user generates it (plan Task 1) and then runs: release/embed-key.sh release/agentbus-release-ed25519.pub install.sh"; return
    fi
    # Extract the PEM textually: sourcing install.sh would run main, whose
    # `exit` on macOS (Darwin check) would end the sourcing shell too.
    if diff <(sed -n "/^PUBKEY_PEM='/,/^-----END PUBLIC KEY-----'$/p" "$REPO_ROOT/install.sh" | sed "s/^PUBKEY_PEM='//; s/'\$//") "$pub" >/dev/null; then
        PASS=$((PASS + 1)); echo "PASS key-matches-pub"
    else
        FAIL=$((FAIL + 1)); echo "FAIL key-matches-pub: install.sh's embedded key differs from $pub; run release/embed-key.sh"
    fi
}

main() {
    if [[ "$MODE" == self ]]; then self_test; exit; fi
    build_fixture
    start_server
    local d a
    for d in "${DISTROS[@]}"; do for a in "${ARCHES[@]}"; do
        docker build -q --platform "linux/$a" -t "agentbus-itest-$d-$a" -f "$HERE/testdata/Dockerfile.$d" "$HERE/testdata" >/dev/null
        cases "$d" "$a"
    done; done
    key_matches_pub
    echo "test-install: $PASS passed, $FAIL failed"
    [[ "$FAIL" -eq 0 ]]
}
main
```

Notes: `key_matches_pub` extracts the PEM with `sed` rather than sourcing `install.sh` (sourcing would run `main`, whose `exit` ends the sourcing shell). `env -u HOME` works in BusyBox `env` (alpine) and GNU `env`. The `killed-mid-install` case depends on POSIX trap semantics: a `TERM` received while `sh` waits for a foreground command (the slow fake `tar`) runs the trap after that command returns, so `cleanup` executes and both the temp directory and `.agentbus.tmp.<pid>` are gone by the time `wait` returns.

- [ ] **Step 2: Self-test, then shellcheck**

Run: `chmod +x /Users/efitz/Projects/agentbus/release/test-install.sh && /Users/efitz/Projects/agentbus/release/test-install.sh --self-test && shellcheck /Users/efitz/Projects/agentbus/release/test-install.sh`
Expected: every `PASS self: ...` line, `test-install self-test: OK`, shellcheck silent.

- [ ] **Step 3: Quick matrix, then fix `install.sh` for any FAIL**

Run: `/Users/efitz/Projects/agentbus/release/test-install.sh --quick`
Expected: 24 `PASS ubuntu/amd64 ...` lines, then `key-matches-pub` (PASS, or FAIL naming the missing `.pub` if Task 1 Step 1 has not happened), then `test-install: N passed, M failed`. Any other FAIL is a bug in `install.sh` or the harness: fix it, rerun. The `killed-mid-install` case relies on `trap 'exit 1' INT TERM` making the EXIT trap run after the slow `tar` returns; if it fails on a distro, print the container output and adjust the sleep, not the assertion.

- [ ] **Step 4: Full matrix**

Run: `/Users/efitz/Projects/agentbus/release/test-install.sh`
Expected: 6 × 24 container cases PASS (arm64 containers run under emulation on an Intel host, natively on Apple silicon) plus `key-matches-pub`.

- [ ] **Step 5: Commit**

```bash
git add release/test-install.sh
git commit -m "release: test-install.sh runs install.sh against a fake release in containers"
```

---

### Task 7: `check-linux-brew.sh` and `make release-check`

**Files:**
- Create: `release/check-linux-brew.sh`
- Modify: `Makefile`

**Interfaces:**
- Produces: `make release-check` (shellcheck on `release/*.sh` and `install.sh`, then `test-release.sh`, `test-render.sh`, `test-install.sh`). #35 and #36 extend this target.
- Produces: `release/check-linux-brew.sh [amd64|arm64]...` (default both), run by the user after a release.

- [ ] **Step 1: Write `release/check-linux-brew.sh`**

```bash
#!/usr/bin/env bash
# After a release: install the published formula with Homebrew on Linux in
# Docker and run its test block, for each architecture given (default both).
#   release/check-linux-brew.sh            # amd64 and arm64
#   release/check-linux-brew.sh arm64
# Needs Docker and network access to GitHub. Read-only: nothing is published.
set -euo pipefail
ARCHES=("$@"); (( ${#ARCHES[@]} )) || ARCHES=(amd64 arm64)
status=0
for arch in "${ARCHES[@]}"; do
    case "$arch" in amd64 | arm64) ;; *) echo "error: unknown architecture '$arch' (amd64 or arm64)" >&2; exit 2 ;; esac
    echo "==> Homebrew on linux/$arch"
    if docker run --rm --platform "linux/$arch" homebrew/brew:latest bash -c \
        'brew install ericfitz/tap/agentbus && brew test agentbus && agentbus version'; then
        echo "linux/$arch: OK"
    else
        echo "linux/$arch: FAILED"; status=1
    fi
done
exit "$status"
```

- [ ] **Step 2: Add the Makefile target**

Replace the `.PHONY` line and append after `verify`:

```make
.PHONY: build build-all fmt-check vet lint test test-race verify release-check
```

```make
release-check: ## Release tooling checks: shellcheck, template rendering, install.sh in containers. Needs Docker, python3, openssl@3; not part of verify
	shellcheck release/*.sh
	shellcheck -s sh install.sh
	release/test-release.sh
	release/test-render.sh
	release/test-install.sh
```

- [ ] **Step 3: Run it**

Run: `chmod +x /Users/efitz/Projects/agentbus/release/check-linux-brew.sh && cd /Users/efitz/Projects/agentbus && make release-check && release/check-linux-brew.sh bogus; echo "exit=$?"`
Expected: shellcheck silent, `test-release: 33 passed, 0 failed`, `test-render: OK`, `test-install: ... 0 failed` (or only `key-matches-pub` failing before Task 1 Step 1), then `error: unknown architecture 'bogus'` and `exit=2`. Do not run `check-linux-brew.sh` for real yet: the published formula is still macOS-only until the first Linux release.

- [ ] **Step 4: Commit**

```bash
git add Makefile release/check-linux-brew.sh
git commit -m "release: make release-check and check-linux-brew.sh"
```

---

### Task 8: Documentation and release notes

**Files:**
- Modify: `docs/install.md` (install section, lines 1-26; Operating bullet at line 394-395)
- Modify: `README.md:78-86`
- Create: `release/notes-v1.14.0.md`

- [ ] **Step 1: Replace the top of `docs/install.md`** (from `# Installing Agentbus` through the end of "Or, Build from source") with:

```markdown
# Installing Agentbus

## macOS: Homebrew

```sh
brew install ericfitz/tap/agentbus
```

The formula installs a signed, notarized universal binary from the matching
[GitHub release](https://github.com/ericfitz/agentbus/releases).

## Linux: install script

```sh
curl -fsSL https://github.com/ericfitz/agentbus/releases/latest/download/install.sh | sh
```

Installs `agentbus` into `~/.local/bin` without root, for x86_64 and
aarch64. The script downloads the release tarball with `SHA256SUMS` and
`SHA256SUMS.sig`, checks the Ed25519 signature with the public key built into
the script (OpenSSL 3 is required: `openssl` or `openssl3` on `PATH`), checks
the tarball's checksum, then replaces the binary atomically. Rerun it to
upgrade; restart harness sessions and the TUI afterwards.

| Variable | Meaning |
|---|---|
| `AGENTBUS_VERSION` | Install this tag (`vX.Y.Z`) instead of the latest release. |
| `AGENTBUS_INSTALL_DIR` | Absolute target directory (default `$HOME/.local/bin`). For a system-wide install: `curl -fsSL <url> \| sudo env AGENTBUS_INSTALL_DIR=/usr/local/bin sh`. The script never runs `sudo` itself. |
| `AGENTBUS_BASE_URL` | A mirror of `https://github.com/ericfitz/agentbus`. |
| `AGENTBUS_SKIP_SIGNATURE` | `1` skips only the signature check (for hosts with OpenSSL 1.1); the checksum is still verified. Any other value is rejected. |

Verifying a download by hand, with the public key from
[`release/agentbus-release-ed25519.pub`](../release/agentbus-release-ed25519.pub):

```sh
openssl pkeyutl -verify -rawin -pubin -inkey agentbus-release-ed25519.pub -in SHA256SUMS -sigfile SHA256SUMS.sig
grep agentbus-vX.Y.Z-linux-amd64.tar.gz SHA256SUMS | sha256sum -c
```

Homebrew on Linux works too: `brew install ericfitz/tap/agentbus` installs
the static amd64 or arm64 binary.

## Or, build from source

```sh
CGO_ENABLED=0 go install -trimpath github.com/ericfitz/agentbus@latest
```

This installs the latest tagged source into `$(go env GOBIN)` (default
`~/go/bin`); `agentbus version` prints an empty version for such a build.
From a clone, `CGO_ENABLED=0 go build -o agentbus .` at the repository root
does the same. Put the binary on your `PATH` so harnesses can find it by name.

Maintainers cut a release with `release/release.sh <tag>`; every release
carries `SHA256SUMS` over all archives and `install.sh`, signed with the
Ed25519 release key.
```

Check `agentbus version` for the `go install` claim before writing it: `cd /tmp && CGO_ENABLED=0 go install -trimpath github.com/ericfitz/agentbus@latest && ~/go/bin/agentbus version` — it prints the `Version` default from `internal/mcpserver/server.go` (currently `1.13.0`), not an empty string. If so, write "prints the version compiled into the source, which may lag the tag" instead of "an empty version".

- [ ] **Step 2: In `docs/install.md`'s Operating section**, replace `The default data directory is ~/.local/share/agentbus.` with `The default data directory is ~/.local/share/agentbus on macOS and Linux.`

- [ ] **Step 3: Replace README lines 78-86** with:

```markdown
## Install

macOS, with Homebrew:

```sh
brew install ericfitz/tap/agentbus
```

Linux (x86_64 or aarch64), without root:

```sh
curl -fsSL https://github.com/ericfitz/agentbus/releases/latest/download/install.sh | sh
```

Both verify what they download; see the [install guide](docs/install.md) for
Homebrew on Linux, the installer's variables, and building from source.
```

- [ ] **Step 4: Write `release/notes-v1.14.0.md`** (rename to the actual next tag at release time if it differs):

```markdown
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
embedded in `install.sh` (#34).
```

- [ ] **Step 5: Check every path and command named in the docs exists**

Run: `cd /Users/efitz/Projects/agentbus && rg -n 'install\.sh|SHA256SUMS|release-ed25519' docs/install.md README.md release/notes-v1.14.0.md && ls install.sh release/embed-key.sh && rg -n 'Linux' README.md | head -3`
Expected: the new lines listed; `ls` succeeds.

- [ ] **Step 6: Commit**

```bash
git add docs/install.md README.md release/notes-v1.14.0.md
git commit -m "docs: Linux install, SHA256SUMS verification, release notes"
```

---

### Task 9: Done gate and first-release instructions

**Files:** none new.

- [ ] **Step 1: Run the done gate and the release checks**

Run: `cd /Users/efitz/Projects/agentbus && make verify && make release-check`
Expected: `verify: OK`; `test-release: 33 passed, 0 failed`; `test-render: OK`; `test-install: 145 passed, 0 failed` (6 × 24 + 1). If `key-matches-pub` is the only failure, Task 1 Step 1 is still pending; report that to the user with the exact commands.

- [ ] **Step 2: Self-test commands to include in the completion report**

```
release/test-install.sh --self-test
release/test-release.sh
release/test-render.sh
```

- [ ] **Step 3: First-release instructions (for the user; no implementer action).** Report these verbatim:

1. Generate the key pair if not done (Task 1 Step 1) and commit the `.pub`.
2. Embed it: `release/embed-key.sh release/agentbus-release-ed25519.pub install.sh && make release-check`, commit `install.sh`.
3. Bump `Version` in `internal/mcpserver/server.go`, rename `release/notes-v1.14.0.md` if the tag differs, commit, tag.
4. `Approve: run release/release.sh v1.14.0 (creates the GitHub release v1.14.0 with Linux assets and pushes the formula to ericfitz/homebrew-tap)`
5. Afterwards: `release/check-linux-brew.sh`, and on one amd64 and one arm64 Linux host: `curl -fsSL https://github.com/ericfitz/agentbus/releases/latest/download/install.sh | sh`.

---

## Self-review notes

- Spec coverage: release assets (Task 2, 8); release.sh preflight/builds/smoke/package/sign/publish/formula and its state (Task 2); key setup (Task 1); install.sh steps 1-8, OpenSSL search and suggestions, `AGENTBUS_SKIP_SIGNATURE`, state (Task 3, tested in Task 6); formula + `check-linux-brew.sh` (Tasks 4, 7); docs and notes (Task 8); `test-install.sh` cases including key equality (Task 6), `test-render.sh` (Task 4), `make release-check` (Task 7); first real release checks (Task 9).
- Layout deviation: Linux binaries are built as `dist/linux-<arch>/agentbus` and smoke-tested as `/d/agentbus` (the spec writes `/d/agentbus-linux-<arch>`); the tarball content (`agentbus`) is what the spec requires.
- Type consistency: `render_template`, `sum_of`, `ARCHIVES`, `DIST`, the key markers and `PUBKEY_PEM` are the names #35 and #36 use.
