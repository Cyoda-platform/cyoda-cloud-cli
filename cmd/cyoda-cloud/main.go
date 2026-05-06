package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/commands"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

func main() {
	os.Exit(run())
}

// run wires the cobra tree, executes it, and returns the exit code.
//
// Splitting this out of main lets tests exercise the exit-code mapping
// directly without invoking os.Exit. cobra prints the error to stderr on
// SilenceErrors=false (default) — we let it do that and only translate the
// returned error into an exit code via output.Exit.
func run() int {
	root := &cobra.Command{
		Use:   "cyoda-cloud",
		Short: "Cyoda Cloud command-line interface",
	}
	root.AddCommand(commands.NewVersionCmd())
	root.AddCommand(commands.NewLoginCmd())
	root.AddCommand(commands.NewRegisterCmd())
	root.AddCommand(commands.NewLogoutCmd())
	root.AddCommand(commands.NewWhoamiCmd())
	root.AddCommand(commands.NewEnvCmd())
	root.AddCommand(commands.NewAppCmd())
	root.AddCommand(commands.NewConfigCmd())
	root.AddCommand(commands.NewTokenCmd())
	return output.Exit(root.Execute())
}
