package commands

import (
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
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
