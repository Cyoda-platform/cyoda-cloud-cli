# Cyoda Cloud CLI

The user-facing command-line tool for Cyoda Cloud.
Authenticates via Auth0 PKCE or Device Authorization Grant; calls the
`cyoda-cloud-manager` v2 API with a bearer JWT; persists refresh tokens in
the OS keychain (or 0600 file fallback). Distributed via GoReleaser with
sigstore keyless signing.

## Development Gates

These are STOP-and-verify checkpoints. Do not proceed past a gate without completing it.

### Gate 1: TDD is mandatory
Do not write implementation code without a failing test driving it.
Use `superpowers:test-driven-development` skill for all feature and bugfix work.

### Gate 2: HTTP test coverage
When adding or changing user-facing behaviour (a new subcommand, a new flag,
a request/response shape, an exit-code mapping), add or update tests in
`internal/commands/*_test.go` that drive the command through an `httptest`
stub of Auth0 + the manager. Tests must partition stdout (data, JSON, table
output) from stderr (logs, status lines, error messages) per spec §6.5.

The test surface is self-contained — no Docker, no testcontainers, no
external services. The keychain test that requires a real OS keychain is
gated by `//go:build !ci`; the file-fallback test runs everywhere. Run
both forms before declaring done (Gate 5).

### Gate 3: Security by default
Never log access tokens, refresh tokens, code verifiers, state values,
idempotency keys, or any other secret at any level. The auth flow assumes
secrets only flow through `internal/auth` and `internal/keychain`; if
something leaks them outside those boundaries (e.g. an error string
embeds Auth0's `error_description` verbatim), that's a bug.

`internal/api/debug.go` redacts `Authorization`, `Cookie`, `Set-Cookie`,
and `Proxy-Authorization` headers when `CYODA_CLOUD_DEBUG=1` is set.
Never bypass that redaction. If you add a new sensitive header, extend
the redaction list in the same commit.

Validate input at boundaries: `--idempotency-key` length, env-name DNS-1123
rules in `internal/envname/`, discovery URL scheme allowlist
(`https://`/`file://` only — no cleartext `http://` without
`CYODA_CLOUD_INSECURE_DISCOVERY=1`). Server is authoritative — client-side
checks are best-effort early-fail.

### Gate 4: Documentation hygiene
When changing the command surface (commands, flags, exit codes, output
shape), update the relevant section in `docs/spec.md` (§3.6 scopes,
§4.x API, §6.x CLI) **in the same change**.

When bumping the vendored OpenAPI spec at `api/openapi/openapi.yaml`,
regenerate `internal/api/client.gen.go` and update the consuming
commands — the three live in lockstep. `go generate ./internal/api/...`
must produce zero diff after the change.

When changing distribution targets, update `.goreleaser.yaml`,
`.github/workflows/release.yml`, and `README.md` together.

### Gate 5: Verify before claiming done
Use `superpowers:verification-before-completion` skill before claiming work is complete.

Run all five before declaring green:

```
GOTOOLCHAIN=local go test -race -timeout 60s ./...
GOTOOLCHAIN=local go test -race -timeout 60s -tags ci ./...
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go generate ./internal/api/... && git diff --exit-code
```

The `-tags ci` form excludes the OS-keychain test that cannot run on a
headless runner. The `go generate` round-trip catches OpenAPI drift.

Do not claim work is done if any of these fails.

### Gate 6: Continuous improvement — resolve, don't defer
We strive for continual improvement of code quality and progressively reduce
technical debt. When you spot an issue — dead code, an outdated comment, a
weakened invariant, a missed test, a stale TODO — **resolve it now via
red/green TDD within reason**, do not delegate it to "later". The default is
to fix; deferring requires a reason (out of scope, architectural decision
needs human input, would balloon the change beyond reviewability).

"Within reason" means:
- Bounded in scope: the fix is comprehensible alongside the work that surfaced it.
- Reversible: small commits, each with its own failing test.
- Doesn't bypass other gates: still TDD, still reviewed, still verified.

If the fix is structural, requires a design decision, or would significantly
expand the change, **stop and surface the choice to the human** rather than
silently leaving it broken. Recording a `TODO(...)` is the last resort, not
the first response.

## Peer Repositories

- **cyoda-cloud-manager** — the v2 API server this CLI calls.
  - Local: `../cyoda-cloud-manager` (read-only via `.sandbox/profile.sb`).
  - `api/openapi/openapi.yaml` here is vendored from
    `<peer>/api/openapi.yaml`. When the manager's spec changes, re-vendor
    + regenerate + update commands in one CLI feature branch. Pre-1.0:
    no backwards-compat constraints — break the old shape cleanly.

The CLI does not directly depend on cyoda-go or its plugins. Workflow
state vocabulary the CLI's `--wait` poll observes (e.g. `Ready`,
`Mint_Failed`, `Bootstrap_Failed`, `Job_Failed`, `Job_Cancelled`,
`Env_Torn_Down`) comes from the manager's deploy workflow, not cyoda-go.

## Go Conventions

- Go 1.22+ (the module's `go` directive). Use `GOTOOLCHAIN=local` for all
  go commands so a newer host toolchain doesn't auto-bump the directive
  in `go.mod` / `go.sum`.
- Manual dependency injection via constructors. No DI frameworks.
- Wrap errors with context: `fmt.Errorf("failed to X: %w", err)`.
  Errors that should map to a specific spec §6.6 exit code are
  `*output.CLIError`; generic errors map to exit 1 via `output.Exit`.
  Refresh-expiry → `mapTransportError` → exit 3.
- Output discipline (spec §6.5): data → stdout, logs / progress / errors
  → stderr. Tests partition both streams. JSON tags on rendered structs
  use the API's snake_case (e.g. `env_id`, `env_name`).
- HTTP transport: no `http.DefaultClient`, no `InsecureSkipVerify`. Every
  external call uses a package-private `*http.Client` with explicit
  `Timeout` and body-size cap (`io.LimitReader`).
- Tokens at rest: OS keychain (preferred) or 0600 file fallback
  (`CYODA_KEYCHAIN_FILE_FALLBACK=1`). Access tokens are in-memory only;
  never written to disk.
- Idempotency: default `--idempotency-key` is per-invocation UUIDv4, min
  16 chars; user can override.
- Config: env vars for ad-hoc (`CYODA_CLOUD_DISCOVERY_URL`,
  `CYODA_CLOUD_LOOPBACK_PORT`, `CYODA_KEYCHAIN_FILE_FALLBACK`,
  `CYODA_CLOUD_DEBUG`, `CYODA_CLOUD_INSECURE_DISCOVERY`) and TOML at
  `~/.config/cyoda-cloud/config.toml` for persistent prefs (`default_org`,
  `output_format`, `discovery_url`). CLI flag > env var > config file >
  built-in default.
- Deferred work: `// TODO(plan-reference): description`. Last resort, not
  first response (Gate 6). Search with `grep -rn TODO`.

## Workflow

### New feature
brainstorming → writing-plans → subagent-driven-development → verification-before-completion → requesting-code-review → security-review → PR/merge

### Bugfix
test-driven-development → verification-before-completion → requesting-code-review → security-review → PR/merge

### Receiving review feedback
receiving-code-review

All workflow skills are in the `superpowers:` namespace.
Security review uses `antigravity-bundle-security-engineer:security-auditor`.

Do not skip steps. Brainstorming prevents building the wrong thing.
TDD prevents shipping untested code. Verification prevents false "done" claims.
Review and security audit prevent defects reaching main.

## Common Commands

Single Go module — no plugin submodules.

- Test: `GOTOOLCHAIN=local go test -race -timeout 60s ./...`
- Test (CI subset, no host keychain): `GOTOOLCHAIN=local go test -race -tags ci -timeout 60s ./...`
- Vet: `GOTOOLCHAIN=local go vet ./...`
- Build: `GOTOOLCHAIN=local make build` (produces `./bin/cyoda-cloud`)
- Regenerate API client: `GOTOOLCHAIN=local go generate ./internal/api/...` (run after re-vendoring `api/openapi/openapi.yaml`)
- Tidy: `GOTOOLCHAIN=local go mod tidy`
- Lint: `golangci-lint run` (CI runs it; install locally only to mirror)
- Validate release config: `goreleaser check`

### Local-dev round-trip

The CLI talks to a real Auth0 tenant + a locally-running cyoda-cloud-manager.

1. Author `~/.config/cyoda-cloud/local-discovery.json` with the dev tenant's
   Auth0 fields and `api_url: http://localhost:8080` (mirror the placeholder
   shape at `deploy/discovery/cyoda-cloud-cli.json`).
2. `export CYODA_CLOUD_DISCOVERY_URL=file:///Users/paul/.config/cyoda-cloud/local-discovery.json`
3. `make build`
4. `./bin/cyoda-cloud login --device && ./bin/cyoda-cloud whoami`
5. `CYODA_CLOUD_DEBUG=1 ./bin/cyoda-cloud env list` — full HTTP req/resp
   trace to stderr (Authorization redacted) for diagnosing 4xx/5xx.

See `docs/cli-handover.md` for the longer setup story including Auth0
native-app configuration and the `CYODA_CLOUD_LOOPBACK_PORT` knob.
