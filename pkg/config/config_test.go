package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig() should pass validation: %v", err)
	}
}

func TestDefaultConfig_UsesMotokSubdirs(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ImagesDir != "/var/lib/libvirt/images/motoko" {
		t.Errorf("ImagesDir = %q, want /var/lib/libvirt/images/motoko", cfg.ImagesDir)
	}
	if cfg.CloudinitDir != "/var/lib/libvirt/cloud-init/motoko" {
		t.Errorf("CloudinitDir = %q, want /var/lib/libvirt/cloud-init/motoko", cfg.CloudinitDir)
	}
}

func TestConfigDir(t *testing.T) {
	dir := ConfigDir()
	if dir == "" {
		t.Fatal("ConfigDir() returned empty string")
	}
	if !strings.HasSuffix(dir, filepath.Join(".config", "motoko")) {
		t.Fatalf("unexpected config dir: %s", dir)
	}
}

func TestConfigDir_NotContainConfigYml(t *testing.T) {
	dir := ConfigDir()
	if strings.Contains(dir, "config.yml") {
		t.Error("ConfigDir() should return the directory, not the file path")
	}
}

func TestDefaultConfigPath(t *testing.T) {
	path := DefaultConfigPath()
	if !strings.HasSuffix(path, filepath.Join(".config", "motoko", "config.yml")) {
		t.Fatalf("unexpected default path: %s", path)
	}
	if !strings.HasPrefix(path, ConfigDir()) {
		t.Fatalf("DefaultConfigPath() should be under ConfigDir(), got %s", path)
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	yaml := `
images_dir: /tmp/images
cloudinit_dir: /tmp/cloudinit
vm_user: testuser
base_image:
  url: https://example.com/image.qcow2
  filename: image.qcow2
golden_image:
  name: golden.qcow2
  root_disk_gb: 16
  packages: [git, curl]
network:
  libvirt_network: mynet
  bridge_ip: 10.0.0.1
  subnet: 10.0.0.0/24
proxy:
  port: 8080
  allowed_domains:
    - '\.example\.com$'
vm_defaults:
  vcpus: 2
  ram_mb: 2048
  root_disk_gb: 16
  data_disk_gb: 20
  blkio_weight: 100
  net_bandwidth_kb: 1000
  locale: C
  timezone: Europe/Berlin
`
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.ImagesDir != "/tmp/images" {
		t.Errorf("ImagesDir = %q, want /tmp/images", cfg.ImagesDir)
	}
	if cfg.VMUser != "testuser" {
		t.Errorf("VMUser = %q, want testuser", cfg.VMUser)
	}
	if cfg.Network.BridgeIP != "10.0.0.1" {
		t.Errorf("BridgeIP = %q, want 10.0.0.1", cfg.Network.BridgeIP)
	}
	if cfg.VMDefaults.VCPUs != 2 {
		t.Errorf("VCPUs = %d, want 2", cfg.VMDefaults.VCPUs)
	}
	if cfg.VMDefaults.Timezone != "Europe/Berlin" {
		t.Errorf("Timezone = %q, want Europe/Berlin", cfg.VMDefaults.Timezone)
	}
	if cfg.GoldenImage.RootDiskGB != 16 {
		t.Errorf("GoldenImage.RootDiskGB = %d, want 16", cfg.GoldenImage.RootDiskGB)
	}
	if len(cfg.GoldenImage.Packages) != 2 || cfg.GoldenImage.Packages[0] != "git" {
		t.Errorf("Packages = %v, want [git curl]", cfg.GoldenImage.Packages)
	}
}

func TestLoad_Defaults(t *testing.T) {
	path := writeConfig(t, "{}")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	defaults := DefaultConfig()

	if cfg.ImagesDir != defaults.ImagesDir {
		t.Errorf("ImagesDir = %q, want %q", cfg.ImagesDir, defaults.ImagesDir)
	}
	if cfg.CloudinitDir != defaults.CloudinitDir {
		t.Errorf("CloudinitDir = %q, want %q", cfg.CloudinitDir, defaults.CloudinitDir)
	}
	if cfg.VMUser != defaults.VMUser {
		t.Errorf("VMUser = %q, want %q", cfg.VMUser, defaults.VMUser)
	}

	if cfg.BaseImage.URL != defaults.BaseImage.URL {
		t.Errorf("BaseImage.URL = %q, want %q", cfg.BaseImage.URL, defaults.BaseImage.URL)
	}
	if cfg.BaseImage.Filename != defaults.BaseImage.Filename {
		t.Errorf("BaseImage.Filename = %q, want %q", cfg.BaseImage.Filename, defaults.BaseImage.Filename)
	}
	if cfg.GoldenImage.Name != defaults.GoldenImage.Name {
		t.Errorf("GoldenImage.Name = %q, want %q", cfg.GoldenImage.Name, defaults.GoldenImage.Name)
	}
	if cfg.GoldenImage.RootDiskGB != defaults.GoldenImage.RootDiskGB {
		t.Errorf("GoldenImage.RootDiskGB = %d, want %d", cfg.GoldenImage.RootDiskGB, defaults.GoldenImage.RootDiskGB)
	}
	if len(cfg.GoldenImage.Packages) != len(defaults.GoldenImage.Packages) {
		t.Errorf("Packages len = %d, want %d", len(cfg.GoldenImage.Packages), len(defaults.GoldenImage.Packages))
	}
	if cfg.Network.BridgeIP != defaults.Network.BridgeIP {
		t.Errorf("BridgeIP = %q, want %q", cfg.Network.BridgeIP, defaults.Network.BridgeIP)
	}
	if cfg.Network.Subnet != defaults.Network.Subnet {
		t.Errorf("Subnet = %q, want %q", cfg.Network.Subnet, defaults.Network.Subnet)
	}
	if cfg.Proxy.Port != defaults.Proxy.Port {
		t.Errorf("Proxy.Port = %d, want %d", cfg.Proxy.Port, defaults.Proxy.Port)
	}
	if len(cfg.Proxy.AllowedDomains) != len(defaults.Proxy.AllowedDomains) {
		t.Errorf("AllowedDomains len = %d, want %d", len(cfg.Proxy.AllowedDomains), len(defaults.Proxy.AllowedDomains))
	}
	if cfg.Proxy.FilterFile != defaults.Proxy.FilterFile {
		t.Errorf("FilterFile = %q, want %q", cfg.Proxy.FilterFile, defaults.Proxy.FilterFile)
	}
	if cfg.VMDefaults.VCPUs != defaults.VMDefaults.VCPUs {
		t.Errorf("VCPUs = %d, want %d", cfg.VMDefaults.VCPUs, defaults.VMDefaults.VCPUs)
	}
	if cfg.VMDefaults.RAMMB != defaults.VMDefaults.RAMMB {
		t.Errorf("RAMMB = %d, want %d", cfg.VMDefaults.RAMMB, defaults.VMDefaults.RAMMB)
	}
	if cfg.VMDefaults.Locale != defaults.VMDefaults.Locale {
		t.Errorf("Locale = %q, want %q", cfg.VMDefaults.Locale, defaults.VMDefaults.Locale)
	}
	if cfg.VMDefaults.Timezone != defaults.VMDefaults.Timezone {
		t.Errorf("Timezone = %q, want %q", cfg.VMDefaults.Timezone, defaults.VMDefaults.Timezone)
	}
}

func TestLoad_PartialOverride(t *testing.T) {
	yaml := `
vm_user: custom
vm_defaults:
  vcpus: 8
`
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.VMUser != "custom" {
		t.Errorf("VMUser = %q, want custom", cfg.VMUser)
	}
	if cfg.VMDefaults.VCPUs != 8 {
		t.Errorf("VCPUs = %d, want 8", cfg.VMDefaults.VCPUs)
	}

	defaults := DefaultConfig()
	if cfg.ImagesDir != defaults.ImagesDir {
		t.Errorf("ImagesDir should keep default, got %q", cfg.ImagesDir)
	}
	if cfg.VMDefaults.RAMMB != defaults.VMDefaults.RAMMB {
		t.Errorf("RAMMB should keep default, got %d", cfg.VMDefaults.RAMMB)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "reading config file") {
		t.Errorf("error = %q, want it to contain 'reading config file'", err.Error())
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeConfig(t, "{{not yaml}}")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parsing config file") {
		t.Errorf("error = %q, want it to contain 'parsing config file'", err.Error())
	}
}

func TestLoad_InvalidConfig(t *testing.T) {
	yaml := `
proxy:
  port: 0
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
	if !strings.Contains(err.Error(), "invalid config") {
		t.Errorf("error = %q, want it to contain 'invalid config'", err.Error())
	}
}

func TestValidate_BadIPAddress(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Network.BridgeIP = "not-an-ip"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for bad IP")
	}
	if !strings.Contains(err.Error(), "bridge_ip") {
		t.Errorf("error = %q, want mention of bridge_ip", err.Error())
	}
}

func TestValidate_IPv6Rejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Network.BridgeIP = "::1"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for IPv6")
	}
	if !strings.Contains(err.Error(), "bridge_ip") {
		t.Errorf("error = %q, want mention of bridge_ip", err.Error())
	}
}

func TestValidate_EmptyDomains(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Proxy.AllowedDomains = nil
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty domains")
	}
	if !strings.Contains(err.Error(), "allowed_domains") {
		t.Errorf("error = %q, want mention of allowed_domains", err.Error())
	}
}

func TestValidate_BadChecksum(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseImage.Checksum = "md5:abc123"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for bad checksum prefix")
	}
	if !strings.Contains(err.Error(), "sha256: or sha512:") {
		t.Errorf("error = %q, want mention of sha256:/sha512:", err.Error())
	}
}

func TestValidate_EmptyChecksum(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseImage.Checksum = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty checksum should be valid: %v", err)
	}
}

func TestValidate_ValidChecksums(t *testing.T) {
	for _, prefix := range []string{"sha256:", "sha512:"} {
		cfg := DefaultConfig()
		cfg.BaseImage.Checksum = prefix + "abc123"
		if err := cfg.Validate(); err != nil {
			t.Errorf("checksum %q should be valid: %v", prefix, err)
		}
	}
}

func TestValidate_VCPUsZero(t *testing.T) {
	cfg := DefaultConfig()
	cfg.VMDefaults.VCPUs = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for zero vcpus")
	}
	if !strings.Contains(err.Error(), "vcpus") {
		t.Errorf("error = %q, want mention of vcpus", err.Error())
	}
}

func TestValidate_RAMTooLow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.VMDefaults.RAMMB = 256
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for low RAM")
	}
	if !strings.Contains(err.Error(), "ram_mb") {
		t.Errorf("error = %q, want mention of ram_mb", err.Error())
	}
}

func TestValidate_BadSubnet(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Network.Subnet = "not-cidr"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for bad subnet")
	}
	if !strings.Contains(err.Error(), "subnet") {
		t.Errorf("error = %q, want mention of subnet", err.Error())
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.VMUser = ""
	cfg.Network.BridgeIP = "bad"
	cfg.Proxy.Port = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	msg := err.Error()
	if !strings.Contains(msg, "vm_user") {
		t.Error("missing vm_user error")
	}
	if !strings.Contains(msg, "bridge_ip") {
		t.Error("missing bridge_ip error")
	}
	if !strings.Contains(msg, "proxy.port") {
		t.Error("missing proxy.port error")
	}
	if !strings.Contains(msg, "3 config errors") {
		t.Errorf("expected '3 config errors' in message, got: %s", msg)
	}
}

func TestValidatePaths_DirNotExist(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ImagesDir = filepath.Join(t.TempDir(), "nonexistent")
	cfg.CloudinitDir = filepath.Join(t.TempDir(), "also-nonexistent")
	err := cfg.ValidatePaths()
	if err == nil {
		t.Fatal("expected error for nonexistent dirs")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not exist") {
		t.Errorf("error = %q, want mention of 'does not exist'", msg)
	}
	if !strings.Contains(msg, "images_dir") {
		t.Errorf("error = %q, want mention of field name 'images_dir'", msg)
	}
	if !strings.Contains(msg, "cloudinit_dir") {
		t.Errorf("error = %q, want mention of field name 'cloudinit_dir'", msg)
	}
}

func TestValidatePaths_DirExists(t *testing.T) {
	dir := t.TempDir()
	imagesDir := filepath.Join(dir, "images")
	cloudinitDir := filepath.Join(dir, "cloudinit")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cloudinitDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.ImagesDir = imagesDir
	cfg.CloudinitDir = cloudinitDir
	if err := cfg.ValidatePaths(); err != nil {
		t.Fatalf("ValidatePaths() should pass: %v", err)
	}
}

func TestValidatePaths_NotADir(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.ImagesDir = filePath
	cfg.CloudinitDir = dir
	err := cfg.ValidatePaths()
	if err == nil {
		t.Fatal("expected error for file instead of dir")
	}
	msg := err.Error()
	if !strings.Contains(msg, "is not a directory") {
		t.Errorf("error = %q, want mention of 'is not a directory'", msg)
	}
	if !strings.Contains(msg, "images_dir") {
		t.Errorf("error = %q, want mention of field name 'images_dir'", msg)
	}
}

func TestValidate_PortBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"negative", -1, true},
		{"zero", 0, true},
		{"lower_bound", 1, false},
		{"mid_range", 3128, false},
		{"upper_bound", 65535, false},
		{"above_upper", 65536, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Proxy.Port = tt.port
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("port %d: expected error", tt.port)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("port %d: unexpected error: %v", tt.port, err)
			}
		})
	}
}

// --- BVA: numeric field boundaries (exact thresholds) ---

func TestValidate_NumericBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"vcpus=1_valid", func(c *Config) { c.VMDefaults.VCPUs = 1 }, false},
		{"vcpus=0_invalid", func(c *Config) { c.VMDefaults.VCPUs = 0 }, true},
		{"vcpus=-1_invalid", func(c *Config) { c.VMDefaults.VCPUs = -1 }, true},
		{"ram_mb=512_valid", func(c *Config) { c.VMDefaults.RAMMB = 512 }, false},
		{"ram_mb=511_invalid", func(c *Config) { c.VMDefaults.RAMMB = 511 }, true},
		{"root_disk=1_valid", func(c *Config) { c.VMDefaults.RootDiskGB = 1 }, false},
		{"root_disk=0_invalid", func(c *Config) { c.VMDefaults.RootDiskGB = 0 }, true},
		{"data_disk=1_valid", func(c *Config) { c.VMDefaults.DataDiskGB = 1 }, false},
		{"data_disk=0_invalid", func(c *Config) { c.VMDefaults.DataDiskGB = 0 }, true},
		{"golden_root_disk=1_valid", func(c *Config) { c.GoldenImage.RootDiskGB = 1 }, false},
		{"golden_root_disk=0_invalid", func(c *Config) { c.GoldenImage.RootDiskGB = 0 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidationErrors_SingleError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.VMUser = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	// Single error should NOT have the "N config errors:" prefix
	if strings.Contains(err.Error(), "config errors:") {
		t.Errorf("single error should not have count prefix, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "vm_user") {
		t.Errorf("error should mention vm_user, got: %s", err.Error())
	}
}

func TestLoad_SliceReplacesDefaults(t *testing.T) {
	yaml := `
golden_image:
  packages: [git]
`
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.GoldenImage.Packages) != 1 || cfg.GoldenImage.Packages[0] != "git" {
		t.Errorf("Packages = %v, want [git] (should replace defaults, not merge)", cfg.GoldenImage.Packages)
	}
}

func TestLoad_YAMLTypeMismatch(t *testing.T) {
	yaml := `
proxy:
  port: "not_a_number"
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	if !strings.Contains(err.Error(), "parsing config file") {
		t.Errorf("error = %q, want 'parsing config file'", err.Error())
	}
}

func TestLoad_UnknownFieldsIgnored(t *testing.T) {
	yaml := `
unknown_field: some_value
nested_unknown:
  foo: bar
vm_user: testuser
`
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unknown fields should be silently ignored: %v", err)
	}
	if cfg.VMUser != "testuser" {
		t.Errorf("VMUser = %q, want testuser", cfg.VMUser)
	}
}

func TestValidate_EmptySubnet(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Network.Subnet = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty subnet should be valid: %v", err)
	}
}

func TestValidate_EmptySliceDomains(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Proxy.AllowedDomains = []string{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty slice domains")
	}
	if !strings.Contains(err.Error(), "allowed_domains") {
		t.Errorf("error = %q, want mention of allowed_domains", err.Error())
	}
}

func TestLoad_EmptyPathUsesDefault(t *testing.T) {
	_, err := Load("")
	if err == nil {
		t.Fatal("expected error when default config doesn't exist")
	}
	if !strings.Contains(err.Error(), "reading config file") {
		t.Errorf("error = %q, want 'reading config file'", err.Error())
	}
}

func TestValidate_AllRequiredStrings(t *testing.T) {
	fields := []struct {
		name   string
		mutate func(*Config)
		errMsg string
	}{
		{"images_dir", func(c *Config) { c.ImagesDir = "" }, "images_dir"},
		{"cloudinit_dir", func(c *Config) { c.CloudinitDir = "" }, "cloudinit_dir"},
		{"vm_user", func(c *Config) { c.VMUser = "" }, "vm_user"},
		{"base_image.url", func(c *Config) { c.BaseImage.URL = "" }, "base_image.url"},
		{"base_image.filename", func(c *Config) { c.BaseImage.Filename = "" }, "base_image.filename"},
		{"golden_image.name", func(c *Config) { c.GoldenImage.Name = "" }, "golden_image.name"},
		{"network.libvirt_network", func(c *Config) { c.Network.LibvirtNetwork = "" }, "libvirt_network"},
	}
	for _, tt := range fields {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for empty %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error = %q, want mention of %q", err.Error(), tt.errMsg)
			}
		})
	}
}
