package commands

import "github.com/spf13/cobra"

// NewRegisterCmd returns the `cyoda-cloud register` cobra command, which is
// an alias for `login --signup=true`.
func NewRegisterCmd() *cobra.Command {
	return newRegisterCmd()
}

func newRegisterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Sign up for a Cyoda Cloud account",
	}
	login := newLoginCmd()
	cmd.RunE = login.RunE
	cmd.Flags().AddFlagSet(login.Flags())
	_ = cmd.Flags().Set("signup", "true")
	return cmd
}
