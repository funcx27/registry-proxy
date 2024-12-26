package cmd

import (
	"github.com/distribution/distribution/v3/version"
	"github.com/spf13/cobra"
)

var showVersion bool

func init() {
	RootCmd.AddCommand(ServeCmd)
	RootCmd.Flags().BoolVarP(&showVersion, "version", "v", false, "show the version and exit")
}

var RootCmd = &cobra.Command{
	Use:   "registry",
	Short: "`registry`",
	Long:  "`registry`",
	Run: func(cmd *cobra.Command, args []string) {
		if showVersion {
			version.PrintVersion()
			return
		}
		// nolint:errcheck
		cmd.Usage()
	},
}
