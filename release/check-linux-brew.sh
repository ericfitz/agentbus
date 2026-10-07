#!/usr/bin/env bash
# After a release: install the published formula with Homebrew on Linux in
# Docker and run its test block, for each architecture given (default both).
#   release/check-linux-brew.sh            # amd64 and arm64
#   release/check-linux-brew.sh arm64
# Needs Docker and network access to GitHub. Read-only: nothing is published.
set -euo pipefail
ARCHES=("$@"); (( ${#ARCHES[@]} )) || ARCHES=(amd64 arm64)
for arch in "${ARCHES[@]}"; do
    case "$arch" in amd64 | arm64) ;; *) echo "error: unknown architecture '$arch' (amd64 or arm64)" >&2; exit 2 ;; esac
done
command -v docker >/dev/null || { echo "error: docker is required (Docker Desktop)" >&2; exit 1; }
status=0
for arch in "${ARCHES[@]}"; do
    echo "==> Homebrew on linux/$arch"
    if docker run --rm --platform "linux/$arch" homebrew/brew:latest bash -c \
        'brew install ericfitz/tap/agentbus && brew test agentbus && agentbus version'; then
        echo "linux/$arch: OK"
    else
        echo "linux/$arch: FAILED"; status=1
    fi
done
exit "$status"
