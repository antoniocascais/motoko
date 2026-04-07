package cmd

import (
	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a stopped instance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := cloudinit.ValidateInstanceName(name); err != nil {
			return err
		}
		return vm.Start(name)
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
}
