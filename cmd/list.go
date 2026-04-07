package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Short:   "List all motoko instances",
	Aliases: []string{"ls"},
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		vms, err := vm.ListAll()
		if err != nil {
			return err
		}
		if len(vms) == 0 {
			fmt.Fprintln(os.Stderr, "No instances found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tSTATE\tIP")
		for _, v := range vms {
			ip := v.IP
			if ip == "" {
				ip = "-"
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", v.Name, v.State, ip)
		}
		return w.Flush()
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}
