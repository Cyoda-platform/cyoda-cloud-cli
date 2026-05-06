// Package commands — app subtree.
//
// `cyoda-cloud app` exposes the v0 build/deploy/list/status/cancel/delete
// surface against /v2/builds. In v0 every mutating action is tier-blocked
// at the server (returns RFC 7807 tier-not-entitled / 403); the read paths
// (list, status) work normally. The CLI translates the Problem document
// into a CLIError so main.go's wrapper sets exit code 5 for blocked
// mutations — see spec §6.6.
package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// appCommonFlags holds the flags shared across the app subcommands.
type appCommonFlags struct {
	org    string
	asJSON bool
}

// NewAppCmd returns the `cyoda-cloud app` parent command.
func NewAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Trigger and inspect application builds and deploys",
	}
	cmd.AddCommand(newAppBuildCmd())
	cmd.AddCommand(newAppDeployCmd())
	cmd.AddCommand(newAppListCmd())
	cmd.AddCommand(newAppStatusCmd())
	cmd.AddCommand(newAppCancelCmd())
	cmd.AddCommand(newAppDeleteCmd())
	return cmd
}

// ---- app build / deploy ----

// appBuildArgs gathers the request shape for both `app build` and
// `app deploy` — the only difference is the `action` discriminator.
type appBuildArgs struct {
	repo           string
	branch         string
	chatID         string
	installationID string
	isPublic       bool
	idemKey        string
	wait           bool
}

func newAppBuildCmd() *cobra.Command {
	var (
		f appCommonFlags
		a appBuildArgs
	)
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Trigger a build (tier-blocked in v0)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppBuildOrDeploy(cmd, f, a, api.PostV2BuildsJSONBodyActionBuild)
		},
	}
	bindAppBuildFlags(cmd, &f, &a)
	return cmd
}

func newAppDeployCmd() *cobra.Command {
	var (
		f appCommonFlags
		a appBuildArgs
	)
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Trigger a deploy (tier-blocked in v0)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppBuildOrDeploy(cmd, f, a, api.PostV2BuildsJSONBodyActionDeploy)
		},
	}
	bindAppBuildFlags(cmd, &f, &a)
	return cmd
}

func bindAppBuildFlags(cmd *cobra.Command, f *appCommonFlags, a *appBuildArgs) {
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	cmd.Flags().StringVar(&a.repo, "repo", "", "Git repository URL")
	cmd.Flags().StringVar(&a.branch, "branch", "main", "Branch name")
	cmd.Flags().StringVar(&a.chatID, "chat-id", "", "Optional chat correlation id")
	cmd.Flags().StringVar(&a.installationID, "installation-id", "", "Optional GitHub App installation id")
	cmd.Flags().BoolVar(&a.isPublic, "public", false, "Mark the repository as public")
	cmd.Flags().StringVar(&a.idemKey, "idempotency-key", "",
		"Idempotency-Key header (default: random UUIDv4). Min 16 chars.")
	cmd.Flags().BoolVar(&a.wait, "wait", false, "Block until the build reaches a terminal state")
}

func runAppBuildOrDeploy(cmd *cobra.Command, f appCommonFlags, a appBuildArgs, action api.PostV2BuildsJSONBodyAction) error {
	if a.repo == "" {
		return &output.CLIError{
			Code: output.CodeBadUsage,
			Err:  errors.New("--repo is required"),
		}
	}
	if a.branch == "" {
		return &output.CLIError{
			Code: output.CodeBadUsage,
			Err:  errors.New("--branch is required"),
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
	cli, _, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}

	body := api.PostV2BuildsJSONRequestBody{
		Action:        action,
		RepositoryUrl: a.repo,
		BranchName:    a.branch,
	}
	if a.chatID != "" {
		body.ChatId = &a.chatID
	}
	if a.installationID != "" {
		body.InstallationId = &a.installationID
	}
	if a.isPublic {
		v := true
		body.IsPublic = &v
	}

	resp, err := cli.PostV2BuildsWithResponse(ctx,
		&api.PostV2BuildsParams{IdempotencyKey: key},
		body,
	)
	if err != nil {
		return fmt.Errorf("app %s: %w", action, err)
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return &output.CLIError{
			Code: output.CodeUnauthenticated,
			Err:  errors.New("session expired. Run \"cyoda-cloud login\"."),
		}
	}
	if cerr := problemToError(resp.StatusCode(), resp.ApplicationproblemJSON403); cerr != nil {
		return cerr
	}
	if resp.JSON202 == nil {
		return &output.CLIError{
			Code: output.CodeGeneric,
			Err:  fmt.Errorf("app %s: unexpected status %d", action, resp.StatusCode()),
		}
	}

	snap := &output.BuildSnapshot{
		BuildId: derefString(resp.JSON202.BuildId),
		Action:  string(action),
		State:   derefString(resp.JSON202.State),
		// branch is request-side; preserve so the user sees it in the table.
		BranchName: a.branch,
	}

	if a.wait && snap.BuildId != "" {
		if !output.IsTerminalAppState(snap.State) {
			final, err := waitForBuildTerminal(cmd, cli, snap.BuildId)
			if err != nil {
				return err
			}
			if final != nil {
				// Carry over the request-side branch onto the polled snapshot.
				if final.BranchName == "" {
					final.BranchName = a.branch
				}
				snap = final
			}
		}
	}

	return renderBuild(cmd, f.asJSON, snap)
}

// ---- app list ----

type appListArgs struct {
	limit  int
	cursor string
	action string
	state  string
	branch string
	since  string
}

func newAppListCmd() *cobra.Command {
	var (
		f appCommonFlags
		a appListArgs
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List builds for caller's org",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppList(cmd, f, a)
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	cmd.Flags().IntVar(&a.limit, "limit", 0, "Max items per page (1-100)")
	cmd.Flags().StringVar(&a.cursor, "cursor", "", "Opaque pagination cursor")
	cmd.Flags().StringVar(&a.action, "action", "", "Filter by action: build|deploy")
	cmd.Flags().StringVar(&a.state, "state", "", "Filter by state")
	// branch and since are accepted for forward compatibility — the v0
	// server schema doesn't yet honour them, but accepting them now keeps
	// shell scripts stable.
	cmd.Flags().StringVar(&a.branch, "branch", "", "Filter by branch (reserved; not yet honoured server-side)")
	cmd.Flags().StringVar(&a.since, "since", "", "Filter by created_at (RFC3339; reserved)")
	return cmd
}

func runAppList(cmd *cobra.Command, f appCommonFlags, a appListArgs) error {
	ctx := cmd.Context()
	cli, _, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}

	params := &api.GetV2BuildsParams{}
	if a.limit > 0 {
		params.Limit = &a.limit
	}
	if a.cursor != "" {
		params.Cursor = &a.cursor
	}
	if a.action != "" {
		act := api.GetV2BuildsParamsAction(a.action)
		params.Action = &act
	}
	if a.state != "" {
		params.State = &a.state
	}

	resp, err := cli.GetV2BuildsWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("app list: %w", err)
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return &output.CLIError{
			Code: output.CodeUnauthenticated,
			Err:  errors.New("session expired. Run \"cyoda-cloud login\"."),
		}
	}
	if cerr := problemToError(resp.StatusCode(), resp.ApplicationproblemJSON409); cerr != nil {
		return cerr
	}
	if resp.JSON200 == nil {
		return &output.CLIError{
			Code: output.CodeGeneric,
			Err:  fmt.Errorf("app list: unexpected status %d", resp.StatusCode()),
		}
	}

	snaps := make([]output.BuildSnapshot, 0, len(resp.JSON200.Items))
	for _, b := range resp.JSON200.Items {
		snaps = append(snaps, buildToSnapshot(&b))
	}
	nextCursor := ""
	if resp.JSON200.NextCursor != nil {
		nextCursor = *resp.JSON200.NextCursor
	}

	stdout := cmd.OutOrStdout()
	if f.asJSON || !stdoutIsTerminal() {
		// JSON shape: {items: [...], next_cursor: "..."}
		out := struct {
			Items      []output.BuildSnapshot `json:"items"`
			NextCursor string                 `json:"next_cursor,omitempty"`
		}{Items: snaps, NextCursor: nextCursor}
		return output.JSON(stdout, out)
	}
	if err := output.BuildListTable(stdout, snaps); err != nil {
		return err
	}
	if nextCursor != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "next_cursor=%s\n", nextCursor)
	}
	return nil
}

// ---- app status ----

func newAppStatusCmd() *cobra.Command {
	var f appCommonFlags
	cmd := &cobra.Command{
		Use:   "status <build_id>",
		Short: "Print current state of a single build",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppStatus(cmd, f, args[0])
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&f.asJSON, "output-json", false, "JSON output")
	return cmd
}

func runAppStatus(cmd *cobra.Command, f appCommonFlags, buildID string) error {
	ctx := cmd.Context()
	cli, _, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}
	resp, err := cli.GetV2BuildsBuildIdWithResponse(ctx, buildID)
	if err != nil {
		return fmt.Errorf("app status: %w", err)
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return &output.CLIError{
			Code: output.CodeUnauthenticated,
			Err:  errors.New("session expired. Run \"cyoda-cloud login\"."),
		}
	}
	if cerr := problemToError(resp.StatusCode(), resp.ApplicationproblemJSON404); cerr != nil {
		return cerr
	}
	if resp.JSON200 == nil {
		return &output.CLIError{
			Code: output.CodeGeneric,
			Err:  fmt.Errorf("app status: unexpected status %d", resp.StatusCode()),
		}
	}
	snap := buildToSnapshot(resp.JSON200)
	return renderBuild(cmd, f.asJSON, &snap)
}

// ---- app cancel ----

func newAppCancelCmd() *cobra.Command {
	var f appCommonFlags
	cmd := &cobra.Command{
		Use:   "cancel <build_id>",
		Short: "Cancel an in-flight build (tier-blocked in v0)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppCancel(cmd, f, args[0])
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	return cmd
}

func runAppCancel(cmd *cobra.Command, f appCommonFlags, buildID string) error {
	ctx := cmd.Context()
	cli, _, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}
	resp, err := cli.PostV2BuildsBuildIdCancelWithResponse(ctx, buildID)
	if err != nil {
		return fmt.Errorf("app cancel: %w", err)
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return &output.CLIError{
			Code: output.CodeUnauthenticated,
			Err:  errors.New("session expired. Run \"cyoda-cloud login\"."),
		}
	}
	if resp.StatusCode() == http.StatusAccepted {
		fmt.Fprintln(cmd.ErrOrStderr(), "build cancellation queued.")
		return nil
	}
	if cerr := problemToError(resp.StatusCode(), resp.ApplicationproblemJSON403); cerr != nil {
		return cerr
	}
	return &output.CLIError{
		Code: output.CodeGeneric,
		Err:  fmt.Errorf("app cancel: unexpected status %d", resp.StatusCode()),
	}
}

// ---- app delete ----

func newAppDeleteCmd() *cobra.Command {
	var f appCommonFlags
	cmd := &cobra.Command{
		Use:   "delete <build_id>",
		Short: "Delete a build's artifacts (tier-blocked in v0)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppDelete(cmd, f, args[0])
		},
	}
	cmd.Flags().StringVar(&f.org, "org", "", "Auth0 organization slug")
	return cmd
}

func runAppDelete(cmd *cobra.Command, f appCommonFlags, buildID string) error {
	ctx := cmd.Context()
	cli, _, _, _, err := BuildAPIClient(ctx, f.org)
	if err != nil {
		return err
	}
	resp, err := cli.DeleteV2BuildsBuildIdWithResponse(ctx, buildID)
	if err != nil {
		return fmt.Errorf("app delete: %w", err)
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return &output.CLIError{
			Code: output.CodeUnauthenticated,
			Err:  errors.New("session expired. Run \"cyoda-cloud login\"."),
		}
	}
	if resp.StatusCode() == http.StatusAccepted {
		fmt.Fprintln(cmd.ErrOrStderr(), "build deletion queued.")
		return nil
	}
	if cerr := problemToError(resp.StatusCode(), resp.ApplicationproblemJSON403); cerr != nil {
		return cerr
	}
	return &output.CLIError{
		Code: output.CodeGeneric,
		Err:  fmt.Errorf("app delete: unexpected status %d", resp.StatusCode()),
	}
}

// ---- shared helpers ----

// waitForBuildTerminal polls GET /v2/builds/{id} until the build reports a
// terminal state. Returns the final snapshot.
func waitForBuildTerminal(cmd *cobra.Command, cli *api.ClientWithResponses, buildID string) (*output.BuildSnapshot, error) {
	var last *output.BuildSnapshot
	_, err := output.PollUntilTerminal(cmd.Context(),
		func(ctx context.Context) (string, bool, error) {
			resp, err := cli.GetV2BuildsBuildIdWithResponse(ctx, buildID)
			if err != nil {
				return "", false, err
			}
			if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
				return "", false, fmt.Errorf("build status during wait: status %d", resp.StatusCode())
			}
			snap := buildToSnapshot(resp.JSON200)
			last = &snap
			return last.State, output.IsTerminalAppState(last.State), nil
		},
		withStatus(defaultWaitOpts(), cmd.ErrOrStderr()),
	)
	if err != nil {
		return last, err
	}
	return last, nil
}

// buildToSnapshot flattens an *api.Build into the unified BuildSnapshot
// shape used by the renderers. Optional fields are dereferenced to "".
func buildToSnapshot(b *api.Build) output.BuildSnapshot {
	if b == nil {
		return output.BuildSnapshot{}
	}
	createdAt := ""
	if b.CreatedAt != nil {
		createdAt = b.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return output.BuildSnapshot{
		BuildId:       b.BuildId,
		Action:        derefString(b.Action),
		State:         b.State,
		CreatedAt:     createdAt,
		JobStatus:     derefString(b.JobStatus),
		JobStatusText: derefString(b.JobStatusText),
		PipelineName:  derefString(b.PipelineName),
		ChatId:        derefString(b.ChatId),
	}
}

// renderBuild emits the BuildSnapshot as JSON or table depending on flag/TTY.
func renderBuild(cmd *cobra.Command, asJSON bool, snap *output.BuildSnapshot) error {
	stdout := cmd.OutOrStdout()
	if asJSON || !stdoutIsTerminal() {
		return output.JSON(stdout, snap)
	}
	return output.BuildTable(stdout, snap)
}
