#!/usr/bin/env bash
# Build, sign, notarize, and publish a macOS release of agentbus, then update
# the Homebrew tap. Run from a checkout whose HEAD is the given tag.
#   ./release/release.sh v0.1.1
# One-time setup: a notarytool keychain profile named $NOTARY_PROFILE, created
# with `xcrun notarytool store-credentials` (Apple ID + app-specific password).
set -euo pipefail

BIN_NAME="agentbus"
GH_REPO="ericfitz/agentbus"
TAP_DIR="${TAP_DIR:-$HOME/Projects/homebrew-tap}" # SSH clone of ericfitz/homebrew-tap
SIGN_IDENTITY="Developer ID Application: Robert Fitzgerald (796T45968D)"
NOTARY_PROFILE="sqdist-notary" # team-level credential, shared across projects
VERSION_VAR="github.com/ericfitz/agentbus/internal/mcpserver.Version"

TAG="${1:?usage: release.sh <tag>}"
VERSION="${TAG#v}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${REPO_ROOT}/dist"
BIN="${DIST}/${BIN_NAME}"
TARBALL="${DIST}/${BIN_NAME}-${TAG}-macos-universal.tar.gz"
cd "$REPO_ROOT"

[[ "$(git describe --tags --exact-match 2>/dev/null)" == "$TAG" ]] ||
    { echo "error: HEAD is not tagged $TAG" >&2; exit 1; }
[[ -z "$(git status --porcelain)" ]] || { echo "error: working tree is dirty" >&2; exit 1; }

echo "==> Building universal binary $VERSION"
rm -rf "$DIST" && mkdir -p "$DIST"
for arch in arm64 amd64; do
    CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath \
        -ldflags "-s -w -X ${VERSION_VAR}=${VERSION}" -o "${BIN}-${arch}" .
done
lipo -create -output "$BIN" "${BIN}-arm64" "${BIN}-amd64"
rm "${BIN}-arm64" "${BIN}-amd64"
lipo -info "$BIN"
[[ "$("$BIN" version)" == "$VERSION" ]] || { echo "error: version mismatch" >&2; exit 1; }

echo "==> Codesigning with hardened runtime"
codesign --force --timestamp --options runtime --sign "$SIGN_IDENTITY" "$BIN"
codesign --verify --strict --verbose=2 "$BIN"

echo "==> Notarizing (bare CLI binaries cannot be stapled; Gatekeeper checks online)"
ZIP="${DIST}/notarize.zip"
ditto -c -k --keepParent "$BIN" "$ZIP"
xcrun notarytool submit "$ZIP" --keychain-profile "$NOTARY_PROFILE" --wait | tee "${DIST}/notary.log"
grep -q 'status: Accepted' "${DIST}/notary.log" || { echo "error: notarization not accepted" >&2; exit 1; }
rm -f "$ZIP"

echo "==> Packaging"
tar -C "$DIST" -czf "$TARBALL" "$BIN_NAME"
SHA="$(shasum -a 256 "$TARBALL" | awk '{print $1}')"
echo "$SHA  $(basename "$TARBALL")" | tee "${TARBALL}.sha256"

echo "==> Creating GitHub release $TAG"
gh release create "$TAG" --repo "$GH_REPO" --title "$TAG" --generate-notes \
    "$TARBALL" "${TARBALL}.sha256"

echo "==> Updating Homebrew tap"
[[ -d "$TAP_DIR" ]] || git clone git@github.com:ericfitz/homebrew-tap.git "$TAP_DIR"
git -C "$TAP_DIR" pull -q --ff-only
URL="https://github.com/${GH_REPO}/releases/download/${TAG}/$(basename "$TARBALL")"
sed -e "s|__URL__|${URL}|" -e "s|__SHA256__|${SHA}|" \
    "${REPO_ROOT}/release/${BIN_NAME}.rb.tmpl" > "${TAP_DIR}/Formula/${BIN_NAME}.rb"
git -C "$TAP_DIR" add "Formula/${BIN_NAME}.rb"
git -C "$TAP_DIR" commit -m "${BIN_NAME} ${VERSION}"
git -C "$TAP_DIR" push
echo "==> Released ${TAG}: brew install ericfitz/tap/${BIN_NAME}"
