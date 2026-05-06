package commands

import (
	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
)

// resolveOrg applies the standard CLI precedence to the --org flag:
//
//	explicit --org flag > config file (default_org) > "" (server-side default)
//
// Resolution happens at the command boundary so BuildAPIClient stays simple.
// A LoadFile error is silently swallowed; commands shouldn't fail for an
// orthogonal config-file problem (the explicit `cyoda-cloud config get/set`
// path surfaces that error directly).
//
// We treat both `cmd.Flags().Changed("org")` and a non-empty flagOrg as
// "user supplied a value": Changed() handles `--org=""` cleanly, and the
// non-empty fallback covers callers (or tests) that bypass cobra's parser
// and pass the value directly.
func resolveOrg(cmd *cobra.Command, flagOrg string) string {
	if flagOrg != "" || (cmd != nil && cmd.Flags().Changed("org")) {
		return flagOrg
	}
	f, err := config.LoadFile()
	if err != nil {
		return flagOrg
	}
	return f.DefaultOrg
}

// resolveOutputJSON applies the standard CLI precedence to --output-json:
//
//	explicit --output-json flag > config file (output_format=="json") > false
//
// As with resolveOrg, a LoadFile error returns the flag value unchanged.
func resolveOutputJSON(cmd *cobra.Command, flagJSON bool) bool {
	if cmd != nil && cmd.Flags().Changed("output-json") {
		return flagJSON
	}
	f, err := config.LoadFile()
	if err != nil {
		return flagJSON
	}
	return f.OutputFormat == "json"
}
