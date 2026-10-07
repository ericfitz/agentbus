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

echo "test-release: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
