package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/antoniocascais/motoko/pkg/config"
	"github.com/antoniocascais/motoko/pkg/preflight"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Check host prerequisites and create default config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		results, allPassed := preflight.RunAll()
		printResults(results, "FAIL")

		if !allPassed {
			return fmt.Errorf("preflight checks failed — fix the issues above and re-run")
		}

		dir := configDir()
		if err := preflight.EnsureConfigDir(dir); err != nil {
			return fmt.Errorf("creating config directory: %w", err)
		}

		cfgPath := filepath.Join(dir, "config.yml")
		if err := preflight.WriteDefaultConfig(cfgPath); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "\nConfig: %s\n", cfgPath)

		cfg, err := config.Load(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [SKIP] config path checks: %v\n", err)
		} else {
			printResults(preflight.CheckConfigPaths(cfg.ImagesDir, cfg.CloudinitDir, cfg.Proxy.FilterFile), "WARN")
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
