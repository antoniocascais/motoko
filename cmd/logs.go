package cmd

import (
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show Claude Code output from the instance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, st, ip, err := resolveInstance(args[0])
		if err != nil {
			return err
		}

		argv := append(sshArgs(st.SSHKeyPath, cfg.VMUser, ip),
			"tmux", "capture-pane", "-p", "-t", "claude", "-S", "-100",
		)
		c := exec.Command("ssh", argv...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
}
