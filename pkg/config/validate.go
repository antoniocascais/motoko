package config

import (
	"fmt"
	"net"
	"os"
	"strings"
)

type ValidationErrors []error

func (ve ValidationErrors) Error() string {
	if len(ve) == 1 {
		return ve[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d config errors:", len(ve))
	for _, e := range ve {
		fmt.Fprintf(&b, "\n  - %s", e.Error())
	}
	return b.String()
}

func (ve ValidationErrors) Unwrap() []error {
	return []error(ve)
}

func (c *Config) Validate() error {
	var errs ValidationErrors

	if c.ImagesDir == "" {
		errs = append(errs, fmt.Errorf("images_dir is required"))
	}
	if c.CloudinitDir == "" {
		errs = append(errs, fmt.Errorf("cloudinit_dir is required"))
	}
	if c.VMUser == "" {
		errs = append(errs, fmt.Errorf("vm_user is required"))
	}

	if c.BaseImage.URL == "" {
		errs = append(errs, fmt.Errorf("base_image.url is required"))
	}
	if c.BaseImage.Filename == "" {
		errs = append(errs, fmt.Errorf("base_image.filename is required"))
	}
	if c.BaseImage.Checksum != "" {
		if !strings.HasPrefix(c.BaseImage.Checksum, "sha256:") &&
			!strings.HasPrefix(c.BaseImage.Checksum, "sha512:") {
			errs = append(errs, fmt.Errorf("base_image.checksum must start with sha256: or sha512: (got %q)", c.BaseImage.Checksum))
		}
	}

	if c.GoldenImage.Name == "" {
		errs = append(errs, fmt.Errorf("golden_image.name is required"))
	}
	if c.GoldenImage.RootDiskGB < 1 {
		errs = append(errs, fmt.Errorf("golden_image.root_disk_gb must be >= 1 (got %d)", c.GoldenImage.RootDiskGB))
	}

	if c.Network.LibvirtNetwork == "" {
		errs = append(errs, fmt.Errorf("network.libvirt_network is required"))
	}
	if ip := net.ParseIP(c.Network.BridgeIP); ip == nil || ip.To4() == nil {
		errs = append(errs, fmt.Errorf("network.bridge_ip must be a valid IPv4 address (got %q)", c.Network.BridgeIP))
	}
	if c.Network.Subnet != "" {
		if _, _, err := net.ParseCIDR(c.Network.Subnet); err != nil {
			errs = append(errs, fmt.Errorf("network.subnet must be valid CIDR (got %q)", c.Network.Subnet))
		}
	}

	if c.Proxy.Port < 1 || c.Proxy.Port > 65535 {
		errs = append(errs, fmt.Errorf("proxy.port must be 1-65535 (got %d)", c.Proxy.Port))
	}
	if len(c.Proxy.AllowedDomains) == 0 {
		errs = append(errs, fmt.Errorf("proxy.allowed_domains must not be empty"))
	}

	if c.VMDefaults.VCPUs < 1 {
		errs = append(errs, fmt.Errorf("vm_defaults.vcpus must be >= 1 (got %d)", c.VMDefaults.VCPUs))
	}
	if c.VMDefaults.RAMMB < 512 {
		errs = append(errs, fmt.Errorf("vm_defaults.ram_mb must be >= 512 (got %d)", c.VMDefaults.RAMMB))
	}
	if c.VMDefaults.RootDiskGB < 1 {
		errs = append(errs, fmt.Errorf("vm_defaults.root_disk_gb must be >= 1 (got %d)", c.VMDefaults.RootDiskGB))
	}
	if c.VMDefaults.DataDiskGB < 1 {
		errs = append(errs, fmt.Errorf("vm_defaults.data_disk_gb must be >= 1 (got %d)", c.VMDefaults.DataDiskGB))
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

func (c *Config) ValidatePaths() error {
	var errs ValidationErrors

	if err := checkDirWritable(c.ImagesDir, "images_dir"); err != nil {
		errs = append(errs, err)
	}
	if err := checkDirWritable(c.CloudinitDir, "cloudinit_dir"); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

func checkDirWritable(path, fieldName string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s directory does not exist: %s", fieldName, path)
		}
		return fmt.Errorf("%s: %w", fieldName, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", fieldName, path)
	}
	f, err := os.CreateTemp(path, ".motoko-write-test-*")
	if err != nil {
		return fmt.Errorf("%s directory is not writable: %s", fieldName, path)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}
