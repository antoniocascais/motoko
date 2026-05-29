package vm

import (
	"fmt"
	"strings"
	"time"
)

const vmPrefix = "motoko-"

// memtune hard-limit needs headroom over guest RAM to absorb QEMU process overhead
// (vCPU stacks, KVM data structures, device emulation). Without it, qemu's RSS hits
// the cgroup memcg cap at guest-RAM size and the kernel OOM-kills the VM.
const (
	hardLimitHeadroomPct   = 15
	hardLimitHeadroomMinKB = 128 * 1024 // 128 MiB floor for small VMs
)

// libvirtName converts a user-facing instance name to the libvirt domain name.
func libvirtName(name string) string { return vmPrefix + name }

// hardLimitKiB returns the memtune --hard-limit value for a VM of ramMB MiB,
// adding headroom (max of 15% or 128 MiB) over the guest RAM allocation.
func hardLimitKiB(ramMB int) int {
	ramKiB := ramMB * 1024
	headroom := max(ramKiB*hardLimitHeadroomPct/100, hardLimitHeadroomMinKB)
	return ramKiB + headroom
}

// DefineConfig holds parameters for defining a new VM via virt-install.
type DefineConfig struct {
	Name         string
	VCPUs        int
	CPUPinning   string // e.g. "8-11"; empty = no cpuset
	RAMMB        int
	OverlayPath  string
	DataPath     string
	CloudInitISO string
	Network      string
}

// VMInfo represents a motoko VM's state.
type VMInfo struct {
	Name  string // user-facing name (prefix stripped)
	State string
	IP    string
}

// Define creates a new VM domain via virt-install. Does not start it.
func Define(cfg DefineConfig) error {
	vmName := libvirtName(cfg.Name)

	vcpuArg := fmt.Sprintf("%d", cfg.VCPUs)
	if cfg.CPUPinning != "" {
		vcpuArg = fmt.Sprintf("%d,cpuset=%s", cfg.VCPUs, cfg.CPUPinning)
	}

	args := []string{
		"--name", vmName,
		"--vcpus", vcpuArg,
		"--memory", fmt.Sprintf("%d", cfg.RAMMB),
		"--disk", fmt.Sprintf("path=%s,format=qcow2", cfg.OverlayPath),
		"--disk", fmt.Sprintf("path=%s,format=qcow2", cfg.DataPath),
		"--disk", fmt.Sprintf("path=%s,device=cdrom", cfg.CloudInitISO),
		"--network", fmt.Sprintf("network=%s", cfg.Network),
		"--os-variant", "debian12",
		"--graphics", "none",
		"--video", "none",
		"--sound", "none",
		"--controller", "usb,model=none",
		"--memballoon", "model=none",
		"--channel", "none",
		"--import",
		"--noautoconsole",
		"--noreboot",
	}

	_, _, err := runCmd("virt-install", libvirtArgs(args...)...)
	return err
}

// ApplyTuning sets blkio weight, network bandwidth cap, and memory hard limit.
// All settings use --config to persist in domain XML.
func ApplyTuning(name string, blkioWeight, netBandwidthKB, ramMB int) error {
	vmName := libvirtName(name)

	if _, _, err := runCmd("virsh", libvirtArgs("blkiotune", vmName, "--weight", fmt.Sprintf("%d", blkioWeight), "--config")...); err != nil {
		return fmt.Errorf("blkiotune: %w", err)
	}

	// Network bandwidth: need MAC from domiflist first
	stdout, _, err := runCmd("virsh", libvirtArgs("domiflist", vmName)...)
	if err != nil {
		return fmt.Errorf("domiflist: %w", err)
	}
	if mac := parseMACFromDomiflist(stdout); mac != "" {
		if _, _, err := runCmd("virsh", libvirtArgs("domiftune", vmName, mac, "--outbound", fmt.Sprintf("%d", netBandwidthKB), "--config")...); err != nil {
			return fmt.Errorf("domiftune: %w", err)
		}
	}

	// Memory hard limit (virsh memtune takes KiB). Uses hardLimitKiB which adds
	// headroom over guest RAM so qemu's process overhead doesn't trip the OOM-killer.
	if _, _, err := runCmd("virsh", libvirtArgs("memtune", vmName, "--hard-limit", fmt.Sprintf("%d", hardLimitKiB(ramMB)), "--config")...); err != nil {
		return fmt.Errorf("memtune: %w", err)
	}

	return nil
}

// UpdateResources persistently updates the VM's RAM (MiB) and/or vCPU count.
// Zero values mean "leave unchanged". VM must be shut off; changes take effect on next start.
//
// libvirt rejects current > max, so max is always set before current. On reduction
// (either RAM or vCPUs) this initial setmaxmem/setvcpus --maximum will fail because
// max would go below current — bump down in two steps (lower current first, then run update).
//
// Order matters for partial-failure safety: memtune (hard-limit) runs BEFORE setmaxmem/setmem
// so a transient memtune failure leaves both guest RAM and cgroup cap at the old value
// rather than raising guest RAM past a stale (smaller) cap and OOM-killing on next start.
func UpdateResources(name string, ramMB, vcpus int) error {
	vmName := libvirtName(name)

	if ramMB > 0 {
		hardLimit := fmt.Sprintf("%d", hardLimitKiB(ramMB))
		if _, _, err := runCmd("virsh", libvirtArgs("memtune", vmName, "--hard-limit", hardLimit, "--config")...); err != nil {
			return fmt.Errorf("memtune: %w", err)
		}
		ramKiB := fmt.Sprintf("%d", ramMB*1024)
		if _, _, err := runCmd("virsh", libvirtArgs("setmaxmem", vmName, ramKiB, "--config")...); err != nil {
			return fmt.Errorf("setmaxmem: %w", err)
		}
		if _, _, err := runCmd("virsh", libvirtArgs("setmem", vmName, ramKiB, "--config")...); err != nil {
			return fmt.Errorf("setmem: %w", err)
		}
	}

	if vcpus > 0 {
		v := fmt.Sprintf("%d", vcpus)
		if _, _, err := runCmd("virsh", libvirtArgs("setvcpus", vmName, v, "--maximum", "--config")...); err != nil {
			return fmt.Errorf("setvcpus --maximum: %w", err)
		}
		if _, _, err := runCmd("virsh", libvirtArgs("setvcpus", vmName, v, "--config")...); err != nil {
			return fmt.Errorf("setvcpus: %w", err)
		}
	}

	return nil
}

// Start boots a defined VM.
func Start(name string) error {
	_, _, err := runCmd("virsh", libvirtArgs("start", libvirtName(name))...)
	return err
}

// Stop sends an ACPI shutdown to the VM (graceful).
func Stop(name string) error {
	_, _, err := runCmd("virsh", libvirtArgs("shutdown", libvirtName(name))...)
	return err
}

// ForceStop immediately terminates the VM (hard kill).
func ForceStop(name string) error {
	_, _, err := runCmd("virsh", libvirtArgs("destroy", libvirtName(name))...)
	return err
}

// Undefine removes the VM domain definition.
func Undefine(name string) error {
	_, _, err := runCmd("virsh", libvirtArgs("undefine", libvirtName(name))...)
	return err
}

// State returns the VM's current state (e.g. "running", "shut off").
func State(name string) (string, error) {
	stdout, _, err := runCmd("virsh", libvirtArgs("domstate", libvirtName(name))...)
	return stdout, err
}

// GetIP returns the VM's IPv4 address, or empty string if unavailable.
func GetIP(name string) (string, error) {
	stdout, _, err := runCmd("virsh", libvirtArgs("domifaddr", libvirtName(name))...)
	if err != nil {
		return "", err
	}
	return parseIPFromDomifaddr(stdout), nil
}

// sleepFn is the inter-poll delay, a package var so tests can stub it out.
var sleepFn = time.Sleep

// WaitForSSHReady polls until the VM answers SSH and reports the expected
// hostname, or the timeout is reached. expectedHostname is the bare instance
// name — cloud-init sets the guest hostname to it. Asserting it proves the box
// answering on ip is actually this instance: a stale DHCP lease or ARP entry
// can route SSH to a different VM, which connectivity alone can't catch.
// Retries on both connection failure and hostname mismatch, since sshd can come
// up before cloud-init applies the hostname.
func WaitForSSHReady(ip, keyPath, user, expectedHostname string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		out, _, err := runCmd("ssh",
			"-o", "ConnectTimeout=2",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "BatchMode=yes",
			"-i", keyPath,
			fmt.Sprintf("%s@%s", user, ip),
			"hostname",
		)
		got := strings.TrimSpace(out)
		switch {
		case err != nil:
			lastErr = err
		case got == expectedHostname:
			return nil
		default:
			lastErr = fmt.Errorf("answered as %q, expected %q (possible wrong VM at %s)",
				got, expectedHostname, ip)
		}
		// Skip the final sleep once the deadline passed — no point waiting 2s
		// only to fail the loop condition.
		if time.Now().Before(deadline) {
			sleepFn(2 * time.Second)
		}
	}

	return fmt.Errorf("VM not ready after %s: %w", timeout, lastErr)
}

// ListAll returns all motoko-prefixed VMs with their state.
func ListAll() ([]VMInfo, error) {
	stdout, _, err := runCmd("virsh", libvirtArgs("list", "--all", "--name")...)
	if err != nil {
		return nil, err
	}

	var vms []VMInfo
	for _, line := range strings.Split(stdout, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || !strings.HasPrefix(name, vmPrefix) {
			continue
		}
		shortName := strings.TrimPrefix(name, vmPrefix)
		st, err := State(shortName)
		if err != nil {
			st = "unknown"
		}
		vm := VMInfo{Name: shortName, State: st}
		if st == "running" {
			vm.IP, _ = GetIP(shortName)
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

// DisableAutostart prevents the VM from starting on host boot.
func DisableAutostart(name string) error {
	_, _, err := runCmd("virsh", libvirtArgs("autostart", "--disable", libvirtName(name))...)
	return err
}

// parseMACFromDomiflist extracts the MAC address from virsh domiflist output.
// Mirrors: awk 'NR>2 && NF{print $5; exit}'
func parseMACFromDomiflist(output string) string {
	for i, line := range strings.Split(output, "\n") {
		if i < 2 { // skip header + separator
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			return fields[4]
		}
	}
	return ""
}

// parseIPFromDomifaddr extracts the IPv4 address from virsh domifaddr output.
// Strips the CIDR prefix (e.g. "192.168.122.45/24" -> "192.168.122.45").
// Searches all fields for a CIDR-formatted address rather than relying on column position,
// since "MAC address" header splits into two tokens with strings.Fields.
func parseIPFromDomifaddr(output string) string {
	for _, line := range strings.Split(output, "\n") {
		for _, field := range strings.Fields(line) {
			if strings.Contains(field, "/") && strings.Contains(field, ".") {
				return strings.SplitN(field, "/", 2)[0]
			}
		}
	}
	return ""
}
