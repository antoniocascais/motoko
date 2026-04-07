package cmd

import (
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var injectCmd = &cobra.Command{
	Use:   "inject <name> <prompt>",
	Short: "Send a prompt to Claude Code running in the instance",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, st, ip, err := resolveInstance(args[0])
		if err != nil {
			return err
		}

		argv := append(sshArgs(st.SSHKeyPath, cfg.VMUser, ip),
			"tmux", "send-keys", "-t", "claude", args[1], "Enter",
		)
		c := exec.Command("ssh", argv...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(injectCmd)
}
