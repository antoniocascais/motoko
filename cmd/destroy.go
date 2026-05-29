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
			// No state file — orphaned VM from interrupted create.
			// Fall back to convention-based names for best-effort cleanup.
			fmt.Fprintf(os.Stderr, "Warning: no state file found, attempting best-effort cleanup\n")
			st = &state.InstanceState{
				OverlayName:  fmt.Sprintf("motoko-%s-overlay.qcow2", name),
				DataDiskName: fmt.Sprintf("motoko-%s-data.qcow2", name),
				CloudInitISO: filepath.Join(cfg.CloudinitDir, fmt.Sprintf("motoko-%s-cloud-init.iso", name)),
				SSHKeyPath:   filepath.Join(dir, "keys", name, "id_ed25519"),
			}
			// Recover the DHCP reservation from the network XML by its deterministic
			// name so orphaned instances still get their reservation cleaned up.
			if mac, ip, found := vm.FindReservationByName(cfg.Network.LibvirtNetwork, name); found {
				st.MAC, st.IP = mac, ip
			}
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

		// Best-effort stop and undefine — always attempt both, warn on failure
		fmt.Fprintln(os.Stderr, "Stopping VM...")
		if err := vm.ForceStop(name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: stop: %v\n", err)
		}

		fmt.Fprintln(os.Stderr, "Undefining VM...")
		if err := vm.Undefine(name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: undefine: %v\n", err)
		}

		// Pre-feature instances have no MAC/IP in state and no reservation — skip.
		if st.MAC != "" && st.IP != "" {
			fmt.Fprintf(os.Stderr, "Removing DHCP reservation %s -> %s...\n", st.MAC, st.IP)
			if err := vm.RemoveDHCPReservation(cfg.Network.LibvirtNetwork, st.MAC, name, st.IP); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: removing reservation: %v\n", err)
			}
		}

		warnRemove(filepath.Join(cfg.ImagesDir, st.OverlayName))
		warnRemove(st.CloudInitISO)

		if purgeFlag {
			warnRemove(filepath.Join(cfg.ImagesDir, st.DataDiskName))
			if err := os.RemoveAll(filepath.Join(dir, "keys", name)); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: removing keys: %v\n", err)
			}
		}

		// Removes the whole instances/<name>/ dir, including the pinned known_hosts.
		if err := state.Delete(dir, name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: removing state: %v\n", err)
		}

		fmt.Fprintf(os.Stderr, "Instance %q destroyed.\n", name)
		return nil
	},
}

func init() {
	destroyCmd.Flags().BoolVar(&purgeFlag, "purge", false, "also delete data disk, SSH keys, and state")
	destroyCmd.Flags().BoolVarP(&destroyYesFlag, "yes", "y", false, "skip confirmation prompt")
	rootCmd.AddCommand(destroyCmd)
}
