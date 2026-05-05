.PHONY: build test lint generate
GO ?= go
build:
	$(GO) build -o bin/cyoda-cloud ./cmd/cyoda-cloud
test:
	$(GO) test -race -timeout 60s ./...
lint:
	golangci-lint run
generate:
	$(GO) generate ./...
