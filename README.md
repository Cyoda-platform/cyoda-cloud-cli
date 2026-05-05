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
