// Package cmd implements the medialnk CLI using cobra.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const Version = "2.1.1"

// cfgPath is the global --config flag value, shared across all commands.
var cfgPath string

// rootCmd is the top-level command. It has no Run of its own; invoking
// medialnk with no subcommand prints help.
var rootCmd = &cobra.Command{
	Use:   "medialnk",
	Short: "Symlink-based media library manager",
	Long:  "medialnk scans your media source directories and builds a clean symlink tree organized for Jellyfin and Plex.",
	// Don't show usage on errors from subcommands.
	SilenceUsage: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", "", "Config file path")
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("medialnk {{.Version}}\n")

	rootCmd.AddCommand(syncCmd)
	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(testLibCmd)
	rootCmd.AddCommand(watchCmd)
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
