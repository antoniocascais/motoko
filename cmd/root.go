package cmd

import (
	"github.com/antoniocascais/motoko/pkg/config"
	"github.com/spf13/cobra"
)

var cfgFile string

var Version string

var Cfg *config.Config

// RequireConfig loads and validates the config file.
func RequireConfig() (*config.Config, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, err
	}
	Cfg = cfg
	return cfg, nil
}

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
