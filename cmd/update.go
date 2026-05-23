package cmd

import (
	"fmt"
	"os"

	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var (
	updateRAMFlag  int
	updateCPUsFlag int

	// Seams for testability — tests swap these to fake vm package calls.
	vmStateFn           = vm.State
	vmUpdateResourcesFn = vm.UpdateResources
)

var updateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Update RAM and/or vCPU count of an existing instance",
	Long: `Persistently update the RAM (--ram MiB) and/or vCPU count (--cpus) of an existing instance.

The instance must be stopped — changes take effect on next start.
At least one of --ram or --cpus must be provided.

Reducing resources: libvirt rejects lowering the maximum below the current value
in a single pass. To shrink RAM or vCPUs, first reduce current with virsh
(e.g. ` + "`virsh setmem motoko-<name> <new-kib> --config`" + ` for RAM, or
` + "`virsh setvcpus motoko-<name> <N> --config`" + ` for CPUs), then re-run
` + "`motoko update`" + ` with the target value.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := cloudinit.ValidateInstanceName(name); err != nil {
			return err
		}
		if updateRAMFlag == 0 && updateCPUsFlag == 0 {
			return fmt.Errorf("at least one of --ram or --cpus must be provided")
		}
		if updateRAMFlag < 0 {
			return fmt.Errorf("--ram must be positive")
		}
		if updateCPUsFlag < 0 {
			return fmt.Errorf("--cpus must be positive")
		}
		if updateRAMFlag != 0 && updateRAMFlag < 512 {
			return fmt.Errorf("--ram must be at least 512 (MiB)")
		}
		if updateCPUsFlag != 0 && updateCPUsFlag < 1 {
			return fmt.Errorf("--cpus must be at least 1")
		}

		domState, err := vmStateFn(name)
		if err != nil {
			return fmt.Errorf("checking VM state: %w", err)
		}
		if domState != "shut off" {
			desc := domState
			if desc == "" {
				desc = "in an unknown state"
			} else {
				desc = "in state " + desc
			}
			return fmt.Errorf("instance %q is %s — stop it first with `motoko stop %s`", name, desc, name)
		}

		if err := vmUpdateResourcesFn(name, updateRAMFlag, updateCPUsFlag); err != nil {
			return err
		}

		if updateRAMFlag > 0 {
			fmt.Fprintf(os.Stderr, "Setting RAM to %d MiB\n", updateRAMFlag)
		}
		if updateCPUsFlag > 0 {
			fmt.Fprintf(os.Stderr, "Setting vCPUs to %d\n", updateCPUsFlag)
		}
		fmt.Fprintf(os.Stderr, "Instance %q updated. Start it with `motoko start %s`.\n", name, name)
		return nil
	},
}

func init() {
	updateCmd.Flags().IntVar(&updateRAMFlag, "ram", 0, "RAM in MiB (>= 512)")
	updateCmd.Flags().IntVar(&updateCPUsFlag, "cpus", 0, "Number of vCPUs (>= 1)")
	rootCmd.AddCommand(updateCmd)
}
