# Authenticode signing of the Windows binaries

The Windows release binaries can be signed with Authenticode through
Microsoft Artifact Signing (formerly Trusted Signing), Basic plan, in the
maintainer's personal Azure subscription. Signing runs inside the
`package-windows` job of `.github/workflows/release-build.yml` with OIDC
federation: no signing secret is stored anywhere. Whether a release is
signed is chosen per release with `release/release.sh <tag>
--windows-signing=on|off`; the flag is required.

The workflow signs with `Azure/artifact-signing-action`, the renamed
`azure/trusted-signing-action` (the old name redirects; the workflow pins
the commit by SHA).

## Cost

Basic plan: $9.99 per month for up to 5,000 signatures, billed per account
per month, not prorated, and it cannot be paused. Deleting the account is
the only way to stop billing, and it discards the identity validation.

## One-time setup

1. In the personal Azure subscription: first register the
   `Microsoft.CodeSigning` resource provider in the subscription, then
   create an Artifact Signing account (Basic), complete individual identity
   validation (the validation email link expires after 7 days), and create a
   Public Trust certificate profile. The signer name on the certificate is
   the validated individual identity. Per Microsoft's quickstart:
   - The *Artifact Signing Identity Verifier* role is needed to create the
     identity validation.
   - Individual validation comes from the Azure billing account (Account
     Type Individual, matching the government ID).
   - The validated name becomes the certificate's common name, which is the
     value of `AUTHENTICODE_SIGNER`; read it from the certificate profile's
     Certificate subject preview.
2. Register an Entra application with a federated credential whose subject
   is `repo:ericfitz/agentbus:environment:release` (issuer
   `https://token.actions.githubusercontent.com`, audience
   `api://AzureADTokenExchange`), so only jobs in this repository's
   `release` environment can obtain a token. Assign it the *Artifact
   Signing Certificate Profile Signer* role on that certificate profile
   only.
3. In the repository, add a `v*` tag rule to the `release` environment
   (created on the first run if absent; Settings > Environments > release >
   Deployment branches and tags > tag pattern `v*`, creating the environment
   there if absent), so only tags matching `v*` can deploy to it.
   `release.sh` dispatches the workflow on the tag itself
   (`gh workflow run --ref <tag>`), so the run's `github.ref` is the tag and
   the rule admits it. The `package-windows` job uses this environment for
   both `on` and `off` releases.
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
list`) before building anything, and names the missing ones. The workflow
checks them again.

## Releasing

- `--windows-signing=on`: `package-windows` signs both executables (file
  digest SHA-256, timestamped by `http://timestamp.acs.microsoft.com` with
  SHA-256), verifies each signature's status, signer common name and
  timestamp countersignature, runs the signed amd64 binary, and only then
  are the zips attested and listed in `SHA256SUMS`. A failure in that job
  (lapsed subscription, expired validation, missing role or variable)
  fails the workflow; `release.sh` stops with the run URL and the draft
  stays for a rerun. Nothing is published unsigned when `on` was chosen.
- `--windows-signing=off`: no Azure step runs, and the release notes end
  with: "The Windows binaries in this release are not Authenticode-signed;
  they are covered by the signed SHA256SUMS and by build attestations."
- A rerun before publishing may change the choice (for example `off` after
  a lapsed subscription); every Windows asset is replaced and the notes
  line is added or removed to match. Do not flip the choice after
  publishing: a rerun then replaces the public zips and `SHA256SUMS` (see
  the public-asset warning in [#36](https://github.com/ericfitz/agentbus/issues/36))
  and winget is not refreshed.

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
