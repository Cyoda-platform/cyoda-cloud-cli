// Package version exposes the binary's build-time version string and the
// helpers that compose it into HTTP headers (User-Agent +
// Cyoda-Cloud-CLI-Version per spec §6.8). Version defaults to "dev" and is
// overwritten at release-build time via -ldflags by GoReleaser.
package version

import (
	"fmt"
	"net/http"
	"runtime"
)

// Version is the CLI's semver string. "dev" by default; release builds
// override via -ldflags "-X .../internal/version.Version=<tag>".
var Version = "dev"

// UserAgent returns the canonical User-Agent header value for the given
// version + OS + arch tuple. Spec §6.8 mandates "cyoda-cloud-cli/<ver> (<os>
// <arch>)".
func UserAgent(v, os, arch string) string {
	return fmt.Sprintf("cyoda-cloud-cli/%s (%s %s)", v, os, arch)
}

// SetStandardHeaders sets the User-Agent and Cyoda-Cloud-CLI-Version headers
// on req. The two headers form the CLI's standard identity that every manager
// endpoint expects (see api.Transport for the per-API-call equivalent and
// internal/config/discovery.go + internal/commands/version.go for the two
// non-API HTTP clients that bypass the Transport but still need to identify
// themselves consistently).
//
// Callers may set Accept separately when their endpoint requires a specific
// content type — this helper is intentionally minimal so it composes with
// per-endpoint header tweaks.
func SetStandardHeaders(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent(Version, runtime.GOOS, runtime.GOARCH))
	req.Header.Set("Cyoda-Cloud-CLI-Version", Version)
}
