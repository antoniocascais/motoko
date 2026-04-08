package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ImagesDir    string      `yaml:"images_dir"`
	CloudinitDir string      `yaml:"cloudinit_dir"`
	VMUser       string      `yaml:"vm_user"`
	BaseImage    BaseImage   `yaml:"base_image"`
	GoldenImage  GoldenImage `yaml:"golden_image"`
	Network      Network     `yaml:"network"`
	Proxy        Proxy       `yaml:"proxy"`
	VMDefaults   VMDefaults  `yaml:"vm_defaults"`
}

type BaseImage struct {
	URL      string `yaml:"url"`
	Checksum string `yaml:"checksum"`
	Filename string `yaml:"filename"`
}

type GoldenImage struct {
	Name       string   `yaml:"name"`
	RootDiskGB int      `yaml:"root_disk_gb"`
	Packages   []string `yaml:"packages"`
}

type Network struct {
	LibvirtNetwork string `yaml:"libvirt_network"`
	BridgeIP       string `yaml:"bridge_ip"`
	Subnet         string `yaml:"subnet"`
}

type Proxy struct {
	Port           int      `yaml:"port"`
	AllowedDomains []string `yaml:"allowed_domains"`
	FilterFile     string   `yaml:"filter_file"`
}

type VMDefaults struct {
	VCPUs          int    `yaml:"vcpus"`
	RAMMB          int    `yaml:"ram_mb"`
	RootDiskGB     int    `yaml:"root_disk_gb"`
	DataDiskGB     int    `yaml:"data_disk_gb"`
	BlkioWeight    int    `yaml:"blkio_weight"`
	NetBandwidthKB int    `yaml:"net_bandwidth_kb"`
	CPUPinning     string `yaml:"cpu_pinning,omitempty"`
	Locale         string `yaml:"locale"`
	Timezone       string `yaml:"timezone"`
}

func DefaultConfig() *Config {
	return &Config{
		ImagesDir:    "/var/lib/libvirt/images/motoko",
		CloudinitDir: "/var/lib/libvirt/cloud-init/motoko",
		VMUser:       "claude",
		BaseImage: BaseImage{
			URL:      "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2",
			Filename: "debian-12-generic-amd64.qcow2",
		},
		GoldenImage: GoldenImage{
			Name:       "debian-12-claude-golden.qcow2",
			RootDiskGB: 8,
			Packages:   []string{"python3", "curl", "git", "vim", "htop", "unzip", "tmux"},
		},
		Network: Network{
			LibvirtNetwork: "default",
			BridgeIP:       "192.168.122.1",
			Subnet:         "192.168.122.0/24",
		},
		Proxy: Proxy{
			Port: 3128,
			AllowedDomains: []string{
				`\.anthropic\.com$`,
				`^platform\.claude\.com$`,
				`^api\.telegram\.org$`,
			},
			FilterFile: "/etc/tinyproxy/allowed-domains",
		},
		VMDefaults: VMDefaults{
			VCPUs:          4,
			RAMMB:          4096,
			RootDiskGB:     8,
			DataDiskGB:     10,
			BlkioWeight:    200,
			NetBandwidthKB: 5000,
			Locale:         "en_US.UTF-8",
			Timezone:       "UTC",
		},
	}
}

// ConfigDir returns the motoko configuration directory (~/.config/motoko).
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "motoko")
}

func DefaultConfigPath() string {
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.yml")
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}
