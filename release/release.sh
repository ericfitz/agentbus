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
    [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
        || { echo "error: tag must look like vX.Y.Z, got '$TAG'" >&2; exit 2; }
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

check_tag_pushed() {
    git -C "$REPO_ROOT" ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1 \
        || fail "tag $TAG is not on origin; push it first (git push origin $TAG)"
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
    local arch archive
    for arch in amd64 arm64; do
        echo "==> Building linux/$arch"
        mkdir -p "$DIST/linux-$arch"
        (cd "$SRC" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
            -ldflags "-s -w -X ${VERSION_VAR}=${VERSION}" -o "$DIST/linux-$arch/$BIN_NAME" .)
        archive="${BIN_NAME}-${TAG}-linux-${arch}.tar.gz"
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
        got="$(docker run --rm --platform "linux/$arch" -v "$DIST/linux-$arch:/d:ro" alpine "/d/$BIN_NAME" version)"
        [[ "$got" == "$VERSION" ]] || fail "linux/$arch binary printed '$got', want $VERSION"
    done
}

# copy_scripts puts the installer(s) from the tag's worktree into $DIST so
# they are listed in SHA256SUMS and published with the archives.
copy_scripts() {
    check_script_embedded "$SRC/install.sh" install.sh
    cp "$SRC/install.sh" "$DIST/install.sh"
}

# check_script_embedded <path> <name> fails when the installer still holds the
# placeholder key block that embed-key.sh replaces.
check_script_embedded() {
    if grep -qxF 'REPLACED-BY-release/embed-key.sh' "$1"; then
        fail "$2 has no embedded release key (placeholder still present); run release/embed-key.sh release/agentbus-release-ed25519.pub $2 and commit before tagging"
    fi
}

# check_installers_embedded is the cheap preflight form: it reads install.sh
# from the tag, before anything is built.
check_installers_embedded() {
    local tmp; tmp="$(mktemp)"
    git -C "$REPO_ROOT" show "$TAG:install.sh" > "$tmp" 2>/dev/null || { rm -f "$tmp"; fail "$TAG has no install.sh; the installer ships with every release, so tag a commit that includes it"; }
    local rc=0
    (check_script_embedded "$tmp" install.sh) || rc=$?
    # install.ps1 is checked whenever the tag has it (the Windows installer
    # joins the release assets in a later step).
    if [[ "$rc" -eq 0 ]] && git -C "$REPO_ROOT" show "$TAG:install.ps1" > "$tmp" 2>/dev/null; then
        (check_script_embedded "$tmp" install.ps1) || rc=$?
    fi
    rm -f "$tmp"
    return "$rc"
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
        local notes=() line
        while IFS= read -r line; do notes+=("$line"); done < <(release_notes_args)
        gh release create "$TAG" --repo "$GH_REPO" --verify-tag --title "$TAG" "${notes[@]}"
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
    local args=() kv left
    for kv in "$@"; do
        [[ "$kv" == *=* ]] || fail "render_template: expected KEY=VALUE, got '$kv'"
        args+=(-e "s|__${kv%%=*}__|${kv#*=}|g")
    done
    sed "${args[@]}" "$tmpl" > "$out"
    left="$(grep -Eo '__[A-Z0-9_]+__' "$out" | sort -u | tr '\n' ' ' || true)"
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
    check_tag_pushed
    check_installers_embedded
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
