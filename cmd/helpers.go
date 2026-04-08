package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/config"
	"github.com/antoniocascais/motoko/pkg/state"
	"github.com/antoniocascais/motoko/pkg/vm"
)

// If --config was set, uses its parent directory; otherwise uses the default.
func configDir() string {
	if cfgFile != "" {
		return filepath.Dir(cfgFile)
	}
	return config.ConfigDir()
}

func confirmAction(prompt string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.EqualFold(strings.TrimSpace(scanner.Text()), "y")
	}
	return false
}

func progress(step, total int, msg string) {
	fmt.Fprintf(os.Stderr, "==> [%d/%d] %s\n", step, total, msg)
}

func waitForIP(name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ip, err := vm.GetIP(name)
		if err == nil && ip != "" {
			return ip, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("no IP address for %q after %s", name, timeout)
}

// resolveInstance validates a name, loads config+state, and resolves the VM's IP.
func resolveInstance(name string) (*config.Config, *state.InstanceState, string, error) {
	if err := cloudinit.ValidateInstanceName(name); err != nil {
		return nil, nil, "", err
	}
	cfg, err := RequireConfig()
	if err != nil {
		return nil, nil, "", err
	}
	st, err := state.Load(configDir(), name)
	if err != nil {
		return nil, nil, "", err
	}
	ip, err := vm.GetIP(name)
	if err != nil || ip == "" {
		return nil, nil, "", fmt.Errorf("cannot determine IP for instance %q (is it running?)", name)
	}
	return cfg, st, ip, nil
}

func sshArgs(keyPath, user, ip string) []string {
	return []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", keyPath,
		fmt.Sprintf("%s@%s", user, ip),
	}
}

// warnRemove removes a file and warns on stderr if removal fails for a reason
// other than the file not existing.
func warnRemove(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: removing %s: %v\n", path, err)
	}
}

// loadOperatorKeyAndPersona reads an optional operator SSH key and persona file.
func loadOperatorKeyAndPersona(keyPath, personaPath string) (operatorKey, persona string, err error) {
	if keyPath != "" {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			return "", "", fmt.Errorf("reading operator SSH key %s: %w", keyPath, err)
		}
		operatorKey = strings.TrimSpace(string(data))
	}
	if personaPath != "" {
		data, err := os.ReadFile(personaPath)
		if err != nil {
			return "", "", fmt.Errorf("reading persona %s: %w", personaPath, err)
		}
		persona = string(data)
	}
	return operatorKey, persona, nil
}

// renderAndBuildISO renders cloud-init templates and produces the NoCloud ISO.
func renderAndBuildISO(cfg *config.Config, name, token string, sshKeys []string, persona, isoPath string) error {
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
	return cloudinit.BuildISO(userdata, metadata, isoPath)
}

func requireEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return v, nil
}
