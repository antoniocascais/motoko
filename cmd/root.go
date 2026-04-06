package cmd

import (
	"github.com/spf13/cobra"
)

var cfgFile string

var Version string

var rootCmd = &cobra.Command{
	Use:   "motoko",
	Short: "Provision KVM-isolated sandboxes for autonomous Claude Code",
	Long: `motoko provisions ephemeral KVM virtual machines with network egress
filtering for running Claude Code as an autonomous AI agent.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/motoko/config.yml)")
}
