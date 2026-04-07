package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/antoniocascais/motoko/pkg/preflight"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Check host prerequisites and create default config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		results, allPassed := preflight.RunAll()

		for _, r := range results {
			if r.Passed {
				fmt.Fprintf(os.Stderr, "  [OK]   %s\n", r.Name)
			} else {
				fmt.Fprintf(os.Stderr, "  [FAIL] %s: %s\n", r.Name, r.Detail)
			}
		}

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
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
