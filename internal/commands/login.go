// Package commands provides the cobra subcommands wired into cyoda-cloud.
package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
)

// envDiscoveryURL allows local development against a file:// or staging
// discovery URL. Lifted from docs/cli-handover.md §"Auth0 setup".
const envDiscoveryURL = "CYODA_CLOUD_DISCOVERY_URL"

// NewLoginCmd returns the `cyoda-cloud login` cobra command.
func NewLoginCmd() *cobra.Command {
	return newLoginCmd()
}

func newLoginCmd() *cobra.Command {
	var device bool
	var org string
	var scopes []string
	var signup bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to Cyoda Cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			discoURL := config.DefaultDiscoveryURL
			if v := os.Getenv(envDiscoveryURL); v != "" {
				discoURL = v
			}
			d, err := config.LoadDiscovery(discoURL, false)
			if err != nil {
				return fmt.Errorf("discovery: %w", err)
			}
			ctx := context.Background()
			loCfg := auth.LoopbackConfig{
				Auth0Domain:  d.Auth0Domain,
				ClientID:     d.Auth0ClientID,
				Audience:     d.Auth0Audience,
				Scopes:       scopes,
				Organization: org,
				SignupHint:   signup,
				Stderr:       cmd.ErrOrStderr(),
			}
			var toks auth.Tokens
			if device {
				toks, err = auth.LoginDevice(ctx, loCfg)
			} else {
				toks, err = auth.LoginPKCE(ctx, loCfg)
			}
			if err != nil {
				return err
			}
			profile := keychain.Profile{
				Org:           org,
				RefreshToken:  toks.RefreshToken,
				APIURL:        d.APIURL,
				Auth0Domain:   d.Auth0Domain,
				Auth0ClientID: d.Auth0ClientID,
				Auth0Audience: d.Auth0Audience,
			}
			if err := keychain.Store(profile); err != nil {
				return err
			}
			// "Logs to stderr, data to stdout" (spec §6.5). "Logged in." is a
			// status line, not data — write to stderr.
			fmt.Fprintln(cmd.ErrOrStderr(), "Logged in.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&device, "device", false, "use Device Authorization Grant (no browser)")
	cmd.Flags().StringVar(&org, "org", "", "Auth0 organization slug")
	cmd.Flags().StringSliceVar(&scopes, "scope", []string{
		"openid", "profile", "email", "offline_access",
		"read:builds", "deploy:env", "cancel:env", "delete:env",
	}, "OAuth scopes")
	cmd.Flags().BoolVar(&signup, "signup", false, "open signup screen instead of login")
	return cmd
}
