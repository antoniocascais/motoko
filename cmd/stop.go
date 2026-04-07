package cmd

import (
	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Gracefully shut down an instance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := cloudinit.ValidateInstanceName(name); err != nil {
			return err
		}
		return vm.Stop(name)
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
