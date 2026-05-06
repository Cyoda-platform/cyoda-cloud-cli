package commands

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
)

// resolveWarnSink returns the writer the resolve helpers route their
// "config unreadable" warning to. We prefer cmd.ErrOrStderr() so cobra's
// test wiring captures it; for callers that bypass cobra (tests passing a
// nil cmd) we fall back to nil and let LoadFileWithWarn use its own default.
func resolveWarnSink(cmd *cobra.Command) io.Writer {
	if cmd == nil {
		return nil
	}
	return cmd.ErrOrStderr()
}

// resolveOrg applies the standard CLI precedence to the --org flag:
//
//	explicit --org flag > config file (default_org) > "" (server-side default)
//
// Resolution happens at the command boundary so BuildAPIClient stays simple.
// A LoadFile error is non-fatal; commands shouldn't fail for an orthogonal
// config-file problem (the explicit `cyoda-cloud config get/set` path
// surfaces that error directly). LoadFileWithWarn prints a one-shot warning
// per process so the user knows the config was ignored.
//
// We treat both `cmd.Flags().Changed("org")` and a non-empty flagOrg as
// "user supplied a value": Changed() handles `--org=""` cleanly, and the
// non-empty fallback covers callers (or tests) that bypass cobra's parser
// and pass the value directly.
func resolveOrg(cmd *cobra.Command, flagOrg string) string {
	if flagOrg != "" || (cmd != nil && cmd.Flags().Changed("org")) {
		return flagOrg
	}
	f, err := config.LoadFileWithWarn(resolveWarnSink(cmd))
	if err != nil {
		return flagOrg
	}
	return f.DefaultOrg
}

// resolveOutputJSON applies the standard CLI precedence to --output-json:
//
//	explicit --output-json flag > config file (output_format=="json") > false
//
// As with resolveOrg, a LoadFile error returns the flag value unchanged but
// surfaces a warning (once per process) via LoadFileWithWarn.
func resolveOutputJSON(cmd *cobra.Command, flagJSON bool) bool {
	if cmd != nil && cmd.Flags().Changed("output-json") {
		return flagJSON
	}
	f, err := config.LoadFileWithWarn(resolveWarnSink(cmd))
	if err != nil {
		return flagJSON
	}
	return f.OutputFormat == "json"
}
