# Handover — `cyoda-cloud-cli` (CLI, Go)

This document is the entry point for a fresh Claude Code session in `/Users/paul/go-projects/cyoda-cloud-cli` that will execute the CLI implementation plan.

**Open this file as the first prompt of the new session, then ask Claude to proceed with the task loop.**

---

## What this session is doing

Building a brand-new Go CLI `cyoda-cloud`. It is the user-facing command-line tool for Cyoda Cloud — Auth0 PKCE/Device login → bearer JWT → call into `cyoda-cloud-manager`'s `/v2/*` API.

The CLI is a separate repo from the server. The only shared artefact is the OpenAPI spec, which the CLI vendors from the server's `/v2/.well-known/openapi.json` and regenerates the client from on every CI build (drift check).

## Ground truth documents (copied into this repo at `docs/`)

- `docs/spec.md` — the design spec (`2026-05-04-cyoda-cloud-manager-and-cli-design.md`). §6 covers the CLI specifically; §3 covers the auth model the CLI uses; §4 covers the API surface the CLI consumes.
- `docs/plan.md` — the CLI implementation plan (`2026-05-04-cyoda-cloud-cli.md`).

Read both in full before starting.

## Skill stack

This session executes the plan via **subagent-driven-development**. Same loop as the server handover document.

## Execution scope for this session

**Tasks 1 through 4** (bootstrap, discovery+keychain, PKCE login, device flow + refresh + logout) are server-independent and can run end-to-end now.

**Tasks 5 onward** (`whoami`, env commands, app commands, distribution, discovery file deployment) depend on:

- A running `cyoda-cloud-manager` to fetch `/v2/.well-known/openapi.json` from. Until the server is deployed at `https://api.cyoda.cloud`, you can either:
  - **(a) Run a local server** built from the companion repo and point at `http://localhost:8080` via the discovery file or env override.
  - **(b) Vendor the OpenAPI spec from the server repo's `internal/api/openapi.json`** during development and switch to the live URL once the server is deployed.

Default to (b): copy `cyoda-cloud-manager/api/openapi.yaml` into `api/openapi/openapi.yaml` for codegen, generate the client, switch to fetching the live spec via OpenAPI drift CI when the server is live.

## Boundary conditions

Same as the server: branch `main`, commit per step, push after each task, TDD throughout.

## Auth0 setup the CLI assumes

- A Native application named (e.g.) `cyoda-cloud-cli` configured in the Cyoda Auth0 tenant.
- Token Endpoint Authentication Method: **None** (no client secret — public PKCE client).
- Grant types enabled: Authorization Code, Refresh Token, Device Code.
- Refresh Token Rotation **on**, Reuse Detection **on**, Absolute Lifetime ~30 d, Inactivity Lifetime ~14 d.
- Allowed Callback URLs: `http://127.0.0.1` (no port; Auth0 permits any port at runtime per RFC 8252).
- Connections: Google + GitHub only (matches tenant config).

These are operator-side settings; the CLI session doesn't configure Auth0. The session does need:

- The Auth0 tenant domain (e.g. `tenant.eu.auth0.com`).
- The Native app's client ID.
- The API audience (`https://api.cyoda.cloud`).

For Tasks 1–4 development, hard-code these into a local discovery file under `deploy/discovery/cyoda-cloud-cli.json` and point the CLI at it via the `CYODA_CLOUD_DISCOVERY_URL` env var:

```bash
export CYODA_CLOUD_DISCOVERY_URL="file:///Users/paul/go-projects/cyoda-cloud-cli/deploy/discovery/cyoda-cloud-cli.json"
```

The `internal/config` package needs a small `file://` scheme branch. (Plan Task 2 covers `https://`; add the `file://` branch as a local-dev convenience.)

Tasks 5+ switch to the production discovery URL.

## Hand-off checkpoints

- **End of Task 1:** "Repo bootstrapped, `cyoda-cloud version` works, CI green." Push.
- **End of Task 4:** "Auth flows working end-to-end against Auth0 (or stubbed Auth0). Token storage, refresh, logout all green." Push.
- **End of Task 7:** "All v0 commands wired (env + tier-blocked app). Awaiting deployed manager for end-to-end smoke test." Push.
- **End of Task 9:** "GoReleaser pipeline configured but not yet tagged. cosign keyless workflow validated against a `dryrun` tag." Push.
- **End of Task 10:** "Discovery file authored. Hand back to ops for Cloudflare Pages deployment."

## What to do if you hit a snag

Same as server handover: prefer the spec; don't guess at server-side semantics; surface library-version surprises.

## First action of the new session

1. Read `docs/spec.md` (focus on §3, §4, §6).
2. Read `docs/plan.md` end to end.
3. Invoke `superpowers:subagent-driven-development`.
4. Create a `TaskCreate` task for each of plan Tasks 1–10.
5. Dispatch the first implementer subagent for Task 1.

## File copy manifest

After creating the GitHub repo and cloning to `/Users/paul/go-projects/cyoda-cloud-cli`:

```bash
# From the legacy repo (/Users/paul/dev/cloud-manager):
mkdir -p /Users/paul/go-projects/cyoda-cloud-cli/docs
cp docs/superpowers/specs/2026-05-04-cyoda-cloud-manager-and-cli-design.md \
   /Users/paul/go-projects/cyoda-cloud-cli/docs/spec.md
cp docs/superpowers/plans/2026-05-04-cyoda-cloud-cli.md \
   /Users/paul/go-projects/cyoda-cloud-cli/docs/plan.md
cp docs/superpowers/handover/cli-handover.md \
   /Users/paul/go-projects/cyoda-cloud-cli/docs/cli-handover.md

cd /Users/paul/go-projects/cyoda-cloud-cli
git add docs/
git commit -m "docs: spec + plan + handover from cloud-manager (legacy)"
git push -u origin main
```

When the server repo's `api/openapi.yaml` becomes available (server Task 6 stabilises it), copy it in:

```bash
mkdir -p /Users/paul/go-projects/cyoda-cloud-cli/api/openapi
cp /Users/paul/go-projects/cyoda-cloud-manager/api/openapi.yaml \
   /Users/paul/go-projects/cyoda-cloud-cli/api/openapi/openapi.yaml
```

Then start the new Claude Code session in `/Users/paul/go-projects/cyoda-cloud-cli/` with the prompt:

> Read `docs/cli-handover.md` first. Then proceed.

## gh PAT scopes

- `repo` — push commits, manage branches.
- `workflow` — modify `.github/workflows/`.
- `write:packages` — for `ghcr.io/cyoda-platform/cyoda-cloud-cli` Docker push (Task 9 release pipeline). Only used at release time; not needed during Tasks 1–8.

Generate via `gh auth refresh -s repo,workflow,write:packages` before starting.

## Auth0 secrets the CLI session does NOT need

The CLI is a public client (no secret). The session never sees an Auth0 client secret. The Auth0 client ID is public-facing and lives in the discovery file. The user's refresh tokens stay on the user's machine in the keychain — they never enter version control or CI.
