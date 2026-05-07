# Cyoda Cloud Manager (greenfield, Go) and `cyoda-cloud-cli` — Design

**Date:** 2026-05-04
**Status:** Draft (awaiting user review — revised after Go-expert review)
**Authors:** Paul Schleger (with Claude)
**Supersedes:** `2026-05-04-cloud-manager-v2-and-cyoda-cloud-cli-design.legacy-python.md`
**Depends on:** cyoda-go ≥ v0.7.0 (for `COMMIT_BEFORE_DISPATCH` execution mode — issue #27).

## 0. Why greenfield, why Go

The legacy Python `cloud-manager` was vibe-coded by people no longer on the team. Its Cyoda client calls endpoints that no longer exist in current `cyoda-go`; v1 auth decodes JWTs without signature verification; `services/deploy.py` mixes dead `Blueprint` registration, broken basic-auth code, and direct `quart.abort()` calls inside business logic. The team does not write Python.

Rather than refactor an unowned Python codebase to add a public-facing v2 surface, this design replaces it with a new Go service, **`cyoda-cloud-manager`**, built deliberately against current `cyoda help` documentation and the cyoda-go gRPC + REST contract.

### Why Go

- `cyoda-go` is Go. SDK ergonomics, gRPC processor types, and idiomatic patterns favour Go consumers.
- `cyoda-cloud-cli` is Go (already decided). One language across the entire Cyoda Cloud control plane.
- Compile-time type checking catches the exact class of drift that bit the legacy Python codebase (Cyoda API endpoint changes, processor signature changes).
- AI-assisted development with a strong type system gives free guard rails — the previous "code drifts silently against an external API" failure mode disappears.
- Single static binary in `FROM scratch` containers — minimal image, minimal attack surface.

### Legacy disposition

The legacy `cloud-manager` Python service stays running, internal-only, frozen at its current shape until the `ai` orchestrator (its sole consumer) is decommissioned. No new features, security patches only. When the orchestrator is gone, the legacy is archived. This design does not modify the legacy at all.

## 1. Goals and non-goals

### Goal

Build a new public-facing Cyoda Cloud control plane that lets authenticated end users — and AI agents acting on their behalf — manage their own Cyoda environments via a CLI. The service is greenfield Go, backed by a cyoda-go (≥ v0.7.0) instance with a Postgres sub-chart, and shares no code with the legacy Python `cloud-manager`.

### In scope

- A new HTTP API at `https://api.cyoda.cloud/v2/...`, exposed publicly via Cloudflare Tunnel → nginx-ingress.
- JWKS-verified Auth0 tokens. Identity, ownership, role, tier, and OAuth scope are derived from JWT claims set by existing post-login Actions.
- Tier-driven entitlement: which Cyoda backend types are selectable, how often the user can provision/teardown an environment.
- Ownership binding on `deploy` entities; list/get/cancel scoped to the caller's organisation.
- Forwarding the user's Auth0 access token to TeamCity pipelines as a password-typed property `cyoda_user_token`. Pipelines run cloud-manager-controlled Ansible playbooks against Cyoda-controlled VCS roots — no user code in the loop in v0.
- A separate Go CLI (`cyoda-cloud-cli` repo, binary `cyoda-cloud`).
- Backing cyoda-go instance (≥ v0.7.0) deployed as a sub-chart of the cyoda-cloud-manager Helm release. cyoda-go's chart requires Postgres (per `cyoda help helm`), so a small Postgres is also deployed as a sibling sub-chart.

### v0 deployment scope: env-only

App deploys (`POST /v2/builds`, `DELETE /v2/builds/{id}`, `POST /v2/builds/{id}:cancel`) are part of the v2 surface so the OpenAPI contract and the Go CLI are forward-compatible, but **the tier policy sets `deploy_app: false` for every tier in v0**. Mutating app-side calls return `403 tier-not-entitled`. Read-only `GET /v2/builds` is permitted.

App deploys run *user-controlled* repo content via TeamCity → Ansible → user image. Re-enabling `deploy_app: true` on any tier requires a professional repo-validation architecture **and** RFC 8693 token-exchange to a build-bound, audience-scoped token before the user JWT can safely flow through user code. Both are explicit prerequisites for the contract tier — see Section 11.

Env operations are safe in v0 because env pipelines run cloud-manager-controlled Ansible playbooks; there is no user code in the loop.

### Out of scope

- Modifying the legacy Python `cloud-manager`. It runs as-is until the `ai` orchestrator is gone.
- Per-org Cyoda credential store. User JWT forwarded; trade-off discussed in Section 9.
- M2M / CI auth path. Acknowledged in the auth model so it can be added later without changes; not built first.
- SPA frontend. Possible later on the same v2 API; not built now.
- RFC 8693 token-exchange. Required prerequisite for app-deploys; not built in v0 because v0 doesn't enable app-deploys.
- Per-user (versus per-org) namespaces. `caas_org_id` is the only namespace key.
- Email-verification gate. Auth0 tenant uses Google + GitHub social only; `email_verified` is always true.
- Pre-User-Registration Action, disposable-email blocking, invitation-only mode. Backlog.
- DB-connection signup. Not enabled in the tenant; intentional.
- Cassandra-backed control-plane Cyoda. v0 uses Postgres (cyoda-go's chart requirement); switching backends later is a chart-dependency change.

## 2. System topology

```
┌─────────────────┐                   ┌──────────────────────┐
│  cyoda-cloud    │   Auth0 OAuth     │       Auth0          │
│   CLI (Go)      │  ◀──────────────▶ │  (tenant + Actions   │
└────────┬────────┘   PKCE / Device   │   that inject        │
         │ Bearer <user JWT>          │   caas_* claims)     │
         │ HTTPS                      └──────────────────────┘
         ▼
┌─────────────────────────────────────────────────────────────┐
│                   Cloudflare Tunnel                          │
│              (api.cyoda.cloud — TLS, WAF, rate limit)        │
└────────┬────────────────────────────────────────────────────┘
         ▼
┌─────────────────────────────────────────────────────────────┐
│              nginx-ingress (single Ingress)                  │
│              host: api.cyoda.cloud, path: /v2/*              │
└────────┬────────────────────────────────────────────────────┘
         │ NetworkPolicy: ingress only from
         │ ingress-controller namespace
         ▼
┌─────────────────────────────────────────────────────────────┐
│              cyoda-cloud-manager (Go, single image)          │
│                                                              │
│   chi router                                                 │
│   middleware: request-id, slog, JWKS-auth, rate-limit       │
│   handlers (oapi-codegen): /v2/me, /v2/env, /v2/builds, ... │
│                                                              │
│   internal/                                                  │
│     auth/        JWKS, Principal, scope checks               │
│     cyoda/       client (REST + gRPC), search, processors   │
│     teamcity/    REST client, password-param triggering      │
│     k8s/         namespace ops                               │
│     tier/        policy loader + entitlement evaluator       │
│     quota/       direct-search-based usage counts           │
│     deploy/      orchestration of env up/down/status        │
└──────────┬──────────────────────────────────┬───────────────┘
           │ REST + gRPC                      │ TeamCity REST
           ▼                                  ▼
┌─────────────────────────┐         ┌────────────────────────┐
│  cyoda-cloud-manager-   │         │   TeamCity             │
│  cyoda  (cyoda-go ≥ v0.7│         │   v2 pipelines:        │
│   + Postgres sub-chart; │         │   - env-up-v2          │
│   compute member: the   │         │   - env-teardown-v2    │
│   manager opens an      │         │   each receives        │
│   outbound gRPC stream  │         │   cyoda_user_token     │
│   to startStreaming)    │         │   (password param)     │
└─────────────────────────┘         └──────────┬─────────────┘
                                               ▼
                                    user's Cyoda env in
                                    `client-<org_slug>-<hash>`
                                    (authorises the
                                    forwarded user JWT)
```

### Topology decisions

- **Single-purpose Pod.** No legacy v1 surface, no internal-only sibling. The Pod serves only the public v2 API. Authorization is uniform: every request must carry a JWKS-verified Auth0 JWT.
- **Single Ingress.** No "internal vs. external" split because the legacy is on its own legacy chart on its own internal hostname. cyoda-cloud-manager is exclusively the public service.
- **NetworkPolicy.** Default-deny ingress. Allow only from the namespace running the external nginx-ingress controller.
- **No in-app host guard.** Not needed — the Pod has no second surface to protect against.
- **Cloudflare** terminates TLS, applies WAF and per-IP rate limits to `api.cyoda.cloud`. Cloudflare Access (mTLS / service-token) is **not** applied — bearer-token authenticated. Cloudflare client-IP headers (`CF-Connecting-IP`, `True-Client-IP`) are trusted only when the source IP is the in-cluster nginx-ingress controller; otherwise stripped.
- **cyoda-cloud-manager-cyoda** (the control-plane Cyoda backend) stays internal. End-user JWTs never reach it; cyoda-cloud-manager uses its own M2M credential issued by the same cyoda-go instance at deploy time.
- **TeamCity** stays internal. Reached only by cyoda-cloud-manager. Pipelines reach the user's own Cyoda env in `client-<org_slug>-<hash>` using the forwarded `cyoda_user_token`.

## 3. Authentication and authorisation model

### 3.1 Layers, applied in order on every v2 request

1. **Authentication.** Token is a valid Auth0 JWT for our tenant and audience.
2. **Identity.** `Principal` extracted from JWT claims.
3. **Scope.** Required OAuth scopes are present on the token.
4. **Ownership.** For operations on existing entities, `entity.OwnerOrgID == principal.OrgID` (employee/admin bypass possible).
5. **Tier entitlement.** Tier policy permits the action and any backend selection.
6. **Quota.** Per-window action count below the tier limit.

If any layer fails the request is rejected with an RFC 7807 Problem Document. Returns 404 (not 403) on ownership failure to avoid leaking entity existence.

### 3.2 Token verification

`internal/auth/jwks.go` exposes:

```go
type Principal struct {
    Kind            string                 // "user" | "m2m"
    UserID          string                 // caas_user_id (empty for m2m)
    OrgID           string                 // caas_org_id
    Tier            string                 // caas_tier
    Roles           map[string]struct{}    // user_roles
    IsCyodaEmployee bool                   // caas_cyoda_employee
    Scopes          map[string]struct{}    // OAuth scopes from `scope` claim
    RawClaims       map[string]any         // for audit logging
}

func RequireAuth(scopes ...string) func(http.Handler) http.Handler
```

`RequireAuth`:

1. Reads `Authorization: Bearer <jwt>`.
2. Verifies the signature against Auth0 JWKS using `github.com/MicahParks/keyfunc/v3` with refresh interval 10 m, refresh-on-unknown-kid `true`, **`RefreshRateLimit=5*time.Second`** (ratelimits unknown-kid refresh attempts so a hostile JWT-replay flood can't DOS Auth0 or the manager), `RefreshTimeout=10*time.Second`, and an in-process stale-keys-acceptable window of 5 minutes (if JWKS is unreachable but the cached key still matches the token's `kid`, accept; if `kid` is unknown and refresh fails, reject). **Explicit `RS256`-only** algorithm allowlist on the `jwt.Parse` call. Algorithm-confusion / `kid` injection / `alg=none` rejected by construction.
3. Verifies `iss` (allow-listed Auth0 tenants), `aud == https://api.cyoda.cloud`, `exp`/`nbf`/`iat` with 60 s leeway.
4. Builds a `Principal`. Presence of `caas_user_id` ⇒ kind `user`. `gty=client-credentials` without user claims ⇒ kind `m2m`.
5. Enforces required scopes (subset check against `principal.Scopes`).
6. Stores `principal` in `request.Context()` for handlers to retrieve via `auth.PrincipalFromContext(ctx)`.

Negative tests cover: HS256-signed-with-public-key, no `kid`, unknown `kid`, `alg=none`, expired-with-leeway-edge, missing `caas_org_id`, mismatched `aud`, mismatched `iss`. Tests run on every CI build.

### 3.3 Identity from JWT, namespaces from `caas_org_id`

v2 request bodies never carry user identity fields. Namespaces derive from `principal.OrgID` via `internal/namespace`:

```go
// DeriveNamespace returns a DNS-1123-valid label of the form
// "<prefix>-<slug>-<hash>". Returns ErrInvalidOrgID if the resulting
// label fails k8s validation.IsDNS1123Label.
func DeriveNamespace(prefix, orgID string) (string, error)
```

Algorithm:
1. Lowercase `orgID`.
2. Replace each rune not in `[a-z0-9-]` with `-` (per-rune, not per-byte; invalid UTF-8 sequences map to a single `-`).
3. Strip leading and trailing `-`.
4. **Slug budget = `63 - len(prefix) - 1 - 1 - 8`** (separators + 8-hex hash). For `prefix="client-app"` (10 chars), slug budget = 43; for `prefix="client"` (6 chars), slug budget = 47. Truncate to budget; right-strip `-` again post-truncation.
5. Compute `hash := hex(sha256(orgID))[:8]`.
6. Compose `<prefix>-<slug>-<hash>`.
7. Validate via `k8s.io/apimachinery/pkg/util/validation.IsDNS1123Label`. On failure, return `ErrInvalidOrgID` (handler maps to `400 validation-error` with `type=invalid-org-id`, never 5xx).

Result form: `client-<slug>-<8hex>` and `client-app-<slug>-<8hex>`. The hash suffix is the disambiguator (collisions on slug alone are real — `org_a.b`, `org-a-b`, `org/a/b` all slug to `org-a-b`); slug provides readability.

DNS-1123 *labels* allow leading digits (only RFC 1035 *hostnames* require leading letter). The validation function from `k8s.io/apimachinery` is the authoritative check; do not roll a regex.

Fuzz tests cover: empty `orgID` post-slug, all-`-`, leading `_`, single char, exactly 63 chars, surrogate-pair UTF-8, real Auth0 shapes (`org_*`, `auth0|*`, `google-oauth2|*`, `github|*`).

### 3.4 Tier policy

Loaded from a ConfigMap mounted at `/config/tier-policy.yaml`. Pod-restart on change (no hot reload in v0).

```yaml
tiers:
  free:
    deploy_env: true
    deploy_app: false              # v0: app-deploys disabled across all public tiers
    backends: ["cassandra-basic"]
    env_deploys_per_day: 1
    app_deploys_per_day: 0
  pro:
    deploy_env: true
    deploy_app: false              # see Section 11 for prerequisites
    backends: ["cassandra-basic", "cassandra-replicated"]
    env_deploys_per_day: 3
    app_deploys_per_day: 0
  enterprise:
    deploy_env: true
    deploy_app: false              # see Section 11 for prerequisites
    backends: ["*"]
    env_deploys_per_day: -1
    app_deploys_per_day: 0
default:
  deploy_env: false
  deploy_app: false
  backends: []
  env_deploys_per_day: 0
  app_deploys_per_day: 0
```

`-1` = unlimited. Unknown tier or missing `caas_tier` claim ⇒ `default` (deny). `IsCyodaEmployee` skips tier and quota checks but still records `OwnerOrgID` from the (possibly impersonated) JWT.

**`deploy_app: true` is gated.** A startup check refuses to start the Pod (readiness fails) if the loaded tier policy contains `deploy_app: true` on any tier unless the env var `TOKEN_EXCHANGE_ENABLED=true` is set. A CI check on `tier-policy.yaml` enforces the same constraint at change time. Mechanical guard against accidental enablement before Section 11 prerequisites are met.

### 3.5 Quota — derived via direct search

Quota counts are computed at request time using `cyoda-go`'s synchronous direct search endpoint `POST /search/direct/{entity}/{ver}` (per `cyoda help search`). Direct search is single-RTT NDJSON streaming, default limit 1000, max 10000 — adequate for per-day quota windows and idempotency lookups.

The query body composes:

- `GroupCondition AND` containing:
  - field condition `$.owner_org_id EQUALS <caas_org_id>`
  - field condition `$.api_version EQUALS "v2"` (excludes any future v1-shaped writes that may end up in this backend)
  - optional `$.pipeline_name IN [...]` (env pipelines for `env_deploys_per_day`; app pipelines for `app_deploys_per_day`; absent for "all")
  - `LifecycleCondition field=creationDate operatorType=GREATER_OR_EQUAL value=<now - window>`

Build and deploy on the app side share `app_deploys_per_day` (variations of the same intent). Teardown pipelines are excluded from the `pipeline_name` filter so teardown actions implicitly do not count against quota. On overrun: 429 with `Retry-After` derived from the oldest counted entity's `creationDate`.

**Concurrency is best-effort.** Two concurrent POSTs that both observe `count == limit - 1` will both succeed and write `count == limit + 1`. Accepted v0 limitation; quota is informational, not a hard isolation boundary. Documented in Section 9.

**Cost amplification.** Each rejected request still pays the search cost. A per-principal in-process token-bucket rate limiter (`golang.org/x/time/rate`) sits in front of the quota check (10 req/s burst, refill 1 req/s), keyed on `(caas_user_id, route)`. Per-Pod, not coordinated across replicas; effective limit under HPA is `N × bucket`. Documented as v0 acceptance.

**Schema-locking note.** `cyoda help models` + `cyoda help errors` confirm that searches by JSONPath against fields not in the locked schema return `errors.INVALID_FIELD_PATH (400)`. The `deploy` entity model registered at deploy time **must** declare `owner_org_id`, `owner_user_id`, `api_version`, `idempotency_key`, `request_hash` as part of its locked schema (with `changeLevel=STRUCTURAL` for forward-compat), and at least one canonical entity must be ingested to populate the schema before v2 search paths are enabled. Step 2 of Section 8 covers this.

### 3.6 Scope vocabulary

| Scope | Endpoints |
|---|---|
| `read:builds` | `GET /v2/me`, `GET /v2/builds`, `GET /v2/builds/{id}`, `GET /v2/env` |
| `deploy:env` | `POST /v2/env` |
| `cancel:env` | `POST /v2/env:cancel` |
| `delete:env` | `DELETE /v2/env` |
| `deploy:app` | `POST /v2/builds` (action=build or deploy) — **v0: tier-blocked** |
| `cancel:app` | `POST /v2/builds/{id}:cancel` — **v0: tier-blocked** |
| `delete:app` | `DELETE /v2/builds/{id}` — **v0: tier-blocked** |
| `admin:all` | implies all of the above |

`cyoda-cloud-cli` requests the union of all non-admin scopes by default. Agent-mode (post-v0) narrows.

### 3.7 M2M

M2M tokens require an Auth0 Client Credentials Exchange Action (separate from the post-login Action) injecting `caas_org_id` from the M2M client's `client_metadata`. Not built in v0; the auth code paths already accept `kind="m2m"` so adding the Action later requires no service-side change.

## 4. v2 API surface

- **Host:** `api.cyoda.cloud` (final name TBD with ops; placeholder).
- **Prefix:** `/v2/...`. Kept on `v2` because the CLI and OpenAPI spec are already drafted around it; the discovery file (Section 6.7) decouples the CLI from the prefix anyway.
- **Versioning:** strict-semver — additive only; breaking change ⇒ `v3` in parallel; ≥6-month sunset notice.
- **Media types:** `application/json` for success, `application/problem+json` (RFC 7807) for errors.
- **Naming:** `snake_case` everywhere on the wire (Go structs use exported camelCase; `oapi-codegen` handles the mapping).
- **Identifiers:** all resource IDs are cyoda-cloud-manager's own opaque ids (the Cyoda entity id), not TeamCity build_ids.
- **Body limits:** 256 KiB on POSTs (enforced at nginx-ingress + at chi `http.MaxBytesReader` in the body decoder).
- **CORS:** `/v2/*` does not return `Access-Control-Allow-Origin`. v0 has no browser caller.

### 4.1 Headers

- `Authorization: Bearer <jwt>` — required on all paths except `/v2/.well-known/*`.
- `Idempotency-Key: <opaque>` — supported on every `POST` that creates state. Same key replays the original `build_id`/`env_id`. Minimum 16 chars of entropy.
- `Cyoda-Cloud-CLI-Version: <semver>` — sent by the CLI; server uses it for audit logs and the soft-deprecation `min-version` check.
- `User-Agent: cyoda-cloud-cli/<ver> (<os>)` — same purpose, also useful for abuse heuristics.

### 4.2 Endpoints

```
GET    /v2/me                                # identity, org, tier, scopes, quota usage

POST   /v2/env                               # provision env for caller's org
GET    /v2/env                               # current env state for caller's org
POST   /v2/env:cancel                        # cancel an in-flight env provision
DELETE /v2/env                               # tear down provisioned env

POST   /v2/builds                            # v0: 403 tier-not-entitled for all tiers
GET    /v2/builds                            # list builds for caller's org (cursor-paginated)
GET    /v2/builds/{build_id}                 # one build (status, statistics)
POST   /v2/builds/{build_id}:cancel          # v0: 403 tier-not-entitled
DELETE /v2/builds/{build_id}                 # v0: 403 tier-not-entitled

GET    /v2/.well-known/openapi.json          # machine-readable spec
GET    /v2/.well-known/cli-min-version       # {"min": "0.4.0"} — for soft-deprecation
```

### 4.3 Key shapes

**`GET /v2/me`**

```json
{
  "user_id": "<caas_user_id>",
  "org_id": "<caas_org_id>",
  "tier": "pro",
  "roles": ["USER", "SUPER_USER"],
  "scopes": ["read:builds", "deploy:env", "cancel:env", "delete:env"],
  "is_cyoda_employee": false,
  "quota": {
    "env_deploys": { "window": "24h", "used": 0,  "limit": 3 },
    "app_deploys": { "window": "24h", "used": 0,  "limit": 0 }
  },
  "features": { "deploy_app": false }
}
```

**`POST /v2/env`** — provision env

```json
// request
{ "backend": "cassandra-basic", "chat_id": "..." }

// response 202
{ "env_id": "<entity_id>", "namespace": "client-<org_slug>-<hash>", "state": "PROCESSING" }
```

`backend` must be in `tier_policy[tier].backends`. Server maps `backend` → pipeline name via `CYODA_ENV_PIPELINE_BACKEND_MAP`.

**`POST /v2/builds`** — v0: returns `403 tier-not-entitled` for every public tier. The endpoint shape is reserved:

```json
// future request (not accepted in v0)
{
  "action": "build" | "deploy",
  "repository_url": "https://github.com/.../...",
  "branch_name": "main",
  "chat_id": "...",
  "installation_id": "...",
  "is_public": false
}
```

**Tier check is the outermost handler step after authentication** — before request body decoding, before idempotency lookup, before any Cyoda call. A `RequireTier(action)` middleware sits between `RequireAuth` and the route handler. Defends against authenticated callers triggering Cyoda searches on tier-blocked endpoints.

When enabled (future contract tier), no `user_name` / `cyoda_client_id` / `cyoda_client_secret` will be accepted. Identity and namespaces derive from the JWT. Token-handling will be RFC 8693 token-exchange to a build-bound, audience-scoped, ~10-minute token — not direct forwarding.

**`GET /v2/builds`** — list with cursor pagination

```
GET /v2/builds?limit=50&cursor=<opaque>&action=deploy&state=SUCCESS
```

```json
{
  "items": [
    { "build_id": "...", "action": "deploy", "state": "SUCCESS",
      "created_at": "2026-05-04T10:11:12Z", "branch_name": "main", "...": "..." }
  ],
  "next_cursor": "<opaque-or-null>"
}
```

**Pagination uses `cyoda-go`'s async-snapshot search** for stable ordering. Per `cyoda help search`, async results are a snapshot at job submission with `pageNumber/pageSize`. The cursor encodes `(jobId, pageNumber, pageSize)` opaquely; **`pageSize` is server-pinned and embedded in the cursor** so that a client that switches `limit=` mid-pagination doesn't cause silent slice mismatches. Snapshot lifetime is 24 h (`expirationDate = createTime + 24h` per `cyoda help search`); on expiry the CLI gets `409 cursor-expired` and re-issues without cursor.

The ownership filter (`owner_org_id == principal.OrgID`) is **always re-injected from the principal on every page** by re-creating the snapshot if the cursor is foreign or malformed. Forging a cursor cannot leak rows from another org. Filters: `action`, `state`, `branch_name`, `since`.

Direct search is preferred for the non-paginated reads (`GET /v2/me` quota counts, idempotency lookups) because it's single-RTT.

**`GET /v2/builds/{build_id}`** — single

Returns the same fields as a list item plus `status`, `status_text`. **TeamCity-sourced free-text fields (`status_text`, `statistics`) are normalised through an explicit allow-list** of canonical strings (`pipeline-failed`, `cancelled`, `timed-out`, ...) plus an opaque `incident_id` for support. Raw TeamCity error strings are never echoed to v2 callers — defends against leaking internal URLs / agent hostnames.

If state isn't terminal, fires the `check_job_state` workflow transition — debounced to at most once per ~10 s per entity via an in-process LRU keyed on `entity_id`.

**`POST /v2/builds/{id}:cancel`** — v0: 403 tier-not-entitled.

**`DELETE /v2/builds/{id}`** — v0: 403 tier-not-entitled.

**`DELETE /v2/env`** — triggers `CYODA_ENV_TEARDOWN_PIPELINE_V2`. Returns 202. Refused with 409 if any user-app is still deployed in the org's app namespace; problem document includes a remediation hint. (In v0 there cannot be a deployed app, but the check stays defensively in.)

**`POST /v2/env:cancel`** — symmetrical to env teardown.

### 4.4 Error shape (RFC 7807)

```json
{
  "type": "https://docs.cyoda.cloud/errors/tier-not-entitled",
  "title": "Subscription tier does not allow this action",
  "status": 403,
  "detail": "App deployment is not yet available on your tier. Run cyoda-cloud env up to provision your environment; contact Cyoda to enable app deploys.",
  "instance": "/v2/builds",
  "tier": "free"
}
```

Common `type` values: `unauthenticated`, `forbidden`, `not-found`, `tier-not-entitled`, `quota-exceeded`, `idempotency-conflict`, `cursor-expired`, `validation-error`, `invalid-org-id`, `upstream-failure`, `revoked`. CLI maps `type` → exit code (Section 6.6).

### 4.5 Idempotency

- `Idempotency-Key` is opaque, max 200 chars, **min 16 chars of entropy** (rejected with `validation-error` otherwise).
- Stored on the `deploy` entity as `idempotency_key`.
- Match key on `(owner_user_id, owner_org_id, idempotency_key)` — narrowed so user-A in org X cannot hijack user-B's in-flight build by reusing a key.
- A `request_hash` (SHA-256 of the canonical JSON body) is persisted alongside. POST replay with same key + same hash → return original `build_id`/`env_id` and 200. Same key + different hash → 409 `idempotency-conflict`.
- Concurrent first-time submits with the same key are racy in v0. Mitigation: an in-process `singleflight.Group` keyed on `(owner_user_id, owner_org_id, idempotency_key, request_hash)` handles the single-replica case. **The `request_hash` must be in the key** — otherwise concurrent submissions with different bodies would all see the leader's result, leaking the body across distinct logical operations. HPA accepts the residual race across replicas and relies on post-hoc reconciliation (next status-poll converges). Documented in Section 9.
- Keys honoured for 24 h after the entity reaches a terminal state. Garbage-collection by a future janitor; until then keys are effectively retained indefinitely — accepted.

### 4.6 Intentionally not in v0

- No streaming/SSE — `--wait` polls.
- No log-tail endpoint — TeamCity log surfacing is a later feature.
- No `/v2/orgs/{id}/...` shape — caller's org is implicit.

## 5. Server-side implementation

### 5.1 Module layout

```
cyoda-cloud-manager/
├── cmd/server/                    # main.go: wire dependencies, start chi server + compute member
├── internal/
│   ├── auth/                      # JWKS verification, Principal, scope checks, redactedToken type
│   ├── cyoda/
│   │   ├── rest/                  # REST client (search/direct, search/async, entity CRUD)
│   │   ├── compute/               # compute-member gRPC client + processor dispatch table
│   │   ├── conditions/            # GroupCondition / LifecycleCondition builders (pure)
│   │   └── model/                 # Deploy entity Go types + SAMPLE_DATA schema registration
│   ├── namespace/                 # DeriveNamespace + IsDNS1123Label (not Cyoda-specific)
│   ├── teamcity/                  # REST client; password-param triggering
│   ├── k8s/                       # namespace ops (env up/down side effects)
│   ├── tier/                      # ConfigMap loader + entitlement evaluator + startup gate
│   ├── quota/                     # direct-search-based usage counting
│   ├── deploy/                    # orchestration: env up/down/status; ownership enforcement
│   ├── api/                       # generated by oapi-codegen from openapi.yaml (committed)
│   ├── handlers/                  # one file per resource: me, env, builds, well_known
│   ├── middleware/                # request_id, logging, recover, ratelimit, RequireTier
│   ├── problem/                   # RFC 7807 builders + error → problem mapping
│   └── version/                   # build-time version info
├── api/openapi.yaml               # source of truth — generates Go server stubs and CLI client
├── deploy/
│   ├── chart/                     # Helm umbrella: cyoda-cloud-manager + cyoda-go + postgres sub-charts
│   │   ├── Chart.yaml
│   │   ├── values.yaml
│   │   ├── charts/
│   │   │   ├── cyoda-go/          # vendored sub-chart, gateway+ingress disabled
│   │   │   └── postgres/          # small Postgres for cyoda-go (CloudNativePG or single-pod PVC)
│   │   └── templates/
│   │       ├── deployment.yaml
│   │       ├── service.yaml
│   │       ├── ingress.yaml
│   │       ├── networkpolicy.yaml
│   │       ├── configmap-tier-policy.yaml
│   │       ├── job-cyoda-bootstrap.yaml   # SAMPLE_DATA model import + lock + STRUCTURAL
│   │       └── secret-cyoda-m2m.yaml      # external-secret reference
│   └── docker/Dockerfile          # FROM gcr.io/distroless/static-debian12:nonroot
├── test/
│   ├── integration/               # spins up cyoda-go + Postgres testcontainers
│   └── contract/                  # schemathesis against the served openapi
├── go.mod
└── README.md
```

### 5.2 Generated handlers, hand-written orchestration

`oapi-codegen` generates (a) the Go server interface from `api/openapi.yaml` and (b) the CLI's Go client (in the cli repo) from the same spec. The server's `internal/handlers` package implements the generated interface; orchestration logic lives in `internal/deploy`, `internal/quota`, etc., and is unit-tested independently of HTTP. The middleware chain is:

```
chi.Router →
  RequestID →
  StructuredLog (slog) →
  Recoverer →
  MaxBytesReader(256 KiB) →
  RequireAuth(scopes...)        ← outermost auth check
  RequireTier(action)            ← outermost tier check (where applicable)
  RateLimit(per-principal)       ← before any Cyoda call
  RequireIdempotencyKey(action)  ← decodes header; idempotency persisted by the handler
  → handler(generated stub)
```

The chain ordering is non-negotiable. `RequireAuth` runs first so an unauthenticated attacker can't force body parsing. `RequireTier` runs immediately after so a tier-blocked endpoint never triggers a Cyoda search (or any further work). `RateLimit` runs after the tier check so quota-search load is bounded; `RequireIdempotencyKey` runs last so it can read the (validated) body to compute `request_hash`.

### 5.3 `deploy` entity — locked schema

Per `cyoda help models`, the schema-registration path is **SAMPLE_DATA import**: `POST /api/model/import/JSON/SAMPLE_DATA/{entityName}/{modelVersion}` with a concrete sample entity (real values for every field, no `null`s, no JSON-Schema unions). The importer walks the document and infers a typed schema. After import, the `job-cyoda-bootstrap.yaml` Helm Job calls `PUT .../lock` and then `POST /api/model/{name}/{version}/changeLevel/STRUCTURAL`. **No seed entity is created** in the data store; the SAMPLE_DATA call registers the schema only. The Job is idempotent — on rerun it detects an already-locked model and exits 0.

`STRUCTURAL` change-level allows additive schema extension automatically when a future entity ingestion includes new fields (cyoda-go applies `schema.Diff` and `ModelStore.ExtendSchema` during ingest). No model-version bump is required for additive changes.

Sample entity posted to SAMPLE_DATA — every field a concrete string (or one concrete object) so every JSONPath the v2 search builders compose is registered:

```json
{
  "pipeline_name":   "client-env-cassandra-basic",
  "build_id":        "0",
  "owner_org_id":    "org_bootstrap_seed",
  "owner_user_id":   "user_bootstrap_seed",
  "api_version":     "v2",
  "idempotency_key": "bootstrap_seed_idempotency_key_x",
  "request_hash":    "0000000000000000000000000000000000000000000000000000000000000000",
  "cyoda_namespace": "client-bootstrap-seed-deadbeef",
  "properties":      {},
  "job_status":      "PROCESSING",
  "job_status_text": "bootstrap seed",
  "job_state":       "PROCESSING",
  "statistics":      {}
}
```

The bootstrap Job runs a regression query immediately after lock + STRUCTURAL: a canonical quota search (`owner_org_id == "<probe>" AND creationDate >= <recent>`) against the empty data store, asserting it returns zero rows without `INVALID_FIELD_PATH`. If the query 400s, the Job exits non-zero — Helm install rolls back, an operator inspects the schema.

No `external_dispatch_key` or `scheduled_at` field — the entity ID and `creationDate` lifecycle metadata serve those roles.

### 5.4 Cyoda client

`internal/cyoda/client.go` calls current cyoda-go endpoints per `cyoda help`:

- **Direct search** (`POST /search/direct/{entity}/{ver}`) — used for quota counts and idempotency lookups. NDJSON streaming consumed line-by-line.
- **Async search** (`POST /search/async/{entity}/{ver}`, `GET /search/async/{jobId}`, `/status`) — used for paginated list endpoints only, where result sets may exceed the direct-search limit (1000).
- **Entity CRUD** (`POST /api/entity/{entity}/{ver}`, `GET /api/entity/{entity}/{ver}/{id}`, `PUT .../transitions/{name}`) — used for create, get, transition.
- **Conditions** are constructed by `internal/cyoda/conditions.go` builders (`AndConditions(...)`, `LifecycleCondition(...)`, `FieldCondition(...)`) producing the JSON shape current cyoda-go expects. No legacy snapshot endpoints; no `_embedded.objectNodes` parsing.

Reference: `cyoda help search`, `cyoda help crud`, and `https://docs.cyoda.net/openapi/openapi.json`.

### 5.5 gRPC compute member and processors

The manager is a **compute-member client** of cyoda-go's gRPC server, not a server itself. Per `cyoda help grpc`: external workflow processors subscribe via `CloudEventsService.startStreaming`, sending `CalculationMemberJoinEvent` first with their tags, then receiving processor and criteria calculation requests on the inbound side and sending responses on the outbound side of the bidirectional stream.

`internal/cyoda/compute_member.go` (not `grpc.go` — naming reflects role):

1. Opens a long-lived gRPC client stream to `cyoda-go:9090` authenticated with the manager's M2M bearer.
2. Sends `CalculationMemberJoinEvent` with tags advertised by the v2 workflow's processors (e.g. `cloud-manager-env`).
3. Dispatches inbound `EntityProcessorCalculationRequest` events via a `processorName` switch to the four handlers below, each producing an `EntityProcessorCalculationResponse`.
4. Sends `EventAckResponse` back over the stream.
5. Owns reconnect/backoff and the 30 s `CYODA_KEEPALIVE_TIMEOUT` budget — server pings every 10 s, drops after 30 s of silence, client must reconnect with exponential backoff.

Routing of which compute member handles which processor is via `WorkflowDefinition.processors[].config.calculationNodesTags`, not by the manager listening on a port.

**Workflow JSON** is authored against the current cyoda-go `WorkflowDefinition` shape (`initialState`, `states`, `transitions[].next`, `processors[].type=EXTERNAL`). No legacy translation layer.

#### 5.5.1 Execution model: `COMMIT_BEFORE_DISPATCH` (cyoda-go v0.7.0 contract)

cyoda-go v0.7.0 introduces `executionMode = COMMIT_BEFORE_DISPATCH` (issue #27) as a per-processor sibling enum on `ExternalizedProcessorDefinitionDto`, alongside `SYNC`, `ASYNC_SAME_TX`, `ASYNC_NEW_TX`. The engine commits the entity in its pre-callout state in TX_pre, dispatches the processor outside any transaction, then opens TX_post on processor return and applies returned mutations to the entity *as it stood at TX_pre commit*. **Returned entity mutations are applied — not discarded.**

The contract was confirmed by the cyoda-go implementors and is pinned here as the basis the manager design depends on:

- **TX_post conflict.** If a concurrent transaction modified the entity between TX_pre and TX_post, TX_post fails once with `ErrConflict` (409 retryable); cascade halts; entity remains durable in pre-callout state. **No engine retry, no engine-side compensation transition.** Application-layer is responsible for cleanup of any external side-effects already initiated. The manager addresses this with the `Queued` state design (§5.5.3) and the orphan-reconciliation CronJob (§5.5.4).
- **No engine re-dispatch.** If the dispatched call fails (timeout, compute-member crash, partition), TX_post never opens, entity remains in pre-callout state, cascade halts. Recovery is *client-side*: the caller re-issues the original API call, restarting the cascade with a fresh dispatch ID. Each retry is a fresh dispatch — dispatch IDs are not stable across retries. Processors that need deduplication must do it on application-meaningful keys (e.g. `cyoda_entity_id` searched via TeamCity locator), not on the dispatch ID.
- **Processor `success=false`.** Same as `SYNC` failure: `WORKFLOW_FAILED` (400), entity stays in pre-callout, cascade halts. No automatic compensation. The manager surfaces the failure via `GET /v2/env` reading the entity's pre-callout state.
- **Idempotency-skip return shape.** `success=true, payload.data=null` preserves the entity's `data` field but **still commits TX_post** with the new state and a new transaction id. There is no engine-side "no-op success" that skips TX_post entirely. For true no-ops (no version bump, no state change), the right pattern is a pre-dispatch transition criterion that refuses the transition. The manager uses transition criteria for state-progression routing (`$.job_status == "SUCCESS"` etc.) but *not* for idempotency dedup, because the natural dedup point (entity has a `build_id` already) only exists after the first successful dispatch.
- **`responseTimeoutMs`.** Per-processor wall-clock budget for the dispatched call between `TX_pre.Commit` and `TX_post.Begin`. Same field, same semantics as for `SYNC` mode.
- **`startNewTxOnDispatch`.** Separate processor config attribute, defaults to `false`. Left at default.

**Idempotency requirement.** Because the engine doesn't re-dispatch, idempotency is needed primarily for **client-driven retry** (CLI re-issues `POST /v2/env` after a transport failure or after manual operator intervention). The HTTP layer's `Idempotency-Key` (§4.5) handles request-level replay; the processor-level idempotency check uses `cyoda_entity_id` as a TeamCity locator (§5.5.2).

#### 5.5.2 The four processors

| Processor | executionMode | responseTimeoutMs | Idempotency strategy |
|---|---|---|---|
| `process_schedule_teamcity_job` | `COMMIT_BEFORE_DISPATCH` | 30000 | **Pre-trigger TeamCity locator search:** `GET /app/rest/builds?locator=property:(name:cyoda_entity_id,value:<id>)`. If found, return `success=true, payload.data={build_id: <recovered>, job_state: "PROCESSING"}` without triggering. Else trigger, stamp `build_id`, return same shape. Recovers cleanly on client retry after compute-member crash. |
| `process_check_job_state` | `COMMIT_BEFORE_DISPATCH` | 5000 | Naturally idempotent (read-only on TeamCity). 10 s in-process LRU debounce per entity in the manager. Under HPA the per-Pod LRU is insufficient — see §11.2. |
| `process_cancel_teamcity_job` | `SYNC` | 5000 | TeamCity cancel is idempotent (cancelling a finished/cancelled build is a no-op). **Plus:** the processor performs a TeamCity locator search by `cyoda_entity_id` regardless of whether `build_id` is on the entity, to cancel any build orphaned by a prior schedule-vs-cancel TX_post conflict. |
| `process_env_teardown` | `SYNC` | 10000 | Precondition check (app namespace empty) is in-process and synchronous. Teardown trigger is idempotent if the playbook is idempotent. |

`process_schedule_teamcity_job` Go shape:

```go
func (s *server) processScheduleTeamCityJob(ctx context.Context, req *cyoda.EntityProcessorCalculationRequest) (*cyoda.EntityProcessorCalculationResponse, error) {
    entityID := req.EntityID
    // Idempotency: search TeamCity for a build already triggered for this entity.
    existing, err := s.teamcity.FindBuildByEntityID(ctx, entityID)
    if err != nil {
        return ackFailure(err), nil
    }
    if existing != nil {
        return ackOKWithData(map[string]any{
            "build_id":  existing.ID,
            "job_state": "PROCESSING",
        }), nil
    }
    // Fresh trigger: stamp cyoda_entity_id property so future retries find this build.
    result, err := s.teamcity.Trigger(ctx, req.Entity["pipeline_name"].(string), req.Entity["properties"], entityID)
    if err != nil {
        return ackFailure(err), nil
    }
    return ackOKWithData(map[string]any{
        "build_id":  result.BuildID,
        "job_state": "PROCESSING",
    }), nil
}
```

`ackOKWithData` produces an `EntityProcessorCalculationResponse` with `success=true` and `payload.data` containing only the fields to merge; the engine merges these into the entity at TX_post. `ackFailure` produces `success=false` with the error message in the response — the engine halts the cascade and leaves the entity in pre-callout state.

#### 5.5.3 Workflow definition (with `Queued` entry state)

Splits "create durable application state" from "call external system." HTTP handler creates the entity directly in `Queued` (a plain entity-create with no processor); the engine then automatically fires `dispatch_teamcity_job` from `Queued` because it's `automated=true`.

States: `Queued` → `Job_Scheduled` → terminal (`Job_Successful` | `Job_Failed` | `Job_Cancelled` | `Env_Torn_Down`). `Job_Cancelled` reachable from both `Queued` and `Job_Scheduled`.

Transitions:

| Transition | start → end | automated | Processor | executionMode | responseTimeoutMs | Notes |
|---|---|---|---|---|---|---|
| *(entity create)* | — → `Queued` | n/a | — | — | — | HTTP `POST /v2/env` handler creates entity directly in `Queued` with `pipeline_name`, `properties`, `cyoda_namespace`, `owner_org_id`, `owner_user_id`, `api_version="v2"`, `idempotency_key`, `request_hash`. No processor; durable on commit. |
| `dispatch_teamcity_job` | `Queued` → `Job_Scheduled` | **yes** | `process_schedule_teamcity_job` | `COMMIT_BEFORE_DISPATCH` | 30000 | Engine fires automatically once entity lands in `Queued`. Processor: locator-search dedup (recovers from crash); trigger if not found; stamp `build_id`. |
| `check_job_state` | `Job_Scheduled` → `Job_Scheduled` | no (manual; CLI `--wait` invokes via `GET /v2/env`) | `process_check_job_state` | `COMMIT_BEFORE_DISPATCH` | 5000 | Polls TeamCity. Mutates `job_status`, `job_status_text`, `statistics`. 10 s debounce. |
| `mark_job_successful` | `Job_Scheduled` → `Job_Successful` | yes | none | — | — | Criterion `simple` `$.job_status == "SUCCESS"`. |
| `mark_job_failed` | `Job_Scheduled` → `Job_Failed` | yes | none | — | — | Criterion `simple` `$.job_status == "FAILURE"`. Fires when *TeamCity* reports failure (after a successful processor return that mutated `job_status`). Does NOT fire on processor-level failures — those leave the entity in pre-callout (`Queued` or `Job_Scheduled`); the v2 HTTP handler surfaces them via `GET /v2/env`. |
| `cancel_queued` | `Queued` → `Job_Cancelled` | no (manual; `POST /v2/env:cancel`) | none | — | — | No TeamCity to cancel — dispatch hasn't fired yet, or its TX_post is racing this. If dispatch already triggered TeamCity but lost the CAS race, the orphan-reconciliation CronJob (§5.5.4) cleans up. |
| `cancel_teamcity_job` | `Job_Scheduled` → `Job_Cancelled` | no (manual; `POST /v2/env:cancel`) | `process_cancel_teamcity_job` | `SYNC` | 5000 | Reads `build_id`; calls TeamCity cancel. Also performs a locator search by `cyoda_entity_id` to catch any orphan from an earlier `Queued` race. |
| `env_teardown` | `Job_Successful` → `Env_Torn_Down` | no (manual; `DELETE /v2/env`) | `process_env_teardown` | `SYNC` | 10000 | Precondition: app namespace empty (refused with 409 otherwise). Triggers `CYODA_ENV_TEARDOWN_PIPELINE_V2`. |

The `Queued` state is the load-bearing change. It eliminates the recovery-from-`None` cliff: a compute-member crash mid-`dispatch_teamcity_job` leaves the entity durable in `Queued` with all dispatch parameters present, and the next `dispatch_teamcity_job` invocation (engine replay or defensive sweep) re-runs the processor, which finds the orphaned TeamCity build via locator and recovers cleanly without duplicating it.

**Crash-recovery walkthrough.** Compute member crashes between TeamCity REST trigger and processor return. TeamCity has build 12345; the entity is in `Queued` (TX_pre committed, TX_post never opened). On client retry (or on engine automated-transition replay), `dispatch_teamcity_job` fires again. The processor's locator search by `cyoda_entity_id` finds build 12345; processor returns `success=true, payload.data={build_id: "12345", job_state: "PROCESSING"}` without triggering a duplicate; TX_post commits at `Job_Scheduled`. Self-healing.

**Cancel-race walkthrough.** User fires `POST /v2/env:cancel` while engine is firing `dispatch_teamcity_job`. Two paths:

- **Cancel wins CAS:** `cancel_queued` commits at `Job_Cancelled`. Dispatch processor's TX_post then fails with 409. The TeamCity build (if already triggered) is orphaned — handled by §5.5.4.
- **Dispatch wins CAS:** `dispatch_teamcity_job`'s TX_post commits at `Job_Scheduled` with `build_id`. `cancel_queued` fails (state moved). User's CLI sees 409 and retries; `cancel_teamcity_job` from `Job_Scheduled` succeeds with the persisted `build_id`.

**Note on terminology.** "Zero criteria services" (gRPC `EntityCriteriaCalculationRequest`, `type=function`) ≠ "zero criteria". The transitions above use **inline `simple` and `lifecycle` criterion JSON** evaluated in-engine. No `function`-type criteria services are dispatched to the compute member.

#### 5.5.4 Orphan reconciliation

A Helm CronJob (every 5 minutes) lists TeamCity builds tagged with `cyoda_namespace=client-*` and a `cyoda_entity_id` property, then for each:

1. Look up the corresponding `deploy` entity by id.
2. If the entity is in `Job_Cancelled` or doesn't exist, cancel the TeamCity build.
3. If the entity is in `Queued` for longer than `dispatch_replay_threshold` (default 10 minutes, configurable), nudge the engine to re-fire `dispatch_teamcity_job` (defensive sweep — covers the case where automated-transition replay didn't pick up).

Defence-in-depth, not load-bearing for normal operation. The `Queued` state design plus the locator-based dedup in `process_schedule_teamcity_job` already covers the common crash-recovery case.

### 5.6 TeamCity integration

`internal/teamcity/client.go` triggers builds via `POST /app/rest/buildQueue` with properties:

```json
{
  "buildType": { "id": "<pipeline-id>" },
  "properties": {
    "property": [
      { "name": "cyoda_entity_id",      "value": "<deploy entity id>" },
      { "name": "cyoda_namespace",      "value": "<derived>" },
      { "name": "cyoda_user_token",     "value": "<jwt>", "type": "password" },
      { "name": "user_env_name",        "value": "<caas_org_id>" },
      { "name": "chat_id",              "value": "<from request>" }
    ]
  }
}
```

`cyoda_entity_id` is the deterministic locator key used by `process_schedule_teamcity_job` to dedup retries after compute-member crashes (§5.5.2). It's a free-text property (not password-typed) so the locator query `GET /app/rest/builds?locator=property:(name:cyoda_entity_id,value:<id>)` returns matches.

**`cyoda_user_token` is declared as a password-typed property on the v2 pipelines.** TeamCity's password masking is substring-only on stdout — Section 5.9 covers the Ansible no_log discipline that does the actual defence.

Status polling uses `GET /app/rest/builds/id:{build_id}` with `?fields=state,status,statusText,statistics`. Response normalisation (allow-list of `statusText` values) happens in the handler before returning to the client.

**Go-side token discipline** (defends against accidental disclosure through Go's stdlib formatting paths):

- The bearer token is wrapped in a `RedactedToken` type (in `internal/auth`) whose `String()` returns `"REDACTED"`, has no `MarshalJSON` or `MarshalText`, and exposes the raw value only through an unexported method `reveal()` called from a single, audited site in the TeamCity client. `slog`, `fmt.Print*`, `errors.Wrap` all see `"REDACTED"`.
- A small CI analyzer (custom `analysis.Analyzer`) fails the build if a `string`-typed token is passed to `slog.*`, `fmt.Print*`/`Sprintf`, `errors.New`, or `fmt.Errorf` — forces use of `RedactedToken` everywhere except the audited reveal site.
- Recovery middleware strips `Authorization` and any header matching `(?i)token|secret|key` from `request.Header.Clone()` before incorporating the request into a panic-dump structured log entry.

### 5.7 Ownership and the `__system__` question

There is no `__system__` sentinel in this design. The greenfield service writes only v2 entities; every entity has a real `caas_org_id` from a JWT. Ownership filtering is straightforward equality.

The legacy Python service uses a separate cyoda backend on the legacy chart; its entities never share storage with cyoda-cloud-manager-cyoda. No cross-service data visibility concerns.

### 5.8 Helm chart

The Helm umbrella chart deploys the manager, its backing cyoda-go, and the small Postgres cyoda-go requires:

- **`cyoda-cloud-manager` Deployment** — Go binary, `gcr.io/distroless/static-debian12:nonroot` image (gives a CA bundle + `/etc/passwd` entry; `FROM scratch` would force manual CA copy and breaks TLS to Auth0 JWKS the moment someone forgets), single replica in v0 (HPA configured but disabled).
- **`cyoda-go` sub-chart** — vendored from the upstream `cyoda-platform/cyoda-go` Helm chart (per `cyoda help helm`). Configured with `gateway.enabled=false` and `ingress.enabled=false` — cyoda-go must not be exposed publicly; the manager talks to it ClusterIP-only over both REST and gRPC.
- **`postgres` sub-chart** — small Postgres deployment for cyoda-go's storage. CloudNativePG, Zalando Postgres Operator, or a single-pod PVC Postgres is acceptable for v0; pin choice at implementation time. `cyoda-go.postgres.existingSecret` is wired to a Secret produced by this sub-chart.
- **Service** — ClusterIP for the manager.
- **Ingress** — single Ingress on `api.cyoda.cloud`, path `/v2/`, 256 KiB body-size annotation.
- **NetworkPolicy** — default-deny ingress to the manager Pod; allow only from the external nginx-ingress controller namespace.
- **ConfigMap** — `tier-policy.yaml`.
- **Manager M2M credential against cyoda-go.** Provisioned via cyoda-go's chart bootstrap mechanism (`cyoda help helm`):
  - `cyoda-go.bootstrap.clientId=cyoda-cloud-manager` (chart only bootstraps when this is non-empty; default is empty).
  - `cyoda-go.bootstrap.tenantId=<tenant>` (matching the manager's expected tenant scope).
  - `cyoda-go.bootstrap.roles=ROLE_ADMIN,ROLE_M2M` (`ROLE_M2M` is required by `startStreaming` per `cyoda help grpc`; `ROLE_ADMIN` is required for entity model registration and search-API access at install time).
  - `cyoda-go.bootstrap.clientSecret.existingSecret=cyoda-cloud-manager-m2m` — operator-supplied Secret created before install; the manager Pod mounts this same Secret at `/secrets/cyoda-m2m.json` via a `volumeMount`.
  - **GitOps note:** the chart's bootstrap mechanism requires live-cluster access at install time (cannot be rendered offline by `helm template` or Argo CD's manifest-rendering step). Either pre-create the Secret with a known random value, or run `helm install --create-namespace` on first install. Subsequent upgrades are offline-renderable.
- **Bootstrap Job** — `job-cyoda-bootstrap.yaml` — runs after cyoda-go is healthy, authenticates with the M2M credential, posts SAMPLE_DATA model registration, locks model, sets `changeLevel=STRUCTURAL`, runs the post-bootstrap regression query (§5.3). Idempotent.
- **Image signing** — server image signed with cosign keyless via GitHub OIDC + Sigstore on tag-push only (`id-token: write` scoped to the publish job). Optional: Kyverno or Sigstore Policy Controller admission policy verifies the signature at install time.

Ops checklist (Section 7) covers labelling the ingress namespace appropriately so the NetworkPolicy selector matches.

### 5.9 Token forwarding to TeamCity — v0 hardening

For v0 (env-only), the user's Auth0 access token is forwarded as a `password`-typed TeamCity property `cyoda_user_token`. The hardening required to ship this:

1. **Ansible discipline**, enforced as a merge-gate in the env-playbooks repo:
   - `ansible.cfg` sets `DEFAULT_NO_LOG = True`.
   - Every task that consumes `cyoda_user_token` has explicit `no_log: true`.
   - No `set_fact` of the token under any name; no fact caching on token-bearing plays.
   - `ANSIBLE_CONFIG` is pinned as a TeamCity *system property* that subsequent build steps cannot override.
   - `ansible-lint` custom rule fails CI if `cyoda_user_token` appears in a task that lacks `no_log: true`.
   - CI assertion against `*.xml` project config that no build with `cyoda_user_token` declared has a `<dependency>` allowing inherited password params (no chained-build forwarding).
2. **TeamCity configuration verified by ops** before launch:
   - `teamcity.encryption.key` is set (not default scrambling).
   - `VIEW_BUILD_RUNTIME_PARAMETERS` REST permission is restricted to the cyoda-cloud-manager service account and TeamCity admins only.
   - Build-step verbosity is not raised globally.
3. **Cyoda env error mode pinned.** The user's own Cyoda env (provisioned by the env-up playbook) starts with `CYODA_ERROR_RESPONSE_MODE=sanitized` (per `cyoda help errors`), enforced via the env-up playbook's Helm values. A smoke check in the env pipeline asserts the freshly-provisioned env returns sanitised-style errors before the playbook starts using the user token. Defends against verbose 5xx responses leaking request fragments through Ansible task output.
4. **Cloudflare Tunnel log level pinned to `info`** — debug level dumps headers including `Authorization`. Documented in ops checklist.

### 5.10 Observability

- Structured JSON logs per request via `slog`: `request_id`, `principal_kind`, `caas_user_id` (hashed in long-term metrics), `caas_org_id`, `tier`, route, `build_id`, decision, latency, problem-type if errored. Log retention ≤30 days.
- The chi access log is verified to **not** include the `Authorization` header (smoke-tested at launch).
- OpenTelemetry traces with baggage (`caas_org_id`, hashed `caas_user_id`, `build_id`). Raw `caas_user_id` and email are **not** put in baggage — they propagate to cyoda-go's spans otherwise (per `cyoda help telemetry`'s W3C Baggage propagator).
- Prometheus counters via `expvar` or `prometheus/client_golang`: `ccm_v2_requests_total{route,principal_kind,status}`, `ccm_v2_quota_denied_total{org,quota_kind}`, `ccm_v2_rate_limited_total{principal,route}`.

### 5.11 Tests

- **Unit** (Go `testing` + `testify`): `Principal` extraction (positive + negatives in 3.2), scope checks, tier evaluation, `DeriveNamespace` (collision cases, DNS-1123, fuzz), idempotency-key matching on `(user, org, key)`, problem-detail rendering, pagination cursor encode/decode (including forged-cursor regression), condition builders.
- **Contract** (`schemathesis`): runs against the served `openapi.yaml`. Exercises every endpoint with property-based inputs.
- **Integration**: spins up real `cyoda-go` + `postgres` containers in CI (testcontainers-go). No in-memory stub. Covers ownership filtering, quota at the boundary, idempotency replay (same hash + different hash), the tier gate (no Cyoda call on tier-blocked endpoint), the `RequireTier` middleware ordering, and the `COMMIT_BEFORE_DISPATCH` round-trip with both happy-path and TX₂-conflict paths.
- **End-to-end**: a single happy-path test that hits a real TeamCity test pipeline, gated by an env flag so it only runs on demand.

## 6. `cyoda-cloud-cli` (Go) — separate repo

### 6.1 Repository

`github.com/cyoda-platform/cyoda-cloud-cli`. Public, Apache-2.0. No code shared with the manager except the OpenAPI spec, fetched at build time from `https://api.cyoda.cloud/v2/.well-known/openapi.json`.

### 6.2 Layout

```
cyoda-cloud-cli/
├── cmd/cyoda-cloud/main.go
├── internal/
│   ├── auth/                    # PKCE loopback + Device Flow + token refresh
│   ├── keychain/                # zalando/go-keyring wrapper, file fallback
│   ├── api/                     # generated client (oapi-codegen) + thin wrapper
│   ├── config/                  # ~/.config/cyoda-cloud/config.toml, --org, host discovery
│   ├── output/                  # text + JSON formatters; respects --output, isatty
│   ├── version/                 # min-version check + soft-deprecation
│   └── commands/
│       ├── login.go
│       ├── register.go          # alias for login with screen_hint=signup
│       ├── logout.go
│       ├── whoami.go
│       ├── env.go               # up | status | cancel | down
│       ├── app.go               # build | deploy | status | cancel | delete | list (v0: mutating commands return tier-not-entitled)
│       └── token.go             # print --show (debug)
├── api/openapi/                 # vendored copy of /v2/.well-known/openapi.json (CI-refreshed)
├── .goreleaser.yaml             # macOS/Linux/Windows × amd64/arm64; brew tap; scoop; deb/rpm
└── .github/workflows/
    ├── ci.yml                   # build + test + lint + openapi-drift check
    └── release.yml              # tag → GoReleaser → cosign keyless + SBOM
```

### 6.3 Auth flows

- **Interactive (`cyoda-cloud login`).** Authorization Code + PKCE, loopback redirect on `127.0.0.1:<random>`. Auth0 application is **Native**, no client secret. Hosted login page presents Continue-with-Google and Continue-with-GitHub.
- **Headless (`cyoda-cloud login --device`).** Device Authorization Grant. CLI prints URL and user code, polls `/oauth/token` until activation. SDK obeys `slow_down`.
- **Sign-up (`cyoda-cloud register`).** Same as `login` with `screen_hint=signup`.
- **Org selection.** `--org <slug>` passes `organization=<org_id>` on `/authorize`. Persisted in config.
- **Storage.** Refresh token + minimal identity in OS keychain via `go-keyring`. Access tokens never persisted. Fallback file `~/.config/cyoda-cloud/credentials` (mode 0600) only when keychain unavailable, with a warning.
- **Refresh.** Silent on next request. On `invalid_grant` (rotation reuse / revoked) the CLI prints `Session expired; run "cyoda-cloud login".` and exits non-zero.

### 6.4 Commands (v0)

```
cyoda-cloud register [--device] [--org <slug>]
cyoda-cloud login    [--device] [--org <slug>] [--scope <list>]
cyoda-cloud logout
cyoda-cloud whoami [--output json]
cyoda-cloud token print --show

cyoda-cloud env up    [--backend <type>] [--wait] [--idempotency-key <key>]
cyoda-cloud env status
cyoda-cloud env cancel
cyoda-cloud env down  [--wait]

cyoda-cloud app build  ...     # v0: 403 tier-not-entitled
cyoda-cloud app deploy ...     # v0: 403 tier-not-entitled
cyoda-cloud app list           # always empty for v0 end-users
cyoda-cloud app status <build_id>
cyoda-cloud app cancel <build_id>     # v0: 403 tier-not-entitled
cyoda-cloud app delete <build_id>     # v0: 403 tier-not-entitled

cyoda-cloud config set/get/list
cyoda-cloud version
```

`--wait` polls with exponential backoff (1 s → 30 s, capped). Default timeout 30 min.

`--idempotency-key` defaults to a per-invocation UUIDv4; users can override.

### 6.5 Output

- **Default** (TTY): tables, coloured states.
- **`--output json`** or non-TTY: JSON only.
- Logs to stderr, data to stdout.
- **No interactive prompts when stdin is not a TTY.** Critical for AI-agent use.

### 6.6 Exit codes

```
0   success
1   generic failure
2   bad usage / validation
3   unauthenticated
4   forbidden
5   tier-not-entitled
6   quota-exceeded
7   not-found
8   conflict
9   upstream-failure
10  server-min-version-required
```

### 6.7 Server URL discovery

CLI builds with no baked-in API host. On first run, fetches `https://cyoda.cloud/.well-known/cyoda-cloud-cli.json`:

```json
{
  "api_url": "https://api.cyoda.cloud",
  "auth0_domain": "<tenant>.eu.auth0.com",
  "auth0_client_id": "<native-app-client-id>",
  "auth0_audience": "https://api.cyoda.cloud"
}
```

Cached locally for 24 h; `--refresh-discovery` forces re-fetch.

`min_cli_version` is **not** part of this static discovery file. It's served by the manager itself at `/v2/.well-known/cli-min-version` (single source of truth). Operator updates the served value by changing the `CLI_MIN_VERSION` env var on the manager Deployment; no static-asset deployment is involved. The CLI consults the served endpoint on `cyoda-cloud version --check` and on every command (cached for 24 h).

### 6.8 Versioning

CLI sends `User-Agent: cyoda-cloud-cli/<ver> (<os> <arch>)` and `Cyoda-Cloud-CLI-Version: <ver>`. Below `min_cli_version` ⇒ refuses with exit 10. Server may return 426 Upgrade Required for true API breakages.

### 6.9 AI-agent considerations (v0)

Agent inherits the human's session via the same keychain entry. Down-scoping (`agent-mode`) and a separate Native app for delegated grants are explicitly v1 work, not v0.

### 6.10 Distribution

GoReleaser on tag push: macOS/Linux/Windows × amd64/arm64; Homebrew tap; Scoop bucket; deb/rpm; Docker image. **Sigstore keyless signing** via GitHub OIDC + cosign. `id-token: write` is granted only on tag-push triggers, never on PR triggers. SBOM via syft.

### 6.11 CI

- **Drift check.** CI fetches the served OpenAPI, regenerates the client, fails if the diff isn't already committed.
- **Lint.** `golangci-lint run`, `gofmt`, `goimports`.
- **Tests.** Unit + integration against an `oapi-codegen`-stub server.

## 7. Setup checklist

### 7.1 Auth0 setup

1. **Native application "cyoda-cloud-cli"**:
   - Token Endpoint Authentication Method: **None**.
   - Grant types: Authorization Code, Refresh Token, Device Code.
   - PKCE required.
   - Allowed Callback URLs: `http://127.0.0.1:42777/callback` (the CLI's `auth.DefaultLoopbackBindAddr`). **Not `localhost`** (IPv6 resolution issues). Auth0 does **not** wildcard ports for loopback URIs despite RFC 8252 §7.3 — the registered URL must match the bound port exactly. Users who can't free port 42777 locally set `CYODA_CLOUD_LOOPBACK_PORT=<port>` and register the matching `http://127.0.0.1:<port>/callback`.
   - Refresh Token Rotation **on**, Reuse Detection **on**, Absolute Lifetime ~30 d, Inactivity Lifetime ~14 d.
   - Connections: **Google** and **GitHub** only.
   - `offline_access` scope allowed.
2. **API "Cyoda Cloud API"**:
   - Identifier (audience): `https://api.cyoda.cloud`.
   - Algorithm: **RS256** (only).
   - Scopes: `read:builds deploy:env cancel:env delete:env deploy:app cancel:app delete:app admin:all`.
3. **Tenant Settings → Attack Protection**:
   - **Bot Detection: Auto.**
   - **Suspicious IP Throttling: enabled.**
4. **Existing post-login Actions** (`Assign CaaS User ID`, the main one) are unchanged. They produce the JWT shape v2 requires.

### 7.2 Cluster setup

1. NetworkPolicy applied (allow-list of source namespaces by label).
2. Ingress applied; `api.cyoda.cloud` DNS → Cloudflare → Cloudflare Tunnel → in-cluster nginx-ingress.
3. **M2M Secret pre-created** before first install (because cyoda-go's bootstrap mechanism requires live cluster access): `kubectl create secret generic cyoda-cloud-manager-m2m --from-literal=client-secret="$(openssl rand -hex 32)" -n <ns>`. Subsequent upgrades are offline-renderable.
4. cyoda-go ≥ v0.7.0 sub-chart deployed with `gateway.enabled=false ingress.enabled=false`, `bootstrap.clientId=cyoda-cloud-manager`, `bootstrap.tenantId=<tenant>`, `bootstrap.roles=ROLE_ADMIN,ROLE_M2M`, `bootstrap.clientSecret.existingSecret=cyoda-cloud-manager-m2m`. Postgres sub-chart deployed with PV; daily Postgres backup CronJob configured.
5. cyoda-go `deploy` entity model registered (post-install Job, idempotent) via SAMPLE_DATA import + lock + STRUCTURAL changeLevel + post-bootstrap regression query.
6. `tier-policy.yaml` ConfigMap applied with `deploy_app: false` for every tier.
7. `TOKEN_EXCHANGE_ENABLED=false` (default — required for the deploy_app gate).
8. Cloudflare WAF rules + per-IP rate limits for `api.cyoda.cloud`.
9. Cloudflare Tunnel log level pinned to `info`.
10. Orphan reconciliation CronJob (§5.5.4) deployed — every 5 minutes, defence-in-depth.

### 7.3 TeamCity / Ansible setup

1. `teamcity.encryption.key` configured.
2. `VIEW_BUILD_RUNTIME_PARAMETERS` REST permission audit completed; restricted to manager service account + TeamCity admins.
3. v2 env pipelines (`CYODA_ENV_PIPELINE_V2`, `CYODA_ENV_TEARDOWN_PIPELINE_V2`) defined; `cyoda_user_token` declared password-typed.
4. Ansible playbooks repo: `ansible.cfg DEFAULT_NO_LOG=True`, `ansible-lint` custom rule, `ANSIBLE_CONFIG` pinned as TeamCity system property.
5. Pipeline VCS root pinned to a Cyoda-controlled branch; user-controlled VCS roots disallowed.
6. Env-up playbook renders Cyoda env Helm values with `CYODA_ERROR_RESPONSE_MODE=sanitized`; smoke check asserts before token use.

### 7.4 Backlog

- **Pre-User-Registration Action** (disposable-email, invitation-only). Only relevant if a DB connection is ever enabled.
- **Post-User-Registration ops-Slack notification** for signup observability.
- **Per-`caas_user_id` ban-list** ConfigMap consulted by `RequireAuth` (returns 403 with `type: revoked`); emergency Auth0 client_id rotation runbook.
- **GitHub private-email handling** — the existing `Assign CaaS User ID` Action throws on missing email; intentional.
- **Idempotency-key garbage collector** (currently retained indefinitely past terminal state).
- **Hot-reload** of `tier-policy.yaml` (currently pod-restart only).
- **Coordinated quota** across replicas (currently per-Pod token bucket).
- **HA Postgres** for the cyoda-go sub-chart (CloudNativePG cluster mode) if v0's single-pod Postgres ever runs out of headroom.

## 8. Build ordering

**Prerequisite:** cyoda-go ≥ v0.7.0 (for `COMMIT_BEFORE_DISPATCH`). Steps 5+ assume the release is available; steps 1-4 can land in parallel with that work.

Each step independently shippable.

1. **Bootstrap.** Repo, CI (lint + test), Helm umbrella chart skeleton with cyoda-go + Postgres sub-charts pinned; Dockerfile on `gcr.io/distroless/static-debian12:nonroot`; `cmd/server/main.go` returning healthcheck only. End state: deployable empty service against a real cyoda-go instance.
2. **Cyoda model registration.** `internal/cyoda/model`, post-install Helm Job that uses SAMPLE_DATA import + lock + STRUCTURAL `changeLevel`. End state: `deploy` entity model locked with the v2 schema; no seed entity needed.
3. **Cyoda REST client.** `internal/cyoda/rest` against current cyoda-go REST API (direct + async search, entity CRUD); `internal/cyoda/conditions` builders. Integration-tested against cyoda-go + Postgres testcontainers.
4. **OpenAPI + handler scaffolding.** `api/openapi.yaml` drafted; `oapi-codegen` integrated; empty handler implementations returning 501. End state: spec served; all endpoints discoverable.
5. **Auth.** `internal/auth/jwks.go`, `RequireAuth`, negative tests. **`GET /v2/me`** implemented end-to-end; smoke-test against a real Auth0 token. End state: identity proven.
6. **Tier policy + middleware.** `internal/tier`, `RequireTier`, ConfigMap loader, startup gate on `deploy_app: true` / `TOKEN_EXCHANGE_ENABLED`. End state: tier-blocked endpoints fail-fast.
7. **Quota.** `internal/quota` direct-search-based. `GET /v2/me` returns real usage.
8. **Compute-member gRPC client.** `internal/cyoda/compute` opens the outbound stream to cyoda-go's `CloudEventsService.startStreaming`, dispatches inbound `EntityProcessorCalculationRequest` events to the four processor handlers. Reconnect/backoff and 30s keepalive handling. Integration-tested with a real cyoda-go instance dispatching synthetic processor calls.
9. **Env endpoints.** `POST /v2/env`, `GET /v2/env`, `POST /v2/env:cancel`, `DELETE /v2/env`. TeamCity client + processors + Ansible discipline (Section 5.9 + 7.3). End state: real envs provision and tear down. **Hard dependency on step 8** — env endpoints write the `deploy` entity, which fires the workflow, which dispatches `process_schedule_teamcity_job` to the compute member. Step 9 cannot ship until step 8 is operational. Includes the orphan-reconciliation CronJob (§5.5.4).
10. **List + single-build endpoints.** `GET /v2/builds`, `GET /v2/builds/{id}`. Forged-cursor regression test.
11. **Tier-blocked app endpoints.** `POST /v2/builds`, etc. — all return 403 tier-not-entitled. End state: surface complete, contract forward-compatible.
12. **Discovery + min-version.** `/v2/.well-known/openapi.json`, `/v2/.well-known/cli-min-version`.
13. **Public exposure.** Cloudflare Tunnel + Ingress + NetworkPolicy applied; DNS pointed; Bot Detection + IP Throttling enabled at Auth0.
14. **`cyoda-cloud-cli` v0.** Separate repo; consumes the served OpenAPI.

Steps 1-3 are foundational and can land before the public hostname is even allocated.

## 9. Risks and mitigations

| Risk | Mitigation | Residual |
|---|---|---|
| Long-running env pipelines fail near the end on token expiry. | Accept for v0 (env playbooks sub-30 min). RFC 8693 token-exchange on the contract-tier roadmap. | accepted |
| `COMMIT_BEFORE_DISPATCH` schedule-vs-cancel race orphans a TeamCity build. | When cancel wins the CAS, dispatch's TX_post fails 409 with TeamCity already triggered. The orphan-reconciliation CronJob (§5.5.4) cancels orphaned builds within 5 minutes. `process_cancel_teamcity_job` also performs a locator search by `cyoda_entity_id` and cancels eagerly when invoked from the `Job_Scheduled` path. | mitigated |
| Compute-member crash mid-processor leaves entity stuck. **No engine re-dispatch** per cyoda-go v0.7.0 contract. | The `Queued` state design (§5.5.3) makes the entity durable in pre-callout state with all dispatch parameters. Recovery: client retry of `POST /v2/env` with the same `Idempotency-Key` replays through the HTTP idempotency layer, fires a fresh `dispatch_teamcity_job`; processor's locator search by `cyoda_entity_id` finds and adopts the orphaned TeamCity build without duplicating. Defensive sweep in §5.5.4 nudges replays on entities stuck in `Queued` past 10 min. | mitigated |
| `process_schedule_teamcity_job` returns `success=false`. Engine halts cascade at pre-callout (`Queued`) per v0.7.0 contract — no automatic compensation. | v2 HTTP handler reads the entity's pre-callout state on `GET /v2/env` and surfaces the failure (last error captured at handler-side from the gRPC response if available; otherwise generic "schedule failed, retry"). User can re-issue via CLI. | mitigated |
| TX_post `payload.data=null` idempotency-skip still bumps version and races other writers. | Per v0.7.0 contract, TX_post always commits when processor returns `success`. The schedule processor's locator-recovery path returns `payload.data={build_id, job_state}` so the state advance to `Job_Scheduled` is intentional. The check-state processor naturally returns updated fields. No skip-without-state-change path is used. | accepted |
| Slug collision between distinct `caas_org_id` values. | `DeriveNamespace` 8-hex sha256 suffix + DNS-1123 validation; fuzz tests. | eliminated |
| Cursor IDOR via forged cursor. | Ownership filter always re-injected from principal; cursor is opaque tiebreak only. Regression test asserts. | eliminated |
| Algorithm-confusion / `kid` injection on JWT verify. | `RS256` pinned via `jwt.Parse` allowlist; `kid` lookup via `keyfunc` fails closed; explicit negative tests. | mitigated |
| Idempotency-Key hijack across users in same org. | Match on `(owner_user_id, owner_org_id, key)`. | mitigated |
| Idempotency-Key replay racing under concurrent first-time submits. | `singleflight.Group` for single-replica; HPA accepts residual race; reconciliation on next status-poll. | accepted |
| Quota search cost weaponised by authenticated attacker. | Per-principal token bucket fronts quota check; tier check fronts everything; tier-blocked endpoints make zero Cyoda calls. | mitigated |
| TeamCity build log leaks the user's bearer token via Ansible task output. | `DEFAULT_NO_LOG=True` + per-task `no_log: true` + lint gate + `ANSIBLE_CONFIG` pinned as system property. TeamCity password masking is substring-only — no_log discipline is the actual defence. | mitigated |
| TeamCity admin / DB readers can recover password params. | TeamCity encryption key configured; `VIEW_BUILD_RUNTIME_PARAMETERS` restricted; documented as part of the TeamCity trust boundary. | accepted (trust ops boundary; revisit at app-deploys) |
| Cyoda env verbose-mode 5xx leaks request fragments through Ansible. | Env-up playbook pins `CYODA_ERROR_RESPONSE_MODE=sanitized` in env Helm values; smoke-check before token use. | mitigated |
| Cloudflare Tunnel debug-level logging dumps `Authorization` headers. | `cloudflared` log level pinned to `info`. | mitigated |
| Per-org service credentials would have a smaller blast radius. | **Inverted in v0 scope**: forwarded user token is ~1 h TTL with rotation, scoped to the user's own env, per-user audit attribution. Service creds would have months-long TTL, full-org scope, "service" attribution. The reviewer's argument applies once user code runs in pipelines (app-deploys); for env-only v0 the trade-off favours forwarding. | mitigated |
| `INVALID_FIELD_PATH` 400 from cyoda-go on first query of a new field. | Locked schema registered via SAMPLE_DATA import + `changeLevel=STRUCTURAL` at install (post-install Job). | eliminated |
| Async search snapshot expires mid-pagination. | `409 cursor-expired`; CLI re-issues without cursor. 24-hour snapshot lifetime per `cyoda help search`. | mitigated |
| Concurrent paginated reads with mismatched `limit` see different slices across pages. | `pageSize` is server-pinned and embedded in the cursor; `limit=` queryparam is honoured only on the first page. | eliminated |
| Auth0 Action churn breaks token shape silently. | `Principal` extraction is strict; missing `caas_org_id` rejects with `unauthenticated`. | mitigated |
| Bot signups via Google/GitHub. | Bot Detection Auto + provider-side spam control + free-tier env quota (1/day). | mitigated |
| Stale CLI binaries call deprecated endpoints. | `Cyoda-Cloud-CLI-Version` header + `min_cli_version` discovery + 426 Upgrade Required for true breakage. | mitigated |
| End-user JWT reaches `cyoda-cloud-manager-cyoda`. | Manager calls cyoda-go with its own M2M credential; user JWT is forwarded only to env-pipeline TeamCity properties. | mitigated |
| Refresh-token theft from user's machine. | Keychain by default + Refresh Token Rotation + Reuse Detection + 30 d absolute lifetime. `cyoda-cloud logout` revokes. | mitigated |
| TeamCity-sourced free-text in responses leaks internals. | `GET /v2/builds/{id}` normalises `status_text` through an explicit allow-list; raw error strings replaced with canonical strings + opaque `incident_id`. | mitigated |
| `deploy_app` accidentally enabled before prerequisites. | Startup gate (`TOKEN_EXCHANGE_ENABLED=false` ⇒ readiness fails with `deploy_app: true`) + CI check on `tier-policy.yaml`. | eliminated |
| Cloudflare header trust spoofing (direct cluster IP access). | `CF-Connecting-IP` / `True-Client-IP` trusted only when source is the in-cluster ingress controller. | mitigated |
| `request.Host` parsing edge cases (IPv6, multi-Host, port-stripping). | Use `net.SplitHostPort` with bracket handling; reject multi-Host. | mitigated |
| PII / GDPR. | `caas_user_id` hashed in long-term metrics; raw claims not in OTel baggage; log retention 30 d. | accepted with policy |
| Supply chain. | Sigstore keyless signing for the Go binary; `id-token: write` only on tag-push triggers; SBOM via syft. | mitigated |

## 10. Open questions deferred to implementation

- Final hostname for `api.cyoda.cloud` (placeholder; ops to confirm).
- Exact mapping of backend types to v2 env pipeline IDs (`CYODA_ENV_PIPELINE_BACKEND_MAP`).
- Concrete Postgres choice for the cyoda-go sub-chart (CloudNativePG vs Zalando Postgres Operator vs single-pod PVC). Default to single-pod PVC for v0; revisit at scale.
- Concrete name of the future Auth0 Client Credentials Exchange Action for M2M (post-v0).
- Whether a future tier "enterprise-app-enabled" lives alongside `enterprise` or replaces it.
- Whether to maintain the cyoda-go sub-chart vendor copy or pull a tagged dependency. Vendor copy in v0; tagged dep when the sub-chart upstream stabilises.

## 11. Deferred features and prerequisites

### 11.1 App-deploy enablement

Re-enabling `deploy_app: true` on any tier requires **all** of the following:

1. **Repo-validation architecture.**
   - Cloud-manager-side allow-list enforcement (per-org or per-tier, ConfigMap-driven).
   - TeamCity-side validation — Ansible playbook hardening so no user-supplied content executes outside a sandboxed step.
   - Build-step lockdown: pipeline VCS roots remain Cyoda-controlled; user repo content mounted as a workdir, never sourced as runner steps.
   - Audit log for every app deploy with the resolved repo URL and the principal that triggered it.
2. **RFC 8693 token-exchange.**
   - Auth0 setup for token-exchange grant.
   - cyoda-cloud-manager mints a build-bound, audience-scoped, ~10-minute token per pipeline trigger.
   - User-controlled build steps cannot exfiltrate a long-lived user token; worst case is a 10-minute deploy-scoped artifact.
3. **`TOKEN_EXCHANGE_ENABLED=true`** flipped on the manager Deployment.
4. **Tier-policy `deploy_app: true`** flipped on the specific contract tier.
5. **Audit / observability** for the new principal flow.

The startup gate enforces step 3 mechanically: any tier-policy ConfigMap loading with `deploy_app: true` while `TOKEN_EXCHANGE_ENABLED=false` causes the Pod to fail readiness.

### 11.2 Multi-replica coordination

Re-evaluate when public traffic justifies HPA scaling beyond one replica:

- **Coordinated `check_job_state` debounce.** The current 10s LRU is per-Pod; under HPA, replicas race for TX_post on the same entity, and per the v0.7.0 contract conflicts halt the cascade with no engine retry. Multi-replica needs a coordinated lock (Redis SETNX with TTL or a cyoda-go advisory lock) before HPA is enabled. Without it, polling storms produce repeated 409s and stuck entities.
- Coordinated quota across replicas (Redis or cyoda-go `INCR`-style entity).
- Coordinated rate-limit token bucket.
- Idempotency-key racing fully closed (currently single-replica).

### 11.3 Other deferrals

- Per-`caas_user_id` ban-list and emergency Auth0 client_id rotation runbook.
- Idempotency-key garbage collector.
- Hot-reload of `tier-policy.yaml`.
- HA Postgres operator (CloudNativePG cluster mode) for cyoda-go's storage if v0's single-pod Postgres ever runs out of headroom.
- Cyoda-branded email-verification return page (only relevant if a DB connection is ever enabled).
