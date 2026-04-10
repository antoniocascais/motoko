package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/state"
	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var rebuildYesFlag bool

var rebuildCmd = &cobra.Command{
	Use:   "rebuild <name>",
	Short: "Reset instance to clean state (preserves data disk)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := cloudinit.ValidateInstanceName(name); err != nil {
			return err
		}
		const totalSteps = 7

		cfg, err := RequireConfig()
		if err != nil {
			return err
		}

		dir := configDir()

		progress(1, totalSteps, "Loading instance state")
		st, err := state.Load(dir, name)
		if err != nil {
			return err
		}

		if !rebuildYesFlag {
			if !confirmAction(fmt.Sprintf("Rebuild %q? This resets the overlay (data disk preserved).", name)) {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
		}

		var token string
		if st.TelegramTokenEnv != "" {
			progress(2, totalSteps, "Reading Telegram bot token")
			token, err = requireEnv(st.TelegramTokenEnv)
			if err != nil {
				return err
			}
		}

		progress(3, totalSteps, "Stopping VM")
		if err := vm.ForceStop(name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: stop: %v\n", err)
		}

		progress(4, totalSteps, "Resetting overlay")
		if err := vm.ResetOverlay(cfg.ImagesDir, cfg.GoldenImage.Name, st.OverlayName); err != nil {
			return err
		}

		progress(5, totalSteps, "Rebuilding cloud-init ISO")
		pubkey, _, err := cloudinit.LoadInstanceKey(dir, name)
		if err != nil {
			return fmt.Errorf("loading SSH key: %w", err)
		}
		sshKeys := []string{pubkey}

		operatorKey, persona, err := loadOperatorKeyAndPersona(st.OperatorKeyPath, st.PersonaPath)
		if err != nil {
			return err
		}
		if operatorKey != "" {
			sshKeys = append(sshKeys, operatorKey)
		}

		if err := renderAndBuildISO(cfg, name, token, sshKeys, persona, st.CloudInitISO); err != nil {
			return err
		}

		progress(6, totalSteps, "Starting VM")
		if err := vm.Start(name); err != nil {
			return err
		}

		progress(7, totalSteps, "Waiting for SSH")
		ip, err := waitForIP(name, 60*time.Second)
		if err != nil {
			return err
		}
		if err := vm.WaitForSSH(ip, st.SSHKeyPath, cfg.VMUser, 120*time.Second); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "Instance %q rebuilt. IP: %s\n", name, ip)
		return nil
	},
}

func init() {
	rebuildCmd.Flags().BoolVarP(&rebuildYesFlag, "yes", "y", false, "skip confirmation prompt")
	rootCmd.AddCommand(rebuildCmd)
}
