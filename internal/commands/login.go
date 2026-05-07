// Package commands provides the cobra subcommands wired into cyoda-cloud.
package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
)

// envLoopbackPort lets users override the PKCE loopback port when the default
// (auth.DefaultLoopbackBindAddr) is unavailable. Whatever value is set must be
// pre-registered on Auth0's Allowed Callback URLs as
// "http://127.0.0.1:<port>/callback" — Auth0 does not wildcard ports.
const envLoopbackPort = "CYODA_CLOUD_LOOPBACK_PORT"

// loginOpts captures the resolved options for an interactive login. Both
// `login` and `register` build one of these and call runLogin — register
// hard-codes Signup=true and never exposes the flag, so users cannot
// accidentally turn it off (which silently demoted register into a login in
// the previous shared-flagset implementation).
type loginOpts struct {
	Device bool
	Org    string
	Scopes []string
	Signup bool
}

// defaultScopes is the scope list used when neither command overrides it.
// Hoisted so login and register share one source of truth.
var defaultScopes = []string{
	"openid", "profile", "email", "offline_access",
	"read:builds", "deploy:env", "cancel:env", "delete:env",
}

// NewLoginCmd returns the `cyoda-cloud login` cobra command.
func NewLoginCmd() *cobra.Command {
	return newLoginCmd()
}

func newLoginCmd() *cobra.Command {
	opts := loginOpts{}
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to Cyoda Cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Device, "device", false, "use Device Authorization Grant (no browser)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Auth0 organization slug")
	cmd.Flags().StringSliceVar(&opts.Scopes, "scope", defaultScopes, "OAuth scopes")
	cmd.Flags().BoolVar(&opts.Signup, "signup", false, "open signup screen instead of login")
	return cmd
}

// runLogin executes the interactive login flow shared by `login` and
// `register`. Splitting it out of the cobra closure keeps the flag wiring per
// command and lets register hard-code Signup=true without ever exposing the
// flag.
func runLogin(cmd *cobra.Command, opts loginOpts) error {
	// Apply config-file default_org when --org is unset; the resolved org is
	// what the keychain profile is keyed by, so a `default_org=acme` config
	// makes `cyoda-cloud login` and subsequent `whoami`/`env`/`app` calls
	// land on the same profile by default.
	opts.Org = resolveOrg(cmd, opts.Org)
	d, err := config.LoadDiscovery(config.ResolveDiscoveryURL(), shouldRefreshDiscovery(cmd))
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	ctx := cmd.Context()
	bindAddr := ""
	if p := os.Getenv(envLoopbackPort); p != "" {
		bindAddr = "127.0.0.1:" + p
	}
	loCfg := auth.LoopbackConfig{
		Auth0Domain:  d.Auth0Domain,
		ClientID:     d.Auth0ClientID,
		Audience:     d.Auth0Audience,
		Scopes:       opts.Scopes,
		Organization: opts.Org,
		SignupHint:   opts.Signup,
		BindAddr:     bindAddr,
		Stderr:       cmd.ErrOrStderr(),
	}
	var toks auth.Tokens
	if opts.Device {
		toks, err = auth.LoginDevice(ctx, loCfg)
	} else {
		toks, err = auth.LoginPKCE(ctx, loCfg)
	}
	if err != nil {
		return err
	}
	profile := keychain.Profile{
		Org:           opts.Org,
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
}
