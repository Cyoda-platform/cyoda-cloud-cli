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

Requires Go 1.26 or newer (matching the `go` directive in `go.mod`).

## Configuration

User-facing environment variables. CLI flag > env var > config file > built-in default.

- `CYODA_CLOUD_DISCOVERY_URL` — override the discovery JSON URL.
  Accepts `https://` or `file://` schemes.
- `CYODA_CLOUD_INSECURE_DISCOVERY=1` — accept cleartext `http://` for
  the discovery URL (development only; ignored in normal operation).
- `CYODA_CLOUD_LOOPBACK_PORT=<port>` — port for the PKCE-login loopback
  callback server (default `42777`). The matching
  `http://127.0.0.1:<port>/callback` must be registered on the Auth0
  native application.
- `CYODA_KEYCHAIN_FILE_FALLBACK=1` — write refresh tokens to
  `~/.config/cyoda-cloud/credentials` (mode `0600`) instead of the OS
  keychain. Use on headless systems without a keychain daemon.
- `CYODA_CLOUD_DEBUG=1` — log redacted HTTP request/response traces to
  stderr. `Authorization`/`Cookie`/`Set-Cookie`/`Proxy-Authorization`
  headers are redacted; bodies are inlined up to 8 KiB.

Persistent preferences live in `~/.config/cyoda-cloud/config.toml`:

- `default_org` — Auth0 organization slug used when `--org` is omitted.
- `output_format` — `table` (default) or `json`; equivalent to passing
  `--output-json` on every command.
- `discovery_url` — alternative discovery URL; overridden by the env
  var of the same purpose (`CYODA_CLOUD_DISCOVERY_URL`).

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

## Discovery file deployment

The CLI ships with no baked-in API host. On first run it fetches
`https://cyoda.cloud/.well-known/cyoda-cloud-cli.json`, caches it for 24
hours, and uses the values to drive both the API base URL and the Auth0
client. The source-of-truth file lives in this repo at
`deploy/discovery/cyoda-cloud-cli.json`.

The team deploys this file via Cloudflare Pages (or whichever static-asset
host is in use). Path on the production host:
`/.well-known/cyoda-cloud-cli.json`.

When the Auth0 native client ID rotates or the API URL changes:
1. Edit `deploy/discovery/cyoda-cloud-cli.json` (the placeholder values
   `TENANT.eu.auth0.com` / `REPLACE_WITH_NATIVE_APP_CLIENT_ID` must be
   replaced before publication).
2. Open a PR; merge after review.
3. Roll out the static-asset host's deployment.
4. End users see the change after their 24-hour cache expires, or
   immediately by passing `--refresh-discovery` on any command.

The Auth0 client ID and tenant domain are public identifiers — there are no
secrets in this file.

### Local development override

For testing against a local manager:

- Set `CYODA_CLOUD_DISCOVERY_URL=file:///path/to/local/cyoda-cloud-cli.json`
  in the environment, or
- Run `cyoda-cloud config set discovery_url file:///path/to/local/cyoda-cloud-cli.json`.

Either path bypasses the 24h on-disk cache (file:// URLs are always
re-read).

## CI

- `ci.yml` — build, test, lint on every PR / `main` push.
- `release.yml` — runs GoReleaser on `v*` tag push; `id-token: write` is granted
  only on this tag-push trigger so the OIDC token is never available on PRs.
- `openapi-drift.yml` — daily and on PRs touching the API: fetches
  `https://api.cyoda.cloud/v2/.well-known/openapi.json`, regenerates the client,
  and fails if the diff isn't already committed. Tolerates an unreachable
  manager so `main` stays green until DNS is live.
