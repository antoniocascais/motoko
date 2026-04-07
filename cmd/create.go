package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/state"
	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var (
	tokenEnvFlag string
	sshKeyFlag   string
	personaFlag  string
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new sandbox instance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		const totalSteps = 17

		progress(1, totalSteps, "Validating instance name")
		if err := cloudinit.ValidateInstanceName(name); err != nil {
			return err
		}

		progress(2, totalSteps, "Loading config")
		cfg, err := RequireConfig()
		if err != nil {
			return err
		}
		if err := cfg.ValidatePaths(); err != nil {
			return err
		}

		progress(3, totalSteps, "Checking golden image")
		exists, err := vm.GoldenImageExists(cfg.ImagesDir, cfg.GoldenImage.Name)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("golden image not found — run `motoko build` first")
		}

		progress(4, totalSteps, "Reading Telegram bot token")
		token, err := requireEnv(tokenEnvFlag)
		if err != nil {
			return err
		}

		dir := configDir()

		progress(5, totalSteps, "Generating SSH keypair")
		pubkey, privPath, err := cloudinit.EnsureInstanceKey(dir, name)
		if err != nil {
			return fmt.Errorf("SSH key generation: %w", err)
		}
		sshKeys := []string{pubkey}

		if sshKeyFlag != "" {
			progress(6, totalSteps, "Loading operator SSH key")
			data, err := os.ReadFile(sshKeyFlag)
			if err != nil {
				return fmt.Errorf("reading SSH key %s: %w", sshKeyFlag, err)
			}
			sshKeys = append(sshKeys, strings.TrimSpace(string(data)))
		} else {
			progress(6, totalSteps, "No operator SSH key specified, skipping")
		}

		var persona string
		if personaFlag != "" {
			progress(7, totalSteps, "Loading persona")
			data, err := os.ReadFile(personaFlag)
			if err != nil {
				return fmt.Errorf("reading persona %s: %w", personaFlag, err)
			}
			persona = string(data)
		} else {
			progress(7, totalSteps, "No persona specified, skipping")
		}

		progress(8, totalSteps, "Rendering cloud-init templates")
		params, err := cloudinit.NewInstanceParams(cfg, name, name, token, sshKeys, persona)
		if err != nil {
			return fmt.Errorf("cloud-init params: %w", err)
		}
		userdata, err := cloudinit.RenderUserData(params)
		if err != nil {
			return err
		}
		metadata, err := cloudinit.RenderMetaData(name, name)
		if err != nil {
			return err
		}

		progress(9, totalSteps, "Building cloud-init ISO")
		isoName := fmt.Sprintf("motoko-%s-cloud-init.iso", name)
		isoPath := filepath.Join(cfg.CloudinitDir, isoName)
		if err := cloudinit.BuildISO(userdata, metadata, isoPath); err != nil {
			return err
		}

		progress(10, totalSteps, "Creating data disk")
		dataName := fmt.Sprintf("motoko-%s-data.qcow2", name)
		dataPath := filepath.Join(cfg.ImagesDir, dataName)
		if _, err := os.Stat(dataPath); os.IsNotExist(err) {
			if err := vm.CreateDataDisk(cfg.ImagesDir, dataName, cfg.VMDefaults.DataDiskGB); err != nil {
				return fmt.Errorf("creating data disk: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("checking data disk: %w", err)
		} else {
			fmt.Fprintln(os.Stderr, "     Data disk exists, reusing")
		}

		progress(11, totalSteps, "Creating overlay disk")
		overlayName := fmt.Sprintf("motoko-%s-overlay.qcow2", name)
		overlayPath := filepath.Join(cfg.ImagesDir, overlayName)
		if err := vm.CreateOverlay(cfg.ImagesDir, cfg.GoldenImage.Name, overlayName); err != nil {
			return fmt.Errorf("creating overlay: %w", err)
		}

		progress(12, totalSteps, "Defining VM")
		if err := vm.Define(vm.DefineConfig{
			Name:         name,
			VCPUs:        cfg.VMDefaults.VCPUs,
			CPUPinning:   cfg.VMDefaults.CPUPinning,
			RAMMB:        cfg.VMDefaults.RAMMB,
			OverlayPath:  overlayPath,
			DataPath:     dataPath,
			CloudInitISO: isoPath,
			Network:      cfg.Network.LibvirtNetwork,
		}); err != nil {
			return fmt.Errorf("defining VM: %w", err)
		}

		progress(13, totalSteps, "Applying resource limits")
		if err := vm.ApplyTuning(name, cfg.VMDefaults.BlkioWeight, cfg.VMDefaults.NetBandwidthKB, cfg.VMDefaults.RAMMB); err != nil {
			return fmt.Errorf("applying tuning: %w", err)
		}

		progress(14, totalSteps, "Disabling autostart")
		if err := vm.DisableAutostart(name); err != nil {
			return fmt.Errorf("disabling autostart: %w", err)
		}

		progress(15, totalSteps, "Starting VM")
		if err := vm.Start(name); err != nil {
			return fmt.Errorf("starting VM: %w", err)
		}

		progress(16, totalSteps, "Waiting for SSH (up to 120s)")
		ip, err := waitForIP(name, 60*time.Second)
		if err != nil {
			return err
		}
		if err := vm.WaitForSSH(ip, privPath, cfg.VMUser, 120*time.Second); err != nil {
			return fmt.Errorf("waiting for SSH: %w", err)
		}

		progress(17, totalSteps, "Saving instance state")
		if err := state.Save(dir, name, &state.InstanceState{
			Name:             name,
			CreatedAt:        time.Now(),
			TelegramTokenEnv: tokenEnvFlag,
			PersonaPath:      personaFlag,
			OperatorKeyPath:  sshKeyFlag,
			SSHKeyPath:       privPath,
			OverlayName:      overlayName,
			DataDiskName:     dataName,
			CloudInitISO:     isoPath,
		}); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}

		fmt.Fprintf(os.Stderr, "\nInstance %q created successfully.\n", name)
		fmt.Fprintf(os.Stderr, "  IP:  %s\n", ip)
		fmt.Fprintf(os.Stderr, "  SSH: ssh -i %s %s@%s\n", privPath, cfg.VMUser, ip)
		return nil
	},
}

func init() {
	createCmd.Flags().StringVar(&tokenEnvFlag, "token-env", "", "environment variable containing Telegram bot token (required)")
	_ = createCmd.MarkFlagRequired("token-env")
	createCmd.Flags().StringVar(&sshKeyFlag, "ssh-key", "", "path to operator SSH public key file")
	createCmd.Flags().StringVar(&personaFlag, "persona", "", "path to persona markdown file")
	rootCmd.AddCommand(createCmd)
}
