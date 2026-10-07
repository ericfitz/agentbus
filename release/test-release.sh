#!/usr/bin/env bash
# Unit tests for the release tooling: embed-key.sh and release.sh's pure
# functions (sourced, never run). No network, no Docker, no keys in ~/.keys.
#   release/test-release.sh
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
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
    # shellcheck disable=SC2001 # prefixing every line is clearer with sed than ${//}
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
printf '# BEGIN agentbus release public key \nx\n# END agentbus release public key\n' > "$WORK/trail.sh"
check embed-marker-near-miss 1 "trail.sh" -- "$HERE/embed-key.sh" "$WORK/throwaway.pub" "$WORK/trail.sh"
printf '# END agentbus release public key\nkeep\n# BEGIN agentbus release public key\nmore\n' > "$WORK/swapped.sh"
check embed-marker-order 1 "swapped.sh" -- "$HERE/embed-key.sh" "$WORK/throwaway.pub" "$WORK/swapped.sh"
check embed-marker-order-untouched 0 "" -- grep -q '^keep$' "$WORK/swapped.sh"
printf '# BEGIN agentbus release public key\nPUBKEY_PEM=placeholder\n# END agentbus release public key\n' > "$WORK/mode.sh"
chmod 750 "$WORK/mode.sh"
check embed-keeps-mode 0 "key embedded" -- bash -c "'$HERE/embed-key.sh' '$WORK/throwaway.pub' '$WORK/mode.sh' && [[ \"\$(ls -l '$WORK/mode.sh' | cut -c1-10)\" == -rwxr-x--- ]]"
printf '# BEGIN agentbus release public key\nPUBKEY_PEM=placeholder\n# END agentbus release public key\n' > "$WORK/ro.sh"
chmod 444 "$WORK/ro.sh"
check embed-readonly-input 0 "key embedded" -- "$HERE/embed-key.sh" "$WORK/throwaway.pub" "$WORK/ro.sh"
check embed-missing-pub 1 "no such public key" -- "$HERE/embed-key.sh" "$WORK/nope.pub" "$WORK/script.sh"
check embed-bad-pub 1 "not a PEM public key" -- bash -c "printf 'junk\n' > '$WORK/junk.pub'; '$HERE/embed-key.sh' '$WORK/junk.pub' '$WORK/script.sh'"
check embed-usage 2 "usage" -- "$HERE/embed-key.sh"

# --- release.sh pure functions ----------------------------------------------
# shellcheck source=release/release.sh
source "$HERE/release.sh"   # the main guard keeps it from running

check args-none 2 "usage" -- parse_args
check args-bad-tag 2 "tag must look like vX.Y.Z" -- parse_args 1.2.3
check args-two 2 "usage" -- parse_args v1.2.3 extra
check args-ok 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 && [[ \$VERSION == 1.2.3 ]]"
check args-prerelease 2 "tag must look like vX.Y.Z, got 'v1.2.3-rc.1'" -- parse_args v1.2.3-rc.1

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

printf "PUBKEY_PEM='-----BEGIN PUBLIC KEY-----\nREPLACED-BY-release/embed-key.sh\n-----END PUBLIC KEY-----'\n" > "$WORK/unembedded.sh"
check embedded-placeholder 1 "install.sh has no embedded release key" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/unembedded.sh' install.sh"
check embedded-placeholder-hint 1 "embed-key.sh" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/unembedded.sh' install.sh"
check embedded-ok 0 "" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/script.sh' install.sh"
cp "$WORK/unembedded.sh" "$WORK/repo/install.sh" && git -C "$WORK/repo" add install.sh && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m inst && git -C "$WORK/repo" tag v0.0.3
check preflight-placeholder 1 "no embedded release key" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.3; check_installers_embedded"
# Build the unembedded copy from install.sh with its key block reset to the
# placeholder, so this case does not depend on whether the repo has embedded
# the real key yet.
awk '
    /^# BEGIN agentbus release public key$/ { print; print "PUBKEY_PEM='"'"'-----BEGIN PUBLIC KEY-----"; print "REPLACED-BY-release/embed-key.sh"; print "-----END PUBLIC KEY-----'"'"'"; skip = 1; next }
    /^# END agentbus release public key$/ { skip = 0 }
    !skip { print }
' "$HERE/../install.sh" > "$WORK/real-install.sh"
check real-unembedded 1 "no embedded release key" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/real-install.sh' install.sh"
"$HERE/embed-key.sh" "$WORK/throwaway.pub" "$WORK/real-install.sh" >/dev/null
check real-embedded 0 "" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/real-install.sh' install.sh"
cp "$WORK/real-install.sh" "$WORK/repo/install.sh" && git -C "$WORK/repo" add install.sh && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m embedded && git -C "$WORK/repo" tag v0.0.4
check preflight-no-ps1 1 "v0.0.4 has no install.ps1; the Windows installer ships with every release, so tag a commit that includes it" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.4; check_installers_embedded"
# install.ps1 in the tag: unembedded refused, embedded accepted.
# unembed_ps1 <in> <out> resets the key block of an install.ps1 to the placeholder.
unembed_ps1() {
    awk '
        /^# BEGIN agentbus release public key$/ { print; print "$PubKeyPem = @'"'"'"; print "-----BEGIN PUBLIC KEY-----"; print "REPLACED-BY-release/embed-key.sh"; print "-----END PUBLIC KEY-----"; print "'"'"'@"; skip = 1; next }
        /^# END agentbus release public key$/ { skip = 0 }
        !skip { print }
    ' "$1" > "$2"
}
unembed_ps1 "$HERE/../install.ps1" "$WORK/real-install.ps1"
check ps1-unembedded 1 "install.ps1 has no embedded release key" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/real-install.ps1' install.ps1"
cp "$WORK/real-install.ps1" "$WORK/repo/install.ps1" && git -C "$WORK/repo" add install.ps1 && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m ps1 && git -C "$WORK/repo" tag v0.0.90
check preflight-ps1-placeholder 1 "install.ps1 has no embedded release key" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.90; check_installers_embedded"
"$HERE/embed-key.sh" "$WORK/throwaway.pub" "$WORK/real-install.ps1" >/dev/null
check ps1-embedded 0 "" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/real-install.ps1' install.ps1"
cp "$WORK/real-install.ps1" "$WORK/repo/install.ps1" && git -C "$WORK/repo" add install.ps1 && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m ps1e && git -C "$WORK/repo" tag v0.0.91
check preflight-embedded 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.91; check_installers_embedded"
check preflight-no-installer 1 "v0.0.1 has no install.sh; the installer ships with every release, so tag a commit that includes it" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; check_installers_embedded"

# check_tag_pushed against a local bare "origin" (no network). v0.0.4 is in the
# clone; v0.0.5 is tagged afterwards and so is only local.
git clone -q --bare "$WORK/repo" "$WORK/origin.git"
git -C "$WORK/repo" remote add origin "$WORK/origin.git"
git -C "$WORK/repo" tag v0.0.5
check tag-pushed-ok 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.4; check_tag_pushed"
check tag-pushed-missing 1 "tag v0.0.5 is not on origin; push it first (git push origin v0.0.5)" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.5; check_tag_pushed"

# --- fixture-server.py ------------------------------------------------------
mkdir -p "$WORK/fx/valid/v9.0.1"
printf 'v9.0.1\n' > "$WORK/fx/valid/latest"
printf 'hello\n' > "$WORK/fx/valid/v9.0.1/asset.txt"
port=$(( 20000 + RANDOM % 20000 ))
python3 -I "$HERE/testdata/fixture-server.py" "$WORK/fx" "$port" & server_pid=$!
trap 'kill "$server_pid" 2>/dev/null || true; rm -rf "$WORK"' EXIT
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$port/" && break; sleep 0.1; done
check server-latest-redirect 0 "/valid/releases/tag/v9.0.1" -- curl -sI -o /dev/null -w '%{redirect_url}' "http://127.0.0.1:$port/valid/releases/latest"
check server-download 0 "hello" -- curl -fsS "http://127.0.0.1:$port/valid/releases/download/v9.0.1/asset.txt"
check server-head 0 "200" -- curl -sI -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/valid/releases/download/v9.0.1/asset.txt"
check server-404-asset 0 "404" -- curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/valid/releases/download/v9.0.1/nope"
check server-404-variant 0 "404" -- curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/other/releases/latest"
check server-traversal 0 "400" -- curl -s -o /dev/null -w '%{http_code}' --path-as-is "http://127.0.0.1:$port/valid/releases/download/../latest"
kill "$server_pid" || true; wait "$server_pid" 2>/dev/null || true
check server-stopped 1 "" -- kill -0 "$server_pid"
check server-no-args 2 "usage: fixture-server.py <root> <port>" -- python3 -I "$HERE/testdata/fixture-server.py"
check server-bad-port 2 "port must be an integer" -- python3 -I "$HERE/testdata/fixture-server.py" "$WORK/fx" abc
check server-missing-root 2 "$WORK/nope" -- python3 -I "$HERE/testdata/fixture-server.py" "$WORK/nope" 20000

# check-linux-brew.sh validates every argument before starting any container
check brew-check-bad-arch 2 "unknown architecture 'bogus' (amd64 or arm64)" -- "$HERE/check-linux-brew.sh" bogus
mkdir -p "$WORK/stubbin"
printf '#!/bin/sh\necho called >> "%s/docker-called"\nexit 1\n' "$WORK" > "$WORK/stubbin/docker"
chmod +x "$WORK/stubbin/docker"
check brew-check-bad-arch-second 2 "unknown architecture 'bogus' (amd64 or arm64)" -- env PATH="$WORK/stubbin:$PATH" "$HERE/check-linux-brew.sh" amd64 bogus
check brew-check-no-container-started 1 "" -- test -e "$WORK/docker-called"

# --- Windows packaging (#35) ------------------------------------------------
printf 'aaaa  agentbus-v1.0.0-windows-amd64.zip\nbbbb  agentbus-v1.0.0-windows-arm64.zip\ncccc  install.ps1\n' > "$WORK/dist/SHA256SUMS"
check scoop-render 0 "" -- bash -c "source '$HERE/release.sh'; render_template '$HERE/agentbus.scoop.json.tmpl' '$WORK/scoop.json' VERSION=1.0.0 AMD64_URL=https://x/a.zip AMD64_SHA256=aaaa ARM64_URL=https://x/r.zip ARM64_SHA256=bbbb && python3 -I -m json.tool '$WORK/scoop.json' >/dev/null"
check scoop-fields 0 "" -- bash -c "python3 -I -c \"import json,sys; m=json.load(open(sys.argv[1])); assert m['version']=='1.0.0' and m['bin']=='agentbus.exe' and m['architecture']['64bit']['hash']=='aaaa' and m['architecture']['arm64']['url']=='https://x/r.zip' and 'checkver' in m and 'autoupdate' in m\" '$WORK/scoop.json'"
# copy_scripts ships install.ps1 and refuses an unembedded one like install.sh.
mkdir -p "$WORK/cs/src" "$WORK/cs/dist"
cp "$WORK/real-install.sh" "$WORK/cs/src/install.sh"
cp "$WORK/real-install.ps1" "$WORK/cs/src/install.ps1"
check copy-scripts-ok 0 "" -- bash -c "source '$HERE/release.sh'; SRC='$WORK/cs/src'; DIST='$WORK/cs/dist'; copy_scripts && cmp '$WORK/cs/src/install.ps1' '$WORK/cs/dist/install.ps1' && cmp '$WORK/cs/src/install.sh' '$WORK/cs/dist/install.sh'"
mkdir -p "$WORK/cs/dist2"
unembed_ps1 "$WORK/cs/src/install.ps1" "$WORK/cs/src/install.ps1.new" && mv "$WORK/cs/src/install.ps1.new" "$WORK/cs/src/install.ps1"
check copy-scripts-ps1-unembedded 1 "install.ps1 has no embedded release key" -- bash -c "source '$HERE/release.sh'; SRC='$WORK/cs/src'; DIST='$WORK/cs/dist2'; copy_scripts"
check copy-scripts-ps1-not-copied 1 "" -- test -e "$WORK/cs/dist2/install.ps1"
check sums-list-ps1 0 "" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/cs/dist'; ARCHIVES=(); cp '$WORK/cs/src/install.sh' '$WORK/cs/dist/install.sh'; cp '$WORK/real-install.ps1' '$WORK/cs/dist/install.ps1'; write_sums >/dev/null && awk '{print \$2}' '$WORK/cs/dist/SHA256SUMS' | diff - <(printf 'install.sh\ninstall.ps1\n')"

# A rerun after "commit ok, push failed" must push (tap and Scoop bucket).
# Local bare remote; a pre-receive hook makes the first push fail.
git init -q --bare "$WORK/rp-origin.git"
git clone -q "$WORK/rp-origin.git" "$WORK/rp-seed" 2>/dev/null
mkdir -p "$WORK/rp-seed/Formula" "$WORK/rp-seed/bucket"
touch "$WORK/rp-seed/Formula/.keep" "$WORK/rp-seed/bucket/.keep"
git -C "$WORK/rp-seed" add -A && git -C "$WORK/rp-seed" -c user.email=t@t -c user.name=t commit -q -m seed && git -C "$WORK/rp-seed" push -q origin HEAD
mkdir -p "$WORK/rp-dist"
printf '1111  agentbus-v1.0.0-macos-universal.tar.gz\n2222  agentbus-v1.0.0-linux-amd64.tar.gz\n3333  agentbus-v1.0.0-linux-arm64.tar.gz\n4444  agentbus-v1.0.0-windows-amd64.zip\n5555  agentbus-v1.0.0-windows-arm64.zip\n' > "$WORK/rp-dist/SHA256SUMS"
rerun_push_case() { # <function> <dir-var> <name>
    local fn="$1" var="$2" name="$3" clone="$WORK/rp-$3"
    git clone -q "$WORK/rp-origin.git" "$clone" 2>/dev/null
    printf '#!/bin/sh\nexit 1\n' > "$WORK/rp-origin.git/hooks/pre-receive"; chmod +x "$WORK/rp-origin.git/hooks/pre-receive"
    local run="source '$HERE/release.sh'; REPO_ROOT='$HERE/..'; DIST='$WORK/rp-dist'; TAG=v1.0.0; VERSION=1.0.0; $var='$clone'; export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t; $fn"
    check "$name-first-push-fails" 1 "" -- bash -c "$run"
    check "$name-commit-stranded" 0 "" -- bash -c "[[ \$(git -C '$clone' rev-list --count @{u}..HEAD) == 1 ]]"
    rm -f "$WORK/rp-origin.git/hooks/pre-receive"
    check "$name-rerun-pushes" 0 "" -- bash -c "$run"
    check "$name-pushed" 0 "" -- bash -c "[[ \$(git -C '$clone' rev-list --count @{u}..HEAD) == 0 && \$(git -C '$WORK/rp-origin.git' log --oneline | wc -l) -ge 2 ]]"
}
rerun_push_case update_tap TAP_DIR tap
rerun_push_case update_scoop SCOOP_DIR scoop

# --- check-pins.sh ----------------------------------------------------------
SHA=3d3c42e5aac5ba805825da76410c181273ba90b1
pin_fixture() { # <name> <uses-line>
    mkdir -p "$WORK/pins-$1"; printf 'jobs:\n  j:\n    steps:\n      - %s\n' "$2" > "$WORK/pins-$1/w.yml"
}
pin_fixture ok "uses: actions/checkout@$SHA # v7.0.1"
pin_fixture local "uses: ./.github/actions/x"
pin_fixture v4 "uses: actions/checkout@v4"
pin_fixture main "uses: actions/checkout@main"
pin_fixture short "uses: actions/checkout@${SHA:0:7} # v7"
pin_fixture noref "uses: actions/checkout"
pin_fixture docker "uses: docker://alpine:3.20"
pin_fixture nocomment "uses: actions/checkout@$SHA"
pin_fixture flow-v4 "{ uses: actions/checkout@v4 }"
pin_fixture flow-ok "{ uses: actions/checkout@$SHA } # v7.0.1"
check pins-sha-comment-ok 0 "OK" -- "$HERE/check-pins.sh" "$WORK/pins-ok"
check pins-local-ok 0 "OK" -- "$HERE/check-pins.sh" "$WORK/pins-local"
check pins-flow-map-ok 0 "OK" -- "$HERE/check-pins.sh" "$WORK/pins-flow-ok"
for n in v4 main short noref docker nocomment flow-v4; do
    check "pins-$n-fails" 1 "w.yml:4" -- "$HERE/check-pins.sh" "$WORK/pins-$n"
done
# A grep that errors (exit 2) must fail the check, not read as "no uses: lines".
mkdir -p "$WORK/badgrep" && printf '#!/bin/sh\nexit 2\n' > "$WORK/badgrep/grep" && chmod +x "$WORK/badgrep/grep"
check pins-grep-error 1 "grep failed" -- env PATH="$WORK/badgrep:$PATH" "$HERE/check-pins.sh" "$WORK/pins-ok"
check pins-missing-dir 1 "not found" -- "$HERE/check-pins.sh" "$WORK/pins-nonexistent"
check pins-real-repo 0 "OK" -- "$HERE/check-pins.sh"

# --- Actions release flow (#36) --------------------------------------------
check removed-build-linux 1 "" -- bash -c "source '$HERE/release.sh'; declare -F build_linux"
check removed-smoke-linux 1 "" -- bash -c "source '$HERE/release.sh'; declare -F smoke_linux"
check removed-build-windows 1 "" -- bash -c "source '$HERE/release.sh'; declare -F build_windows"
check removed-check-pe 1 "" -- bash -c "source '$HERE/release.sh'; declare -F check_windows_pe"
check removed-ensure-release 1 "" -- bash -c "source '$HERE/release.sh'; declare -F ensure_release"
check no-docker-in-release-sh 1 "" -- grep -qi docker "$HERE/release.sh"
check args-no-publish-before 0 "" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish v1.2.3 && [[ \$NO_PUBLISH == 1 && \$TAG == v1.2.3 ]]"
check args-no-publish-after 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 --no-publish && [[ \$NO_PUBLISH == 1 ]]"
check args-default-publish 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 && [[ \$NO_PUBLISH == 0 ]]"
check args-no-publish-twice 2 "usage" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish --no-publish v1.2.3"
check args-unknown-flag 2 "unknown option --foo" -- bash -c "source '$HERE/release.sh'; parse_args --foo v1.2.3"
check args-flag-only 2 "usage" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish"
check args-flag-prerelease 2 "tag must look like vX.Y.Z" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish v1.2.3-rc.1"

# check_workflow_at_tag: v0.0.1 predates the workflow file; v0.0.92 has it.
mkdir -p "$WORK/repo/.github/workflows" && printf 'name: x\n' > "$WORK/repo/.github/workflows/release-build.yml"
git -C "$WORK/repo" add .github && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m wf && git -C "$WORK/repo" tag v0.0.92
check workflow-at-tag-ok 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.92; check_workflow_at_tag"
check workflow-at-tag-missing 1 "release-build.yml is not in tag v0.0.1" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; check_workflow_at_tag"

# A stub gh records its arguments and answers from recorded output. A
# "workflow run" swaps in $GH_RUNS_AFTER (when set) as the new run list, which
# models the run that appears after the dispatch.
cat > "$WORK/gh-stub.sh" <<'EOF'
gh() {
    printf '%s\n' "gh $*" >> "$GH_LOG"
    case "$1 $2" in
        "run list") local expr=""; while (($#)); do [[ "$1" == --jq ]] && expr="$2"; shift; done; jq -r "$expr" "$GH_RUNS" ;;
        "release view") [[ -n "${GH_VIEW:-}" ]] && { [[ -n "${GH_VIEW_STDERR:-}" ]] && echo "$GH_VIEW_STDERR" >&2; printf '%s\n' "$GH_VIEW"; return 0; }; echo "${GH_VIEW_ERR:-release not found}" >&2; return 1 ;;
        "workflow view") return "${GH_RC_WORKFLOW:-${GH_RC:-0}}" ;;
        "workflow run") [[ -n "${GH_RUNS_AFTER:-}" ]] && cp "$GH_RUNS_AFTER" "$GH_RUNS"; return "${GH_RC:-0}" ;;
        "release create"|"release edit"|"release download"|"run watch"|"auth status"|"release upload") return "${GH_RC:-0}" ;;
        "attestation verify") return "${GH_RC_ATTEST:-${GH_RC:-0}}" ;;
        *) echo "gh stub: unexpected call: $*" >&2; return 99 ;;
    esac
}
EOF
cat > "$WORK/runs.json" <<'EOF'
[
  {"databaseId": 101, "displayTitle": "release-build v1.2.3", "createdAt": "2026-10-07T10:00:00Z"},
  {"databaseId": 102, "displayTitle": "release-build v1.2.3", "createdAt": "2026-10-07T10:05:00Z"},
  {"databaseId": 103, "displayTitle": "release-build v9.9.9", "createdAt": "2026-10-07T10:06:00Z"}
]
EOF
# Two stale runs for the tag dated far in the future, so the since bound in
# dispatch_build (now minus a minute) cannot be what excludes them; the
# recorded pre-dispatch ids must.
cat > "$WORK/runs-stale.json" <<'EOF'
[
  {"databaseId": 201, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:00:00Z"},
  {"databaseId": 202, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:01:00Z"}
]
EOF
cat > "$WORK/runs-fresh.json" <<'EOF'
[
  {"databaseId": 201, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:00:00Z"},
  {"databaseId": 202, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:01:00Z"},
  {"databaseId": 203, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:02:00Z"}
]
EOF
with_stub() { bash -c "source '$HERE/release.sh'; source '$WORK/gh-stub.sh'; export GH_LOG='$WORK/gh.log' GH_RUNS='$WORK/runs.json' FIND_RUN_TRIES=1 FIND_RUN_SLEEP=0; TAG=v1.2.3; VERSION=1.2.3; DIST='$WORK/dist'; REPO_ROOT='$WORK/repo'; $1"; }
check find-run-newest 0 "102" -- with_stub "find_run 2026-10-07T09:00:00Z"
check find-run-since 0 "102" -- with_stub "find_run 2026-10-07T10:04:00Z"
check find-run-none 1 "could not find the dispatched run" -- with_stub "find_run 2026-10-07T10:30:00Z"
check find-run-other-tag-ignored 1 "could not find the dispatched run" -- with_stub "TAG=v9.9.8; find_run 2026-10-07T09:00:00Z"
check find-run-excludes-stale 0 "101" -- with_stub "find_run 2026-10-07T09:00:00Z 102"
check find-run-excludes-all-stale 1 "could not find the dispatched run" -- with_stub "find_run 2026-10-07T09:00:00Z 101 102"
check find-run-bad-exclude 1 "bad run id" -- with_stub "find_run 2026-10-07T09:00:00Z '1; drop'"
check list-runs-for-tag 0 "101 102" -- with_stub "echo \$(list_run_ids | sort)"
check list-runs-other-tag 0 "" -- with_stub "TAG=v8.8.8; [[ -z \$(list_run_ids) ]]"
check dispatch-ignores-stale-run 1 "could not find the dispatched run" -- with_stub "export GH_RUNS='$WORK/runs-stale.json'; dispatch_build"
check dispatch-adopts-new-run 0 "actions/runs/203" -- with_stub "cp '$WORK/runs-stale.json' '$WORK/runs-live.json'; export GH_RUNS='$WORK/runs-live.json' GH_RUNS_AFTER='$WORK/runs-fresh.json'; : > '$WORK/gh.log'; dispatch_build && [[ \$RUN_ID == 203 ]] && grep -q 'gh workflow run release-build.yml --repo ericfitz/agentbus --ref v1.2.3 -f tag=v1.2.3' '$WORK/gh.log'"
check dispatch-failure-stops 1 "could not dispatch release-build.yml on v1.2.3" -- with_stub "GH_RC=1 dispatch_build"
check draft-created 0 "Creating draft release" -- with_stub ": > '$WORK/gh.log'; ensure_draft && grep -q 'gh release create v1.2.3 .*--verify-tag.*--draft' '$WORK/gh.log'"
check draft-other-error-fails 1 "could not query release v1.2.3: HTTP 502" -- with_stub ": > '$WORK/gh.log'; GH_VIEW_ERR='HTTP 502: bad gateway' ensure_draft; rc=\$?; ! grep -q 'release create' '$WORK/gh.log' && exit \$rc"
check draft-reused 0 "Reusing draft release" -- with_stub "GH_VIEW=true ensure_draft && [[ \$PUBLISHED == 0 ]]"
check draft-reused-despite-stderr-noise 0 "Reusing draft release" -- with_stub "GH_VIEW=true GH_VIEW_STDERR='warning: x' ensure_draft && [[ \$PUBLISHED == 0 ]]"
check draft-published-despite-stderr-noise 0 "already published" -- with_stub "GH_VIEW=false GH_VIEW_STDERR='warning: x' ensure_draft && [[ \$PUBLISHED == 1 ]]"
check draft-garbage-stdout-fails 1 "unexpected output" -- with_stub ": > '$WORK/gh.log'; GH_VIEW=\$'true\nwarning: x' ensure_draft; rc=\$?; ! grep -q 'release create' '$WORK/gh.log' && exit \$rc"
check draft-reuse-no-create 0 "" -- with_stub ": > '$WORK/gh.log'; GH_VIEW=true ensure_draft; ! grep -q 'release create' '$WORK/gh.log'"
check draft-already-published 0 "already published" -- with_stub "GH_VIEW=false ensure_draft && [[ \$PUBLISHED == 1 ]]"
check watch-fails-with-url 1 "actions/runs/102" -- with_stub "RUN_ID=102 GH_RC=1 watch_run"
mkdir -p "$WORK/adist" && for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip; do : > "$WORK/adist/agentbus-v1.2.3-$a"; done
with_dist() { with_stub "DIST='$WORK/adist'; $1"; }
check download-requires-four 1 "agentbus-v1.2.3-windows-arm64.zip" -- with_dist "download_archives"
check download-failure-message 1 "could not download the archives of v1.2.3; rerun the workflow" -- with_dist "GH_RC=1 download_archives"
: > "$WORK/adist/agentbus-v1.2.3-windows-arm64.zip"
check download-ok 0 "" -- with_dist "download_archives && [[ \${#ARCHIVES[@]} == 4 ]]"
check download-keeps-macos-first 0 "" -- with_dist "ARCHIVES=(agentbus-v1.2.3-macos-universal.tar.gz); download_archives && [[ \${#ARCHIVES[@]} == 5 && \${ARCHIVES[0]} == agentbus-v1.2.3-macos-universal.tar.gz ]]"
check attest-ok 0 "" -- with_dist ": > '$WORK/gh.log'; download_archives && verify_attestations && [[ \$(grep -c 'gh attestation verify' '$WORK/gh.log') == 4 ]] && grep -q -- '--repo ericfitz/agentbus' '$WORK/gh.log' && [[ \$(grep -c -- '--signer-workflow ericfitz/agentbus/.github/workflows/release-build.yml --source-ref refs/tags/v1.2.3' '$WORK/gh.log') == 4 ]]"
check attest-fails 1 "attestation" -- with_dist "download_archives; GH_RC=1 verify_attestations"
check attest-skips-macos 0 "" -- with_dist ": > '$WORK/gh.log'; MACOS_ARCHIVE=agentbus-v1.2.3-macos-universal.tar.gz; ARCHIVES=(\$MACOS_ARCHIVE); download_archives; verify_attestations && ! grep -q macos '$WORK/gh.log' && [[ \$(grep -c 'gh attestation verify' '$WORK/gh.log') == 4 ]]"
check no-publish-stops 0 "stopping before publish" -- with_stub ": > '$WORK/gh.log'; update_tap() { echo TAP; }; NO_PUBLISH=1 publish_release && ! grep -q 'release edit' '$WORK/gh.log'"
check no-publish-says-draft 0 "draft: https://github.com/ericfitz/agentbus/releases/tag/v1.2.3" -- with_stub "NO_PUBLISH=1 publish_release"
check no-publish-rerun-says-release 0 "release: https://github.com/ericfitz/agentbus/releases/tag/v1.2.3" -- with_stub "NO_PUBLISH=1 PUBLISHED=1 publish_release"
check no-publish-skips-packagers 0 "" -- with_stub "update_tap() { echo TAP; }; update_scoop() { echo SCOOP; }; submit_winget() { echo WINGET; }; [[ -z \$(NO_PUBLISH=1 publish_release | grep -E '^(TAP|SCOOP|WINGET)\$') ]]"
check publish-undrafts-then-packagers 0 "TAP SCOOP WINGET" -- with_stub ": > '$WORK/gh.log'; update_tap() { echo TAP >> '$WORK/order'; }; update_scoop() { echo SCOOP >> '$WORK/order'; }; submit_winget() { echo WINGET >> '$WORK/order'; }; : > '$WORK/order'; publish_release >/dev/null && grep -q 'gh release edit v1.2.3 --repo ericfitz/agentbus --draft=false' '$WORK/gh.log' && echo \$(cat '$WORK/order')"
check publish-rerun-skips-edit 0 "" -- with_stub ": > '$WORK/gh.log'; update_tap() { :; }; update_scoop() { :; }; submit_winget() { :; }; PUBLISHED=1 publish_release >/dev/null && ! grep -q 'release edit' '$WORK/gh.log'"
check publish-edit-failure-stops 1 "could not publish v1.2.3; the draft stays" -- with_stub "update_tap() { echo TAP; }; GH_RC=1 publish_release"
check upload-assets 0 "" -- with_stub ": > '$WORK/gh.log'; mkdir -p '$WORK/udist'; MACOS_ARCHIVE=agentbus-v1.2.3-macos-universal.tar.gz; DIST='$WORK/udist'; upload_assets && grep -q 'gh release upload v1.2.3 --repo ericfitz/agentbus --clobber agentbus-v1.2.3-macos-universal.tar.gz install.sh install.ps1 SHA256SUMS SHA256SUMS.sig' '$WORK/gh.log'"
check gh-auth-fails 1 "gh is not authenticated" -- with_stub "GH_RC=1 check_gh"
check gh-attest-missing 1 "cannot verify attestations" -- with_stub "GH_RC_ATTEST=1 check_gh"
check gh-workflow-not-on-default 1 "release-build.yml is not on the default branch of ericfitz/agentbus; merge it to main first" -- with_stub "GH_RC_WORKFLOW=1 check_gh"
check gh-ok 0 "" -- with_stub "check_gh"
# main runs the steps in the order the spec pins; every step is replaced by a
# recorder so nothing builds, dispatches or publishes.
check main-order 0 "" -- bash -c "source '$HERE/release.sh'; : > '$WORK/steps'
for f in require_tools check_signing_key check_tag_exists check_tag_pushed check_gh check_workflow_at_tag check_installers_embedded checkout_tag build_macos ensure_draft dispatch_build watch_run download_archives verify_attestations copy_scripts write_sums sign_sums upload_assets publish_release; do eval \"\$f() { echo \$f >> '$WORK/steps'; }\"; done
main v1.2.3 --no-publish && diff '$WORK/steps' <(printf '%s\n' require_tools check_signing_key check_tag_exists check_tag_pushed check_gh check_workflow_at_tag check_installers_embedded checkout_tag build_macos ensure_draft dispatch_build watch_run download_archives verify_attestations copy_scripts write_sums sign_sums upload_assets publish_release)"

echo "test-release: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
