package commands

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// configKey* are the only TOML keys exposed via `cyoda-cloud config set`.
// Adding a new key here means adding a corresponding field to config.File and
// extending the get/set/list switches below.
const (
	configKeyDefaultOrg   = "default_org"
	configKeyOutputFormat = "output_format"
	configKeyDiscoveryURL = "discovery_url"
)

// configKeys is the canonical, ordered list used by `config list` and the
// "unknown key" error message. List is short enough that an inline switch is
// fine; we still keep this slice for the user-facing surface.
var configKeys = []string{configKeyDefaultOrg, configKeyOutputFormat, configKeyDiscoveryURL}

// NewConfigCmd returns the `cyoda-cloud config` parent command. The three
// subcommands (get/set/list) operate on ~/.config/cyoda-cloud/config.toml.
func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read/write the CLI's persistent config (TOML)",
	}
	cmd.AddCommand(newConfigGetCmd())
	cmd.AddCommand(newConfigSetCmd())
	cmd.AddCommand(newConfigListCmd())
	return cmd
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print the value of a config key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigGet(cmd, args[0])
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config key to a value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSet(cmd, args[0], args[1])
		},
	}
}

func newConfigListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all known config keys and their values",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigList(cmd)
		},
	}
}

// runConfigGet writes the value to stdout (so $(cyoda-cloud config get K)
// works in shell). Empty/unset values still produce a trailing newline.
func runConfigGet(cmd *cobra.Command, key string) error {
	f, err := config.LoadFile()
	if err != nil {
		return err
	}
	v, ok := configFieldGet(&f, key)
	if !ok {
		return unknownKeyErr(key)
	}
	fmt.Fprintln(cmd.OutOrStdout(), v)
	return nil
}

// runConfigSet validates key+value and persists the file. Confirmation goes
// to stderr; stdout stays clean for scripting.
func runConfigSet(cmd *cobra.Command, key, value string) error {
	if err := validateConfigValue(key, value); err != nil {
		return err
	}
	f, err := config.LoadFile()
	if err != nil {
		return err
	}
	if !configFieldSet(&f, key, value) {
		return unknownKeyErr(key)
	}
	if err := config.SaveFile(f); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "set %s=%s\n", key, value)
	return nil
}

// runConfigList prints all known keys (in canonical order) as key=value
// lines on stdout.
func runConfigList(cmd *cobra.Command) error {
	f, err := config.LoadFile()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	for _, k := range configKeys {
		v, _ := configFieldGet(&f, k)
		fmt.Fprintf(out, "%s=%s\n", k, v)
	}
	return nil
}

// configFieldGet returns the current string value for a known key. The bool
// reports whether the key is recognised; unknown keys must NOT be silently
// treated as empty.
func configFieldGet(f *config.File, key string) (string, bool) {
	switch key {
	case configKeyDefaultOrg:
		return f.DefaultOrg, true
	case configKeyOutputFormat:
		return f.OutputFormat, true
	case configKeyDiscoveryURL:
		return f.DiscoveryURL, true
	}
	return "", false
}

// configFieldSet writes value into the appropriate File field. Returns false
// for unknown keys.
func configFieldSet(f *config.File, key, value string) bool {
	switch key {
	case configKeyDefaultOrg:
		f.DefaultOrg = value
	case configKeyOutputFormat:
		f.OutputFormat = value
	case configKeyDiscoveryURL:
		f.DiscoveryURL = value
	default:
		return false
	}
	return true
}

// validateConfigValue rejects bad-shape values for the constrained keys.
// default_org accepts any string (Auth0 org slugs are server-validated).
func validateConfigValue(key, value string) error {
	switch key {
	case configKeyDefaultOrg:
		return nil
	case configKeyOutputFormat:
		if value != "table" && value != "json" {
			return &output.CLIError{
				Code: output.CodeBadUsage,
				Err:  fmt.Errorf("output_format must be \"table\" or \"json\", got %q", value),
			}
		}
		return nil
	case configKeyDiscoveryURL:
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || (u.Scheme != "https" && u.Scheme != "file") {
			return &output.CLIError{
				Code: output.CodeBadUsage,
				Err:  fmt.Errorf("discovery_url must be a https:// or file:// URL, got %q", value),
			}
		}
		return nil
	}
	// Unknown key — caller will translate, but keep this honest.
	return unknownKeyErr(key)
}

func unknownKeyErr(key string) error {
	return &output.CLIError{
		Code: output.CodeBadUsage,
		Err: fmt.Errorf("unknown config key %q (known: %s)",
			key, joinKeys(configKeys)),
	}
}

func joinKeys(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	out := keys[0]
	for _, k := range keys[1:] {
		out += ", " + k
	}
	return out
}

