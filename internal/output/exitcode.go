// Package output also defines the CLI's exit-code namespace and the
// RFC 7807 → exit-code mapping. Per spec §6.6, every command-side error
// returned from a cobra RunE must be a *CLIError so main.go's wrapper can
// translate it into the documented exit code.
//
// Other command code constructs *CLIError either directly (for
// CLI-originated errors like "not logged in") or via WrapHTTP, which decodes
// an *api.Problem's `type` URI to a Code and falls back to status when the
// type is missing or unrecognised.
package output

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
)

// Code is the CLI's exit-code namespace per spec §6.6.
type Code int

const (
	CodeOK                       Code = 0
	CodeGeneric                  Code = 1
	CodeBadUsage                 Code = 2
	CodeUnauthenticated          Code = 3
	CodeForbidden                Code = 4
	CodeTierNotEntitled          Code = 5
	CodeQuotaExceeded            Code = 6
	CodeNotFound                 Code = 7
	CodeConflict                 Code = 8
	CodeUpstreamFailure          Code = 9
	CodeServerMinVersionRequired Code = 10
)

// CLIError carries an exit code with an underlying error message.
//
// main.go's wrapper inspects errors via errors.As(err, **CLIError) to set
// os.Exit appropriately. The Error() text is what cobra will print to
// stderr, so embed clean single-line messages.
type CLIError struct {
	Code Code
	Err  error
}

// Error implements error by delegating to the wrapped Err.
func (e *CLIError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

// Unwrap supports errors.Is / errors.As traversal.
func (e *CLIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Exit converts an arbitrary error into the process exit code.
//
//   - nil → 0 (CodeOK).
//   - *CLIError → its Code.
//   - any other non-nil error → 1 (CodeGeneric).
//
// This is the single shared lookup used by main.go's wrapper. Tests assert
// the per-Code mappings directly.
func Exit(err error) int {
	if err == nil {
		return int(CodeOK)
	}
	var ce *CLIError
	if errors.As(err, &ce) && ce != nil {
		return int(ce.Code)
	}
	return int(CodeGeneric)
}

// problemTypeSlug returns the last path segment of an RFC 7807 type URI.
// "https://docs.cyoda.cloud/errors/tier-not-entitled" → "tier-not-entitled".
// Empty string when the URI has no slash-separated segment.
func problemTypeSlug(t string) string {
	if t == "" || t == "about:blank" {
		return ""
	}
	if i := strings.LastIndex(t, "/"); i >= 0 && i+1 < len(t) {
		return t[i+1:]
	}
	return t
}

// FromProblem maps an *api.Problem to a Code based on its `type` URI's last
// path segment. Falls back to status-based mapping when the type is missing
// or unrecognised. Returns CodeGeneric when nothing matches.
func FromProblem(p *api.Problem) Code {
	if p == nil {
		return CodeGeneric
	}
	if c, ok := codeForSlug(problemTypeSlug(p.Type)); ok {
		return c
	}
	return codeForStatus(p.Status)
}

// codeForSlug performs the RFC 7807 `type` slug lookup. Returns ok=false
// for unknown slugs (including the empty string) so the caller can fall back
// to status-based mapping.
func codeForSlug(slug string) (Code, bool) {
	switch slug {
	case "unauthenticated", "revoked":
		return CodeUnauthenticated, true
	case "forbidden":
		return CodeForbidden, true
	case "tier-not-entitled":
		return CodeTierNotEntitled, true
	case "quota-exceeded":
		return CodeQuotaExceeded, true
	case "not-found":
		return CodeNotFound, true
	case "idempotency-conflict", "cursor-expired":
		return CodeConflict, true
	case "validation-error", "invalid-org-id":
		return CodeBadUsage, true
	case "upstream-failure":
		return CodeUpstreamFailure, true
	}
	return CodeGeneric, false
}

// codeForStatus is the HTTP-status fallback used when the Problem `type` is
// missing or unknown. Mirrors spec §6.6 and §6.8.
func codeForStatus(status int) Code {
	switch status {
	case 401:
		return CodeUnauthenticated
	case 403:
		return CodeForbidden
	case 404:
		return CodeNotFound
	case 409:
		return CodeConflict
	case 412, 426:
		// 412 Precondition Failed and 426 Upgrade Required both indicate
		// the server demands a newer CLI — see spec §6.8.
		return CodeServerMinVersionRequired
	case 429:
		// Bare 429 may be a generic rate limit, but spec uses
		// `quota-exceeded` for the corresponding type — match it here.
		return CodeQuotaExceeded
	}
	if status >= 500 && status < 600 {
		return CodeUpstreamFailure
	}
	return CodeGeneric
}

// WrapHTTP turns a non-2xx HTTP response into a *CLIError. When body is
// non-nil it drives both the Code (via FromProblem) and the message; when
// body is nil the caller has only the status to go on, so the message names
// the status and the Code falls back via codeForStatus.
//
// Returns nil when status is 2xx — the call site should check ok responses
// before invoking this.
func WrapHTTP(status int, body *api.Problem) *CLIError {
	if status >= 200 && status < 300 {
		return nil
	}
	if body != nil {
		code := FromProblem(body)
		// Detail is optional but high-signal — include it when present so
		// the user gets actionable text without needing to repeat the call
		// with --output-json.
		msg := body.Title
		if body.Detail != nil && *body.Detail != "" {
			msg = fmt.Sprintf("%s: %s", body.Title, *body.Detail)
		}
		return &CLIError{Code: code, Err: fmt.Errorf("%s (status %d)", msg, body.Status)}
	}
	return &CLIError{
		Code: codeForStatus(status),
		Err:  fmt.Errorf("unexpected status %d", status),
	}
}
