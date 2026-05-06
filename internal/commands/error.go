package commands

import (
	"errors"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// problemToError converts an HTTP response status + optional decoded Problem
// into a *output.CLIError suitable for return from a cobra RunE.
//
// Returns nil for 2xx responses. For non-2xx, output.WrapHTTP picks the
// exit code from the Problem's `type` slug (or status fallback) and embeds
// the Title/Detail in the message. Pass problem=nil when the response body
// was empty or did not decode as a Problem — WrapHTTP will then use the raw
// status to pick a code and emit a generic "unexpected status N" message.
func problemToError(status int, problem *api.Problem) error {
	if status >= 200 && status < 300 {
		return nil
	}
	if cerr := output.WrapHTTP(status, problem); cerr != nil {
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
