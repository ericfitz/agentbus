# Authenticode Signing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sign `agentbus.exe` (amd64 and arm64) with Authenticode through Microsoft Artifact Signing in GitHub Actions, chosen per release with a required `release.sh --windows-signing=on|off`, storing no secret.

**Architecture:** `release-build.yml` (#36) gains a `windows_signing` choice input; the two Windows `build` entries upload the tested, unsigned `agentbus.exe` (artifacts `exe-windows-<arch>`) instead of zips; a new `package-windows` job (`windows-latest`, `environment: release`, OIDC via `azure/login` then `Azure/artifact-signing-action`) signs both files when `on`, verifies `Get-AuthenticodeSignature` (Valid, expected signer CN, timestamp present), runs the signed amd64 binary, and packages the zips as `archive-windows-<arch>` for `publish`, which attests them as before. `release.sh` requires the flag, checks the seven repository variables with `gh variable list` when `on`, passes the input to the dispatch, and materializes release notes (`render_notes`) with or without the "not Authenticode-signed" line so reruns can flip the choice.

**Tech Stack:** GitHub Actions (`azure/login@a641126d1b8aa4d1fa005f4f92df94a3a4c4c906 # v3.1.0`, `Azure/artifact-signing-action@c7ab2a863ab5f9a846ddb8265964877ef296ee82 # v2.0.0`, the former `azure/trusted-signing-action`, renamed upstream), PowerShell `Get-AuthenticodeSignature`, `gh variable list`, bash tooling from #34–#36.

**Spec:** `docs/superpowers/specs/2026-10-06-authenticode-design.md`

## Global Constraints

- **Release notes (orchestrator override, 2026-10-07):** release notes files are written in the `chore: release vX.Y.Z` commit at release time (`git log -- release/notes-v1.13.0.md`), never in a feature commit. Wherever a task below says to create, append to or `git add` `release/notes-v1.14.0.md` (or any `release/notes-*.md`), do not touch the file: put that text in the execution's final report for the release author instead, and drop the path from the commit.
- Depends on #34, #35 and #36 being merged: this plan edits `.github/workflows/release-build.yml` by step id (`package-windows-zip`, `upload` in `build`; `download`, `attest` in `publish`) and `release/release.sh` functions `parse_args`, `usage`, `require_tools`, `ensure_draft`, `dispatch_build`, `release_notes_args`, `main` from #36.
- `--windows-signing=on|off` is required; a missing or other value fails before any work, with usage.
- Repository **variables** (not secrets), all required when `on`: `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `ARTIFACT_SIGNING_ENDPOINT`, `ARTIFACT_SIGNING_ACCOUNT`, `ARTIFACT_SIGNING_PROFILE`, `AUTHENTICODE_SIGNER`. The federated credential subject is `repo:ericfitz/agentbus:environment:release`; the `release` environment allows only tags matching `v*`.
- Signing: file digest SHA-256, timestamp `http://timestamp.acs.microsoft.com` with SHA-256; both executables signed on the amd64 runner (signing is architecture-independent). Verification in the job: `Status -eq 'Valid'`, signer CN equals `AUTHENTICODE_SIGNER`, a timestamp countersignature present, and the signed amd64 binary's `agentbus version` equals the version.
- `off`: no Azure step runs; the release notes carry, verbatim: `The Windows binaries in this release are not Authenticode-signed; they are covered by the signed SHA256SUMS and by build attestations.` `on`: the line is absent. Reruns may change the choice; every Windows asset is replaced and the line added or removed.
- Order stays build → Authenticode → attestation → `SHA256SUMS` signature.
- No step touches Azure, creates the `release` environment, sets repository variables, dispatches a workflow or publishes; those are user instructions with an `Approve:` line.
- Bash with `set -euo pipefail`; `rg PATTERN <path>`; American English.

## Review Focus

- `AUTHENTICODE_SIGNER` set to the full subject (`CN=Jane Doe, O=...`) instead of the common name: the comparison must be against the certificate's simple name, and the error must print both values so the mismatch is obvious. Pinned in Task 1 (the verify step prints `got`/`want`) and documented in Task 3.
- A rerun that flips `on` → `off` after a lapsed subscription: the notes line must be added exactly once, and a later `off` → `on` rerun must remove it, with `release/notes-<tag>.md` content otherwise untouched. Pinned in Task 2 (`render_notes_text` cases `off-idempotent`, `on-removes`).
- `--windows-signing=ON` (uppercase) or `--windows-signing on` (space instead of `=`): rejected with usage, not treated as `off`. Pinned in Task 2.
- A dispatch where `package-windows` is skipped or cancelled (environment rule rejects the ref): `publish` must not run with only the Linux archives; its "require all four archives" check (#36) covers it, and `needs: [validate, build, package-windows]` makes a skipped job fail the chain. Pinned by `actionlint` in Task 1 and the dry run in Task 4.
- `gh variable list` returning fewer variables because the token lacks `actions:read` on variables: the preflight must say which names are missing, not succeed silently. Pinned in Task 2 (`check_signing_vars` with a stub listing a subset).

---

### Task 1: Workflow: `windows_signing` input and the `package-windows` job

**Files:**
- Modify: `.github/workflows/release-build.yml`

**Interfaces:**
- Produces: input `windows_signing` (`type: choice`, `options: ['on', 'off']`, `required: true`); artifacts `exe-windows-amd64`, `exe-windows-arm64` (each containing `agentbus.exe`) from `build`; job `package-windows` with step ids `check-vars`, `login`, `sign`, `verify-signature`, `package`, `upload-amd64`, `upload-arm64`; `publish` needs `[validate, build, package-windows]`.

- [ ] **Step 1: Edit the workflow.** Under `inputs:` add:

```yaml
      windows_signing:
        description: Authenticode-sign the Windows binaries (on) or not (off)
        required: true
        type: choice
        options: ['on', 'off'] # quoted: bare on/off are YAML 1.1 booleans
```

In `validate`'s `check` step add `WINDOWS_SIGNING: ${{ inputs.windows_signing }}` to `env` and, before the `echo "version=..."` line:

```bash
          case "$WINDOWS_SIGNING" in on|off) ;; *) echo "::error::windows_signing must be on or off, got '$WINDOWS_SIGNING'"; exit 1 ;; esac
```

In `build`, delete the `package-windows-zip` step, and replace the `upload` step with two:

```yaml
      - id: upload
        if: matrix.goos == 'linux'
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: archive-${{ matrix.goos }}-${{ matrix.goarch }}
          path: agentbus-${{ inputs.tag }}-linux-${{ matrix.goarch }}.tar.gz
          if-no-files-found: error
      - id: upload-exe
        if: matrix.goos == 'windows'
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: exe-windows-${{ matrix.goarch }}
          path: out/agentbus.exe
          if-no-files-found: error
```

Add the job between `build` and `publish`:

```yaml
  package-windows:
    needs: [validate, build]
    runs-on: windows-latest
    environment: release
    permissions:
      contents: read
      id-token: write
    env:
      TAG: ${{ inputs.tag }}
      VERSION: ${{ needs.validate.outputs.version }}
      WINDOWS_SIGNING: ${{ inputs.windows_signing }}
    steps:
      - id: download-exe
        uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1
        with:
          pattern: exe-windows-*
          path: exe
      - id: check-vars
        if: inputs.windows_signing == 'on'
        name: Require the signing variables
        shell: pwsh
        env:
          AZURE_CLIENT_ID: ${{ vars.AZURE_CLIENT_ID }}
          AZURE_TENANT_ID: ${{ vars.AZURE_TENANT_ID }}
          AZURE_SUBSCRIPTION_ID: ${{ vars.AZURE_SUBSCRIPTION_ID }}
          ARTIFACT_SIGNING_ENDPOINT: ${{ vars.ARTIFACT_SIGNING_ENDPOINT }}
          ARTIFACT_SIGNING_ACCOUNT: ${{ vars.ARTIFACT_SIGNING_ACCOUNT }}
          ARTIFACT_SIGNING_PROFILE: ${{ vars.ARTIFACT_SIGNING_PROFILE }}
          AUTHENTICODE_SIGNER: ${{ vars.AUTHENTICODE_SIGNER }}
        run: |
          $names = 'AZURE_CLIENT_ID', 'AZURE_TENANT_ID', 'AZURE_SUBSCRIPTION_ID', 'ARTIFACT_SIGNING_ENDPOINT', 'ARTIFACT_SIGNING_ACCOUNT', 'ARTIFACT_SIGNING_PROFILE', 'AUTHENTICODE_SIGNER'
          $missing = @($names | Where-Object { -not [Environment]::GetEnvironmentVariable($_) })
          if ($missing.Count -gt 0) { Write-Host "::error::missing repository variables: $($missing -join ', ') (docs/release-signing.md, step 4)"; exit 1 }
      - id: login
        if: inputs.windows_signing == 'on'
        uses: azure/login@a641126d1b8aa4d1fa005f4f92df94a3a4c4c906 # v3.1.0
        with:
          client-id: ${{ vars.AZURE_CLIENT_ID }}
          tenant-id: ${{ vars.AZURE_TENANT_ID }}
          subscription-id: ${{ vars.AZURE_SUBSCRIPTION_ID }}
      - id: sign
        if: inputs.windows_signing == 'on'
        uses: Azure/artifact-signing-action@c7ab2a863ab5f9a846ddb8265964877ef296ee82 # v2.0.0
        with:
          endpoint: ${{ vars.ARTIFACT_SIGNING_ENDPOINT }}
          signing-account-name: ${{ vars.ARTIFACT_SIGNING_ACCOUNT }}
          certificate-profile-name: ${{ vars.ARTIFACT_SIGNING_PROFILE }}
          files-folder: ${{ github.workspace }}\exe
          files-folder-filter: exe
          files-folder-recurse: true
          file-digest: SHA256
          timestamp-rfc3161: http://timestamp.acs.microsoft.com
          timestamp-digest: SHA256
          # Only the Azure CLI credential from the login step above is tried.
          exclude-environment-credential: true
          exclude-workload-identity-credential: true
          exclude-managed-identity-credential: true
          exclude-shared-token-cache-credential: true
          exclude-visual-studio-credential: true
          exclude-visual-studio-code-credential: true
          exclude-azure-powershell-credential: true
          exclude-azure-developer-cli-credential: true
          exclude-interactive-browser-credential: true
      - id: verify-signature
        if: inputs.windows_signing == 'on'
        name: Verify Authenticode
        shell: pwsh
        env:
          AUTHENTICODE_SIGNER: ${{ vars.AUTHENTICODE_SIGNER }}
        run: |
          $ErrorActionPreference = 'Stop'
          foreach ($f in 'exe\exe-windows-amd64\agentbus.exe', 'exe\exe-windows-arm64\agentbus.exe') {
            $s = Get-AuthenticodeSignature -FilePath $f
            if ($s.Status -ne 'Valid') { Write-Host "::error::$f signature status is $($s.Status) ($($s.StatusMessage))"; exit 1 }
            $cn = $s.SignerCertificate.GetNameInfo([System.Security.Cryptography.X509Certificates.X509NameType]::SimpleName, $false)
            if ($cn -ne $env:AUTHENTICODE_SIGNER) { Write-Host "::error::$f signer CN is '$cn', want AUTHENTICODE_SIGNER='$env:AUTHENTICODE_SIGNER' (the certificate's common name, not the full subject)"; exit 1 }
            if (-not $s.TimeStamperCertificate) { Write-Host "::error::$f has no timestamp countersignature; the signature would expire with the certificate"; exit 1 }
            Write-Host "$f signed by $cn, timestamped by $($s.TimeStamperCertificate.Subject)"
          }
          $v = (& .\exe\exe-windows-amd64\agentbus.exe version | Select-Object -First 1)
          if ($v -ne $env:VERSION) { Write-Host "::error::signed agentbus version printed '$v', want $env:VERSION"; exit 1 }
      - id: package
        shell: pwsh
        run: |
          Compress-Archive -LiteralPath exe\exe-windows-amd64\agentbus.exe -DestinationPath "agentbus-$env:TAG-windows-amd64.zip"
          Compress-Archive -LiteralPath exe\exe-windows-arm64\agentbus.exe -DestinationPath "agentbus-$env:TAG-windows-arm64.zip"
      - id: upload-amd64
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: archive-windows-amd64
          path: agentbus-${{ inputs.tag }}-windows-amd64.zip
          if-no-files-found: error
      - id: upload-arm64
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: archive-windows-arm64
          path: agentbus-${{ inputs.tag }}-windows-arm64.zip
          if-no-files-found: error
```

Change `publish`'s `needs: [validate, build]` to `needs: [validate, build, package-windows]`. Its `download`, "Require all four archives", `attest` and `upload-release` steps are unchanged: `archive-linux-*` still come from `build`, `archive-windows-*` now from `package-windows`, and the attestation covers the signed zips, so the order is build → Authenticode → attestation → `SHA256SUMS` signature. Update the header comment: "Windows binaries are Authenticode-signed in package-windows when dispatched with windows_signing=on (Microsoft Artifact Signing over OIDC; no stored secret)."

- [ ] **Step 2: Lint and pin check**

Run: `cd /Users/efitz/Projects/agentbus && actionlint .github/workflows/release-build.yml && if rg -n 'uses: .*@v[0-9]' .github/workflows/; then echo UNPINNED; fi && rg -n 'id: (package-windows-zip)' .github/workflows/release-build.yml; echo "removed-check exit=$?"`
Expected: actionlint silent; no `UNPINNED`; the last `rg` finds nothing (`removed-check exit=1`).

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release-build.yml
git commit -m "ci: Authenticode-sign the Windows binaries in package-windows when requested"
```

---

### Task 2: `release.sh --windows-signing=on|off`, variable preflight, notes line

**Files:**
- Modify: `release/release.sh`, `release/test-release.sh`

**Interfaces:**
- Produces: global `WINDOWS_SIGNING` (`on`/`off`, empty until parsed); `UNSIGNED_NOTE` constant string; functions `check_signing_vars`, `render_notes_text <text> <on|off>` (pure; prints the final notes), `render_notes` (writes `$DIST/notes.md` from `release/notes-<tag>.md` or GitHub's generated notes). `ensure_draft` passes `--notes-file "$DIST/notes.md"` on create and runs `gh release edit "$TAG" --notes-file "$DIST/notes.md"` on reuse; `dispatch_build` adds `-f "windows_signing=$WINDOWS_SIGNING"`. Removed: `release_notes_args` (and its two tests).

- [ ] **Step 1: Failing tests.** In `release/test-release.sh`, delete the `notes-generated` and `notes-file` cases and append before the summary:

```bash
# --- Authenticode choice (#37) ---------------------------------------------
check removed-release-notes-args 1 "" -- bash -c "source '$HERE/release.sh'; declare -F release_notes_args"
check ws-missing 2 "--windows-signing=on|off is required" -- parse_args v1.2.3
check ws-on 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 --windows-signing=on && [[ \$WINDOWS_SIGNING == on ]]"
check ws-off-first 0 "" -- bash -c "source '$HERE/release.sh'; parse_args --windows-signing=off v1.2.3 --no-publish && [[ \$WINDOWS_SIGNING == off && \$NO_PUBLISH == 1 ]]"
check ws-bad-value 2 "--windows-signing must be on or off, got 'maybe'" -- parse_args v1.2.3 --windows-signing=maybe
check ws-uppercase 2 "got 'ON'" -- parse_args v1.2.3 --windows-signing=ON
check ws-space-form 2 "unknown option --windows-signing" -- parse_args v1.2.3 --windows-signing on
check ws-twice 2 "usage" -- parse_args v1.2.3 --windows-signing=on --windows-signing=off
check ws-empty 2 "got ''" -- parse_args v1.2.3 --windows-signing=

base=$'## Changes\n\n- something\n'
line='The Windows binaries in this release are not Authenticode-signed; they are covered by the signed SHA256SUMS and by build attestations.'
check notes-off-appends 0 "" -- bash -c "source '$HERE/release.sh'; [[ \$(render_notes_text \"\$1\" off) == \$'## Changes\n\n- something\n\n$line' ]]" _ "$base"
check notes-off-idempotent 0 "1" -- bash -c "source '$HERE/release.sh'; render_notes_text \"\$(render_notes_text \"\$1\" off)\" off | grep -c 'not Authenticode-signed'" _ "$base"
check notes-on-plain 0 "" -- bash -c "source '$HERE/release.sh'; [[ \$(render_notes_text \"\$1\" on) == \$'## Changes\n\n- something' ]]" _ "$base"
check notes-on-removes 0 "0" -- bash -c "source '$HERE/release.sh'; render_notes_text \"\$(render_notes_text \"\$1\" off)\" on | grep -c 'not Authenticode-signed' || true" _ "$base"
check notes-bad-mode 1 "render_notes_text: mode must be on or off" -- bash -c "source '$HERE/release.sh'; render_notes_text x sideways"

cat > "$WORK/gh-vars.sh" <<'EOF'
gh() {
    case "$1 $2" in
        "variable list") printf '%s\n' $GH_VARS ;;
        "api repos/ericfitz/agentbus/releases/generate-notes") printf 'generated notes\n' ;;
        *) return 0 ;;
    esac
}
EOF
all_vars="AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_SUBSCRIPTION_ID ARTIFACT_SIGNING_ENDPOINT ARTIFACT_SIGNING_ACCOUNT ARTIFACT_SIGNING_PROFILE AUTHENTICODE_SIGNER"
with_vars() { bash -c "source '$HERE/release.sh'; source '$WORK/gh-vars.sh'; export GH_VARS='$1'; WINDOWS_SIGNING=$2; TAG=v1.2.3; DIST='$WORK/dist'; REPO_ROOT='$WORK/repo'; mkdir -p '$WORK/dist'; $3"; }
check vars-all-present 0 "" -- with_vars "$all_vars" on check_signing_vars
check vars-missing-named 1 "ARTIFACT_SIGNING_PROFILE, AUTHENTICODE_SIGNER" -- with_vars "AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_SUBSCRIPTION_ID ARTIFACT_SIGNING_ENDPOINT ARTIFACT_SIGNING_ACCOUNT" on check_signing_vars
check vars-skipped-when-off 0 "" -- with_vars "" off check_signing_vars
check render-notes-generated-off 0 "" -- with_vars "" off "render_notes && grep -q 'generated notes' '$WORK/dist/notes.md' && grep -q 'not Authenticode-signed' '$WORK/dist/notes.md'"
printf 'from file\n' > "$WORK/repo/release/notes-v1.2.3.md"
check render-notes-file-on 0 "" -- with_vars "" on "render_notes && grep -q 'from file' '$WORK/dist/notes.md' && ! grep -q 'not Authenticode-signed' '$WORK/dist/notes.md'"
```

- [ ] **Step 2: Run to verify it fails**

Run: `/Users/efitz/Projects/agentbus/release/test-release.sh`
Expected: `ws-*` FAIL (flag unknown), `notes-*` and `vars-*` and `render-notes-*` FAIL (undefined), `removed-release-notes-args` FAIL.

- [ ] **Step 3: Implement in `release/release.sh`.** Globals, after `WORKFLOW=`:

```bash
WINDOWS_SIGNING="" # on|off, required (--windows-signing=on|off)
UNSIGNED_NOTE="The Windows binaries in this release are not Authenticode-signed; they are covered by the signed SHA256SUMS and by build attestations."
SIGNING_VARS=(AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_SUBSCRIPTION_ID ARTIFACT_SIGNING_ENDPOINT ARTIFACT_SIGNING_ACCOUNT ARTIFACT_SIGNING_PROFILE AUTHENTICODE_SIGNER)
```

`usage` and `parse_args`:

```bash
usage() { echo "usage: release.sh <tag> --windows-signing=on|off [--no-publish]" >&2; exit 2; }

parse_args() {
    local a
    for a in "$@"; do
        case "$a" in
            --no-publish) [[ "$NO_PUBLISH" == 0 ]] || usage; NO_PUBLISH=1 ;;
            --windows-signing=*)
                [[ -z "$WINDOWS_SIGNING" ]] || usage
                case "${a#*=}" in
                    on | off) WINDOWS_SIGNING="${a#*=}" ;;
                    *) echo "error: --windows-signing must be on or off, got '${a#*=}'" >&2; usage ;;
                esac ;;
            --*) echo "error: unknown option $a" >&2; usage ;;
            *) [[ -z "$TAG" ]] || usage; TAG="$a" ;;
        esac
    done
    [[ -n "$TAG" ]] || usage
    [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] \
        || { echo "error: tag must look like v1.2.3 (or v1.2.3-rc.1), got '$TAG'" >&2; exit 2; }
    [[ -n "$WINDOWS_SIGNING" ]] || { echo "error: --windows-signing=on|off is required (billing for Artifact Signing is monthly; choose per release)" >&2; usage; }
    VERSION="${TAG#v}"
}
```

Notes: the tag-format check stays before the required-flag check so #34's `args-bad-tag` case keeps its message. The `ws-space-form` case: `--windows-signing on` is two arguments; `--windows-signing` hits `--*` ("unknown option") before `on` could become a second positional. The earlier success cases from #34 and #36 (`args-ok`, `args-no-publish-before`, `args-no-publish-after`, `args-default-publish`) must now pass the flag: add `--windows-signing=off` to each of those four `parse_args` invocations in `release/test-release.sh` (the `ws-missing` case is the one that pins the omission).

Add after `check_workflow_at_tag`:

```bash
# check_signing_vars: with --windows-signing=on every Artifact Signing
# variable must exist as a repository variable (gh variable list), else the
# workflow would fail after the macOS build and the dispatch.
check_signing_vars() {
    [[ "$WINDOWS_SIGNING" == on ]] || return 0
    local have missing=() v
    have="$(gh variable list --repo "$GH_REPO" --json name --jq '.[].name')"
    for v in "${SIGNING_VARS[@]}"; do
        grep -qx "$v" <<<"$have" || missing+=("$v")
    done
    (( ${#missing[@]} == 0 )) && return 0
    local joined; joined="$(printf '%s, ' "${missing[@]}")"; joined="${joined%, }"
    fail "missing repository variables for Authenticode signing: $joined (docs/release-signing.md, step 4; or release with --windows-signing=off)"
}
```

Replace `release_notes_args` with:

```bash
# render_notes_text <notes> <on|off> prints the notes with the unsigned-
# Windows line present exactly once (off) or absent (on), so a rerun can
# flip the choice. The line is always the last paragraph.
render_notes_text() {
    local text="$1" mode="$2"
    [[ "$mode" == on || "$mode" == off ]] || fail "render_notes_text: mode must be on or off, got '$mode'"
    # Command substitution strips every trailing newline, so a previous
    # run's blank line before the note disappears with the note itself.
    text="$(grep -vF "$UNSIGNED_NOTE" <<<"$text" || true)"
    if [[ "$mode" == off ]]; then
        printf '%s\n\n%s\n' "$text" "$UNSIGNED_NOTE"
    else
        printf '%s\n' "$text"
    fi
}

# render_notes writes $DIST/notes.md: release/notes-<tag>.md when present,
# else GitHub's generated notes for the tag, then render_notes_text.
render_notes() {
    local base
    if [[ -f "$REPO_ROOT/release/notes-${TAG}.md" ]]; then
        base="$(cat "$REPO_ROOT/release/notes-${TAG}.md")"
    else
        base="$(gh api "repos/${GH_REPO}/releases/generate-notes" -f "tag_name=$TAG" --jq .body)"
    fi
    render_notes_text "$base" "$WINDOWS_SIGNING" > "$DIST/notes.md"
}
```

In `ensure_draft`: replace the `local notes=()...` and `gh release create ... "${notes[@]}"` lines with `gh release create "$TAG" --repo "$GH_REPO" --title "$TAG" --draft --notes-file "$DIST/notes.md"`, and in both reuse branches (draft and already-published) add, before `return`, `gh release edit "$TAG" --repo "$GH_REPO" --notes-file "$DIST/notes.md"` with the comment "a rerun may have changed --windows-signing: the notes line follows it". In `dispatch_build`, the `gh workflow run` line becomes `gh workflow run "$WORKFLOW" --repo "$GH_REPO" --ref "$TAG" -f "tag=$TAG" -f "windows_signing=$WINDOWS_SIGNING"`. In `main`, insert `check_signing_vars` after `check_workflow_at_tag`, and `render_notes` after `checkout_tag` (it needs `$DIST`) and before `ensure_draft`. Update the header usage lines to `./release/release.sh v0.1.1 --windows-signing=on` and `... --windows-signing=off --no-publish`.

Because `publish_release` (#36) runs `update_tap`, `update_scoop` and `submit_winget` unchanged, the Windows assets it points at are whatever `package-windows` produced for this dispatch: signed for `on`, unsigned for `off`.

- [ ] **Step 4: Run the tests**

Run: `cd /Users/efitz/Projects/agentbus && release/test-release.sh && shellcheck release/*.sh`
Expected: every `ws-*`, `notes-*`, `vars-*`, `render-notes-*` case PASS; `test-release: 78 passed, 0 failed` (61 from #36 minus the two `notes-*` cases plus 19 here); shellcheck silent. The #36 `draft-*` stub cases still pass because the stub answers `release edit` with `GH_RC` and `render_notes` is not called by `ensure_draft` (the test for `draft-created` must now create `$WORK/dist/notes.md` first: prepend `mkdir -p '$WORK/dist'; : > '$WORK/dist/notes.md';` to its `with_stub` command, and fix the test's `grep` to `'gh release create v1.2.3 .*--draft --notes-file'`).

- [ ] **Step 5: Commit**

```bash
git add release/release.sh release/test-release.sh
git commit -m "release: required --windows-signing=on|off, variable preflight, unsigned-Windows note"
```

---

### Task 3: Documentation

**Files:**
- Create: `docs/release-signing.md`
- Modify: `docs/install.md` (Windows section), `docs/windows-checklist.md` (Packaging list)

- [ ] **Step 1: `docs/release-signing.md`**

```markdown
# Authenticode signing of the Windows binaries

The Windows release binaries can be signed with Authenticode through
Microsoft Artifact Signing (formerly Trusted Signing), Basic plan, in the
maintainer's personal Azure subscription. Signing runs inside the
`package-windows` job of `.github/workflows/release-build.yml` with OIDC
federation: no signing secret is stored anywhere. Whether a release is
signed is chosen per release with `release/release.sh <tag>
--windows-signing=on|off`; the flag is required.

## Cost

Basic plan: $9.99 per month for up to 5,000 signatures, billed per account
per month, not prorated, and it cannot be paused. Deleting the account is
the only way to stop billing, and it discards the identity validation.

## One-time setup

1. In the personal Azure subscription (not the tmi account): create an
   Artifact Signing account (Basic), complete individual identity
   validation (the validation email link expires after 7 days), and create
   a Public Trust certificate profile. The signer name on the certificate
   is the validated individual identity.
2. Register an Entra application with a federated credential whose subject
   is `repo:ericfitz/agentbus:environment:release` (issuer
   `https://token.actions.githubusercontent.com`, audience
   `api://AzureADTokenExchange`), so only jobs in this repository's
   `release` environment can obtain a token. Assign it the *Artifact
   Signing Certificate Profile Signer* role on that certificate profile
   only.
3. In the repository, create the `release` environment with a deployment
   branch/tag rule allowing only tags matching `v*`. `release.sh`
   dispatches the workflow on the tag itself (`gh workflow run --ref
   <tag>`), so the run's `github.ref` is the tag and the rule admits it.
4. Store these as repository **variables** (Settings > Secrets and
   variables > Actions > Variables), not secrets; none of them grants
   access on its own:

   | Variable | Value |
   |---|---|
   | `AZURE_CLIENT_ID` | the Entra application's client id |
   | `AZURE_TENANT_ID` | the tenant id |
   | `AZURE_SUBSCRIPTION_ID` | the subscription id |
   | `ARTIFACT_SIGNING_ENDPOINT` | the account's regional endpoint, for example `https://eus.codesigning.azure.net/` |
   | `ARTIFACT_SIGNING_ACCOUNT` | the Artifact Signing account name |
   | `ARTIFACT_SIGNING_PROFILE` | the certificate profile name |
   | `AUTHENTICODE_SIGNER` | the certificate's common name only (what `Get-AuthenticodeSignature` reports as the signer's simple name), not the full subject |

`release.sh --windows-signing=on` checks that all seven exist (`gh variable
list`) before building anything.

## Releasing

- `--windows-signing=on`: `package-windows` signs both executables (file
  digest SHA-256, timestamped by `http://timestamp.acs.microsoft.com` with
  SHA-256), verifies each signature's status, signer and timestamp, runs
  the signed amd64 binary, and only then are the zips attested and listed
  in `SHA256SUMS`. A failure in that job (lapsed subscription, expired
  validation, missing role or variable) fails the workflow; `release.sh`
  stops with the run URL and the draft stays for a rerun. Nothing is
  published unsigned when `on` was chosen.
- `--windows-signing=off`: no Azure step runs, and the release notes end
  with: "The Windows binaries in this release are not Authenticode-signed;
  they are covered by the signed SHA256SUMS and by build attestations."
- A rerun may change the choice (for example `off` after a lapsed
  subscription); every Windows asset is replaced and the notes line is
  added or removed to match.

## Pausing and resuming

Pausing: delete the Artifact Signing account. Billing stops; the identity
validation and the certificate profile are lost. Releases continue with
`--windows-signing=off`.

Resuming: repeat setup step 1 (identity validation takes days), reassign
the role in step 2 to the new certificate profile, and update the
`ARTIFACT_SIGNING_*` variables. The workflow is unchanged either way.

## Checking a release

```powershell
Get-AuthenticodeSignature .\agentbus.exe | Format-List Status, SignerCertificate, TimeStamperCertificate
```

`Status` is `Valid` and the signer's subject names the maintainer. An
unsigned release says so in its notes; its binaries are still covered by
the signed `SHA256SUMS` and the build attestations.
```

- [ ] **Step 2: `docs/install.md`.** In the Windows section, after the package-managers paragraph add:

```markdown
When a release says so in its notes, its Windows binaries are
Authenticode-signed by the maintainer through Microsoft Artifact Signing;
check with `Get-AuthenticodeSignature .\agentbus.exe` (`Status` `Valid`).
A release whose notes say the binaries are not Authenticode-signed is
still covered by the signed `SHA256SUMS` and the build attestations. See
[release-signing.md](release-signing.md).
```

- [ ] **Step 3: `docs/windows-checklist.md`.** In "Packaging" add:

```markdown
- [ ] On a release made with `--windows-signing=on`:
      `Get-AuthenticodeSignature .\agentbus.exe` reports `Valid` with the
      expected signer, for the arm64 binary in the VM and for the amd64
      binary (downloaded zip), and SmartScreen does not warn on first run.
```

- [ ] **Step 4: Check the claims**

Run: `cd /Users/efitz/Projects/agentbus && rg -n 'environment: release|timestamp.acs.microsoft.com|GetNameInfo' .github/workflows/release-build.yml && rg -n 'SIGNING_VARS=|UNSIGNED_NOTE=' release/release.sh`
Expected: each documented behavior has a source line.

- [ ] **Step 5: Commit**

```bash
git add docs/release-signing.md docs/install.md docs/windows-checklist.md
git commit -m "docs: Authenticode signing setup, pausing, and verification"
```

---

### Task 4: Done gate and the two dry runs (user)

- [ ] **Step 1: Gates**

Run: `cd /Users/efitz/Projects/agentbus && make verify && make release-check`
Expected: `verify: OK`; `test-release: 78 passed, 0 failed`; `test-render: OK`; `test-install: ... 0 failed`; actionlint and the pin check silent.

- [ ] **Step 2: Self-test commands for the report**

```
release/test-release.sh
actionlint .github/workflows/release-build.yml
```

- [ ] **Step 3: User steps (report verbatim).** Before the first signed release the user completes `docs/release-signing.md` steps 1-4 (Azure, Entra, the `release` environment, the seven variables); none is an implementer action. Then #36's dry run is done twice on a pre-release tag that contains this plan's workflow:

1. `Approve: push main and tag v1.14.0-rc.2 on it (git tag v1.14.0-rc.2 && git push origin main v1.14.0-rc.2)`
2. `Approve: run release/release.sh v1.14.0-rc.2 --windows-signing=off --no-publish` — expect: no `login`/`sign`/`verify-signature` step runs in `package-windows` (`gh run view <id> --json jobs --jq '.jobs[] | select(.name=="package-windows") | .steps[] | {name, conclusion}'` shows them `skipped`), and the draft's notes end with the unsigned line (`gh release view v1.14.0-rc.2 --json body --jq .body | tail -1`).
3. `Approve: run release/release.sh v1.14.0-rc.2 --windows-signing=on --no-publish` — expect: `verify-signature` prints both files "signed by <CN>, timestamped by ..."; the notes line is gone; in the VM `Get-AuthenticodeSignature` on the arm64 binary from the draft's zip reports `Valid` with the expected signer.
4. `Approve: delete the draft release and tag v1.14.0-rc.2 (gh release delete v1.14.0-rc.2 --yes --repo ericfitz/agentbus; git push origin :refs/tags/v1.14.0-rc.2; git tag -d v1.14.0-rc.2)`

---

## Self-review notes

- Spec coverage: workflow input and rejection (Task 1 validate step), Windows builds upload exes (Task 1), `package-windows` with `on`/`off` branches, signature checks and the signed-binary run (Task 1), `publish` attests the zips after signing (Task 1), `release.sh` flag, preflight, notes line and rerun behavior (Task 2), docs (Task 3), `actionlint`/`shellcheck`/flag tests and the two dry runs (Tasks 2, 4).
- Resolved: the spec names `azure/trusted-signing-action`; upstream renamed the repository to `Azure/artifact-signing-action` (the old name redirects), so the pin uses the canonical name with the same SHA. OIDC is done by `azure/login` (as the action's README recommends) and every other credential source is excluded.
- Resolved: the spec says "appends to the release notes (generated or from `release/notes-<tag>.md`)"; to make reruns idempotent the notes are always materialized to `dist/notes.md` and re-applied with `gh release edit` on reuse, replacing #36's `release_notes_args`.
