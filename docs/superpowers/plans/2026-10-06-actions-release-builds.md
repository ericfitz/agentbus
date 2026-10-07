# Actions Release Builds Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the four non-macOS release binaries on native GitHub runners, attest them (Sigstore build provenance), and have `release.sh` drive a draft release through that workflow before signing `SHA256SUMS` and publishing.

**Architecture:** A new `workflow_dispatch` workflow `release-build.yml` validates the tag, builds and runs each binary on a runner of its own OS/arch, and in a `publish` job attests the four archives and uploads them to the draft release. `release.sh` loses its local Linux/Windows builds (`build_linux`, `smoke_linux`, `build_windows`, `check_windows_pe`) and gains draft handling, dispatch, run discovery, `gh run watch`, `gh attestation verify`, and a `--no-publish` dry-run flag. Every piece of `release.sh` that does not need GitHub is unit-tested by sourcing it with a stub `gh` function.

**Tech Stack:** GitHub Actions (SHA-pinned actions; `ubuntu-latest`, `ubuntu-24.04-arm`, `windows-latest`, `windows-11-arm`), `gh` 2.102 (`gh attestation verify`), `actionlint`, bash release tooling from #34/#35.

**Spec:** `docs/superpowers/specs/2026-10-06-actions-release-builds-design.md`

## Global Constraints

- **Release notes (orchestrator override, 2026-10-07):** release notes files are written in the `chore: release vX.Y.Z` commit at release time (`git log -- release/notes-v1.13.0.md`), never in a feature commit. Wherever a task below says to create, append to or `git add` `release/notes-v1.14.0.md` (or any `release/notes-*.md`), do not touch the file: put that text in the execution's final report for the release author instead, and drop the path from the commit.
- Depends on #34 and #35 being merged: this plan edits `release/release.sh` functions by name (`parse_args`, `require_tools`, `main`, `ensure_release`, `upload_assets`, `write_sums`, `copy_scripts`) and removes `build_linux`, `smoke_linux` (#34) and `build_windows`, `check_windows_pe` (#35). It keeps `update_tap` (#34), `update_scoop`, `submit_winget` (#35). #37 later edits this plan's workflow by step `id` and adds `--windows-signing` next to `--no-publish` in `parse_args`.
- Workflow: `workflow_dispatch` with required `tag` input; `run-name: release-build ${{ inputs.tag }}`; tag must match `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`, exist, and equal `github.ref_name`; `concurrency: group: release-${{ inputs.tag }}`, `cancel-in-progress: false`; `build` has `permissions: contents: read`; only `publish` has `contents: write, id-token: write, attestations: write`; every action pinned to a full commit SHA with the version in a comment; no secrets beyond `GITHUB_TOKEN`.
- Build command on every runner, verbatim: `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/ericfitz/agentbus/internal/mcpserver.Version=<version>"`; the built binary's `agentbus version` must equal the version (tag without `v`).
- Archive names unchanged: `agentbus-<tag>-linux-amd64.tar.gz`, `agentbus-<tag>-linux-arm64.tar.gz`, `agentbus-<tag>-windows-amd64.zip`, `agentbus-<tag>-windows-arm64.zip`. Artifact names: `archive-<goos>-<goarch>`.
- `release.sh` order: preflight (incl. `gh auth status`, workflow present at the tag) → macOS build/sign/notarize → draft release (create or reuse) → dispatch with `--ref <tag>` → watch → download the four archives → `gh attestation verify <file> --repo ericfitz/agentbus` each → `SHA256SUMS` over the macOS tarball, the four archives, `install.sh`, `install.ps1` → sign and verify → upload `SHA256SUMS`, `SHA256SUMS.sig`, both scripts and the macOS tarball with `--clobber` → publish (`gh release edit --draft=false`) → Homebrew, Scoop, winget. `--no-publish` stops before the publish step.
- Rerun semantics: draft reused; already-published release reused after a warning; `dist/` recreated per run.
- Action pins (looked up 2026-10-07): `actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1`, `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0`, `actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1`, `actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1`, `actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8 # v4.2.2`.
- No step pushes, tags, dispatches a workflow, creates or deletes releases; those are user instructions with an `Approve:` line.
- Bash with `set -euo pipefail`; executor searches use `rg PATTERN <path>`; American English.

## Review Focus

- A tag that exists locally but was never pushed: the dispatch would fail with an opaque `gh` error after the macOS build. Preflight must check `git ls-remote --exit-code --tags origin refs/tags/<tag>`. Pinned in Task 2 (`check_tag_pushed`).
- Two runs of `release-build.yml` for the same tag within one minute (a rerun after a transient failure): `find_run` must pick the newest run created after the dispatch, never an older one. Pinned in Task 2 (`find_run` with recorded `gh run list` output).
- `gh release download` that finds only three of four archives (the workflow's `publish` job was interrupted after one upload): the release must stop before writing `SHA256SUMS`. Pinned in Task 2 (`download_archives` requires all four names).
- A run dispatched with `--ref main` by hand: the workflow must refuse (`github.ref_name != tag`) rather than build from `main`. Pinned in Task 1's validate step (verified with `actionlint` for syntax; behavior exercised in the dry run).
- `--no-publish` given twice or combined with an unknown flag: usage error, not a silent ignore. Pinned in Task 2 (`parse_args`).

---

### Task 1: `release-build.yml` and the pin check

**Files:**
- Create: `.github/workflows/release-build.yml`
- Modify: `Makefile` (`release-check`)

**Interfaces:**
- Produces: workflow `release-build.yml`, jobs `validate` (output `version`), `build` (matrix; step ids `build`, `package-linux`, `package-windows-zip`, `upload`), `publish` (step ids `download`, `attest`, `upload-release`). #37 replaces `package-windows-zip`/`upload` for the Windows matrix entries, adds `package-windows`, and makes `publish` need it.

- [ ] **Step 1: Write the workflow**

```yaml
# Builds the Linux and Windows release archives on runners of their own OS
# and architecture, runs each binary, attests build provenance, and uploads
# the archives to the draft release for the tag. Dispatched by
# release/release.sh on the tag itself (gh workflow run --ref <tag>), so the
# checked-out source and this file are the tag's. macOS is built locally.
name: release-build
run-name: release-build ${{ inputs.tag }}
on:
  workflow_dispatch:
    inputs:
      tag:
        description: Release tag (vX.Y.Z); dispatch on that tag with --ref
        required: true
        type: string
permissions:
  contents: read
concurrency:
  group: release-${{ inputs.tag }}
  cancel-in-progress: false
jobs:
  validate:
    runs-on: ubuntu-latest
    outputs:
      version: ${{ steps.check.outputs.version }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-tags: true
      - id: check
        shell: bash
        env:
          TAG: ${{ inputs.tag }}
        run: |
          set -euo pipefail
          [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "::error::tag '$TAG' must look like v1.2.3 or v1.2.3-rc.1"; exit 1; }
          git rev-parse -q --verify "refs/tags/$TAG" >/dev/null || { echo "::error::tag $TAG not found in the repository"; exit 1; }
          [[ "$GITHUB_REF_NAME" == "$TAG" ]] || { echo "::error::dispatched on '$GITHUB_REF_NAME', not on tag $TAG; use: gh workflow run release-build.yml --ref $TAG -f tag=$TAG"; exit 1; }
          echo "version=${TAG#v}" >> "$GITHUB_OUTPUT"
  build:
    needs: validate
    permissions:
      contents: read
    strategy:
      fail-fast: true
      matrix:
        include:
          - { runner: ubuntu-latest, goos: linux, goarch: amd64 }
          - { runner: ubuntu-24.04-arm, goos: linux, goarch: arm64 }
          - { runner: windows-latest, goos: windows, goarch: amd64 }
          - { runner: windows-11-arm, goos: windows, goarch: arm64 }
    runs-on: ${{ matrix.runner }}
    env:
      TAG: ${{ inputs.tag }}
      VERSION: ${{ needs.validate.outputs.version }}
      GOOS: ${{ matrix.goos }}
      GOARCH: ${{ matrix.goarch }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod
      - id: build
        name: Build and run
        shell: bash
        run: |
          set -euo pipefail
          ext=""; [[ "$GOOS" == windows ]] && ext=".exe"
          CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/ericfitz/agentbus/internal/mcpserver.Version=$VERSION" -o "out/agentbus$ext" .
          got="$("./out/agentbus$ext" version)"
          [[ "$got" == "$VERSION" ]] || { echo "::error::agentbus version printed '$got', want $VERSION"; exit 1; }
      - id: package-linux
        if: matrix.goos == 'linux'
        name: Package tar.gz
        shell: bash
        run: tar -C out -czf "agentbus-$TAG-linux-$GOARCH.tar.gz" agentbus
      - id: package-windows-zip
        if: matrix.goos == 'windows'
        name: Package zip
        shell: pwsh
        run: Compress-Archive -LiteralPath out\agentbus.exe -DestinationPath "agentbus-$env:TAG-windows-$env:GOARCH.zip"
      - id: upload
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: archive-${{ matrix.goos }}-${{ matrix.goarch }}
          path: agentbus-${{ inputs.tag }}-${{ matrix.goos }}-${{ matrix.goarch }}.*
          if-no-files-found: error
  publish:
    needs: [validate, build]
    runs-on: ubuntu-latest
    permissions:
      contents: write
      id-token: write
      attestations: write
    env:
      TAG: ${{ inputs.tag }}
    steps:
      - id: download
        uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1
        with:
          pattern: archive-*
          merge-multiple: true
          path: archives
      - name: Require all four archives
        shell: bash
        run: |
          set -euo pipefail
          ls -l archives
          for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip windows-arm64.zip; do
            [[ -f "archives/agentbus-$TAG-$a" ]] || { echo "::error::missing archives/agentbus-$TAG-$a"; exit 1; }
          done
      - id: attest
        uses: actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8 # v4.2.2
        with:
          subject-path: archives/*
      - id: upload-release
        name: Upload to the draft release
        shell: bash
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh release upload "$TAG" archives/* --clobber --repo "$GITHUB_REPOSITORY"
```

- [ ] **Step 2: Add the pin check to `make release-check`.** After the `actionlint` line (from #35) add:

```make
	@if rg -n 'uses: .*@v[0-9]' .github/workflows/; then echo "error: unpinned action (use a commit SHA with the version in a comment)"; exit 1; fi
```

- [ ] **Step 3: Lint**

Run: `cd <repo root> && actionlint .github/workflows/release-build.yml && make release-check 2>&1 | tail -5`
Expected: actionlint silent (it validates the runner labels, the `needs` graph, the `${{ }}` expressions and the `shell: pwsh` steps); `release-check` passes.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/release-build.yml Makefile
git commit -m "ci: release-build.yml builds and attests Linux and Windows archives"
```

---

### Task 2: `release.sh` drives the draft release through Actions

**Files:**
- Modify: `release/release.sh`, `release/test-release.sh`

**Interfaces:**
- Produces: globals `NO_PUBLISH` (0/1), `PUBLISHED` (0/1), `RUN_ID`, `MACOS_ARCHIVE`, `FIND_RUN_TRIES` (default 30), `FIND_RUN_SLEEP` (default 2), `WORKFLOW=release-build.yml`; functions `check_tag_pushed`, `check_gh`, `check_workflow_at_tag`, `ensure_draft`, `dispatch_build`, `find_run <since>`, `watch_run`, `download_archives`, `verify_attestations`, `publish_release`. `parse_args` accepts `--no-publish` before or after the tag. Removed: `build_linux`, `smoke_linux`, `build_windows`, `check_windows_pe`, `ensure_release`; `require_tools` no longer needs `docker`, `file`, `zip`, `lipo`-unrelated tools stay. #37 adds `--windows-signing` to `parse_args` and `-f windows_signing=...` to `dispatch_build`.

- [ ] **Step 1: Failing tests.** In `release/test-release.sh`, delete the `pe-ok` and `pe-wrong` cases and the `$WORK/pe` build (from #35), and append before the summary:

```bash
# --- Actions release flow (#36) --------------------------------------------
check removed-build-linux 1 "" -- bash -c "source '$HERE/release.sh'; declare -F build_linux"
check removed-smoke-linux 1 "" -- bash -c "source '$HERE/release.sh'; declare -F smoke_linux"
check removed-build-windows 1 "" -- bash -c "source '$HERE/release.sh'; declare -F build_windows"
check removed-check-pe 1 "" -- bash -c "source '$HERE/release.sh'; declare -F check_windows_pe"
check removed-ensure-release 1 "" -- bash -c "source '$HERE/release.sh'; declare -F ensure_release"
check args-no-publish-before 0 "" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish v1.2.3 && [[ \$NO_PUBLISH == 1 && \$TAG == v1.2.3 ]]"
check args-no-publish-after 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 --no-publish && [[ \$NO_PUBLISH == 1 ]]"
check args-default-publish 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 && [[ \$NO_PUBLISH == 0 ]]"
check args-no-publish-twice 2 "usage" -- parse_args --no-publish --no-publish v1.2.3
check args-unknown-flag 2 "unknown option --foo" -- parse_args --foo v1.2.3
check args-flag-only 2 "usage" -- parse_args --no-publish

# check_workflow_at_tag: v0.0.1 predates the workflow file; v0.0.3 has it.
mkdir -p "$WORK/repo/.github/workflows" && printf 'name: x\n' > "$WORK/repo/.github/workflows/release-build.yml"
git -C "$WORK/repo" add .github && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m wf && git -C "$WORK/repo" tag v0.0.3
check workflow-at-tag-ok 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.3; check_workflow_at_tag"
check workflow-at-tag-missing 1 "release-build.yml is not in tag v0.0.1" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; check_workflow_at_tag"

# check_tag_pushed against a local bare "origin".
git clone -q --bare "$WORK/repo" "$WORK/origin.git" && git -C "$WORK/repo" remote add origin "$WORK/origin.git" 2>/dev/null || true
git -C "$WORK/repo" tag v0.0.4
check tag-pushed-ok 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.3; check_tag_pushed"
check tag-pushed-missing 1 "tag v0.0.4 is not on origin" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.4; check_tag_pushed"

# A stub gh records its arguments and answers from recorded output.
cat > "$WORK/gh-stub.sh" <<'EOF'
gh() {
    printf '%s\n' "gh $*" >> "$GH_LOG"
    case "$1 $2" in
        "run list") local expr=""; while (($#)); do [[ "$1" == --jq ]] && expr="$2"; shift; done; jq -r "$expr" "$GH_RUNS" ;;
        "release view") [[ -n "${GH_VIEW:-}" ]] && { printf '%s\n' "$GH_VIEW"; return 0; }; return 1 ;;
        "release create"|"release edit"|"release download"|"workflow run"|"run watch"|"attestation verify") return "${GH_RC:-0}" ;;
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
with_stub() { bash -c "source '$HERE/release.sh'; source '$WORK/gh-stub.sh'; export GH_LOG='$WORK/gh.log' GH_RUNS='$WORK/runs.json' FIND_RUN_TRIES=1 FIND_RUN_SLEEP=0; TAG=v1.2.3; VERSION=1.2.3; DIST='$WORK/dist'; REPO_ROOT='$WORK/repo'; $1"; }
check find-run-newest 0 "102" -- with_stub "find_run 2026-10-07T09:00:00Z"
check find-run-since 0 "102" -- with_stub "find_run 2026-10-07T10:04:00Z"
check find-run-none 1 "could not find the dispatched run" -- with_stub "find_run 2026-10-07T10:30:00Z"
check draft-created 0 "Creating draft release" -- with_stub ": > '$WORK/gh.log'; ensure_draft && grep -q 'gh release create v1.2.3 .*--draft' '$WORK/gh.log'"
check draft-reused 0 "Reusing draft release" -- with_stub "GH_VIEW=true ensure_draft && [[ \$PUBLISHED == 0 ]]"
check draft-already-published 0 "already published" -- with_stub "GH_VIEW=false ensure_draft && [[ \$PUBLISHED == 1 ]]"
check watch-fails-with-url 1 "actions/runs/102" -- with_stub "RUN_ID=102 GH_RC=1 watch_run"
mkdir -p "$WORK/dist" && for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip; do : > "$WORK/dist/agentbus-v1.2.3-$a"; done
check download-requires-four 1 "agentbus-v1.2.3-windows-arm64.zip" -- with_stub "download_archives"
: > "$WORK/dist/agentbus-v1.2.3-windows-arm64.zip"
check download-ok 0 "" -- with_stub "download_archives && [[ \${#ARCHIVES[@]} == 4 ]]"
check attest-fails 1 "attestation" -- with_stub "download_archives; GH_RC=1 verify_attestations"
check no-publish-stops 0 "stopping before publish" -- with_stub ": > '$WORK/gh.log'; NO_PUBLISH=1 publish_release && ! grep -q 'release edit' '$WORK/gh.log'"
```

- [ ] **Step 2: Run to verify it fails**

Run: `release/test-release.sh`
Expected: `removed-*` FAIL (functions still exist), `args-no-publish-*` FAIL, `workflow-at-tag-*`, `tag-pushed-*`, `find-run-*`, `draft-*`, `watch-*`, `download-*`, `attest-*`, `no-publish-*` FAIL (undefined).

- [ ] **Step 3: Rewrite the affected parts of `release/release.sh`.** Header comment becomes:

```bash
# Build, sign and publish a release of agentbus, then update Homebrew, Scoop
# and winget. macOS is built, codesigned and notarized here; the Linux and
# Windows archives are built, run and attested by .github/workflows/
# release-build.yml on native runners, dispatched on the tag. Every archive
# is listed in SHA256SUMS, signed with the Ed25519 release key.
#   ./release/release.sh v0.1.1              # full release
#   ./release/release.sh v0.1.1 --no-publish # stop with a draft release (dry run)
# Reruns for the same tag reuse the draft release and replace every asset.
```

Globals: add after `ARCHIVES=()`:

```bash
MACOS_ARCHIVE=""   # file name of the macOS tarball under $DIST
NO_PUBLISH=0       # --no-publish: stop after signing, leave the draft
PUBLISHED=0        # set when the release for $TAG is already public (rerun)
RUN_ID=""          # the release-build.yml run this release used
WORKFLOW="release-build.yml"
FIND_RUN_TRIES="${FIND_RUN_TRIES:-30}"
FIND_RUN_SLEEP="${FIND_RUN_SLEEP:-2}"
```

`usage` and `parse_args`:

```bash
usage() { echo "usage: release.sh <tag> [--no-publish]" >&2; exit 2; }

parse_args() {
    local a
    for a in "$@"; do
        case "$a" in
            --no-publish) [[ "$NO_PUBLISH" == 0 ]] || usage; NO_PUBLISH=1 ;;
            --*) echo "error: unknown option $a" >&2; usage ;;
            *) [[ -z "$TAG" ]] || usage; TAG="$a" ;;
        esac
    done
    [[ -n "$TAG" ]] || usage
    [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] \
        || { echo "error: tag must look like v1.2.3 (or v1.2.3-rc.1), got '$TAG'" >&2; exit 2; }
    VERSION="${TAG#v}"
}
```

`require_tools`: the tool loop becomes `for t in gh git go lipo codesign xcrun shasum komac; do` (no `docker`, `file`, `zip`); remove the `docker info` line. Delete the functions `build_linux`, `smoke_linux`, `build_windows`, `check_windows_pe` and `ensure_release`. In `build_macos`, after `ARCHIVES+=("$archive")` add `MACOS_ARCHIVE="$archive"`. Add after `check_tag_exists`:

```bash
check_tag_pushed() {
    git -C "$REPO_ROOT" ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1 \
        || fail "tag $TAG is not on origin; push it first (git push origin $TAG)"
}

check_gh() {
    gh auth status >/dev/null 2>&1 || fail "gh is not authenticated (gh auth login)"
    gh attestation verify --help >/dev/null 2>&1 || fail "this gh cannot verify attestations; upgrade gh (brew upgrade gh)"
}

# A tag cut before the workflow landed cannot use this flow: the run would
# use the workflow file as of the tag.
check_workflow_at_tag() {
    git -C "$REPO_ROOT" cat-file -e "$TAG:.github/workflows/$WORKFLOW" 2>/dev/null \
        || fail ".github/workflows/$WORKFLOW is not in tag $TAG; tag a commit that has it"
}
```

Replace `ensure_release` and add the Actions steps (after `release_notes_args`):

```bash
# ensure_draft creates the draft release for $TAG, reuses an existing draft
# (rerun after a failure), or, after a warning, reuses a published release
# (rerun that replaces public assets). releases/latest never resolves to a
# draft, so the install scripts cannot see a partial release.
ensure_draft() {
    local draft
    if draft="$(gh release view "$TAG" --repo "$GH_REPO" --json isDraft --jq .isDraft 2>/dev/null)"; then
        if [[ "$draft" == true ]]; then
            echo "==> Reusing draft release $TAG"
        else
            echo "warning: release $TAG is already published; continuing replaces its public assets" >&2
            PUBLISHED=1
        fi
        return
    fi
    echo "==> Creating draft release $TAG"
    local notes=(); while IFS= read -r line; do notes+=("$line"); done < <(release_notes_args)
    gh release create "$TAG" --repo "$GH_REPO" --title "$TAG" --draft "${notes[@]}"
}

# dispatch_build runs the workflow on the tag itself (its github.ref is the
# tag, and the workflow file used is the tag's) and records the run id.
dispatch_build() {
    echo "==> Dispatching $WORKFLOW on $TAG"
    local since; since="$(date -u -v-60S +%Y-%m-%dT%H:%M:%SZ)" # one minute of clock slack
    gh workflow run "$WORKFLOW" --repo "$GH_REPO" --ref "$TAG" -f "tag=$TAG"
    RUN_ID="$(find_run "$since")"
    echo "run: https://github.com/$GH_REPO/actions/runs/$RUN_ID"
}

# find_run <since>: the newest workflow_dispatch run of $WORKFLOW created at
# or after <since> (RFC 3339 UTC) whose display title is "release-build
# <tag>". Polls because the run appears a moment after the dispatch.
find_run() {
    local since="$1" id i
    for ((i = 0; i < FIND_RUN_TRIES; i++)); do
        id="$(gh run list --repo "$GH_REPO" --workflow "$WORKFLOW" --event workflow_dispatch --limit 20 \
            --json databaseId,displayTitle,createdAt \
            --jq "[.[] | select(.createdAt >= \"$since\" and .displayTitle == \"release-build $TAG\")] | sort_by(.createdAt) | last | .databaseId // empty")"
        if [[ -n "$id" ]]; then printf '%s\n' "$id"; return 0; fi
        sleep "$FIND_RUN_SLEEP"
    done
    fail "could not find the dispatched run of $WORKFLOW for $TAG"
}

watch_run() {
    echo "==> Waiting for run $RUN_ID"
    gh run watch "$RUN_ID" --repo "$GH_REPO" --exit-status \
        || fail "$WORKFLOW failed; the draft stays for a rerun: https://github.com/$GH_REPO/actions/runs/$RUN_ID"
}

# download_archives fetches the four attested archives the workflow uploaded
# to the draft and requires every one of them.
download_archives() {
    echo "==> Downloading the Linux and Windows archives"
    gh release download "$TAG" --repo "$GH_REPO" --dir "$DIST" --clobber \
        --pattern "${BIN_NAME}-${TAG}-linux-*.tar.gz" --pattern "${BIN_NAME}-${TAG}-windows-*.zip"
    local a
    for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip windows-arm64.zip; do
        [[ -f "$DIST/${BIN_NAME}-${TAG}-$a" ]] || fail "release $TAG lacks ${BIN_NAME}-${TAG}-$a; rerun the workflow"
        ARCHIVES+=("${BIN_NAME}-${TAG}-$a")
    done
}

# verify_attestations checks the Sigstore build provenance of every
# downloaded archive against this repository before anything is signed.
verify_attestations() {
    echo "==> Verifying build attestations"
    local a
    for a in "${ARCHIVES[@]}"; do
        [[ "$a" == "$MACOS_ARCHIVE" ]] && continue
        gh attestation verify "$DIST/$a" --repo "$GH_REPO" >/dev/null \
            || fail "attestation of $a does not verify against $GH_REPO"
    done
}

# publish_release makes the draft public and updates the package managers,
# or stops with the draft in place under --no-publish.
publish_release() {
    if [[ "$NO_PUBLISH" == 1 ]]; then
        echo "==> --no-publish: stopping before publish; draft: https://github.com/$GH_REPO/releases/tag/$TAG"
        return 0
    fi
    if [[ "$PUBLISHED" == 0 ]]; then
        echo "==> Publishing release $TAG"
        gh release edit "$TAG" --repo "$GH_REPO" --draft=false
    fi
    update_tap
    update_scoop
    submit_winget
    echo "==> Released ${TAG}: brew install ericfitz/tap/${BIN_NAME}; curl -fsSL https://github.com/${GH_REPO}/releases/latest/download/install.sh | sh; irm https://github.com/${GH_REPO}/releases/latest/download/install.ps1 | iex"
}
```

`upload_assets` becomes (the four archives are already on the release, uploaded by the workflow):

```bash
upload_assets() {
    echo "==> Uploading the macOS tarball, the install scripts and SHA256SUMS"
    (cd "$DIST" && gh release upload "$TAG" --repo "$GH_REPO" --clobber "$MACOS_ARCHIVE" install.sh install.ps1 SHA256SUMS SHA256SUMS.sig)
}
```

`main`:

```bash
main() {
    parse_args "$@"
    require_tools
    check_signing_key
    check_tag_exists
    check_tag_pushed
    check_gh
    check_workflow_at_tag
    checkout_tag
    build_macos
    ensure_draft
    dispatch_build
    watch_run
    download_archives
    verify_attestations
    copy_scripts
    write_sums
    sign_sums
    upload_assets
    publish_release
}
```

`verify_attestations` skips `MACOS_ARCHIVE` because `ARCHIVES` holds the macOS tarball first (from `build_macos`) and then the four downloaded archives, and `write_sums` lists `ARCHIVES` in that order.

- [ ] **Step 4: Run the tests**

Run: `cd <repo root> && release/test-release.sh && shellcheck release/*.sh`
Expected: every new case PASS; `test-release: 61 passed, 0 failed` (37 from #35 minus the two `pe-*` cases plus 26 here); shellcheck silent.

- [ ] **Step 5: Commit**

```bash
git add release/release.sh release/test-release.sh
git commit -m "release: build Linux and Windows in Actions, verify attestations, draft until signed"
```

---

### Task 3: Documentation

**Files:**
- Modify: `docs/install.md` (after the "Verifying a download by hand" block), `CLAUDE.md`, `release/notes-v1.14.0.md` (or the current next-tag notes)

- [ ] **Step 1: `docs/install.md`.** After the manual verification code block add:

```markdown
The Linux and Windows archives are built in GitHub Actions on runners of
their own OS and architecture and carry a build provenance attestation.
With the GitHub CLI you can check that an archive was built from this
repository by that workflow, in addition to the signature check:

```sh
gh attestation verify agentbus-vX.Y.Z-linux-amd64.tar.gz --repo ericfitz/agentbus
```
```

Also change "Maintainers cut a release with `release/release.sh <tag>`" to "Maintainers cut a release with `release/release.sh <tag>`: macOS is built and notarized locally, the other archives come from the `release-build` workflow, and the release is a draft until `SHA256SUMS` is signed."

- [ ] **Step 2: `CLAUDE.md`.** Replace the sentence from #35 (`The Windows workflow (.github/workflows/windows.yml) is informational; this gate is the only check before a push.`) with: `Release builds for Linux and Windows run in Actions (release-build.yml); the Windows test workflow reports only; make verify is still the done gate.`

- [ ] **Step 3: Release notes.** Append:

```markdown
## Build provenance

The Linux and Windows archives are now built in GitHub Actions on runners of
their own OS and architecture, run there, and published with build
provenance attestations (`gh attestation verify <file> --repo
ericfitz/agentbus`). The signed `SHA256SUMS` is unchanged (#36).
```

- [ ] **Step 4: Check the claims**

Run: `cd <repo root> && rg -n 'attest-build-provenance|ubuntu-24.04-arm|windows-11-arm' .github/workflows/release-build.yml && rg -n 'gh attestation verify' docs/install.md release/release.sh`
Expected: lines in both.

- [ ] **Step 5: Commit**

```bash
git add docs/install.md CLAUDE.md release/notes-v1.14.0.md
git commit -m "docs: attestation verification and the Actions release flow"
```

---

### Task 4: Done gate and the dry run (user)

- [ ] **Step 1: Gates**

Run: `cd <repo root> && make verify && make release-check`
Expected: `verify: OK`; `test-release: 61 passed, 0 failed`; `test-render: OK`; `test-install: ... 0 failed`; actionlint silent; the pin check silent.

- [ ] **Step 2: Self-test commands for the report**

```
release/test-release.sh
release/test-install.sh --self-test
actionlint .github/workflows/release-build.yml
```

- [ ] **Step 3: Dry run on a pre-release tag (user; report verbatim).** The workflow file must be in the tag, so the tag is cut after this plan lands on `main`:

1. `Approve: push main and tag v1.14.0-rc.1 on it (git tag v1.14.0-rc.1 && git push origin main v1.14.0-rc.1)`
2. `Approve: run release/release.sh v1.14.0-rc.1 --no-publish (creates a draft release, dispatches release-build.yml on the tag, downloads and verifies the four attestations, signs SHA256SUMS; stops before publishing)`
3. Check: `gh release view v1.14.0-rc.1 --repo ericfitz/agentbus --json isDraft,assets --jq '{draft: .isDraft, assets: [.assets[].name]}'` lists the draft with 9 assets; `gh attestation verify dist/agentbus-v1.14.0-rc.1-linux-arm64.tar.gz --repo ericfitz/agentbus` passes; `"$(brew --prefix openssl@3)/bin/openssl" pkeyutl -verify -rawin -pubin -inkey release/agentbus-release-ed25519.pub -in dist/SHA256SUMS -sigfile dist/SHA256SUMS.sig` prints `Signature Verified Successfully`.
4. `Approve: delete the draft release and tag v1.14.0-rc.1 (gh release delete v1.14.0-rc.1 --yes --repo ericfitz/agentbus; git push origin :refs/tags/v1.14.0-rc.1; git tag -d v1.14.0-rc.1)`

---

## Self-review notes

- Spec coverage: release flow steps 1-7 and `--no-publish` (Task 2), state (Task 2: `ensure_draft`, `PUBLISHED`, `dist/` recreated by `checkout_tag` from #34), workflow trigger, validation, matrix, build/run/package/upload, publish job, hardening and concurrency (Task 1), removals named (Task 2), docs and CLAUDE.md and header (Task 3), `actionlint` and the pin check in `release-check` (Task 1; `actionlint` itself entered `release-check` in #35), dry run (Task 4).
- Resolved: the workflow validates the tag with `fetch-tags: true` on a shallow checkout; the run is found by `createdAt >= dispatch time - 60 s` plus the `run-name` title; artifacts are named `archive-<goos>-<goarch>` so `publish` downloads them with one pattern (#37 keeps that pattern and only changes who produces `archive-windows-*`).
- Spec gap noted: the spec's preflight does not mention that the tag must be pushed; `check_tag_pushed` is added because `gh workflow run --ref <tag>` needs the tag on origin.
