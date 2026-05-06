package commands

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// NewTokenCmd returns the `cyoda-cloud token` parent command. Today only
// `print` is wired; the parent shape exists so future commands (e.g. rotate)
// can attach without breaking the user-facing surface.
func NewTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage and inspect API tokens",
	}
	cmd.AddCommand(newTokenPrintCmd())
	return cmd
}

func newTokenPrintCmd() *cobra.Command {
	var (
		org  string
		show bool
	)
	cmd := &cobra.Command{
		Use:   "print",
		Short: "Print the current access token (requires --show)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokenPrint(cmd, org, show)
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&show, "show", false, "explicitly confirm printing the access token")
	return cmd
}

func runTokenPrint(cmd *cobra.Command, org string, show bool) error {
	if !show {
		// Spec §6.6: bad usage maps to exit code 2. Refuse the command rather
		// than printing the token by default — tokens grant API access; the
		// user must explicitly opt in so a curious `token print` in shell
		// history doesn't leak credentials.
		return &output.CLIError{
			Code: output.CodeBadUsage,
			Err: errors.New(
				"refusing to print access token without --show. Tokens grant API access; pass --show to confirm."),
		}
	}
	org = resolveOrg(cmd, org)
	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, org)
	if err != nil {
		return err
	}
	at, err := b.Cache.AccessToken(ctx)
	if err != nil {
		return fmt.Errorf("token print: %w", err)
	}
	// Per plan §Step 3: token goes to stderr (data-destination is stderr here
	// because the value is sensitive and stdout streams are commonly logged
	// or captured by shell pipes; the user must consciously redirect 2>).
	fmt.Fprintln(cmd.ErrOrStderr(), at)
	return nil
}
