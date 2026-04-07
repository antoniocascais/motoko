package cmd

import (
	"os"
	"os/exec"
	"fmt"
	"syscall"

	"github.com/spf13/cobra"
)

var sshCmd = &cobra.Command{
	Use:   "ssh <name>",
	Short: "Open an SSH session to an instance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, st, ip, err := resolveInstance(args[0])
		if err != nil {
			return err
		}

		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			return fmt.Errorf("ssh not found in PATH")
		}

		argv := append([]string{"ssh"}, sshArgs(st.SSHKeyPath, cfg.VMUser, ip)...)
		return syscall.Exec(sshBin, argv, os.Environ())
	},
}

func init() {
	rootCmd.AddCommand(sshCmd)
}
