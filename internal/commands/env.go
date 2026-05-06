package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// minIdempotencyKeyLen mirrors the OpenAPI minLength constraint on the
// Idempotency-Key header (see openapi.yaml /v2/env POST). UUIDv4 trivially
// satisfies it; we re-check here so a user-supplied key fails fast in the CLI
// rather than at the server.
const minIdempotencyKeyLen = 16

// newIdempotencyKey is a test seam — production calls uuid.NewString().
var newIdempotencyKey = func() string { return uuid.NewString() }

// defaultWaitOpts produces the WaitOpts used by --wait. Tests override this
// to shrink the polling clock to milliseconds. Status writer is filled in by
// the caller (we need cmd.ErrOrStderr() per-invocation).
var defaultWaitOpts = func() output.WaitOpts {
	return output.WaitOpts{} // zero-value → 1s/30s/30 min defaults from output.PollUntilTerminal.
}

// envCommonFlags holds the flags shared across the env subcommands. Each
// subcommand binds the subset it needs.
type envCommonFlags struct {
	org    string
	asJSON bool
}

// NewEnvCmd returns the `cyoda-cloud env` parent command. Subcommands are
// up / status / cancel / down per spec §6.4.
func NewEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Provision and manage the org's environment",
	}
	cmd.AddCommand(newEnvUpCmd())
	cmd.AddCommand(newEnvStatusCmd())
	cmd.AddCommand(newEnvCancelCmd())
	cmd.AddCommand(newEnvDownCmd())
	return cmd
}

// ---- env up ----

func newEnvUpCmd() *cobra.Command {
	var (
		f         envCommonFlags
		backend   string
		chatID    string
		idemKey   string
		wait      bool
	)
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Provision the env for caller's org",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvUp(cmd, f, envUpArgs{
				backend: backend,
				chatID:  chatID,
				idemKey: idemKey,
				wait:    wait,
			})
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	cmd.Flags().StringVar(&backend, "backend", "", "Backend tier (server-defined; required)")
	cmd.Flags().StringVar(&chatID, "chat-id", "", "Optional chat correlation id")
	cmd.Flags().StringVar(&idemKey, "idempotency-key", "",
		"Idempotency-Key header (default: random UUIDv4). Min 16 chars.")
	cmd.Flags().BoolVar(&wait, "wait", false, "Block until env reaches a terminal state")
	return cmd
}

type envUpArgs struct {
	backend string
	chatID  string
	idemKey string
	wait    bool
}

func runEnvUp(cmd *cobra.Command, f envCommonFlags, a envUpArgs) error {
	f.org = resolveOrg(cmd, f.org)
	f.asJSON = resolveOutputJSON(cmd, f.asJSON)
	if a.backend == "" {
		// Spec says backend is required. Don't hardcode a default — surface
		// a clear error so the user (or shell completion) supplies one.
		// Spec §6.6: bad usage maps to exit code 2 via CLIError.
		return &output.CLIError{
			Code: output.CodeBadUsage,
			Err:  errors.New("--backend is required"),
		}
	}
	key := a.idemKey
	if key == "" {
		key = newIdempotencyKey()
	} else if len(key) < minIdempotencyKeyLen {
		return &output.CLIError{
			Code: output.CodeBadUsage,
			Err:  fmt.Errorf("--idempotency-key must be at least %d characters", minIdempotencyKeyLen),
		}
	}

	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, f.org)
	if err != nil {
		return err
	}
	cli := b.Client

	body := api.PostV2EnvJSONRequestBody{Backend: a.backend}
	if a.chatID != "" {
		body.ChatId = &a.chatID
	}
	resp, err := cli.PostV2EnvWithResponse(ctx,
		&api.PostV2EnvParams{IdempotencyKey: key},
		body,
	)
	if err != nil {
		return mapTransportError(fmt.Errorf("env up: %w", err))
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return errSessionExpired()
	}
	if cerr := problemToError(resp.StatusCode(), envUpProblem(resp)); cerr != nil {
		return cerr
	}
	snap, err := envUpSnapshot(resp)
	if err != nil {
		return err
	}

	if a.wait {
		// Short-circuit when the initial response is already terminal — e.g.
		// an idempotent replay returning 200 with state=SUCCESS. There's
		// nothing to poll for and the user shouldn't see "still …" lines.
		if !output.IsTerminalState(snap.State) {
			final, err := waitForEnvTerminal(cmd, cli)
			if err != nil {
				return err
			}
			snap = final
		}
	}

	return renderEnv(cmd, f.asJSON, snap)
}

// envUpSnapshot maps the POST /v2/env response into the unified EnvSnapshot.
// 200 = idempotent replay, 202 = newly queued. Both shapes carry the same
// three required fields. Caller has already routed non-2xx responses through
// problemToError, so the only failure mode here is an unexpected status with
// no decoded body — surface as a generic CLIError.
func envUpSnapshot(resp *api.PostV2EnvResponse) (*output.EnvSnapshot, error) {
	switch {
	case resp.JSON202 != nil:
		return &output.EnvSnapshot{
			EnvId:     resp.JSON202.EnvId,
			Namespace: resp.JSON202.Namespace,
			State:     resp.JSON202.State,
		}, nil
	case resp.JSON200 != nil:
		return &output.EnvSnapshot{
			EnvId:     resp.JSON200.EnvId,
			Namespace: resp.JSON200.Namespace,
			State:     resp.JSON200.State,
		}, nil
	}
	return nil, &output.CLIError{
		Code: output.CodeGeneric,
		Err:  fmt.Errorf("env up: unexpected status %d", resp.StatusCode()),
	}
}

// envUpProblem dispatches to the per-status Problem field on PostV2EnvResponse.
// The codegen produces one ApplicationproblemJSON<status> field per status
// declared in the spec; problemToError tolerates a nil Problem (falls back to
// status-only mapping) so undeclared statuses still produce a CLIError.
func envUpProblem(resp *api.PostV2EnvResponse) *api.Problem {
	switch resp.StatusCode() {
	case http.StatusBadRequest:
		return resp.ApplicationproblemJSON400
	case http.StatusForbidden:
		return resp.ApplicationproblemJSON403
	case http.StatusConflict:
		return resp.ApplicationproblemJSON409
	case http.StatusTooManyRequests:
		return resp.ApplicationproblemJSON429
	}
	return nil
}

// ---- env status ----

func newEnvStatusCmd() *cobra.Command {
	var f envCommonFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print current env state for caller's org",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvStatus(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	return cmd
}

func runEnvStatus(cmd *cobra.Command, f envCommonFlags) error {
	f.org = resolveOrg(cmd, f.org)
	f.asJSON = resolveOutputJSON(cmd, f.asJSON)
	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, f.org)
	if err != nil {
		return err
	}
	cli := b.Client
	resp, err := cli.GetV2EnvWithResponse(ctx)
	if err != nil {
		return mapTransportError(fmt.Errorf("env status: %w", err))
	}
	switch resp.StatusCode() {
	case http.StatusUnauthorized:
		return errSessionExpired()
	case http.StatusNotFound:
		// Spec §6.6: not-found maps to exit code 7. Keep the informational
		// stderr line — it's the most actionable signal in a no-env shell —
		// but return a CLIError so main.go's wrapper sets the exit code.
		fmt.Fprintln(cmd.ErrOrStderr(), "No environment provisioned.")
		return &output.CLIError{
			Code: output.CodeNotFound,
			Err:  errors.New("no environment provisioned"),
		}
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		// Any other non-2xx routes through problemToError. GET /v2/env's only
		// declared error body is 404, handled above; for unexpected statuses
		// the Problem field is nil and WrapHTTP falls back to status-only.
		if cerr := problemToError(resp.StatusCode(), nil); cerr != nil {
			return cerr
		}
		return &output.CLIError{
			Code: output.CodeGeneric,
			Err:  fmt.Errorf("env status: unexpected status %d", resp.StatusCode()),
		}
	}
	snap := &output.EnvSnapshot{
		EnvId:         derefString(resp.JSON200.EnvId),
		Namespace:     derefString(resp.JSON200.Namespace),
		State:         derefString(resp.JSON200.State),
		JobStatus:     derefString(resp.JSON200.JobStatus),
		JobStatusText: derefString(resp.JSON200.JobStatusText),
	}
	return renderEnv(cmd, f.asJSON, snap)
}

// ---- env cancel ----

func newEnvCancelCmd() *cobra.Command {
	var f envCommonFlags
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel an in-flight env operation",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvCancel(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	return cmd
}

func runEnvCancel(cmd *cobra.Command, f envCommonFlags) error {
	f.org = resolveOrg(cmd, f.org)
	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, f.org)
	if err != nil {
		return err
	}
	cli := b.Client
	resp, err := cli.PostV2EnvCancelWithResponse(ctx)
	if err != nil {
		return mapTransportError(fmt.Errorf("env cancel: %w", err))
	}
	switch resp.StatusCode() {
	case http.StatusAccepted:
		fmt.Fprintln(cmd.ErrOrStderr(), "env cancellation queued.")
		return nil
	case http.StatusUnauthorized:
		return errSessionExpired()
	}
	var p *api.Problem
	switch resp.StatusCode() {
	case http.StatusNotFound:
		p = resp.ApplicationproblemJSON404
	case http.StatusConflict:
		p = resp.ApplicationproblemJSON409
	}
	if cerr := problemToError(resp.StatusCode(), p); cerr != nil {
		return cerr
	}
	return &output.CLIError{
		Code: output.CodeGeneric,
		Err:  fmt.Errorf("env cancel: unexpected status %d", resp.StatusCode()),
	}
}

// ---- env down ----

func newEnvDownCmd() *cobra.Command {
	var (
		f    envCommonFlags
		wait bool
	)
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Tear down provisioned env",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvDown(cmd, f, wait)
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	cmd.Flags().BoolVar(&wait, "wait", false, "Block until teardown reaches a terminal state")
	return cmd
}

func runEnvDown(cmd *cobra.Command, f envCommonFlags, wait bool) error {
	f.org = resolveOrg(cmd, f.org)
	f.asJSON = resolveOutputJSON(cmd, f.asJSON)
	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, f.org)
	if err != nil {
		return err
	}
	cli := b.Client
	resp, err := cli.DeleteV2EnvWithResponse(ctx)
	if err != nil {
		return mapTransportError(fmt.Errorf("env down: %w", err))
	}
	switch resp.StatusCode() {
	case http.StatusUnauthorized:
		return errSessionExpired()
	case http.StatusAccepted:
		// Fallthrough to wait/render.
	default:
		var p *api.Problem
		switch resp.StatusCode() {
		case http.StatusNotFound:
			p = resp.ApplicationproblemJSON404
		case http.StatusConflict:
			p = resp.ApplicationproblemJSON409
		}
		if cerr := problemToError(resp.StatusCode(), p); cerr != nil {
			return cerr
		}
		return &output.CLIError{
			Code: output.CodeGeneric,
			Err:  fmt.Errorf("env down: unexpected status %d", resp.StatusCode()),
		}
	}

	if !wait {
		fmt.Fprintln(cmd.ErrOrStderr(), "env teardown queued.")
		return nil
	}

	// Poll GET /v2/env until 404 (gone) or — in principle — a terminal env
	// state. With IsTerminalState narrowed to SUCCESS/FAILED/CANCELLED
	// (spec §4.3 vocabulary), the 404 path is the primary signal for
	// teardown completion: the server typically transitions through
	// non-terminal states like DELETING and then removes the resource.
	// A future server version that emits an explicit terminal state on
	// teardown will be picked up only when added to IsTerminalState.
	if _, err := waitForEnvTeardown(cmd, cli); err != nil {
		return err
	}
	// Both exit paths (404 gone, or terminal state observed) are success.
	// Don't render the snapshot — the env is torn down so any remembered
	// state is stale and unhelpful.
	fmt.Fprintln(cmd.ErrOrStderr(), "env torn down.")
	if f.asJSON {
		return output.JSON(cmd.OutOrStdout(), map[string]string{"status": "torn_down"})
	}
	return nil
}

// ---- shared helpers ----

// waitForEnvTerminal polls GET /v2/env until the env reports a terminal state.
// Used by env up --wait.
func waitForEnvTerminal(cmd *cobra.Command, cli *api.ClientWithResponses) (*output.EnvSnapshot, error) {
	var last *output.EnvSnapshot
	// The closure updates `last` on every successful poll and `last.State` is
	// the canonical post-loop value, so the state returned by PollUntilTerminal
	// is redundant here.
	_, err := output.PollUntilTerminal(cmd.Context(),
		func(ctx context.Context) (string, bool, error) {
			resp, err := cli.GetV2EnvWithResponse(ctx)
			if err != nil {
				return "", false, err
			}
			if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
				if cerr := problemToError(resp.StatusCode(), resp.ApplicationproblemJSON404); cerr != nil {
					return "", false, cerr
				}
				return "", false, fmt.Errorf("env status during wait: status %d", resp.StatusCode())
			}
			last = &output.EnvSnapshot{
				EnvId:         derefString(resp.JSON200.EnvId),
				Namespace:     derefString(resp.JSON200.Namespace),
				State:         derefString(resp.JSON200.State),
				JobStatus:     derefString(resp.JSON200.JobStatus),
				JobStatusText: derefString(resp.JSON200.JobStatusText),
			}
			return last.State, output.IsTerminalState(last.State), nil
		},
		withStatus(defaultWaitOpts(), cmd.ErrOrStderr()),
	)
	if err != nil {
		return last, err
	}
	return last, nil
}

// waitForEnvTeardown polls GET /v2/env until 404 (env gone) or a terminal
// state on the env entity. Returns nil snapshot when 404 was observed.
func waitForEnvTeardown(cmd *cobra.Command, cli *api.ClientWithResponses) (*output.EnvSnapshot, error) {
	var last *output.EnvSnapshot
	gone := false
	_, err := output.PollUntilTerminal(cmd.Context(),
		func(ctx context.Context) (string, bool, error) {
			resp, err := cli.GetV2EnvWithResponse(ctx)
			if err != nil {
				return "", false, err
			}
			if resp.StatusCode() == http.StatusNotFound {
				// Teardown completion: 404 means the env is gone. Don't route
				// this through problemToError — that would surface a CLIError
				// when in fact this is the success signal we're polling for.
				gone = true
				return "GONE", true, nil
			}
			if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
				if cerr := problemToError(resp.StatusCode(), nil); cerr != nil {
					return "", false, cerr
				}
				return "", false, fmt.Errorf("env status during teardown: status %d", resp.StatusCode())
			}
			last = &output.EnvSnapshot{
				EnvId:         derefString(resp.JSON200.EnvId),
				Namespace:     derefString(resp.JSON200.Namespace),
				State:         derefString(resp.JSON200.State),
				JobStatus:     derefString(resp.JSON200.JobStatus),
				JobStatusText: derefString(resp.JSON200.JobStatusText),
			}
			return last.State, output.IsTerminalState(last.State), nil
		},
		withStatus(defaultWaitOpts(), cmd.ErrOrStderr()),
	)
	if err != nil {
		return last, err
	}
	if gone {
		return nil, nil
	}
	return last, nil
}

// renderEnv emits the env snapshot as JSON or table depending on the flag /
// TTY.
func renderEnv(cmd *cobra.Command, asJSON bool, snap *output.EnvSnapshot) error {
	stdout := cmd.OutOrStdout()
	if asJSON || !stdoutIsTerminal() {
		return output.JSON(stdout, snap)
	}
	return output.EnvTable(stdout, snap)
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// withStatus returns a copy of opts with Status overridden — used so the test
// seam (defaultWaitOpts) can stay free of any cobra-derived writer.
func withStatus(opts output.WaitOpts, w io.Writer) output.WaitOpts {
	opts.Status = w
	return opts
}

