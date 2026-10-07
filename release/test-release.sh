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
check preflight-embedded 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.4; check_installers_embedded"
check preflight-no-installer 1 "v0.0.1 has no install.sh; the installer ships with every release, so tag a commit that includes it" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; check_installers_embedded"

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

echo "test-release: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
