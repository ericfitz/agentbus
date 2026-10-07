# Authenticode signing for the Windows binaries

Status: Approved 2026-10-06 (design). Tracked in #37. Extends #36's
`release-build.yml` (`2026-10-06-actions-release-builds-design.md`) and
#35's Windows packaging (`2026-10-06-windows-support-design.md`).

## Goal

Sign `agentbus.exe` (amd64 and arm64) with Authenticode so Windows itself
trusts it (SmartScreen, and Smart App Control, which blocks unsigned
executables when enabled), without storing any signing secret, and
without making a paid signing subscription a precondition for releasing.

## Human decisions (user, 2026-10-06)

1. **Certificate source: Microsoft Artifact Signing** (formerly Trusted
   Signing), Basic plan, in the user's personal Azure subscription (not the
   tmi account). Open to individual developers in the US and Canada since
   October 2025; $9.99/month for up to 5,000 signatures. The signer name is
   the validated individual identity.
2. **Signing runs in GitHub Actions** with OIDC federation; no secret is
   stored.
3. **Signing is chosen per release:** `release.sh --windows-signing=on|off`,
   required, no default. Billing is per account per month, not prorated,
   and cannot be paused (deleting the account is the only way to stop it,
   and discards identity validation), so the user may delete the account
   between releases and still release unsigned.

Rejected: an OV certificate from a commercial CA (cloud-HSM keys,
typically a few hundred dollars a year, stored credentials in CI); EV (no
longer bypasses SmartScreen reputation); signing every release
unconditionally; checking Authenticode in `install.ps1` (the signed
`SHA256SUMS` already covers integrity, and unsigned releases would need
special cases).

## Human decisions (user, 2026-10-07)

Approved by Eric on 2026-10-07.

4. **Stable tags only.** `release.sh` and `release-build.yml` accept only
   `vX.Y.Z` tags; there are no release candidates. Dry runs use the next
   stable tag with `release.sh --no-publish` (draft only), and a tag is final
   once pushed.

## One-time setup (user; documented in docs/release-signing.md)

1. In the personal Azure subscription: create an Artifact Signing account
   (Basic), complete individual identity validation, and create a Public
   Trust certificate profile.
2. Register an Entra application with a federated credential whose subject
   is `repo:ericfitz/agentbus:environment:release`, so only jobs in the
   `release` environment of this repository can obtain a token. Assign it
   the Artifact Signing Certificate Profile Signer role on that certificate
   profile only.
3. In the repository, create the `release` environment with a deployment
   rule allowing only tags matching `v*`. This works because `release.sh`
   dispatches the workflow on the tag itself (`gh workflow run --ref <tag>`,
   #36), so the run's `github.ref` is the tag.
4. Store as Actions **variables** (not secrets; none of them grants access
   alone): `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`,
   `ARTIFACT_SIGNING_ENDPOINT`, `ARTIFACT_SIGNING_ACCOUNT`,
   `ARTIFACT_SIGNING_PROFILE`, `AUTHENTICODE_SIGNER` (the expected subject
   common name).

Pausing: delete the Artifact Signing account (billing stops; validation and
profile are lost). Resuming: repeat step 1 (identity validation takes days;
its email link expires after 7 days), reassign the role in step 2, and
update the variables. The workflow is unchanged either way.

## Workflow changes (release-build.yml)

- New required `workflow_dispatch` input `windows_signing`, `type: choice`,
  options `on` and `off`. The input step rejects anything else.
- The two Windows build jobs upload their tested, unsigned `agentbus.exe`
  as artifacts instead of zips.
- New job **package-windows**, `needs: build`, on `windows-latest`,
  `environment: release`, `permissions: contents: read, id-token: write`:
  - `windows_signing == 'on'`:
    1. Fail with a message naming any missing variable from setup step 4.
    2. Sign both executables with `azure/trusted-signing-action` (pinned
       SHA), file digest SHA-256, timestamped with SHA-256 by Microsoft's
       timestamp service. Signing is architecture-independent, so the amd64
       runner signs the arm64 file.
    3. Require `Get-AuthenticodeSignature` to report `Status = Valid` and a
       signer subject CN equal to `AUTHENTICODE_SIGNER` for both files, and
       a timestamp countersignature to be present (Artifact Signing
       certificates are valid for days; the timestamp keeps the signature
       valid after expiry).
    4. Run the signed amd64 binary's `agentbus version`.
  - `windows_signing == 'off'`: no Azure step runs.
  - Both: package `agentbus-<tag>-windows-amd64.zip` and `-arm64.zip` and
    upload them for **publish**.
- **publish** (from #36) attests the zips from package-windows, so the
  order is build → Authenticode → attestation → `SHA256SUMS` signature.

## release.sh changes

- `--windows-signing=on|off` is required; a missing or other value fails
  before any work, with usage.
- `on`: preflight checks the variables exist (`gh variable list`) and
  passes the input to the workflow.
- `off`: passes `off`, and appends to the release notes (generated or from
  `release/notes-<tag>.md`): "The Windows binaries in this release are not
  Authenticode-signed; they are covered by the signed SHA256SUMS and by
  build attestations."
- Failures inside package-windows (lapsed subscription, expired
  validation, missing role or variable) fail the workflow; `release.sh`
  stops with the run URL and the draft stays for a rerun (#36). Nothing is
  published unsigned when `on` was chosen.
- Rerun state is as in #36. A rerun may change the choice (for example
  `off` after a lapsed subscription); every Windows asset is replaced, and
  the notes line is added or removed to match.

## Documentation

- `docs/release-signing.md`: the one-time setup, pausing and resuming,
  and the cost.
- `docs/install.md`: Windows binaries are Authenticode-signed by the named
  individual when the release says so; how to check
  (`Get-AuthenticodeSignature`).
- `docs/windows-checklist.md`: on a signed release, `Get-AuthenticodeSignature`
  reports Valid with the expected signer in the VM, for arm64 and amd64.

## Testing

- `actionlint` on the changed workflow (`make release-check`).
- `shellcheck` on `release.sh`; a test that `--windows-signing` rejects a
  missing value and values other than `on`/`off`.
- #36's pre-release dry run is done twice when signing is first enabled:
  with `off` (no Azure step runs; notes line present) and with `on`
  (signatures verified in the job and again in the VM).

> **Implementation note (2026-10-07, controller ruling approved by Eric on 2026-10-07):**
> #36's dry run is done on the next stable `vX.Y.Z` tag with `release.sh
> --no-publish`; `release.sh` and `release-build.yml` accept stable tags only, so
> read "pre-release dry run" here as that.

## Implementation note (2026-10-07, controller ruling approved by Eric on 2026-10-07)

`docs/install.md` states that releases which are not Authenticode-signed say
so in their notes, and that `Get-AuthenticodeSignature` is the way to tell
whether a Windows binary is signed. The Documentation text above is kept as
originally written.
