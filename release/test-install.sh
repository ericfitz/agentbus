#!/usr/bin/env bash
# Tests install.sh against a fake release served locally, inside Linux
# containers. Needs Docker (Desktop, for host.docker.internal), python3 and
# Homebrew openssl@3. Never touches ~/.keys: it signs with a throwaway key
# and rewrites install.sh's key block in test copies only.
#   release/test-install.sh             full matrix (ubuntu, debian, alpine x amd64, arm64)
#   release/test-install.sh --quick     ubuntu/amd64 only
#   release/test-install.sh --self-test fixture builder, server and judging checks, no Docker
# Exit 0 only when every case passes. Every container is labeled with this
# run's id and removed on any exit path; the server and temp dir go with it.
# SC2016: the scripts handed to containers are single-quoted on purpose; the
# container's shell expands them, not this one.
# shellcheck disable=SC2016
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
OPENSSL="$(brew --prefix openssl@3)/bin/openssl"
WORK="$(mktemp -d)"
FX="$WORK/fixture"
PORT=$(( 20000 + RANDOM % 20000 ))
RUN_ID="$$-$RANDOM"
SERVER_PID=""
DOCKER_USED=0
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

remove_containers() {
    local c
    [[ "$DOCKER_USED" -eq 1 ]] || return 0
    for c in $(docker ps -aq --filter "label=agentbus-itest-run=$RUN_ID" 2>/dev/null || true); do
        docker rm -f "$c" >/dev/null 2>&1 || true
    done
}
cleanup() {
    if [[ -n "$SERVER_PID" ]]; then kill "$SERVER_PID" 2>/dev/null || true; fi
    remove_containers
    rm -rf "${WORK:?}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
[[ -x "$OPENSSL" ]] || { echo "error: Homebrew openssl@3 missing (brew install openssl@3)" >&2; exit 1; }

# --- fixture ------------------------------------------------------------------
# build_fixture lays out $FX/<variant>/{latest,<tag>/...} for the variants
# valid (v9.0.0 and v9.0.1), tampered-tarball, tampered-sums, tampered-sig,
# missing-entry and no-archive (v9.0.1 with SHA256SUMS but no tarballs, so
# the archive download is a 404), plus $FX/install.sh with the throwaway key embedded and
# $FX/install-nokey.sh with the placeholder key block.
build_fixture() {
    "$OPENSSL" genpkey -algorithm ed25519 -out "$WORK/key.pem" 2>/dev/null
    "$OPENSSL" pkey -in "$WORK/key.pem" -pubout -out "$WORK/key.pub" 2>/dev/null
    mkdir -p "$FX/fakes"
    cp "$REPO_ROOT/install.sh" "$FX/install.sh"
    "$HERE/embed-key.sh" "$WORK/key.pub" "$FX/install.sh" >/dev/null
    # A copy with the placeholder block, whatever the repo's install.sh carries.
    awk '
        /^# BEGIN agentbus release public key$/ { print; print "PUBKEY_PEM=\047-----BEGIN PUBLIC KEY-----"; print "REPLACED-BY-release/embed-key.sh"; print "-----END PUBLIC KEY-----\047"; skip = 1; next }
        /^# END agentbus release public key$/ { skip = 0 }
        !skip { print }
    ' "$REPO_ROOT/install.sh" > "$FX/install-nokey.sh"
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
    mkdir -p "$FX/no-archive"; cp -R "$FX/valid/v9.0.1" "$FX/no-archive/v9.0.1"; printf 'v9.0.1\n' > "$FX/no-archive/latest"
    rm -f "$FX"/no-archive/v9.0.1/agentbus-*.tar.gz
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
    local tag="$2" version="$3" d="$FX/$1/$2" arch
    mkdir -p "$d/bin"
    printf '#!/bin/sh\necho %s\n' "$version" > "$d/bin/agentbus"; chmod 755 "$d/bin/agentbus"
    for arch in amd64 arm64; do
        tar -C "$d/bin" -czf "$d/agentbus-$tag-linux-$arch.tar.gz" agentbus
    done
    rm -r "${d:?}/bin"
    cp "$FX/install.sh" "$d/install.sh"
    (cd "$d" && shasum -a 256 "agentbus-$tag-linux-amd64.tar.gz" "agentbus-$tag-linux-arm64.tar.gz" install.sh > SHA256SUMS)
    "$OPENSSL" pkeyutl -sign -rawin -inkey "$WORK/key.pem" -in "$d/SHA256SUMS" -out "$d/SHA256SUMS.sig"
}

# append_byte makes a file differ from its recorded checksum (or makes a
# signature the wrong length) without depending on the file's content.
append_byte() { printf '\0' >> "$1"; }

start_server() {
    python3 -I "$HERE/testdata/fixture-server.py" "$FX" "$PORT" & SERVER_PID=$!
    for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$PORT/" && return 0; sleep 0.1; done
    echo "error: fixture server did not start" >&2; exit 1
}

# judge <exit> <output> <want-exit> <want-substring>: true when both match.
judge() { [[ "$1" == "$3" && "$2" == *"$4"* ]]; }

# key_matches_pub <install.sh> <pub>: prints one PASS or FAIL line. Extracts
# the PEM textually: sourcing install.sh would run main, whose `exit` on macOS
# (Darwin check) would end the sourcing shell too.
key_matches_pub() {
    local script="$1" pub="$2"
    if [[ ! -r "$pub" ]]; then
        echo "FAIL key-matches-pub: $pub missing; the user generates it (plan Task 1) and then runs: release/embed-key.sh release/agentbus-release-ed25519.pub install.sh"; return 1
    fi
    if diff <(sed -n "/^PUBKEY_PEM='/,/^-----END PUBLIC KEY-----'$/p" "$script" | sed "s/^PUBKEY_PEM='//; s/'\$//") "$pub" >/dev/null; then
        echo "PASS key-matches-pub"
    else
        echo "FAIL key-matches-pub: install.sh's embedded key differs from $pub; run release/embed-key.sh"; return 1
    fi
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
    assert "no-archive has sums but no tarballs" "[[ -f '$FX/no-archive/v9.0.1/SHA256SUMS' && -z \$(ls '$FX'/no-archive/v9.0.1/agentbus-*.tar.gz 2>/dev/null) ]]"
    assert "stub prints its version" "[[ \$(tar -xzOf '$FX/valid/v9.0.0/agentbus-v9.0.0-linux-amd64.tar.gz' agentbus | sh) == 9.0.0 ]]"
    assert "install.sh copy carries the throwaway key" "grep -q \"\$(sed -n 2p '$WORK/key.pub')\" '$FX/install.sh'"
    assert "install.sh copy differs from the repo only in the key block" "diff <(sed '/BEGIN agentbus release public key/,/END agentbus release public key/d' '$REPO_ROOT/install.sh') <(sed '/BEGIN agentbus release public key/,/END agentbus release public key/d' '$FX/install.sh') >/dev/null"
    assert "install-nokey.sh has the placeholder and differs only in the key block" "grep -q '^REPLACED-BY-release/embed-key.sh\$' '$FX/install-nokey.sh' && ! grep -qF \"\$(sed -n 2p '$WORK/key.pub')\" '$FX/install-nokey.sh' && diff <(sed '/BEGIN agentbus release public key/,/END agentbus release public key/d' '$REPO_ROOT/install.sh') <(sed '/BEGIN agentbus release public key/,/END agentbus release public key/d' '$FX/install-nokey.sh') >/dev/null"
    # Recorded outputs for the judging and key-check logic.
    assert "judge accepts matching exit and substring" "judge 1 'agentbus install: checksum mismatch for x' 1 'checksum mismatch'"
    assert "judge rejects a wrong exit" "! judge 0 'checksum mismatch' 1 'checksum mismatch'"
    assert "judge rejects a missing substring" "! judge 1 'something else' 1 'checksum mismatch'"
    assert "key_matches_pub passes for the embedded throwaway key" "[[ \$(key_matches_pub '$FX/install.sh' '$WORK/key.pub') == 'PASS key-matches-pub' ]]"
    assert "key_matches_pub fails for the placeholder block" "[[ \$(key_matches_pub '$FX/install-nokey.sh' '$WORK/key.pub') == 'FAIL key-matches-pub: install.sh'*differs* ]]"
    assert "key_matches_pub fails when the pub file is missing" "[[ \$(key_matches_pub '$FX/install.sh' '$WORK/nope.pub') == 'FAIL key-matches-pub: $WORK/nope.pub missing;'* ]]"
    start_server
    assert "server redirects latest" "[[ \$(curl -sI -o /dev/null -w '%{redirect_url}' http://127.0.0.1:$PORT/valid/releases/latest) == */valid/releases/tag/v9.0.1 ]]"
    assert "server serves SHA256SUMS" "curl -fsS http://127.0.0.1:$PORT/valid/releases/download/v9.0.1/SHA256SUMS | cmp -s - '$FX/valid/v9.0.1/SHA256SUMS'"
    local pid="$SERVER_PID"
    kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true
    assert "server stopped" "! kill -0 $pid 2>/dev/null"
    SERVER_PID=""
    local w="$WORK"; cleanup; trap - EXIT
    if [[ -d "$w" ]]; then echo "FAIL self: cleanup left $w"; ok=0; else echo "PASS self: cleanup removed the work directory"; fi
    if [[ "$ok" -eq 1 ]]; then echo "test-install self-test: OK"; fi
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
    out="$(docker run --rm --label "agentbus-itest-run=$RUN_ID" --platform "linux/$arch" -v "$FX:/fixture:ro" \
        -e "AGENTBUS_BASE_URL=$BASE/valid" -e "BASE=$BASE" -e HOME=/root \
        "agentbus-itest-$distro-$arch" sh -c "$script" 2>&1)" || exit=$?
    if judge "$exit" "$out" "$want_exit" "$want_out"; then
        PASS=$((PASS + 1)); echo "PASS $distro/$arch $name"
    else
        FAIL=$((FAIL + 1)); echo "FAIL $distro/$arch $name: exit=$exit want=$want_exit; output:"
        # shellcheck disable=SC2001
        echo "$out" | sed 's/^/    /'
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
    run_case version-prerelease "$d" "$a" 1 "version must look like vX.Y.Z" 'AGENTBUS_VERSION=v1.2.3-rc1 sh /fixture/install.sh'
    run_case version-newline "$d" "$a" 1 "containing a newline" 'AGENTBUS_VERSION="v9.0.0
v9.0.1" sh /fixture/install.sh'
    run_case positional-argument "$d" "$a" 1 "takes no arguments" 'sh /fixture/install.sh extra'
    run_case no-embedded-key "$d" "$a" 1 "no embedded release key" 'sh /fixture/install-nokey.sh'
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
    run_case install-dir-is-directory "$d" "$a" 1 "refused-clean" 'mkdir -p /tmp/t /opt/x/agentbus; if out=$(TMPDIR=/tmp/t AGENTBUS_INSTALL_DIR=/opt/x sh /fixture/install.sh 2>&1); then echo "unexpected success"; exit 9; fi; case "$out" in *"/opt/x/agentbus is a directory"*) ;; *) echo "$out"; exit 8 ;; esac; [ -z "$(ls -A /tmp/t)" ] && [ "$(ls -A /opt/x)" = agentbus ] && [ -z "$(ls -A /opt/x/agentbus)" ] && echo refused-clean && exit 1'
    run_case trailing-slashes "$d" "$a" 0 "installed agentbus 9.0.1 to /opt/y/agentbus" 'AGENTBUS_BASE_URL=$BASE/valid// AGENTBUS_INSTALL_DIR=/opt/y// sh /fixture/install.sh && [ "$(/opt/y/agentbus version)" = 9.0.1 ]'
    run_case home-unset "$d" "$a" 1 "HOME is not set" 'env -u HOME sh /fixture/install.sh'
    run_case upgrade "$d" "$a" 0 "upgraded from 9.0.0: restart" 'AGENTBUS_VERSION=v9.0.0 sh /fixture/install.sh >/dev/null && sh /fixture/install.sh && [ "$(~/.local/bin/agentbus version)" = 9.0.1 ]'
    run_case reinstall-same-version "$d" "$a" 0 "reinstalled: agentbus 9.0.1 was already installed" 'sh /fixture/install.sh >/dev/null && out=$(sh /fixture/install.sh 2>&1) && echo "$out" && case "$out" in *upgraded*) echo "unexpected upgraded line"; exit 9 ;; esac'
    run_case reinstall-broken-old-binary "$d" "$a" 0 "upgraded: restart" 'mkdir -p ~/.local/bin && printf "#!/bin/sh\nexit 3\n" > ~/.local/bin/agentbus && chmod 755 ~/.local/bin/agentbus && sh /fixture/install.sh && [ "$(~/.local/bin/agentbus version)" = 9.0.1 ]'
    run_case release-without-linux-build "$d" "$a" 1 "agentbus-v9.0.1-linux-$a.tar.gz" 'AGENTBUS_BASE_URL=$BASE/no-archive sh /fixture/install.sh'
    run_case release-without-linux-build-advice "$d" "$a" 1 "Linux builds start at v1.14.1" 'AGENTBUS_BASE_URL=$BASE/no-archive sh /fixture/install.sh'
    run_case download-network-failure "$d" "$a" 1 "download failed: http://host.docker.internal:1/releases/download/v9.0.1/" 'AGENTBUS_VERSION=v9.0.1 AGENTBUS_BASE_URL=http://host.docker.internal:1 sh /fixture/install.sh'
    run_case concurrent "$d" "$a" 0 "9.0.1" 'sh /fixture/install.sh >/dev/null & p1=$!; sh /fixture/install.sh >/dev/null & p2=$!; wait $p1 && wait $p2 && ~/.local/bin/agentbus version'
    run_case leftover-tmp "$d" "$a" 0 "installed agentbus 9.0.1" 'mkdir -p ~/.local/bin && echo junk > ~/.local/bin/.agentbus.tmp.12345 && sh /fixture/install.sh && [ -f ~/.local/bin/.agentbus.tmp.12345 ]'
    # The fake tar is installed in its own statement so that `&` backgrounds
    # install.sh itself and $! is its pid (not a wrapper list's).
    run_case killed-mid-install "$d" "$a" 0 "clean" "mkdir -p /tmp/t && $(fake tar)true; TMPDIR=/tmp/t sh /fixture/install.sh & p=\$!; sleep 2; kill -TERM \$p; wait \$p; [ -z \"\$(ls -A /tmp/t)\" ] && [ -z \"\$(ls -A ~/.local/bin 2>/dev/null)\" ] && echo clean"
}

main() {
    if [[ "$MODE" == self ]]; then self_test; exit; fi
    command -v docker >/dev/null || { echo "error: docker is required for the container matrix" >&2; exit 1; }
    DOCKER_USED=1
    build_fixture
    start_server
    local d a line
    for d in "${DISTROS[@]}"; do for a in "${ARCHES[@]}"; do
        docker build -q --platform "linux/$a" -t "agentbus-itest-$d-$a" -f "$HERE/testdata/Dockerfile.$d" "$HERE/testdata" >/dev/null
        cases "$d" "$a"
    done; done
    if line="$(key_matches_pub "$REPO_ROOT/install.sh" "$REPO_ROOT/release/agentbus-release-ed25519.pub")"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); fi
    echo "$line"
    echo "test-install: $PASS passed, $FAIL failed"
    [[ "$FAIL" -eq 0 ]]
}
main
