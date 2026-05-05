package commands

import "github.com/spf13/cobra"

// NewRegisterCmd returns the `cyoda-cloud register` cobra command, which
// shares the login flow but always requests the Auth0 signup screen.
func NewRegisterCmd() *cobra.Command {
	return newRegisterCmd()
}

func newRegisterCmd() *cobra.Command {
	// Deliberately does NOT expose `--signup`. The previous implementation
	// shared the login command's flag set and pre-set --signup=true before
	// arg parsing, so `register --signup=false` silently became a login. By
	// hard-coding Signup=true here and refusing the flag, we close that
	// regression at the cobra layer.
	opts := loginOpts{Signup: true}
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Sign up for a Cyoda Cloud account",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Device, "device", false, "use Device Authorization Grant (no browser)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Auth0 organization slug")
	cmd.Flags().StringSliceVar(&opts.Scopes, "scope", defaultScopes, "OAuth scopes")
	return cmd
}
