#!/usr/bin/env bash
# Renders every release template with fake values, asserts no __PLACEHOLDER__
# survives, and syntax-checks the result. No network.
#   release/test-render.sh
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# shellcheck source=release/release.sh
source "$HERE/release.sh"
FAIL=0

# render_check <template> <out-name> <syntax-check command...> -- KEY=VALUE...
# Each render runs in a subshell: render_template exits on an unfilled
# placeholder, and one bad template must not abort the checks after it.
render_check() {
    local tmpl="$1" out="$WORK/$2"; shift 2
    local syntax=(); while [[ "$1" != "--" ]]; do syntax+=("$1"); shift; done; shift
    if (render_template "$tmpl" "$out" "$@" && "${syntax[@]}" "$out" >/dev/null 2>&1); then
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
if grep -q 'depends_on :macos' "$WORK/agentbus.rb"; then echo "FAIL formula still macOS only"; FAIL=1; fi

# A template with an extra placeholder must fail (render_template's guard).
printf 'x __UNFILLED__\n' > "$WORK/bad.tmpl"
if (render_template "$WORK/bad.tmpl" "$WORK/bad.out" A=1) 2>/dev/null; then echo "FAIL unfilled placeholder accepted"; FAIL=1; else echo "PASS unfilled placeholder rejected"; fi

[[ "$FAIL" -eq 0 ]] && echo "test-render: OK"
exit "$FAIL"
