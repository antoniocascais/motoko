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
	"github.com/antoniocascais/motoko/pkg/preflight"
	"github.com/antoniocascais/motoko/pkg/state"
	"github.com/antoniocascais/motoko/pkg/vm"
)

// pinHostKeys writes the instance's SSH host keys (captured by WaitForSSHReady on
// the connection that proves identity) to its per-instance known_hosts file,
// returning the path. create pins once with rollback on failure; rebuild re-pins
// to overwrite the keys a fresh overlay regenerated.
func pinHostKeys(dir, name, knownHosts string) (string, error) {
	khPath := state.KnownHostsPath(dir, name)
	if err := os.MkdirAll(filepath.Dir(khPath), 0700); err != nil {
		return "", fmt.Errorf("creating instance dir: %w", err)
	}
	if err := os.WriteFile(khPath, []byte(knownHosts), 0600); err != nil {
		return "", fmt.Errorf("writing known_hosts: %w", err)
	}
	return khPath, nil
}

func printResults(results []preflight.CheckResult, failLabel string) {
	for _, r := range results {
		if r.Passed {
			fmt.Fprintf(os.Stderr, "  [OK]   %s\n", r.Name)
		} else {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", failLabel, r.Name, r.Detail)
		}
	}
}

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

// resolveInstance validates a name, loads config+state, and resolves the VM's IP
// and per-instance known_hosts path.
func resolveInstance(name string) (cfg *config.Config, st *state.InstanceState, ip, knownHosts string, err error) {
	if err = cloudinit.ValidateInstanceName(name); err != nil {
		return nil, nil, "", "", err
	}
	cfg, err = RequireConfig()
	if err != nil {
		return nil, nil, "", "", err
	}
	st, err = state.Load(configDir(), name)
	if err != nil {
		return nil, nil, "", "", err
	}
	ip, err = vm.GetIP(name)
	if err != nil || ip == "" {
		return nil, nil, "", "", fmt.Errorf("cannot determine IP for instance %q (is it running?)", name)
	}
	return cfg, st, ip, state.KnownHostsPath(configDir(), name), nil
}

// sshArgs builds the ssh argument list for interactive/non-interactive commands.
// accept-new pins the host key on first contact (capturing it for pre-feature
// instances that have no file yet) while still hard-failing on a CHANGED key —
// the signal that the IP now answers as a different VM.
func sshArgs(keyPath, user, ip, knownHostsPath string) []string {
	return []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", fmt.Sprintf("UserKnownHostsFile=%s", knownHostsPath),
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
