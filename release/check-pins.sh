#!/usr/bin/env bash
# Fails unless every `uses:` in the workflows is pinned to a full 40-hex commit
# SHA followed by a version comment (`@<sha> # v1.2.3`). Local `uses: ./...`
# actions pass. docker:// refs and anything else fail.
#   release/check-pins.sh [workflows-dir]    (default: .github/workflows)
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIR="${1:-$HERE/../.github/workflows}"
[[ -d "$DIR" ]] || { echo "error: workflows dir not found: $DIR" >&2; exit 1; }
shopt -s nullglob
files=("$DIR"/*.yml "$DIR"/*.yaml)
[[ ${#files[@]} -gt 0 ]] || { echo "error: no workflow files in $DIR" >&2; exit 1; }

rc=0
hits="$(rg -nH --no-heading '^\s*(-\s+)?uses:' "${files[@]}")" || rc=$?
case "$rc" in
    0) ;;
    1) echo "check-pins: no uses: lines in $DIR"; exit 0 ;;
    *) echo "error: rg failed (exit $rc) reading $DIR" >&2; exit 1 ;;
esac

bad=0
pin='^[^:]+:[0-9]+:[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]+[^[:space:]@]+@[0-9a-f]{40}[[:space:]]+#[[:space:]]*v[0-9]'
local_ref='^[^:]+:[0-9]+:[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]+\./'
while IFS= read -r line; do
    if [[ "$line" =~ $local_ref ]] || [[ "$line" =~ $pin ]]; then continue; fi
    echo "error: unpinned action (want @<40-hex sha> # vX.Y.Z): $line" >&2
    bad=1
done <<<"$hits"
[[ "$bad" -eq 0 ]] || exit 1
echo "check-pins: OK"
