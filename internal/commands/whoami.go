package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/version"
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
		org     string
		asJSON  bool
		jsonSet = "output-json"
	)
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Print identity, tier, scopes, quota usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWhoami(cmd, org, asJSON)
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Auth0 organization slug")
	cmd.Flags().BoolVar(&asJSON, jsonSet, false, "JSON output")
	return cmd
}

func runWhoami(cmd *cobra.Command, org string, asJSON bool) error {
	discoURL := config.DefaultDiscoveryURL
	if v := os.Getenv(envDiscoveryURL); v != "" {
		discoURL = v
	}
	d, err := config.LoadDiscovery(discoURL, false)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}

	profile, err := keychain.Load(org)
	if err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			// Task 7 will translate this to exit code 3 via an exitcode
			// middleware. For now return a clear error so the user gets
			// actionable text.
			return errors.New("not logged in. Run \"cyoda-cloud login\".")
		}
		return fmt.Errorf("keychain load: %w", err)
	}

	ctx := context.Background()
	cli, err := buildAPIClient(profile, d)
	if err != nil {
		return err
	}
	resp, err := cli.GetV2MeWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("whoami: %w", err)
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return errors.New("session expired. Run \"cyoda-cloud login\".")
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

// buildAPIClient wires together the TokenCache, auth-injecting Transport,
// and oapi-codegen client for the given profile/discovery pair.
//
// The cache's PersistFunc writes the rotated profile back to the keychain so
// a refresh-token rotation survives across invocations. The persist callback
// rewrites only the RefreshToken field — APIURL/Auth0* are sourced from
// discovery on every invocation, so we keep them in sync.
func buildAPIClient(profile keychain.Profile, d config.Discovery) (*api.ClientWithResponses, error) {
	cache := auth.NewTokenCache(
		auth.Tokens{RefreshToken: profile.RefreshToken},
		func(ctx context.Context, rt string) (auth.Tokens, error) {
			return auth.Refresh(ctx, auth.RefreshConfig{
				Auth0Domain:  d.Auth0Domain,
				ClientID:     d.Auth0ClientID,
				RefreshToken: rt,
			})
		},
		func(t auth.Tokens) error {
			updated := profile
			updated.RefreshToken = t.RefreshToken
			updated.APIURL = d.APIURL
			updated.Auth0Domain = d.Auth0Domain
			updated.Auth0ClientID = d.Auth0ClientID
			updated.Auth0Audience = d.Auth0Audience
			return keychain.Store(updated)
		},
	)
	tr := &api.Transport{
		Cache:      cache,
		CLIVersion: version.Version,
		UserAgent:  version.UserAgent(version.Version, runtime.GOOS, runtime.GOARCH),
	}
	httpClient := &http.Client{Transport: tr}
	return api.NewClientWithResponses(d.APIURL, api.WithHTTPClient(httpClient))
}

// stdoutIsTerminal reports whether stdout is attached to a TTY. Out-of-line
// so tests can stub it.
var stdoutIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
