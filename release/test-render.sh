#!/usr/bin/env bash
# Renders every release template with fake values, asserts no __PLACEHOLDER__
# survives, and syntax-checks the result. No network. When brew is installed it
# also runs `brew style` and a stubbed OS/CPU URL-selection check on the formula.
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

H64() { printf "$1%.0s" {1..64}; }
render_check "$HERE/agentbus.rb.tmpl" agentbus.rb ruby -c -- \
    MACOS_URL=https://example.invalid/agentbus-v1.2.3-macos-universal.tar.gz MACOS_SHA256="$(H64 1)" \
    LINUX_AMD64_URL=https://example.invalid/agentbus-v1.2.3-linux-amd64.tar.gz LINUX_AMD64_SHA256="$(H64 2)" \
    LINUX_ARM64_URL=https://example.invalid/agentbus-v1.2.3-linux-arm64.tar.gz LINUX_ARM64_SHA256="$(H64 3)"
for want in 'elsif OS.linux? && Hardware::CPU.intel?' 'elsif OS.linux? && Hardware::CPU.arm?' \
    'macos-universal.tar.gz' 'linux-amd64.tar.gz' 'linux-arm64.tar.gz'; do
    grep -qF "$want" "$WORK/agentbus.rb" || { echo "FAIL formula lacks: $want"; FAIL=1; }
done
# on_macos/on_linux blocks may not contain url/sha256 (brew style rejects them).
if grep -qE '^[[:space:]]*on_(macos|linux|intel|arm)[[:space:]]+do' "$WORK/agentbus.rb"; then echo "FAIL formula uses on_* blocks for url"; FAIL=1; fi
if grep -q 'depends_on :macos' "$WORK/agentbus.rb"; then echo "FAIL formula still macOS only"; FAIL=1; fi

# Optional, offline: brew style must report no offenses. brew only treats the
# file as a formula under a Formula/ directory.
if command -v brew >/dev/null 2>&1; then
    mkdir -p "$WORK/Formula" && cp "$WORK/agentbus.rb" "$WORK/Formula/agentbus.rb"
    if brew style "$WORK/Formula/agentbus.rb" >"$WORK/style.out" 2>&1; then echo "PASS brew style"; else echo "FAIL brew style"; cat "$WORK/style.out"; FAIL=1; fi
    # Host-conditional URL selection with OS/CPU stubbed (SimulateSystem cannot
    # see host conditionals, so redefine the predicates themselves).
    cat > "$WORK/select.rb" <<'RB'
file, os, cpu = ARGV
mac = os == "mac"
arm = cpu == "arm"
OS.define_singleton_method(:mac?) { mac }
OS.define_singleton_method(:linux?) { !mac }
Hardware::CPU.define_singleton_method(:arm?) { arm }
Hardware::CPU.define_singleton_method(:intel?) { !arm }
require "formula"
puts Formulary.from_contents("agentbus", Pathname(file), File.read(file)).stable.url
RB
    for combo in "mac arm macos-universal" "mac intel macos-universal" "linux intel linux-amd64" "linux arm linux-arm64"; do
        read -r -a c <<<"$combo"
        got="$(brew ruby "$WORK/select.rb" "$WORK/Formula/agentbus.rb" "${c[0]}" "${c[1]}" 2>&1 | tail -1)"
        if [[ "$got" == *"-${c[2]}.tar.gz" ]]; then echo "PASS url select ${c[0]}/${c[1]}"; else echo "FAIL url select ${c[0]}/${c[1]}: $got"; FAIL=1; fi
    done
else
    echo "SKIP brew checks (brew not installed)"
fi

render_check "$HERE/agentbus.scoop.json.tmpl" agentbus.json python3 -I -m json.tool -- \
    VERSION=1.2.3 AMD64_URL=https://example.invalid/a.zip AMD64_SHA256=aaaa ARM64_URL=https://example.invalid/r.zip ARM64_SHA256=bbbb

# A template with an extra placeholder must fail (render_template's guard).
printf 'x __UNFILLED__\n' > "$WORK/bad.tmpl"
if (render_template "$WORK/bad.tmpl" "$WORK/bad.out" A=1) 2>/dev/null; then echo "FAIL unfilled placeholder accepted"; FAIL=1; else echo "PASS unfilled placeholder rejected"; fi

[[ "$FAIL" -eq 0 ]] && echo "test-render: OK"
exit "$FAIL"
