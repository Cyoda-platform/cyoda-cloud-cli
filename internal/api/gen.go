// Package api hosts the oapi-codegen-generated client for the Cyoda Cloud
// API along with the authenticated HTTP transport that injects bearer tokens
// and refreshes on 401.
//
// The generated client lives in client.gen.go and is committed; running
// `go generate ./internal/api/...` regenerates it from
// ../../api/openapi/openapi.yaml. The version of oapi-codegen is pinned in
// the directive below so CI and local runs stay reproducible without
// requiring a host-installed binary.
package api

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 -config gen.yaml ../../api/openapi/openapi.yaml
