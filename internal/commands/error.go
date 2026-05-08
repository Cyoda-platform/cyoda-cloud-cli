package commands

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// problemFromBody attempts to decode body as *api.Problem when contentType
// indicates application/problem+json. Returns nil for any other content
// type or on JSON decode failure. The caller treats nil as "no typed
// problem available, fall back to status-only mapping".
//
// This is the fallback path for endpoints whose OpenAPI spec didn't declare
// a `default:` error response — codegen then doesn't expose a typed
// *Problem field for unmapped statuses (e.g. 426 Upgrade Required for
// spec §6.8 server-min-version enforcement). Once the manager's spec adds
// default error responses, this path becomes effectively dead (typed
// always wins) but staying harmless.
func problemFromBody(body []byte, contentType string) *api.Problem {
	if !strings.Contains(contentType, "application/problem+json") {
		return nil
	}
	var p api.Problem
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	return &p
}

// problemToError maps a non-2xx response into a *output.CLIError suitable
// for return from a cobra RunE.
//
// The typed parameter is the codegen-decoded *api.Problem (one of the
// per-status ApplicationproblemJSON{N} fields on a *WithResponse type).
// When typed is nil — typically because the response status wasn't declared
// in the OpenAPI spec for this endpoint — the helper falls back to decoding
// body as Problem if contentType says problem+json. Pass the body and
// content-type from resp.Body / resp.HTTPResponse.Header.Get("Content-Type").
//
// Returns nil for 2xx responses. For non-2xx, output.WrapHTTP picks the
// exit code from the Problem's `type` slug (or status fallback) and embeds
// the Title/Detail in the message.
func problemToError(status int, contentType string, body []byte, typed *api.Problem) error {
	if status >= 200 && status < 300 {
		return nil
	}
	if typed == nil {
		typed = problemFromBody(body, contentType)
	}
	if cerr := output.WrapHTTP(status, typed); cerr != nil {
		return cerr
	}
	return nil
}

// errSessionExpired wraps the canonical "session expired" message at the
// CodeUnauthenticated exit code (spec §6.6). Used for HTTP 401 paths and for
// transport-level refresh-token expiry so the process exits with code 3
// (unauthenticated) instead of the generic 1.
func errSessionExpired() error {
	return &output.CLIError{
		Code: output.CodeUnauthenticated,
		Err:  errors.New(`session expired. Run "cyoda-cloud login".`),
	}
}

// mapTransportError wraps transport-level errors that signal session expiry
// into *output.CLIError{CodeUnauthenticated}. Returns the input unchanged for
// any other error or nil.
//
// Rationale: when an API call's refresh path fails with auth.ErrSessionExpired,
// the request never produces an HTTP response — the error bubbles up from
// http.Client.Do as a plain error. Without this helper, callers wrap with
// fmt.Errorf and the typed sentinel is lost in the chain (errors.As recovers
// it, but output.Exit only inspects *CLIError). Routing every transport-error
// site through mapTransportError ensures refresh-token expiry maps to exit
// code 3 (CodeUnauthenticated) rather than the generic 1 — see spec §6.6.
func mapTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, auth.ErrSessionExpired) {
		return errSessionExpired()
	}
	return err
}
