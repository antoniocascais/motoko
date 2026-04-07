package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type InstanceState struct {
	Name             string    `json:"name"`
	CreatedAt        time.Time `json:"created_at"`
	TelegramTokenEnv string    `json:"telegram_token_env,omitempty"`
	PersonaPath      string    `json:"persona_path,omitempty"`
	OperatorKeyPath  string    `json:"operator_key_path,omitempty"`
	SSHKeyPath       string    `json:"ssh_key_path"`
	OverlayName      string    `json:"overlay_name"`
	DataDiskName     string    `json:"data_disk_name"`
	CloudInitISO     string    `json:"cloudinit_iso"`
}

func stateDir(configDir, name string) string {
	return filepath.Join(configDir, "instances", name)
}

func statePath(configDir, name string) string {
	return filepath.Join(stateDir(configDir, name), "state.json")
}

func Save(configDir, name string, s *InstanceState) error {
	dir := stateDir(configDir, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating instance state dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling instance state: %w", err)
	}
	if err := os.WriteFile(statePath(configDir, name), data, 0600); err != nil {
		return fmt.Errorf("writing instance state: %w", err)
	}
	return nil
}

func Load(configDir, name string) (*InstanceState, error) {
	data, err := os.ReadFile(statePath(configDir, name))
	if err != nil {
		return nil, fmt.Errorf("reading instance state: %w", err)
	}
	var s InstanceState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing instance state: %w", err)
	}
	return &s, nil
}

func Delete(configDir, name string) error {
	if err := os.RemoveAll(stateDir(configDir, name)); err != nil {
		return fmt.Errorf("deleting instance state: %w", err)
	}
	return nil
}

// List returns sorted names of all instances with a state.json file.
func List(configDir string) ([]string, error) {
	instancesDir := filepath.Join(configDir, "instances")
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(statePath(configDir, e.Name())); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
