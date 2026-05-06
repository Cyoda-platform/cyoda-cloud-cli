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

// errSessionExpired wraps the canonical "session expired" message at the
// CodeUnauthenticated exit code (spec §6.6). Used for HTTP 401 paths so the
// process exits with code 3 (unauthenticated) instead of the generic 1.
func errSessionExpired() error {
	return &output.CLIError{
		Code: output.CodeUnauthenticated,
		Err:  errors.New(`session expired. Run "cyoda-cloud login".`),
	}
}

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
	if a.backend == "" {
		// Spec says backend is required. Don't hardcode a default — surface
		// a clear error so the user (or shell completion) supplies one.
		return errors.New("--backend is required")
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
	cli, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}

	body := api.PostV2EnvJSONRequestBody{Backend: a.backend}
	if a.chatID != "" {
		body.ChatId = &a.chatID
	}
	resp, err := cli.PostV2EnvWithResponse(ctx,
		&api.PostV2EnvParams{IdempotencyKey: key},
		body,
	)
	if err != nil {
		return fmt.Errorf("env up: %w", err)
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return errSessionExpired()
	}
	snap, err := envUpSnapshot(resp)
	if err != nil {
		return err
	}

	if a.wait {
		// Short-circuit when the initial response is already terminal — e.g.
		// an idempotent replay returning 200 with state=SUCCESS. There's
		// nothing to poll for and the user shouldn't see "still …" lines.
		if !output.IsTerminalEnvState(snap.State) {
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
// three required fields.
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
	if p := firstProblem(resp.ApplicationproblemJSON400, resp.ApplicationproblemJSON403,
		resp.ApplicationproblemJSON409, resp.ApplicationproblemJSON429); p != nil {
		return nil, fmt.Errorf("env up: %s (status %d)", p.Title, p.Status)
	}
	return nil, fmt.Errorf("env up: unexpected status %d", resp.StatusCode())
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
	ctx := cmd.Context()
	cli, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}
	resp, err := cli.GetV2EnvWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("env status: %w", err)
	}
	switch resp.StatusCode() {
	case http.StatusUnauthorized:
		return errSessionExpired()
	case http.StatusNotFound:
		// Per spec §6.6 a "not-found" maps to exit code 7 — that wiring lands
		// in Task 7. For now: informational stderr message, exit zero.
		// TODO(task-7): translate this to a sentinel error so the
		// exit-code middleware can return 7 here.
		fmt.Fprintln(cmd.ErrOrStderr(), "No environment provisioned.")
		return nil
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return fmt.Errorf("env status: unexpected status %d", resp.StatusCode())
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
	ctx := cmd.Context()
	cli, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}
	resp, err := cli.PostV2EnvCancelWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("env cancel: %w", err)
	}
	switch resp.StatusCode() {
	case http.StatusAccepted:
		fmt.Fprintln(cmd.ErrOrStderr(), "env cancellation queued.")
		return nil
	case http.StatusUnauthorized:
		return errSessionExpired()
	}
	if p := firstProblem(resp.ApplicationproblemJSON404, resp.ApplicationproblemJSON409); p != nil {
		return fmt.Errorf("env cancel: %s (status %d)", p.Title, p.Status)
	}
	return fmt.Errorf("env cancel: unexpected status %d", resp.StatusCode())
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
	ctx := cmd.Context()
	cli, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}
	resp, err := cli.DeleteV2EnvWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("env down: %w", err)
	}
	switch resp.StatusCode() {
	case http.StatusUnauthorized:
		return errSessionExpired()
	case http.StatusAccepted:
		// Fallthrough to wait/render.
	default:
		if p := firstProblem(resp.ApplicationproblemJSON404, resp.ApplicationproblemJSON409); p != nil {
			return fmt.Errorf("env down: %s (status %d)", p.Title, p.Status)
		}
		return fmt.Errorf("env down: unexpected status %d", resp.StatusCode())
	}

	if !wait {
		fmt.Fprintln(cmd.ErrOrStderr(), "env teardown queued.")
		return nil
	}

	// Poll GET /v2/env until 404 (gone) or — in principle — a terminal env
	// state. With IsTerminalEnvState narrowed to SUCCESS/FAILED/CANCELLED
	// (spec §4.3 vocabulary), the 404 path is the primary signal for
	// teardown completion: the server typically transitions through
	// non-terminal states like DELETING and then removes the resource.
	// A future server version that emits an explicit terminal state on
	// teardown will be picked up only when added to IsTerminalEnvState.
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
				return "", false, fmt.Errorf("env status during wait: status %d", resp.StatusCode())
			}
			last = &output.EnvSnapshot{
				EnvId:         derefString(resp.JSON200.EnvId),
				Namespace:     derefString(resp.JSON200.Namespace),
				State:         derefString(resp.JSON200.State),
				JobStatus:     derefString(resp.JSON200.JobStatus),
				JobStatusText: derefString(resp.JSON200.JobStatusText),
			}
			return last.State, output.IsTerminalEnvState(last.State), nil
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
				gone = true
				return "GONE", true, nil
			}
			if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
				return "", false, fmt.Errorf("env status during teardown: status %d", resp.StatusCode())
			}
			last = &output.EnvSnapshot{
				EnvId:         derefString(resp.JSON200.EnvId),
				Namespace:     derefString(resp.JSON200.Namespace),
				State:         derefString(resp.JSON200.State),
				JobStatus:     derefString(resp.JSON200.JobStatus),
				JobStatusText: derefString(resp.JSON200.JobStatusText),
			}
			return last.State, output.IsTerminalEnvState(last.State), nil
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

// firstProblem returns the first non-nil problem pointer.
func firstProblem(ps ...*api.Problem) *api.Problem {
	for _, p := range ps {
		if p != nil {
			return p
		}
	}
	return nil
}
