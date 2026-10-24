package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/antoniocascais/motoko/pkg/config"
	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var forceRebuild bool

// Seams for testability — tests swap these to fake vm package calls.
var (
	goldenExistsFn     = vm.GoldenImageExists
	goldenReferencesFn = vm.GoldenReferences
	buildGoldenFn      = vm.BuildGoldenImage
)

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
		return runBuild(cfg, forceRebuild)
	},
}

func runBuild(cfg *config.Config, force bool) error {
	exists, err := goldenExistsFn(cfg.ImagesDir, cfg.GoldenImage.Name)
	if err != nil {
		return err
	}

	if exists && !force {
		fmt.Fprintln(os.Stderr, "Golden image already exists. Use --force to rebuild.")
		return nil
	}

	// Scanned even when the golden is missing: overlays may still point at its
	// path. Fails fast before minutes of baking; BuildGoldenImage re-checks right
	// before replacing, so this check only spares the wasted build.
	refs, err := goldenReferencesFn(cfg.ImagesDir, cfg.GoldenImage.Name)
	if err != nil {
		return fmt.Errorf("checking golden image references: %w", err)
	}
	if len(refs) > 0 {
		return &vm.GoldenInUseError{Golden: filepath.Join(cfg.ImagesDir, cfg.GoldenImage.Name), Refs: refs}
	}

	// The existing golden is not removed up front: the new image is baked under a
	// temp name and renamed over it only on success, so a failed build leaves the
	// previous golden usable.
	fmt.Fprintln(os.Stderr, "Building golden image (this may take several minutes)...")
	return buildGoldenFn(vm.BuildGoldenConfig{
		ImagesDir:     cfg.ImagesDir,
		BaseURL:       cfg.BaseImage.URL,
		BaseChecksum:  cfg.BaseImage.Checksum,
		BaseFilename:  cfg.BaseImage.Filename,
		GoldenName:    cfg.GoldenImage.Name,
		RootDiskGB:    cfg.GoldenImage.RootDiskGB,
		Packages:      cfg.GoldenImage.Packages,
		VMUser:        cfg.VMUser,
		ClaudeChannel: cfg.GoldenImage.ClaudeChannel,
		ClaudeVersion: cfg.GoldenImage.ClaudeVersion,
	})
}

func init() {
	buildCmd.Flags().BoolVar(&forceRebuild, "force", false, "rebuild even if golden image exists")
	rootCmd.AddCommand(buildCmd)
}
