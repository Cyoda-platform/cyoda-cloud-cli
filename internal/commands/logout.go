package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
)

// NewLogoutCmd returns the `cyoda-cloud logout` cobra command. Logout reads
// the keychain profile, revokes the refresh token at Auth0, and deletes the
// local entry. It is idempotent: running it twice is fine.
func NewLogoutCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Log out and revoke the stored refresh token",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogout(cmd, org)
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Auth0 organization slug")
	return cmd
}

func runLogout(cmd *cobra.Command, org string) error {
	stderr := cmd.ErrOrStderr()
	profile, err := keychain.Load(org)
	if err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			fmt.Fprintln(stderr, "Not logged in.")
			return nil
		}
		return fmt.Errorf("keychain load: %w", err)
	}

	// Best-effort revoke: failing the network call must not block the user
	// from getting logged out locally. Per RFC 7009 §2.2 the endpoint is
	// idempotent, so retries are also safe.
	if err := auth.Revoke(context.Background(), profile.Auth0Domain, profile.Auth0ClientID, profile.RefreshToken); err != nil {
		fmt.Fprintln(stderr, "warning: failed to revoke refresh token:", err)
	}

	if err := keychain.Delete(org); err != nil && !errors.Is(err, keychain.ErrNotFound) {
		return fmt.Errorf("keychain delete: %w", err)
	}
	fmt.Fprintln(stderr, "Logged out.")
	return nil
}
