package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/commands"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:   "cyoda-cloud",
		Short: "Cyoda Cloud command-line interface",
	}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version info",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.UserAgent(version.Version, runtime.GOOS, runtime.GOARCH))
		},
	})
	root.AddCommand(commands.NewLoginCmd())
	root.AddCommand(commands.NewRegisterCmd())
	root.AddCommand(commands.NewLogoutCmd())
	root.AddCommand(commands.NewWhoamiCmd())
	root.AddCommand(commands.NewEnvCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
