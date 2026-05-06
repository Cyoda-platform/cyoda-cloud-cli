package commands

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestShouldRefreshDiscovery covers the wiring between the root cobra
// command's --refresh-discovery persistent flag and the helper that
// BuildAPIClient / login / version use to consult it. The integration
// (LoadDiscovery actually re-fetches when force=true) is asserted in the
// config package; here we only confirm the flag-plumbing.
func TestShouldRefreshDiscovery(t *testing.T) {
	t.Run("nil cmd returns false", func(t *testing.T) {
		if shouldRefreshDiscovery(nil) {
			t.Fatal("nil cmd should yield false")
		}
	})

	t.Run("flag absent returns false", func(t *testing.T) {
		// A cobra subcommand built in isolation, without the root flag
		// registered, must not panic and must default to false.
		sub := &cobra.Command{Use: "sub"}
		if shouldRefreshDiscovery(sub) {
			t.Fatal("missing flag should yield false")
		}
	})

	t.Run("default false; --refresh-discovery=true seen from subcommand", func(t *testing.T) {
		root := &cobra.Command{Use: "root"}
		root.PersistentFlags().Bool("refresh-discovery", false, "")
		sub := &cobra.Command{Use: "sub", RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		}}
		root.AddCommand(sub)

		// Default: flag unset.
		if shouldRefreshDiscovery(sub) {
			t.Fatal("unset flag should yield false")
		}

		// After parsing --refresh-discovery on the root, the subcommand sees it.
		root.SetArgs([]string{"sub", "--refresh-discovery"})
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		// The flag persists on the cobra command after Execute; the helper
		// reads it via cmd.Root().PersistentFlags().
		if !shouldRefreshDiscovery(sub) {
			t.Fatal("--refresh-discovery=true not visible to subcommand")
		}
	})
}
