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
MCowBQYDK2VwAyEAx7/YtImf3eM+x0+mN3CEmMiGsSZ404MWe0G7UModNsQ=
-----END PUBLIC KEY-----'
# END agentbus release public key

BASE_URL="${AGENTBUS_BASE_URL:-https://github.com/ericfitz/agentbus}"
while [ "${BASE_URL%/}" != "$BASE_URL" ]; do BASE_URL="${BASE_URL%/}"; done
ARCH="" TAG="" ASSET="" OPENSSL="" OLD_OPENSSL="" SHA_CMD="" SKIP_SIG=0 INSTALL_DIR="" TMP="" UPGRADE=0 OLD_VERSION=""

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
        while [ "${INSTALL_DIR%/}" != "$INSTALL_DIR" ] && [ "$INSTALL_DIR" != / ]; do INSTALL_DIR="${INSTALL_DIR%/}"; done
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
    case "$TAG" in *"
"*) die "version must look like vX.Y.Z, got a value containing a newline" ;; esac
    printf '%s\n' "$TAG" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || die "version must look like vX.Y.Z, got '$TAG'"
    ASSET="agentbus-$TAG-linux-$ARCH.tar.gz"
}

# fetch <remote-name> <local-name>. A 404 on the release archive means the tag
# has no Linux build (they began at v1.14.1); any other failure is a download error.
fetch() {
    url="$BASE_URL/releases/download/$TAG/$1"
    rc=0
    code="$(curl -fsSL -o "$TMP/$2" -w '%{http_code}' "$url")" || rc=$?
    [ "$rc" -ne 0 ] || return 0
    if [ "$1" = "$ASSET" ] && [ "$rc" -eq 22 ] && [ "$code" = 404 ]; then
        die "release $TAG has no Linux build: $ASSET was not found at $url. Linux builds start at v1.14.1; rerun with AGENTBUS_VERSION=v1.14.1 or later"
    fi
    die "download failed: $url"
}

download() {
    TMP="$(mktemp -d)"
    fetch "$ASSET" "$ASSET"
    fetch SHA256SUMS SHA256SUMS
    [ "$SKIP_SIG" = 1 ] || fetch SHA256SUMS.sig SHA256SUMS.sig
}

verify_signature() {
    [ "$SKIP_SIG" = 1 ] && return 0
    placeholder="$(printf '%s\n%s\n%s' '-----BEGIN PUBLIC KEY-----' 'REPLACED-BY-release/embed-key.sh' '-----END PUBLIC KEY-----')"
    [ "$PUBKEY_PEM" != "$placeholder" ] \
        || die "this copy of install.sh has no embedded release key; fetch it from $BASE_URL/releases/latest/download/install.sh"
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
    [ ! -d "$INSTALL_DIR/agentbus" ] || die "$INSTALL_DIR/agentbus is a directory; remove it or choose another AGENTBUS_INSTALL_DIR"
    if [ -x "$INSTALL_DIR/agentbus" ]; then
        UPGRADE=1
        # A binary that does not run counts as an upgrade with an unknown old version.
        OLD_VERSION="$("$INSTALL_DIR/agentbus" version 2>/dev/null)" || OLD_VERSION=""
    fi
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
    if [ "$UPGRADE" = 1 ]; then
        if [ -n "$OLD_VERSION" ] && [ "$OLD_VERSION" = "$v" ]; then
            echo "reinstalled: agentbus $v was already installed"
        elif [ -n "$OLD_VERSION" ]; then
            echo "upgraded from $OLD_VERSION: restart every harness session and the TUI to pick up the new binary"
        else
            echo "upgraded: restart every harness session and the TUI to pick up the new binary"
        fi
    fi
    return 0
}

main() {
    [ $# -eq 0 ] || die "this script takes no arguments; configure it with AGENTBUS_VERSION, AGENTBUS_INSTALL_DIR, AGENTBUS_BASE_URL, AGENTBUS_SKIP_SIGNATURE"
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
