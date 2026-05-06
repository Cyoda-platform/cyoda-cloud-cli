package commands

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// NewWhoamiCmd returns the `cyoda-cloud whoami` cobra command. It calls
// /v2/me on the configured API and renders identity, quota, and feature
// flags either as a human-readable table or as JSON.
//
// Output policy:
//   - Default: table to stdout.
//   - --output-json or non-TTY stdout: pretty-printed JSON to stdout.
//
// Per spec §6.5, status messages remain on stderr. Whoami has no status
// messages on the happy path — just data.
func NewWhoamiCmd() *cobra.Command {
	var (
		org    string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Print identity, tier, scopes, quota usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWhoami(cmd, org, asJSON)
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&asJSON, "output-json", false, "JSON output")
	return cmd
}

func runWhoami(cmd *cobra.Command, org string, asJSON bool) error {
	org = resolveOrg(cmd, org)
	asJSON = resolveOutputJSON(cmd, asJSON)
	ctx := cmd.Context()
	b, err := BuildAPIClient(cmd, org)
	if err != nil {
		return err
	}
	resp, err := b.Client.GetV2MeWithResponse(ctx)
	if err != nil {
		return mapTransportError(fmt.Errorf("whoami: %w", err))
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return errSessionExpired()
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return fmt.Errorf("whoami: unexpected status %d", resp.StatusCode())
	}

	stdout := cmd.OutOrStdout()
	useJSON := asJSON || !stdoutIsTerminal()
	if useJSON {
		return output.JSON(stdout, resp.JSON200)
	}
	return output.MeTable(stdout, resp.JSON200)
}

// stdoutIsTerminal reports whether stdout is attached to a TTY. Out-of-line
// so tests can stub it.
var stdoutIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
