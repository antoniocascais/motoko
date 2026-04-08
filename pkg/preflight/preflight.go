package preflight

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"

	"github.com/antoniocascais/motoko/pkg/config"
	"gopkg.in/yaml.v3"
)

type CheckResult struct {
	Name   string
	Passed bool
	Detail string
}

var (
	lookPath = exec.LookPath
	runCmd   = defaultRunCmd
)

var requiredBinaries = []string{
	"virsh", "virt-install", "virt-customize",
	"qemu-img", "guestfish", "cloud-localds",
}

func defaultRunCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func checkLinux() CheckResult {
	if runtime.GOOS == "linux" {
		return CheckResult{Name: "Linux OS", Passed: true}
	}
	return CheckResult{Name: "Linux OS", Passed: false, Detail: fmt.Sprintf("found %s", runtime.GOOS)}
}

func checkKVM() CheckResult {
	if _, err := os.Stat("/dev/kvm"); err == nil {
		return CheckResult{Name: "/dev/kvm", Passed: true}
	}
	return CheckResult{Name: "/dev/kvm", Passed: false, Detail: "not found — enable KVM in BIOS/kernel"}
}

func checkBinaries() []CheckResult {
	var results []CheckResult
	for _, bin := range requiredBinaries {
		if _, err := lookPath(bin); err == nil {
			results = append(results, CheckResult{Name: bin, Passed: true})
		} else {
			results = append(results, CheckResult{Name: bin, Passed: false, Detail: "not found in PATH"})
		}
	}
	return results
}

func checkLibvirtGroup() CheckResult {
	u, err := user.Current()
	if err != nil {
		return CheckResult{Name: "libvirt group", Passed: false, Detail: err.Error()}
	}
	gids, err := u.GroupIds()
	if err != nil {
		return CheckResult{Name: "libvirt group", Passed: false, Detail: err.Error()}
	}
	for _, gid := range gids {
		g, err := user.LookupGroupId(gid)
		if err != nil {
			continue
		}
		if g.Name == "libvirt" {
			return CheckResult{Name: "libvirt group", Passed: true}
		}
	}
	return CheckResult{Name: "libvirt group", Passed: false, Detail: fmt.Sprintf("user %s not in libvirt group", u.Username)}
}

func checkDefaultNetwork() CheckResult {
	out, err := runCmd("virsh", "--connect", "qemu:///system", "net-info", "default")
	if err != nil {
		return CheckResult{Name: "libvirt default network", Passed: false, Detail: "virsh net-info default failed"}
	}
	// Parse the "Active:" line specifically to avoid matching "yes" on other lines
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "Active:" && fields[1] == "yes" {
			return CheckResult{Name: "libvirt default network", Passed: true}
		}
	}
	return CheckResult{Name: "libvirt default network", Passed: false, Detail: "network not active"}
}

func RunAll() ([]CheckResult, bool) {
	var results []CheckResult
	allPassed := true

	add := func(r CheckResult) {
		results = append(results, r)
		if !r.Passed {
			allPassed = false
		}
	}

	add(checkLinux())
	add(checkKVM())
	for _, r := range checkBinaries() {
		add(r)
	}
	add(checkLibvirtGroup())
	add(checkDefaultNetwork())

	return results, allPassed
}

// CheckConfigPaths validates that images_dir and cloudinit_dir exist and are writable.
func CheckConfigPaths(imagesDir, cloudinitDir string) []CheckResult {
	return []CheckResult{
		checkDirResult(imagesDir, "images_dir"),
		checkDirResult(cloudinitDir, "cloudinit_dir"),
	}
}

func checkDirResult(path, fieldName string) CheckResult {
	if err := config.CheckDirWritable(path, fieldName); err != nil {
		return CheckResult{Name: fieldName, Passed: false, Detail: err.Error()}
	}
	return CheckResult{Name: fieldName, Passed: true}
}

func EnsureConfigDir(configDir string) error {
	return os.MkdirAll(configDir, 0700)
}

func WriteDefaultConfig(configPath string) error {
	cfg := config.DefaultConfig()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling default config: %w", err)
	}

	f, err := os.OpenFile(configPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("writing default config: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing default config: %w", err)
	}
	return nil
}
