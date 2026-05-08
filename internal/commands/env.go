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
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/envname"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// minIdempotencyKeyLen mirrors the OpenAPI minLength constraint on the
// Idempotency-Key header (see openapi.yaml /v2/envs POST). UUIDv4 trivially
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
// up / list / status / cancel / down per spec §6.4.
func NewEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Provision and manage envs for the caller's org",
	}
	cmd.AddCommand(newEnvUpCmd())
	cmd.AddCommand(newEnvListCmd())
	cmd.AddCommand(newEnvStatusCmd())
	cmd.AddCommand(newEnvCancelCmd())
	cmd.AddCommand(newEnvDownCmd())
	return cmd
}

// ---- env up ----

func newEnvUpCmd() *cobra.Command {
	var (
		f                envCommonFlags
		backend          string
		chatID           string
		idemKey          string
		wait             bool
		m2mWithAdminRole bool
	)
	cmd := &cobra.Command{
		Use:   "up <name>",
		Short: "Provision a named env for caller's org",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvUp(cmd, f, envUpArgs{
				name:             args[0],
				backend:          backend,
				chatID:           chatID,
				idemKey:          idemKey,
				wait:             wait,
				m2mWithAdminRole: m2mWithAdminRole,
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
	cmd.Flags().BoolVar(&m2mWithAdminRole, "m2m-with-admin-role", false,
		"Request that the bootstrap-minted M2M client also receive ADMIN on the env")
	return cmd
}

type envUpArgs struct {
	name             string
	backend          string
	chatID           string
	idemKey          string
	wait             bool
	m2mWithAdminRole bool
}

func runEnvUp(cmd *cobra.Command, f envCommonFlags, a envUpArgs) error {
	f.org = resolveOrg(cmd, f.org)
	f.asJSON = resolveOutputJSON(cmd, f.asJSON)

	// Best-effort client-side env-name validation. Server is authoritative;
	// fail fast here to spare a network round trip on obvious mistakes.
	if err := envname.Validate(a.name); err != nil {
		return &output.CLIError{Code: output.CodeBadUsage, Err: err}
	}
	if a.backend == "" {
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

	body := api.ProvisionEnvJSONRequestBody{
		EnvName: a.name,
		Backend: a.backend,
	}
	if a.chatID != "" {
		body.ChatId = &a.chatID
	}
	if a.m2mWithAdminRole {
		t := true
		body.M2mWithAdminRole = &t
	}
	resp, err := cli.ProvisionEnvWithResponse(ctx,
		&api.ProvisionEnvParams{IdempotencyKey: key},
		body,
	)
	if err != nil {
		return mapTransportError(fmt.Errorf("env up: %w", err))
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return errSessionExpired()
	}
	if cerr := envUpErrorFromResponse(resp); cerr != nil {
		return cerr
	}
	snap, err := envUpSnapshot(resp)
	if err != nil {
		return err
	}

	if a.wait {
		// Short-circuit when the initial response is already terminal — e.g.
		// an idempotent replay returning 200 with state=Ready. There's
		// nothing to poll for and the user shouldn't see "still …" lines.
		if !output.IsTerminalState(snap.State) {
			final, err := waitForEnvTerminal(cmd, cli, a.name)
			if err != nil {
				return err
			}
			snap = final
		}
	}

	return renderEnv(cmd, f.asJSON, snap)
}

// envUpSnapshot maps the POST /v2/envs response into the unified EnvSnapshot.
// 200 = idempotent replay, 202 = newly queued. Both shapes carry an EnvDetail.
// Caller has already routed non-2xx responses through envUpErrorFromResponse,
// so the only failure mode here is an unexpected status with no decoded body —
// surface as a generic CLIError.
func envUpSnapshot(resp *api.ProvisionEnvResponse) (*output.EnvSnapshot, error) {
	switch {
	case resp.JSON202 != nil:
		return envSnapshotFromDetail(resp.JSON202), nil
	case resp.JSON200 != nil:
		return envSnapshotFromDetail(resp.JSON200), nil
	}
	return nil, &output.CLIError{
		Code: output.CodeGeneric,
		Err:  fmt.Errorf("env up: unexpected status %d", resp.StatusCode()),
	}
}

// envUpErrorFromResponse maps the per-status Problem field on
// ProvisionEnvResponse to a CLIError. The 409 path carries the specialised
// EnvAlreadyExistsProblem (which differs structurally from Problem only in
// the optional env_id extension) — we collapse it to a generic Problem so the
// existing problemToError plumbing handles the exit-code mapping. Both
// idempotency-conflict and env-already-exists map to CodeConflict per spec
// §6.6, so the collapse is lossless for exit-code purposes.
//
// A future task could surface result.LeaderIdemKey's env_id back to the user
// (e.g. "env 'dev' already exists at <env_id>") — left as a follow-up.
func envUpErrorFromResponse(resp *api.ProvisionEnvResponse) error {
	status := resp.StatusCode()
	if status >= 200 && status < 300 {
		return nil
	}
	var p *api.Problem
	switch status {
	case http.StatusBadRequest:
		p = resp.ApplicationproblemJSON400
	case http.StatusForbidden:
		p = resp.ApplicationproblemJSON403
	case http.StatusConflict:
		if eae := resp.ApplicationproblemJSON409; eae != nil {
			p = problemFromEnvAlreadyExists(eae)
		}
	case http.StatusTooManyRequests:
		p = resp.ApplicationproblemJSON429
	}
	return problemToError(status, p)
}

// problemFromEnvAlreadyExists collapses the specialised 409 shape into the
// generic Problem the exit-code mapper consumes. Title/Status/Detail/Type are
// the only fields the mapper reads, so dropping the env_id extension here is
// safe for now.
func problemFromEnvAlreadyExists(eae *api.EnvAlreadyExistsProblem) *api.Problem {
	if eae == nil {
		return nil
	}
	out := &api.Problem{}
	if eae.Type != nil {
		out.Type = *eae.Type
	}
	if eae.Title != nil {
		out.Title = *eae.Title
	}
	if eae.Status != nil {
		out.Status = *eae.Status
	}
	out.Detail = eae.Detail
	return out
}

// ---- env list ----

func newEnvListCmd() *cobra.Command {
	var (
		f               envCommonFlags
		includeTerminal bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List envs for caller's org",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvList(cmd, f, includeTerminal)
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	cmd.Flags().BoolVar(&includeTerminal, "include-terminal", false,
		"Include envs in terminal states (torn down, failed)")
	return cmd
}

func runEnvList(cmd *cobra.Command, f envCommonFlags, includeTerminal bool) error {
	f.org = resolveOrg(cmd, f.org)
	f.asJSON = resolveOutputJSON(cmd, f.asJSON)

	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, f.org)
	if err != nil {
		return err
	}
	cli := b.Client

	params := &api.ListEnvsParams{}
	if includeTerminal {
		t := true
		params.IncludeTerminal = &t
	}
	resp, err := cli.ListEnvsWithResponse(ctx, params)
	if err != nil {
		return mapTransportError(fmt.Errorf("env list: %w", err))
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return errSessionExpired()
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		if cerr := problemToError(resp.StatusCode(), nil); cerr != nil {
			return cerr
		}
		return &output.CLIError{
			Code: output.CodeGeneric,
			Err:  fmt.Errorf("env list: unexpected status %d", resp.StatusCode()),
		}
	}

	envs := envSnapshotsFromList(*resp.JSON200)
	stdout := cmd.OutOrStdout()
	if f.asJSON || !stdoutIsTerminal() {
		// JSON consumers see the array directly (parses cleanly into []EnvSnapshot).
		return output.JSON(stdout, envs)
	}
	if len(envs) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "no envs")
		return nil
	}
	return output.EnvListTable(stdout, envs)
}

// ---- env status ----

func newEnvStatusCmd() *cobra.Command {
	var f envCommonFlags
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Print state for a named env",
		// Implement the no-arg case ourselves so the error message can hint at
		// `env list` per spec §6.4.
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &output.CLIError{
					Code: output.CodeBadUsage,
					Err: errors.New(
						"env status requires a name. Run 'cyoda-cloud env list' to see available envs."),
				}
			}
			return runEnvStatus(cmd, f, args[0])
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	return cmd
}

func runEnvStatus(cmd *cobra.Command, f envCommonFlags, name string) error {
	f.org = resolveOrg(cmd, f.org)
	f.asJSON = resolveOutputJSON(cmd, f.asJSON)

	if err := envname.Validate(name); err != nil {
		return &output.CLIError{Code: output.CodeBadUsage, Err: err}
	}

	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, f.org)
	if err != nil {
		return err
	}
	cli := b.Client
	resp, err := cli.GetEnvWithResponse(ctx, name)
	if err != nil {
		return mapTransportError(fmt.Errorf("env status: %w", err))
	}
	switch resp.StatusCode() {
	case http.StatusUnauthorized:
		return errSessionExpired()
	case http.StatusNotFound:
		// Spec §6.6: not-found maps to exit code 7.
		return &output.CLIError{
			Code: output.CodeNotFound,
			Err:  fmt.Errorf("env %q not found", name),
		}
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		if cerr := problemToError(resp.StatusCode(), nil); cerr != nil {
			return cerr
		}
		return &output.CLIError{
			Code: output.CodeGeneric,
			Err:  fmt.Errorf("env status: unexpected status %d", resp.StatusCode()),
		}
	}
	snap := envSnapshotFromDetail(resp.JSON200)
	return renderEnv(cmd, f.asJSON, snap)
}

// ---- env cancel ----

func newEnvCancelCmd() *cobra.Command {
	var f envCommonFlags
	cmd := &cobra.Command{
		Use:   "cancel <name>",
		Short: "Cancel an in-flight env operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvCancel(cmd, f, args[0])
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	return cmd
}

func runEnvCancel(cmd *cobra.Command, f envCommonFlags, name string) error {
	f.org = resolveOrg(cmd, f.org)

	if err := envname.Validate(name); err != nil {
		return &output.CLIError{Code: output.CodeBadUsage, Err: err}
	}

	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, f.org)
	if err != nil {
		return err
	}
	cli := b.Client
	resp, err := cli.CancelEnvWithResponse(ctx, name)
	if err != nil {
		return mapTransportError(fmt.Errorf("env cancel: %w", err))
	}
	switch resp.StatusCode() {
	case http.StatusAccepted:
		fmt.Fprintf(cmd.ErrOrStderr(), "env cancellation queued for %s.\n", name)
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
		Use:   "down <name>",
		Short: "Tear down a named env",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvDown(cmd, f, args[0], wait)
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	cmd.Flags().BoolVar(&wait, "wait", false, "Block until teardown reaches a terminal state")
	return cmd
}

func runEnvDown(cmd *cobra.Command, f envCommonFlags, name string, wait bool) error {
	f.org = resolveOrg(cmd, f.org)
	f.asJSON = resolveOutputJSON(cmd, f.asJSON)

	if err := envname.Validate(name); err != nil {
		return &output.CLIError{Code: output.CodeBadUsage, Err: err}
	}

	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, f.org)
	if err != nil {
		return err
	}
	cli := b.Client
	resp, err := cli.DeleteEnvWithResponse(ctx, name)
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
		if resp.StatusCode() == http.StatusNotFound {
			p = resp.ApplicationproblemJSON404
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
		fmt.Fprintf(cmd.ErrOrStderr(), "env teardown queued for %s.\n", name)
		return nil
	}

	// Poll GET /v2/envs/{name} until 404 (gone) or a terminal state. The
	// teardown vocabulary on the new server includes Env_Torn_Down as a
	// terminal state in addition to the legacy SUCCESS/FAILED/CANCELLED set
	// — see output.IsTerminalState.
	if _, err := waitForEnvTeardown(cmd, cli, name); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "env %s torn down.\n", name)
	if f.asJSON {
		return output.JSON(cmd.OutOrStdout(), map[string]string{"status": "torn_down"})
	}
	return nil
}

// ---- shared helpers ----

// waitForEnvTerminal polls GET /v2/envs/{name} until the env reports a
// terminal state. Used by env up --wait.
func waitForEnvTerminal(cmd *cobra.Command, cli *api.ClientWithResponses, name string) (*output.EnvSnapshot, error) {
	var last *output.EnvSnapshot
	_, err := output.PollUntilTerminal(cmd.Context(),
		func(ctx context.Context) (string, bool, error) {
			resp, err := cli.GetEnvWithResponse(ctx, name)
			if err != nil {
				return "", false, err
			}
			if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
				if cerr := problemToError(resp.StatusCode(), resp.ApplicationproblemJSON404); cerr != nil {
					return "", false, cerr
				}
				return "", false, fmt.Errorf("env status during wait: status %d", resp.StatusCode())
			}
			last = envSnapshotFromDetail(resp.JSON200)
			return last.State, output.IsTerminalState(last.State), nil
		},
		withStatus(defaultWaitOpts(), cmd.ErrOrStderr()),
	)
	if err != nil {
		return last, err
	}
	return last, nil
}

// waitForEnvTeardown polls GET /v2/envs/{name} until 404 (env gone) or a
// terminal state on the env entity. Returns nil snapshot when 404 was
// observed.
func waitForEnvTeardown(cmd *cobra.Command, cli *api.ClientWithResponses, name string) (*output.EnvSnapshot, error) {
	var last *output.EnvSnapshot
	gone := false
	_, err := output.PollUntilTerminal(cmd.Context(),
		func(ctx context.Context) (string, bool, error) {
			resp, err := cli.GetEnvWithResponse(ctx, name)
			if err != nil {
				return "", false, err
			}
			if resp.StatusCode() == http.StatusNotFound {
				gone = true
				return "GONE", true, nil
			}
			if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
				if cerr := problemToError(resp.StatusCode(), nil); cerr != nil {
					return "", false, cerr
				}
				return "", false, fmt.Errorf("env status during teardown: status %d", resp.StatusCode())
			}
			last = envSnapshotFromDetail(resp.JSON200)
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

// envSnapshotFromDetail maps an *api.EnvDetail to the unified EnvSnapshot
// shape rendered by output.EnvTable. The codegen makes most fields
// pointer-typed (because the spec doesn't declare them required); we collapse
// nils to empty strings so the renderer stays uniform.
func envSnapshotFromDetail(d *api.EnvDetail) *output.EnvSnapshot {
	if d == nil {
		return &output.EnvSnapshot{}
	}
	snap := &output.EnvSnapshot{
		EnvName:      derefString(d.EnvName),
		Namespace:    derefString(d.Namespace),
		AppNamespace: derefString(d.AppNamespace),
		CyodaEnvURL:  derefString(d.CyodaEnvUrl),
		M2MClientID:  derefString(d.M2mClientId),
		State:        derefString(d.State),
		BuildID:      derefString(d.BuildId),
	}
	if d.EnvId != nil {
		snap.EnvID = d.EnvId.String()
	}
	if d.CreationDate != nil {
		snap.CreationDate = d.CreationDate.UTC().Format("2006-01-02T15:04:05Z")
	}
	return snap
}

// envSnapshotsFromList maps an EnvList (slice of EnvSummary) to a slice of
// snapshots suitable for table or JSON rendering. Summary lacks the detail-
// only fields (app_namespace, cyoda_env_url, m2m_client_id, build_id), which
// stay zero — the table omits them in summary mode.
func envSnapshotsFromList(list api.EnvList) []output.EnvSnapshot {
	out := make([]output.EnvSnapshot, 0, len(list))
	for i := range list {
		s := list[i]
		snap := output.EnvSnapshot{
			EnvName:   derefString(s.EnvName),
			Namespace: derefString(s.Namespace),
			State:     derefString(s.State),
		}
		if s.EnvId != nil {
			snap.EnvID = s.EnvId.String()
		}
		if s.CreationDate != nil {
			snap.CreationDate = s.CreationDate.UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, snap)
	}
	return out
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
