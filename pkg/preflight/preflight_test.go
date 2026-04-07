package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckLinux(t *testing.T) {
	r := checkLinux()
	// We're running on Linux in the Docker toolchain
	if !r.Passed {
		t.Skipf("not running on Linux: %s", r.Detail)
	}
	if r.Name != "Linux OS" {
		t.Errorf("Name = %q, want 'Linux OS'", r.Name)
	}
}

func TestCheckKVM(t *testing.T) {
	r := checkKVM()
	if r.Name != "/dev/kvm" {
		t.Errorf("Name = %q, want '/dev/kvm'", r.Name)
	}
	// Result depends on host — just verify structure
	if !r.Passed && r.Detail == "" {
		t.Error("failed check should have detail")
	}
}

func TestCheckBinaries_AllMissing(t *testing.T) {
	orig := lookPath
	defer func() { lookPath = orig }()
	lookPath = func(file string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	results := checkBinaries()
	if len(results) != len(requiredBinaries) {
		t.Fatalf("expected %d results, got %d", len(requiredBinaries), len(results))
	}
	for _, r := range results {
		if r.Passed {
			t.Errorf("%s should fail when lookPath returns error", r.Name)
		}
		if r.Detail != "not found in PATH" {
			t.Errorf("%s detail = %q, want 'not found in PATH'", r.Name, r.Detail)
		}
	}
}

func TestCheckBinaries_AllPresent(t *testing.T) {
	orig := lookPath
	defer func() { lookPath = orig }()
	lookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}

	results := checkBinaries()
	for _, r := range results {
		if !r.Passed {
			t.Errorf("%s should pass when lookPath succeeds", r.Name)
		}
	}
}

func TestCheckBinaries_Partial(t *testing.T) {
	orig := lookPath
	defer func() { lookPath = orig }()
	lookPath = func(file string) (string, error) {
		if file == "virsh" || file == "qemu-img" {
			return "/usr/bin/" + file, nil
		}
		return "", fmt.Errorf("not found")
	}

	results := checkBinaries()
	passed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
	}
	if passed != 2 {
		t.Errorf("expected 2 passed, got %d", passed)
	}
}

func TestCheckDefaultNetwork_Active(t *testing.T) {
	orig := runCmd
	defer func() { runCmd = orig }()
	runCmd = func(name string, args ...string) (string, error) {
		return "Name:           default\nActive:         yes\nPersistent:     yes", nil
	}

	r := checkDefaultNetwork()
	if !r.Passed {
		t.Errorf("expected pass for active network, got: %s", r.Detail)
	}
}

func TestCheckDefaultNetwork_Inactive(t *testing.T) {
	orig := runCmd
	defer func() { runCmd = orig }()
	runCmd = func(name string, args ...string) (string, error) {
		return "Name:           default\nActive:         no", nil
	}

	r := checkDefaultNetwork()
	if r.Passed {
		t.Error("expected fail for inactive network")
	}
	if r.Detail != "network not active" {
		t.Errorf("detail = %q, want 'network not active'", r.Detail)
	}
}

func TestCheckDefaultNetwork_MissingActiveLabel(t *testing.T) {
	// Has "yes" but not "Active:" — should still fail
	orig := runCmd
	defer func() { runCmd = orig }()
	runCmd = func(name string, args ...string) (string, error) {
		return "Name:           default\nPersistent:     yes", nil
	}

	r := checkDefaultNetwork()
	if r.Passed {
		t.Error("expected fail when Active: label is missing")
	}
}

func TestCheckDefaultNetwork_HasActiveLabelButNotYes(t *testing.T) {
	// Has "Active:" but value is not "yes" — should fail
	orig := runCmd
	defer func() { runCmd = orig }()
	runCmd = func(name string, args ...string) (string, error) {
		return "Name:           default\nActive:         no\nPersistent:     no", nil
	}

	r := checkDefaultNetwork()
	if r.Passed {
		t.Error("expected fail when Active: is present but value is not yes")
	}
}

func TestCheckDefaultNetwork_VirshFails(t *testing.T) {
	orig := runCmd
	defer func() { runCmd = orig }()
	runCmd = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("virsh: command not found")
	}

	r := checkDefaultNetwork()
	if r.Passed {
		t.Error("expected fail when virsh fails")
	}
	if r.Detail != "virsh net-info default failed" {
		t.Errorf("detail = %q, want 'virsh net-info default failed'", r.Detail)
	}
}

func TestCheckDefaultNetwork_VerifiesCommand(t *testing.T) {
	orig := runCmd
	defer func() { runCmd = orig }()

	var capturedName string
	var capturedArgs []string
	runCmd = func(name string, args ...string) (string, error) {
		capturedName = name
		capturedArgs = args
		return "Active:         yes", nil
	}

	checkDefaultNetwork()
	if capturedName != "virsh" {
		t.Errorf("command = %q, want virsh", capturedName)
	}
	if len(capturedArgs) != 2 || capturedArgs[0] != "net-info" || capturedArgs[1] != "default" {
		t.Errorf("args = %v, want [net-info default]", capturedArgs)
	}
}

func TestRunAll_Structure(t *testing.T) {
	orig := runCmd
	origLP := lookPath
	defer func() { runCmd = orig; lookPath = origLP }()

	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	runCmd = func(name string, args ...string) (string, error) {
		return "Active:         yes", nil
	}

	results, _ := RunAll()
	// Should have: linux, kvm, 6 binaries, libvirt group, default network = 10
	expectedMin := 9 // kvm and libvirt group may vary
	if len(results) < expectedMin {
		t.Errorf("expected at least %d results, got %d", expectedMin, len(results))
	}
}

func TestRunAll_ReturnsFalseOnAnyFailure(t *testing.T) {
	orig := runCmd
	origLP := lookPath
	defer func() { runCmd = orig; lookPath = origLP }()

	// One binary missing — should make allPassed false
	lookPath = func(file string) (string, error) {
		if file == "guestfish" {
			return "", fmt.Errorf("not found")
		}
		return "/usr/bin/" + file, nil
	}
	runCmd = func(name string, args ...string) (string, error) {
		return "Active:         yes", nil
	}

	results, allPassed := RunAll()
	if allPassed {
		t.Error("allPassed should be false when any check fails")
	}
	// Verify the failed check exists in results
	found := false
	for _, r := range results {
		if r.Name == "guestfish" && !r.Passed {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected guestfish to appear as failed in results")
	}
}

func TestEnsureConfigDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "motoko")
	if err := EnsureConfigDir(dir); err != nil {
		t.Fatalf("EnsureConfigDir() error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("perm = %o, want 0700", perm)
	}
}

func TestEnsureConfigDir_Idempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "motoko")
	if err := EnsureConfigDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := EnsureConfigDir(dir); err != nil {
		t.Fatalf("second call should not error: %v", err)
	}
}

func TestWriteDefaultConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := WriteDefaultConfig(path); err != nil {
		t.Fatalf("WriteDefaultConfig() error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("config file should not be empty")
	}
}

func TestWriteDefaultConfig_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	existing := []byte("custom config")
	if err := os.WriteFile(path, existing, 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefaultConfig(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom config" {
		t.Error("existing config should not be overwritten")
	}
}

func TestWriteDefaultConfig_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := WriteDefaultConfig(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
}
