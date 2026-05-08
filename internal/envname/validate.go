// Package envname provides best-effort client-side validation of
// user-supplied env names before they are sent on the wire.
//
// The server's namespace.ValidateEnvName is authoritative — drift
// between this validator and the server is acceptable: the server's 400
// is the canonical error. The CLI runs Validate first only so that
// obvious local mistakes (empty, reserved, malformed) fail fast without
// a network round trip.
//
// The rules mirror cyoda-cloud-manager's namespace package as of the
// feat-env-naming-multi-env branch:
//
//   - Required (non-empty).
//   - Lower-case DNS-1123 label: ^[a-z][a-z0-9-]{0,21}$ (max 22 chars).
//   - No trailing hyphen.
//   - No consecutive hyphens.
//   - Reserved literals: default, kube-system, kube-public, kube-node-lease.
//   - Reserved prefixes: app- / cl- (or the bare "app" / "cl" forms).
//
// Keep this list in sync with the server when convenient; do not block
// CLI releases on it.
package envname

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxLen caps env_name length. Mirrors namespace.MaxEnvNameLen.
const MaxLen = 22

var (
	envNameRe        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,21}$`)
	reservedPrefixRe = regexp.MustCompile(`^(app|cl)(-|$)`)
)

var reservedNames = map[string]struct{}{
	"default":         {},
	"kube-system":     {},
	"kube-public":     {},
	"kube-node-lease": {},
}

// Validate reports whether name is acceptable for use as an env_name.
// Returns nil on valid input; on invalid, an error whose message names
// the rule violated.
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("env_name empty")
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("env_name must be lowercase: got %q", name)
	}
	if len(name) > MaxLen {
		return fmt.Errorf("env_name length %d exceeds max %d", len(name), MaxLen)
	}
	if !envNameRe.MatchString(name) {
		return fmt.Errorf("env_name must start with a letter and contain only [a-z0-9-]")
	}
	if name[len(name)-1] == '-' {
		return fmt.Errorf("env_name has trailing hyphen")
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("env_name has consecutive hyphens")
	}
	if _, ok := reservedNames[name]; ok {
		return fmt.Errorf("env_name %q is reserved", name)
	}
	if reservedPrefixRe.MatchString(name) {
		return fmt.Errorf("env_name %q matches reserved prefix (app- or cl-)", name)
	}
	return nil
}
