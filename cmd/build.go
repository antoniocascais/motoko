package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var forceRebuild bool

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build the golden base image",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := RequireConfig()
		if err != nil {
			return err
		}
		if err := cfg.ValidatePaths(); err != nil {
			return err
		}

		exists, err := vm.GoldenImageExists(cfg.ImagesDir, cfg.GoldenImage.Name)
		if err != nil {
			return err
		}
		if exists && !forceRebuild {
			fmt.Fprintln(os.Stderr, "Golden image already exists. Use --force to rebuild.")
			return nil
		}
		if exists && forceRebuild {
			if err := os.Remove(filepath.Join(cfg.ImagesDir, cfg.GoldenImage.Name)); err != nil {
				return fmt.Errorf("removing existing golden image: %w", err)
			}
		}

		fmt.Fprintln(os.Stderr, "Building golden image (this may take several minutes)...")
		return vm.BuildGoldenImage(vm.BuildGoldenConfig{
			ImagesDir:    cfg.ImagesDir,
			BaseURL:      cfg.BaseImage.URL,
			BaseChecksum: cfg.BaseImage.Checksum,
			BaseFilename: cfg.BaseImage.Filename,
			GoldenName:   cfg.GoldenImage.Name,
			RootDiskGB:   cfg.GoldenImage.RootDiskGB,
			Packages:     cfg.GoldenImage.Packages,
			VMUser:       cfg.VMUser,
		})
	},
}

func init() {
	buildCmd.Flags().BoolVar(&forceRebuild, "force", false, "rebuild even if golden image exists")
	rootCmd.AddCommand(buildCmd)
}
