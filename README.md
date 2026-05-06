# cyoda-cloud-cli

Command-line interface for [Cyoda Cloud](https://cyoda.com). Authenticates via Auth0
(PKCE / Device Authorization Flow) and operates against the Cyoda Cloud `/v2/*` API.

This repository is in early bootstrap. See [`docs/spec.md`](docs/spec.md) for the
product specification and [`docs/plan.md`](docs/plan.md) for the implementation plan.

## Build & test

```bash
make build   # produces ./bin/cyoda-cloud
make test    # go test -race ./...
make lint    # golangci-lint run
```

Requires Go 1.22 or newer.

## Distribution

Releases are cut by pushing a `v*` tag; [GoReleaser](https://goreleaser.com)
publishes to the following channels:

- GitHub Release with `tar.gz` (Linux / macOS) and `zip` (Windows) archives
  for `amd64` and `arm64` (Windows: `amd64` only).
- Homebrew tap: `cyoda-platform/homebrew-tap`.
- Scoop bucket: `cyoda-platform/scoop-bucket`.
- `deb` and `rpm` packages for `amd64` and `arm64`.
- Container image: `ghcr.io/cyoda-platform/cyoda-cloud-cli:<tag>`
  (multi-arch manifest covering `linux/amd64` and `linux/arm64`).

The Homebrew and Scoop publish steps require the repo secrets
`HOMEBREW_TAP_GITHUB_TOKEN` and `SCOOP_TAP_GITHUB_TOKEN`. When unset, those
steps are skipped and the rest of the release still succeeds.

### Verifying release artefacts

Every artefact (archives, packages, checksums, container manifest) is signed
**keyless** with [Sigstore cosign](https://docs.sigstore.dev/cosign/overview/)
via GitHub OIDC; an SPDX SBOM is attached for each archive.

Verify a downloaded archive:

```bash
cosign verify-blob \
  --certificate cyoda-cloud-cli_<ver>_<os>_<arch>.tar.gz.pem \
  --signature  cyoda-cloud-cli_<ver>_<os>_<arch>.tar.gz.sig \
  --certificate-identity-regexp 'https://github.com/cyoda-platform/cyoda-cloud-cli/.*' \
  --certificate-oidc-issuer     https://token.actions.githubusercontent.com \
  cyoda-cloud-cli_<ver>_<os>_<arch>.tar.gz
```

Verify the container image:

```bash
cosign verify ghcr.io/cyoda-platform/cyoda-cloud-cli:<tag> \
  --certificate-identity-regexp 'https://github.com/cyoda-platform/cyoda-cloud-cli/.*' \
  --certificate-oidc-issuer     https://token.actions.githubusercontent.com
```

The accompanying `*.spdx.sbom.json` document lists every Go module shipped in
the archive.

## CI

- `ci.yml` — build, test, lint on every PR / `main` push.
- `release.yml` — runs GoReleaser on `v*` tag push; `id-token: write` is granted
  only on this tag-push trigger so the OIDC token is never available on PRs.
- `openapi-drift.yml` — daily and on PRs touching the API: fetches
  `https://api.cyoda.cloud/v2/.well-known/openapi.json`, regenerates the client,
  and fails if the diff isn't already committed. Tolerates an unreachable
  manager so `main` stays green until DNS is live.
