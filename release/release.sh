#!/usr/bin/env bash
# Build, sign and publish a release of agentbus, then update Homebrew, Scoop
# and winget. macOS is built, codesigned and notarized here; the Linux and
# Windows archives are built, run and attested by .github/workflows/
# release-build.yml on native runners, dispatched on the tag. Every archive
# is listed in SHA256SUMS, signed with the Ed25519 release key.
#   ./release/release.sh v0.1.1              # full release
#   ./release/release.sh v0.1.1 --no-publish # stop with a draft release (dry run)
# Builds the macOS binary from the tag, so HEAD may be anywhere. Reruns for the
# same tag reuse the draft release and replace every asset.
# One-time setup: a notarytool keychain profile named $NOTARY_PROFILE
# (`xcrun notarytool store-credentials`), and the release key pair (see
# docs/superpowers/specs/2026-10-06-linux-releases-design.md, "Key setup").
set -euo pipefail

BIN_NAME="agentbus"
GH_REPO="ericfitz/agentbus"
TAP_DIR="${TAP_DIR:-$HOME/Projects/homebrew-tap}" # SSH clone of ericfitz/homebrew-tap
SCOOP_DIR="${SCOOP_DIR:-$HOME/Projects/scoop-bucket}" # SSH clone of ericfitz/scoop-bucket
KOMAC_KEY_FILE="$HOME/.keys/KOMAC_GITHUB_KEY"           # `export KOMAC_GITHUB_KEY='<PAT with public_repo>'`
WINGET_ID="ericfitz.agentbus"
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
MACOS_ARCHIVE=""   # file name of the macOS tarball under $DIST
NO_PUBLISH=0       # --no-publish: stop after signing, leave the draft
PUBLISHED=0        # set when the release for $TAG is already public (rerun)
RUN_ID=""          # the release-build.yml run this release used
WORKFLOW="release-build.yml"
FIND_RUN_TRIES="${FIND_RUN_TRIES:-30}"
FIND_RUN_SLEEP="${FIND_RUN_SLEEP:-2}"

usage() { echo "usage: release.sh <tag> [--no-publish]" >&2; exit 2; }
fail() { echo "error: $*" >&2; exit 1; }

parse_args() {
    local a
    for a in "$@"; do
        case "$a" in
            --no-publish) [[ "$NO_PUBLISH" == 0 ]] || usage; NO_PUBLISH=1 ;;
            --*) echo "error: unknown option $a" >&2; usage ;;
            *) [[ -z "$TAG" ]] || usage; TAG="$a" ;;
        esac
    done
    [[ -n "$TAG" ]] || usage
    [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
        || { echo "error: tag must look like vX.Y.Z, got '$TAG'" >&2; exit 2; }
    VERSION="${TAG#v}"
}

# require_tools fails once, naming everything that is missing.
require_tools() {
    local missing=() t prefix
    for t in gh git go lipo codesign xcrun shasum komac; do
        command -v "$t" >/dev/null 2>&1 || missing+=("$t")
    done
    prefix="$(brew --prefix openssl@3 2>/dev/null || true)"
    if [[ -x "$prefix/bin/openssl" ]]; then OPENSSL="$prefix/bin/openssl"; else missing+=("openssl@3 (brew install openssl@3)"); fi
    [[ -r "$SIGNING_KEY" ]] || missing+=("signing key $SIGNING_KEY (see the spec's Key setup)")
    [[ -r "$KOMAC_KEY_FILE" ]] || missing+=("komac token $KOMAC_KEY_FILE")
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

check_gh() {
    gh auth status >/dev/null 2>&1 || fail "gh is not authenticated (gh auth login)"
    gh attestation verify --help >/dev/null 2>&1 || fail "this gh cannot verify attestations; upgrade gh (brew upgrade gh)"
}

# A tag cut before the workflow landed cannot use this flow: the run would
# use the workflow file as of the tag.
check_workflow_at_tag() {
    git -C "$REPO_ROOT" cat-file -e "$TAG:.github/workflows/$WORKFLOW" 2>/dev/null \
        || fail ".github/workflows/$WORKFLOW is not in tag $TAG; tag a commit that has it"
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
    MACOS_ARCHIVE="$archive"
}

# copy_scripts puts the installer(s) from the tag's worktree into $DIST so
# they are listed in SHA256SUMS and published with the archives.
copy_scripts() {
    check_script_embedded "$SRC/install.sh" install.sh
    check_script_embedded "$SRC/install.ps1" install.ps1
    cp "$SRC/install.sh" "$DIST/install.sh"
    cp "$SRC/install.ps1" "$DIST/install.ps1"
}

# check_script_embedded <path> <name> fails when the installer still holds the
# placeholder key block that embed-key.sh replaces.
check_script_embedded() {
    if grep -qxF 'REPLACED-BY-release/embed-key.sh' "$1"; then
        fail "$2 has no embedded release key (placeholder still present); run release/embed-key.sh release/agentbus-release-ed25519.pub $2 and commit before tagging"
    fi
}

# check_installers_embedded is the cheap preflight form: it reads install.sh
# and install.ps1 from the tag, before anything is built.
check_installers_embedded() {
    local tmp; tmp="$(mktemp)"
    git -C "$REPO_ROOT" show "$TAG:install.sh" > "$tmp" 2>/dev/null || { rm -f "$tmp"; fail "$TAG has no install.sh; the installer ships with every release, so tag a commit that includes it"; }
    local rc=0
    (check_script_embedded "$tmp" install.sh) || rc=$?
    if [[ "$rc" -eq 0 ]]; then
        git -C "$REPO_ROOT" show "$TAG:install.ps1" > "$tmp" 2>/dev/null || { rm -f "$tmp"; fail "$TAG has no install.ps1; the Windows installer ships with every release, so tag a commit that includes it"; }
        (check_script_embedded "$tmp" install.ps1) || rc=$?
    fi
    rm -f "$tmp"
    return "$rc"
}

write_sums() {
    echo "==> Writing SHA256SUMS"
    (cd "$DIST" && shasum -a 256 "${ARCHIVES[@]}" install.sh install.ps1 > SHA256SUMS)
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

# ensure_draft creates the draft release for $TAG, reuses an existing draft
# (rerun after a failure), or, after a warning, reuses a published release
# (rerun that replaces public assets). releases/latest never resolves to a
# draft, so the install scripts cannot see a partial release.
ensure_draft() {
    local draft
    if draft="$(gh release view "$TAG" --repo "$GH_REPO" --json isDraft --jq .isDraft 2>/dev/null)"; then
        if [[ "$draft" == true ]]; then
            echo "==> Reusing draft release $TAG"
        else
            echo "warning: release $TAG is already published; continuing replaces its public assets" >&2
            PUBLISHED=1
        fi
        return
    fi
    echo "==> Creating draft release $TAG"
    local notes=() line
    while IFS= read -r line; do notes+=("$line"); done < <(release_notes_args)
    gh release create "$TAG" --repo "$GH_REPO" --verify-tag --title "$TAG" --draft "${notes[@]}"
}

# list_run_ids prints the ids of the existing $WORKFLOW runs titled for $TAG.
list_run_ids() {
    gh run list --repo "$GH_REPO" --workflow "$WORKFLOW" --event workflow_dispatch --limit 50 \
        --json databaseId,displayTitle \
        --jq ".[] | select(.displayTitle == \"release-build $TAG\") | .databaseId"
}

# dispatch_build runs the workflow on the tag itself (its github.ref is the
# tag, and the workflow file used is the tag's) and records the run id. The
# ids of the tag's earlier runs are recorded first and excluded, so a stale
# run (a rerun within the clock slack) is never mistaken for this dispatch.
dispatch_build() {
    local existing since
    existing="$(list_run_ids)" || fail "could not list the existing $WORKFLOW runs"
    echo "==> Dispatching $WORKFLOW on $TAG"
    since="$(date -u -v-60S +%Y-%m-%dT%H:%M:%SZ)" # one minute of clock slack
    gh workflow run "$WORKFLOW" --repo "$GH_REPO" --ref "$TAG" -f "tag=$TAG"
    local ids=() id
    while IFS= read -r id; do
        if [[ -n "$id" ]]; then ids+=("$id"); fi
    done <<< "$existing"
    RUN_ID="$(find_run "$since" ${ids[@]+"${ids[@]}"})"
    echo "run: https://github.com/$GH_REPO/actions/runs/$RUN_ID"
}

# find_run <since> [excluded-run-id...]: the newest workflow_dispatch run of
# $WORKFLOW created at or after <since> (RFC 3339 UTC) whose display title is
# "release-build <tag>" and whose id is not excluded. Polls because the run
# appears a moment after the dispatch.
find_run() {
    local since="$1" id i ex="" x; shift
    for x in "$@"; do
        [[ "$x" =~ ^[0-9]+$ ]] || fail "find_run: bad run id '$x'"
        ex+="${ex:+,}$x"
    done
    for ((i = 0; i < FIND_RUN_TRIES; i++)); do
        id="$(gh run list --repo "$GH_REPO" --workflow "$WORKFLOW" --event workflow_dispatch --limit 50 \
            --json databaseId,displayTitle,createdAt \
            --jq "[.[] | select(.createdAt >= \"$since\" and .displayTitle == \"release-build $TAG\" and (.databaseId as \$i | [$ex] | index(\$i) | not))] | sort_by(.createdAt) | last | .databaseId // empty")"
        if [[ -n "$id" ]]; then printf '%s\n' "$id"; return 0; fi
        sleep "$FIND_RUN_SLEEP"
    done
    fail "could not find the dispatched run of $WORKFLOW for $TAG"
}

watch_run() {
    echo "==> Waiting for run $RUN_ID"
    gh run watch "$RUN_ID" --repo "$GH_REPO" --exit-status \
        || fail "$WORKFLOW failed; the draft stays for a rerun: https://github.com/$GH_REPO/actions/runs/$RUN_ID"
}

# download_archives fetches the four attested archives the workflow uploaded
# to the draft and requires every one of them.
download_archives() {
    echo "==> Downloading the Linux and Windows archives"
    gh release download "$TAG" --repo "$GH_REPO" --dir "$DIST" --clobber \
        --pattern "${BIN_NAME}-${TAG}-linux-*.tar.gz" --pattern "${BIN_NAME}-${TAG}-windows-*.zip"
    local a
    for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip windows-arm64.zip; do
        [[ -f "$DIST/${BIN_NAME}-${TAG}-$a" ]] || fail "release $TAG lacks ${BIN_NAME}-${TAG}-$a; rerun the workflow"
        ARCHIVES+=("${BIN_NAME}-${TAG}-$a")
    done
}

# verify_attestations checks the Sigstore build provenance of every
# downloaded archive against this repository before anything is signed.
verify_attestations() {
    echo "==> Verifying build attestations"
    local a
    for a in "${ARCHIVES[@]}"; do
        [[ "$a" == "$MACOS_ARCHIVE" ]] && continue
        gh attestation verify "$DIST/$a" --repo "$GH_REPO" >/dev/null \
            || fail "attestation of $a does not verify against $GH_REPO"
    done
}

# upload_assets adds what only this machine has to the draft; the four
# archives are already there, uploaded by the workflow.
upload_assets() {
    echo "==> Uploading the macOS tarball, the install scripts and SHA256SUMS"
    (cd "$DIST" && gh release upload "$TAG" --repo "$GH_REPO" --clobber "$MACOS_ARCHIVE" install.sh install.ps1 SHA256SUMS SHA256SUMS.sig)
}

# publish_release makes the draft public and updates the package managers,
# or stops with the draft in place under --no-publish.
publish_release() {
    if [[ "$NO_PUBLISH" == 1 ]]; then
        echo "==> --no-publish: stopping before publish; draft: https://github.com/$GH_REPO/releases/tag/$TAG"
        return 0
    fi
    if [[ "$PUBLISHED" == 0 ]]; then
        echo "==> Publishing release $TAG"
        gh release edit "$TAG" --repo "$GH_REPO" --draft=false
    fi
    update_tap
    update_scoop
    submit_winget
    echo "==> Released ${TAG}: brew install ericfitz/tap/${BIN_NAME}; curl -fsSL https://github.com/${GH_REPO}/releases/latest/download/install.sh | sh; irm https://github.com/${GH_REPO}/releases/latest/download/install.ps1 | iex"
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
        echo "formula unchanged"
    else
        cp "$rendered" "$TAP_DIR/Formula/${BIN_NAME}.rb"
        git -C "$TAP_DIR" add "Formula/${BIN_NAME}.rb"
        git -C "$TAP_DIR" commit -m "${BIN_NAME} ${VERSION}"
    fi
    # Always push: a rerun after "commit ok, push failed" must publish the
    # stranded commit, and the push is a no-op when up to date.
    git -C "$TAP_DIR" push
}

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
        echo "scoop manifest unchanged"
    else
        cp "$rendered" "$SCOOP_DIR/bucket/${BIN_NAME}.json"
        git -C "$SCOOP_DIR" add "bucket/${BIN_NAME}.json"
        git -C "$SCOOP_DIR" commit -m "${BIN_NAME} ${VERSION}"
    fi
    # Always push (see update_tap).
    git -C "$SCOOP_DIR" push
}

# submit_winget opens the winget-pkgs pull request for this version with
# komac. An open pull request for the version counts as done (rerun). A
# komac failure does not fail the release: the exact rerun command is
# printed. The token is scoped to the one command, because exporting
# GITHUB_TOKEN would override gh's own auth for the rest of the run.
submit_winget() {
    echo "==> Submitting ${WINGET_ID} ${VERSION} to winget"
    local open
    open="$(gh pr list --repo microsoft/winget-pkgs --state open --search "\"${WINGET_ID} version ${VERSION}\" in:title" --json number --jq '.[].number')" \
        || fail "could not list open pull requests in microsoft/winget-pkgs; rerun once gh works (an open pull request for ${VERSION} would make komac open a duplicate)"
    if [[ -n "$open" ]]; then
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

main() {
    parse_args "$@"
    require_tools
    check_signing_key
    check_tag_exists
    check_tag_pushed
    check_gh
    check_workflow_at_tag
    check_installers_embedded
    checkout_tag
    build_macos
    ensure_draft
    dispatch_build
    watch_run
    download_archives
    verify_attestations
    copy_scripts
    write_sums
    sign_sums
    upload_assets
    publish_release
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi
