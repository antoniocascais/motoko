package vm

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLibvirtName(t *testing.T) {
	if got := libvirtName("test"); got != "motoko-test" {
		t.Errorf("libvirtName(%q) = %q, want %q", "test", got, "motoko-test")
	}
}

func TestDefine_BuildsCorrectVirtInstallArgs(t *testing.T) {
	recs := mockRunner(t)

	cfg := DefineConfig{
		Name:         "sandbox",
		VCPUs:        4,
		RAMMB:        4096,
		OverlayPath:  "/images/motoko-sandbox-overlay.qcow2",
		DataPath:     "/images/motoko-sandbox-data.qcow2",
		CloudInitISO: "/cloud-init/motoko-sandbox.iso",
		Network:      "default",
	}

	if err := Define(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*recs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*recs))
	}

	rec := (*recs)[0]
	if rec.name != "virt-install" {
		t.Fatalf("command = %q, want virt-install", rec.name)
	}

	args := strings.Join(rec.args, " ")

	checks := []string{
		"--name motoko-sandbox",
		"--vcpus 4",
		"--memory 4096",
		"--disk path=/images/motoko-sandbox-overlay.qcow2,format=qcow2",
		"--disk path=/images/motoko-sandbox-data.qcow2,format=qcow2",
		"--disk path=/cloud-init/motoko-sandbox.iso,device=cdrom",
		"--network network=default",
		"--os-variant debian12",
		"--graphics none",
		"--video none",
		"--sound none",
		"--controller usb,model=none",
		"--memballoon model=none",
		"--channel none",
		"--import",
		"--noautoconsole",
		"--noreboot",
	}

	for _, want := range checks {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q\ngot: %s", want, args)
		}
	}

	// No cpuset when CPUPinning is empty
	if strings.Contains(args, "cpuset") {
		t.Error("args contain cpuset when CPUPinning is empty")
	}
}

func TestDefine_WithCPUPinning(t *testing.T) {
	recs := mockRunner(t)

	cfg := DefineConfig{
		Name:         "pinned",
		VCPUs:        4,
		CPUPinning:   "8-11",
		RAMMB:        4096,
		OverlayPath:  "/images/overlay.qcow2",
		DataPath:     "/images/data.qcow2",
		CloudInitISO: "/ci/cloud-init.iso",
		Network:      "default",
	}

	if err := Define(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	args := strings.Join((*recs)[0].args, " ")
	if !strings.Contains(args, "--vcpus 4,cpuset=8-11") {
		t.Errorf("args missing cpuset\ngot: %s", args)
	}
}

func TestDefine_NamePrefix(t *testing.T) {
	recs := mockRunner(t)

	cfg := DefineConfig{
		Name:         "myvm",
		VCPUs:        2,
		RAMMB:        2048,
		OverlayPath:  "/images/o.qcow2",
		DataPath:     "/images/d.qcow2",
		CloudInitISO: "/ci/ci.iso",
		Network:      "default",
	}

	if err := Define(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	args := (*recs)[0].args
	// args[0:2] are --connect qemu:///system, actual virt-install args start at [2]
	if len(args) < 4 || args[2] != "--name" || args[3] != libvirtName("myvm") {
		t.Errorf("first virt-install args = %v, want [--name %s ...]", args[2:4], libvirtName("myvm"))
	}
}

func TestApplyTuning_AllFourCalls(t *testing.T) {
	domiflistOutput := " Interface  Type       Source     Model       MAC\n" +
		"-------------------------------------------------------\n" +
		" vnet0      network    default    virtio      52:54:00:ab:cd:ef\n"

	callCount := 0
	recs := mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		callCount++
		// Return domiflist output on the second call (virsh domiflist)
		if callCount == 2 {
			return domiflistOutput, "", nil
		}
		return "", "", nil
	})

	if err := ApplyTuning("test", 200, 5000, 4096); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*recs) != 4 {
		t.Fatalf("expected 4 calls, got %d", len(*recs))
	}

	// 1: blkiotune
	if (*recs)[0].name != "virsh" || (*recs)[0].args[2] != "blkiotune" {
		t.Errorf("call 1: got %s %v, want virsh blkiotune", (*recs)[0].name, (*recs)[0].args)
	}

	// 2: domiflist
	if (*recs)[1].args[2] != "domiflist" {
		t.Errorf("call 2: got %v, want domiflist", (*recs)[1].args)
	}

	// 3: domiftune
	if (*recs)[2].args[2] != "domiftune" {
		t.Errorf("call 3: got %v, want domiftune", (*recs)[2].args)
	}

	// 4: memtune
	if (*recs)[3].args[2] != "memtune" {
		t.Errorf("call 4: got %v, want memtune", (*recs)[3].args)
	}
}

func TestApplyTuning_BlkioWeightArgs(t *testing.T) {
	recs := mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", nil
	})

	_ = ApplyTuning("test", 200, 5000, 4096)

	args := strings.Join((*recs)[0].args, " ")
	want := "--connect qemu:///system blkiotune motoko-test --weight 200 --config"
	if args != want {
		t.Errorf("blkiotune args = %q, want %q", args, want)
	}
}

func TestApplyTuning_MemtuneConversion(t *testing.T) {
	recs := mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", nil
	})

	_ = ApplyTuning("test", 200, 5000, 4096)

	// memtune is the last call
	last := (*recs)[len(*recs)-1]
	args := strings.Join(last.args, " ")
	// 4096 MB * 1024 = 4194304 KiB
	want := "--connect qemu:///system memtune motoko-test --hard-limit 4194304 --config"
	if args != want {
		t.Errorf("memtune args = %q, want %q", args, want)
	}
}

func TestApplyTuning_SkipsNetIfNoMAC(t *testing.T) {
	recs := mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		// Return empty domiflist output
		return "", "", nil
	})

	if err := ApplyTuning("test", 200, 5000, 4096); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be 3 calls: blkiotune, domiflist, memtune (no domiftune)
	if len(*recs) != 3 {
		names := make([]string, len(*recs))
		for i, r := range *recs {
			names[i] = fmt.Sprintf("%s %s", r.name, r.args[0])
		}
		t.Fatalf("expected 3 calls (no domiftune), got %d: %v", len(*recs), names)
	}
}

func TestParseMACFromDomiflist(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
	}{
		{
			name: "standard output",
			input: " Interface  Type       Source     Model       MAC\n" +
				"-------------------------------------------------------\n" +
				" vnet0      network    default    virtio      52:54:00:ab:cd:ef\n",
			want: "52:54:00:ab:cd:ef",
		},
		{
			name:  "empty output",
			input: "",
			want:  "",
		},
		{
			name: "header only",
			input: " Interface  Type       Source     Model       MAC\n" +
				"-------------------------------------------------------\n",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseMACFromDomiflist(tt.input); got != tt.want {
				t.Errorf("parseMACFromDomiflist() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseIPFromDomifaddr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "standard output",
			input: " Name       MAC address          Protocol     Address\n" +
				"-------------------------------------------------------------------------------\n" +
				" vnet0      52:54:00:ab:cd:ef    ipv4         192.168.122.45/24\n",
			want: "192.168.122.45",
		},
		{
			name:  "empty output",
			input: "",
			want:  "",
		},
		{
			name: "no IP line",
			input: " Name       MAC address          Protocol     Address\n" +
				"-------------------------------------------------------------------------------\n",
			want: "",
		},
		{
			name: "multiple interfaces",
			input: " Name       MAC address          Protocol     Address\n" +
				"-------------------------------------------------------------------------------\n" +
				" vnet0      52:54:00:ab:cd:ef    ipv4         192.168.122.45/24\n" +
				" vnet1      52:54:00:11:22:33    ipv4         10.0.0.5/8\n",
			want: "192.168.122.45",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseIPFromDomifaddr(tt.input); got != tt.want {
				t.Errorf("parseIPFromDomifaddr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestState_ReturnsCleanString(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "running", "", nil
	})

	state, err := State("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "running" {
		t.Errorf("State() = %q, want %q", state, "running")
	}
}

func TestListAll_FiltersByPrefix(t *testing.T) {
	callCount := 0
	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		callCount++
		if callCount == 1 {
			// virsh list --all --name
			return "motoko-vm1\nother-vm\nmotoko-vm2\n", "", nil
		}
		// State calls return "running" for vm1, "shut off" for vm2
		if callCount == 2 {
			return "running", "", nil
		}
		if callCount == 3 {
			// GetIP for vm1
			return " Name       MAC address          Protocol     Address\n" +
				"-------------------------------------------------------------------------------\n" +
				" vnet0      52:54:00:ab:cd:ef    ipv4         192.168.122.10/24\n", "", nil
		}
		// State for vm2
		return "shut off", "", nil
	})

	vms, err := ListAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(vms))
	}

	if vms[0].Name != "vm1" {
		t.Errorf("vms[0].Name = %q, want %q", vms[0].Name, "vm1")
	}
	if vms[0].State != "running" {
		t.Errorf("vms[0].State = %q, want %q", vms[0].State, "running")
	}
	if vms[0].IP != "192.168.122.10" {
		t.Errorf("vms[0].IP = %q, want %q", vms[0].IP, "192.168.122.10")
	}

	if vms[1].Name != "vm2" {
		t.Errorf("vms[1].Name = %q, want %q", vms[1].Name, "vm2")
	}
	if vms[1].State != "shut off" {
		t.Errorf("vms[1].State = %q, want %q", vms[1].State, "shut off")
	}
	if vms[1].IP != "" {
		t.Errorf("vms[1].IP = %q, want empty", vms[1].IP)
	}
}


func TestLibvirtCommands_IncludeConnectFlag(t *testing.T) {
	// Invariant: every virsh/virt-install/virt-customize call must pass
	// --connect qemu:///system to target the system daemon.
	libvirtBins := map[string]bool{"virsh": true, "virt-install": true, "virt-customize": true}

	domiflistOutput := " Interface  Type       Source     Model       MAC\n" +
		"-------------------------------------------------------\n" +
		" vnet0      network    default    virtio      52:54:00:ab:cd:ef\n"

	tests := []struct {
		name         string
		fn           func()
		wantMinCalls int // minimum libvirt calls expected
	}{
		{"Start", func() { _ = Start("test") }, 1},
		{"Stop", func() { _ = Stop("test") }, 1},
		{"ForceStop", func() { _ = ForceStop("test") }, 1},
		{"Undefine", func() { _ = Undefine("test") }, 1},
		{"State", func() { _, _ = State("test") }, 1},
		{"GetIP", func() { _, _ = GetIP("test") }, 1},
		{"DisableAutostart", func() { _ = DisableAutostart("test") }, 1},
		{"ListAll", func() { _, _ = ListAll() }, 1},
		{"Define", func() {
			_ = Define(DefineConfig{
				Name: "test", VCPUs: 2, RAMMB: 2048,
				OverlayPath: "/o.qcow2", DataPath: "/d.qcow2",
				CloudInitISO: "/ci.iso", Network: "default",
			})
		}, 1},
		{"ApplyTuning", func() { _ = ApplyTuning("test", 200, 5000, 4096) }, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
				// ApplyTuning needs domiflist output to reach domiftune
				if name == "virsh" && len(args) >= 3 && args[2] == "domiflist" {
					return domiflistOutput, "", nil
				}
				return "", "", nil
			})
			tt.fn()

			libvirtCalls := 0
			for _, rec := range *recs {
				if !libvirtBins[rec.name] {
					continue
				}
				libvirtCalls++
				if len(rec.args) < 2 || rec.args[0] != "--connect" || rec.args[1] != libvirtURI {
					t.Errorf("%s %v: missing --connect %s as first args", rec.name, rec.args, libvirtURI)
				}
			}
			if libvirtCalls < tt.wantMinCalls {
				t.Errorf("expected at least %d libvirt calls, got %d", tt.wantMinCalls, libvirtCalls)
			}
		})
	}
}

func TestLifecycleActions_Args(t *testing.T) {
	tests := []struct {
		name    string
		fn      func(string) error
		wantCmd string
	}{
		{"Start", Start, "start"},
		{"Stop", Stop, "shutdown"},
		{"ForceStop", ForceStop, "destroy"},
		{"Undefine", Undefine, "undefine"},
		{"DisableAutostart", DisableAutostart, "autostart"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := mockRunner(t)
			_ = tt.fn("myvm")
			args := (*recs)[0].args
			if args[2] != tt.wantCmd {
				t.Errorf("virsh subcommand = %q, want %q", args[2], tt.wantCmd)
			}
			// VM name should be the last arg
			last := args[len(args)-1]
			if last != libvirtName("myvm") {
				t.Errorf("last arg = %q, want %q", last, libvirtName("myvm"))
			}
		})
	}
}

func TestWaitForSSH_ImmediateSuccess(t *testing.T) {
	recs := mockRunner(t)

	err := WaitForSSH("192.168.122.10", "/keys/id_ed25519", "claude", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*recs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*recs))
	}

	args := strings.Join((*recs)[0].args, " ")
	wantParts := []string{
		"-o ConnectTimeout=2",
		"-o StrictHostKeyChecking=no",
		"-o UserKnownHostsFile=/dev/null",
		"-o BatchMode=yes",
		"-i /keys/id_ed25519",
		"claude@192.168.122.10",
		"true",
	}
	for _, want := range wantParts {
		if !strings.Contains(args, want) {
			t.Errorf("SSH args missing %q\ngot: %s", want, args)
		}
	}
}

func TestWaitForSSH_Timeout(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", fmt.Errorf("connection refused")
	})

	err := WaitForSSH("192.168.122.10", "/keys/id_ed25519", "claude", 1*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "SSH not ready") {
		t.Errorf("error = %q, want mention of 'SSH not ready'", err.Error())
	}
}

func TestWaitForSSH_SucceedsOnRetry(t *testing.T) {
	callCount := 0
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		callCount++
		if callCount < 3 {
			return "", "", fmt.Errorf("connection refused")
		}
		return "", "", nil
	})

	err := WaitForSSH("192.168.122.10", "/keys/id_ed25519", "claude", 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount < 3 {
		t.Errorf("expected at least 3 attempts, got %d", callCount)
	}
}

func TestLifecycleActions_ErrorPropagation(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) error
	}{
		{"Start", Start},
		{"Stop", Stop},
		{"ForceStop", ForceStop},
		{"Undefine", Undefine},
		{"DisableAutostart", DisableAutostart},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
				return "", "", fmt.Errorf("virsh error")
			})
			if err := tt.fn("test"); err == nil {
				t.Fatalf("%s should propagate error", tt.name)
			}
		})
	}
}

func TestApplyTuning_BlkiotuneFailure(t *testing.T) {
	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		return "", "", fmt.Errorf("blkiotune error")
	})

	err := ApplyTuning("test", 200, 5000, 4096)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "blkiotune") {
		t.Errorf("error = %q, want mention of 'blkiotune'", err.Error())
	}
}

func TestApplyTuning_DomiflistFailure(t *testing.T) {
	callCount := 0
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		callCount++
		if callCount == 2 { // domiflist
			return "", "", fmt.Errorf("domiflist error")
		}
		return "", "", nil
	})

	err := ApplyTuning("test", 200, 5000, 4096)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "domiflist") {
		t.Errorf("error = %q, want mention of 'domiflist'", err.Error())
	}
}

func TestApplyTuning_MemtuneFailure(t *testing.T) {
	callCount := 0
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		callCount++
		if callCount == 4 { // memtune (after blkiotune, domiflist, domiftune)
			return "", "", fmt.Errorf("memtune error")
		}
		if callCount == 2 { // domiflist
			return " Interface  Type       Source     Model       MAC\n" +
				"-------------------------------------------------------\n" +
				" vnet0      network    default    virtio      52:54:00:ab:cd:ef\n", "", nil
		}
		return "", "", nil
	})

	err := ApplyTuning("test", 200, 5000, 4096)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "memtune") {
		t.Errorf("error = %q, want mention of 'memtune'", err.Error())
	}
}

func TestListAll_EmptyResult(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", nil
	})

	vms, err := ListAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vms) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(vms))
	}
}

func TestListAll_NoMotokoPrefixVMs(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "other-vm\nanother-vm\n", "", nil
	})

	vms, err := ListAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vms) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(vms))
	}
}

func TestListAll_VirshError(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", fmt.Errorf("libvirtd not running")
	})

	_, err := ListAll()
	if err == nil {
		t.Fatal("expected error from ListAll")
	}
}

func TestParseIPFromDomifaddr_FalsePositiveResistance(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "separator with slashes",
			input: "---/---/---\n",
			want:  "",
		},
		{
			name:  "path-like field",
			input: " Name  MAC  Proto  /dev/sda1\n",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseIPFromDomifaddr(tt.input); got != tt.want {
				t.Errorf("parseIPFromDomifaddr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetIP_ErrorPropagation(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", fmt.Errorf("domain not found")
	})

	ip, err := GetIP("missing")
	if err == nil {
		t.Fatal("expected error from GetIP")
	}
	if ip != "" {
		t.Errorf("ip = %q, want empty on error", ip)
	}
}
