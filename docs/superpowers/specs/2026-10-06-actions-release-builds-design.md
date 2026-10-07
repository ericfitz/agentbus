# Release builds for Linux and Windows in GitHub Actions, with attestations

Status: Approved 2026-10-06 (design). Tracked in #36. Changes the build
step of #34 (`2026-10-06-linux-releases-design.md`) and #35
(`2026-10-06-windows-support-design.md`); their assets, signed
`SHA256SUMS`, install scripts and package managers are unchanged.

## Goal

Build every non-macOS release binary on a runner of its own OS and
architecture, run it there, and publish GitHub artifact attestations
(Sigstore build provenance) for it. The macOS binary stays a local build
because signing and notarization need the user's Developer ID.

## Human decisions (user, 2026-10-06)

1. **Attestations in addition to the signed `SHA256SUMS`** (#34 decision
   4). The install scripts keep verifying the signature only, because
   verifying an attestation needs `gh` or `cosign`.
2. **Actions builds all four non-macOS binaries:** `linux-amd64`,
   `linux-arm64`, `windows-amd64`, `windows-arm64`.

Rejected: Linux only (Windows would keep a second, unattested build path).

## Release flow (release.sh)

1. **Preflight** as in #34, plus `gh auth status` and that
   `.github/workflows/release-build.yml` exists at the tag (`git cat-file
   -e <tag>:.github/workflows/release-build.yml`); a tag cut before the
   workflow landed cannot use this flow.
2. **macOS:** build, sign and notarize as today.
3. **Draft release:** create it with `gh release create <tag> --draft`, or
   reuse an existing draft for the tag. `releases/latest` never resolves to
   a draft, so the install scripts cannot see a partial release.
4. **Build:** `gh workflow run release-build.yml --ref <tag> -f tag=<tag>`
   (the run uses the workflow file as of the tag, and its `github.ref` is
   the tag); find the run
   (the newest `workflow_dispatch` run of that workflow created after the
   dispatch, matched on its `tag` input via the run's display title); `gh
   run watch <id> --exit-status`. On failure, stop and print the run URL.
5. **Check provenance:** `gh release download <tag>` the four archives into
   `dist/`, and `gh attestation verify <file> --repo ericfitz/agentbus` each
   one. Any failure stops the release before publishing.
6. **Sign:** write `SHA256SUMS` over the macOS tarball, the four archives,
   `install.sh` and `install.ps1`; sign and verify it as in #34; upload it,
   `SHA256SUMS.sig`, the scripts and the macOS tarball with `--clobber`.
7. **Publish:** `gh release edit <tag> --draft=false`, then the Homebrew,
   Scoop and winget steps from #34 and #35.

The local Linux and Windows builds, #34's Docker smoke test and #35's
`file` check are removed: the workflow runs each binary natively.

### State

- Rerun after a failure: the draft is reused, the workflow is dispatched
  again and re-uploads with `--clobber`, and steps 5-7 repeat. Repeated
  attestations for the same digest are harmless.
- Rerun on an already published release: allowed, after a warning that it
  replaces public assets; the draft step is skipped.
- Overlap: the workflow sets `concurrency: release-${{ inputs.tag }}` with
  `cancel-in-progress: false`, so a second dispatch for the same tag waits
  for the first. `release.sh` is run by one person at a time.
- Leftovers: `dist/` is recreated per run; a leftover draft is what a
  rerun resumes.

## Workflow (.github/workflows/release-build.yml)

- **Trigger:** `workflow_dispatch` with a required `tag` input;
  `run-name: release-build ${{ inputs.tag }}`. The first step rejects a tag
  not matching `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`, not present
  in the repository, or not equal to `github.ref_name` (the run must be
  dispatched on the tag).

> **Implementation note (2026-10-07, controller ruling pending Eric's review):**
> Tags are stable `vX.Y.Z` only, both in `release.sh` (`parse_args`) and in
> `release-build.yml`; the pre-release suffix allowed by the regex above and the
> `-rc.1` dry run below were not implemented. A dry run uses the next stable tag
> with `release.sh --no-publish`, then deletes the draft and the tag.
- **build** job, `permissions: contents: read`, matrix:

  | runner | GOOS/GOARCH | archive |
  |---|---|---|
  | `ubuntu-latest` | linux/amd64 | `agentbus-<tag>-linux-amd64.tar.gz` |
  | `ubuntu-24.04-arm` | linux/arm64 | `agentbus-<tag>-linux-arm64.tar.gz` |
  | `windows-latest` | windows/amd64 | `agentbus-<tag>-windows-amd64.zip` |
  | `windows-11-arm` | windows/arm64 | `agentbus-<tag>-windows-arm64.zip` |

  Steps: check out the tag; `actions/setup-go` with `go-version-file:
  go.mod`; `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X
  github.com/ericfitz/agentbus/internal/mcpserver.Version=<version>"`; run
  the binary and require `agentbus version` to equal the version (the tag
  without `v`); package; `actions/upload-artifact`.
- **publish** job, `needs: build`, `permissions: contents: write,
  id-token: write, attestations: write`: `actions/download-artifact`;
  `actions/attest-build-provenance` with all four archives as subjects;
  `gh release upload <tag> <archives> --clobber` with `GITHUB_TOKEN`.
- **Hardening:** every action pinned to a full commit SHA (with the version
  in a comment); no secrets beyond `GITHUB_TOKEN`; only `publish` can write.
- The repository is public, so runners and the public Sigstore instance are
  free.

## Documentation

- `docs/install.md`: how to verify provenance with `gh attestation verify
  <file> --repo ericfitz/agentbus`, next to the existing signature check.
- CLAUDE.md: "Release builds for Linux and Windows run in Actions
  (`release-build.yml`); the Windows test workflow reports only; `make
  verify` is still the done gate."
- `release.sh` header comment describes the split (local macOS, Actions for
  the rest).

## Testing

- `actionlint` on `.github/workflows/*.yml`, added to `make
  release-check`.
- `shellcheck` on the changed `release.sh` (already in `make
  release-check`).
- First use is a dry run on a pre-release tag (`vX.Y.Z-rc.1`): run
  `release.sh` with a flag that stops before step 7 (`--no-publish`), check
  the draft's assets, `gh attestation verify` on each archive, and the
  `SHA256SUMS` signature; then delete the draft and the tag (an outward
  action the user approves at the time).

> **Implementation note (2026-10-07, controller ruling pending Eric's review):**
> The dry run uses the next stable `vX.Y.Z` tag with `release.sh
> --no-publish`, not a `vX.Y.Z-rc.1` tag: `release.sh` and `release-build.yml`
> accept stable tags only. The draft and the tag are deleted afterward.
