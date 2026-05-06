package version

import (
	"fmt"
	"net/http"
	"runtime"
)

var Version = "dev"

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
