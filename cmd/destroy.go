package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/state"
	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var (
	purgeFlag      bool
	destroyYesFlag bool
)

var destroyCmd = &cobra.Command{
	Use:   "destroy <name>",
	Short: "Stop and remove a sandbox instance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := cloudinit.ValidateInstanceName(name); err != nil {
			return err
		}

		cfg, err := RequireConfig()
		if err != nil {
			return err
		}

		dir := configDir()
		st, err := state.Load(dir, name)
		if err != nil {
			return err
		}

		if !destroyYesFlag {
			action := "destroy"
			if purgeFlag {
				action = "destroy and PURGE ALL DATA for"
			}
			if !confirmAction(fmt.Sprintf("Really %s instance %q?", action, name)) {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
		}

		vmState, _ := vm.State(name)
		if vmState == "running" {
			fmt.Fprintln(os.Stderr, "Stopping VM...")
			if err := vm.ForceStop(name); err != nil {
				return fmt.Errorf("force-stopping VM: %w", err)
			}
		}

		if defined, _ := vm.IsDefined(name); defined {
			fmt.Fprintln(os.Stderr, "Undefining VM...")
			if err := vm.Undefine(name); err != nil {
				return fmt.Errorf("undefining VM: %w", err)
			}
		}

		_ = os.Remove(filepath.Join(cfg.ImagesDir, st.OverlayName))
		_ = os.Remove(st.CloudInitISO)

		if purgeFlag {
			_ = os.Remove(filepath.Join(cfg.ImagesDir, st.DataDiskName))
			_ = os.RemoveAll(filepath.Join(dir, "keys", name))
		}

		_ = state.Delete(dir, name)

		fmt.Fprintf(os.Stderr, "Instance %q destroyed.\n", name)
		return nil
	},
}

func init() {
	destroyCmd.Flags().BoolVar(&purgeFlag, "purge", false, "also delete data disk, SSH keys, and state")
	destroyCmd.Flags().BoolVarP(&destroyYesFlag, "yes", "y", false, "skip confirmation prompt")
	rootCmd.AddCommand(destroyCmd)
}
