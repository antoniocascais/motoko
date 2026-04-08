package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if len(capturedArgs) != 4 || capturedArgs[0] != "--connect" || capturedArgs[1] != "qemu:///system" || capturedArgs[2] != "net-info" || capturedArgs[3] != "default" {
		t.Errorf("args = %v, want [--connect qemu:///system net-info default]", capturedArgs)
	}
}

func writableFilter(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "allowed-domains")
	if err := os.WriteFile(path, []byte(""), 0664); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckConfigPaths_AllValid(t *testing.T) {
	dir := t.TempDir()
	imagesDir := filepath.Join(dir, "images")
	cloudinitDir := filepath.Join(dir, "cloudinit")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cloudinitDir, 0755); err != nil {
		t.Fatal(err)
	}
	filterFile := writableFilter(t)

	results := CheckConfigPaths(imagesDir, cloudinitDir, filterFile)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].Name != "images_dir" {
		t.Errorf("results[0].Name = %q, want images_dir", results[0].Name)
	}
	if results[1].Name != "cloudinit_dir" {
		t.Errorf("results[1].Name = %q, want cloudinit_dir", results[1].Name)
	}
	if results[2].Name != "proxy.filter_file" {
		t.Errorf("results[2].Name = %q, want proxy.filter_file", results[2].Name)
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("%s should pass, got: %s", r.Name, r.Detail)
		}
	}
}

func TestCheckConfigPaths_DirNotExist(t *testing.T) {
	filterFile := writableFilter(t)
	results := CheckConfigPaths("/nonexistent/images", "/nonexistent/cloudinit", filterFile)
	for _, r := range results[:2] {
		if r.Passed {
			t.Errorf("%s should fail for nonexistent dir", r.Name)
		}
		if !strings.Contains(r.Detail, "does not exist") {
			t.Errorf("%s detail = %q, want mention of 'does not exist'", r.Name, r.Detail)
		}
	}
}

func TestCheckConfigPaths_NotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("write permission test unreliable as root")
	}
	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(roDir, 0555); err != nil {
		t.Fatal(err)
	}
	filterFile := writableFilter(t)

	results := CheckConfigPaths(roDir, roDir, filterFile)
	for _, r := range results[:2] {
		if r.Passed {
			t.Errorf("%s should fail for non-writable dir", r.Name)
		}
		if !strings.Contains(r.Detail, "not writable") {
			t.Errorf("%s detail = %q, want mention of 'not writable'", r.Name, r.Detail)
		}
	}
}

func TestCheckConfigPaths_MixedState(t *testing.T) {
	dir := t.TempDir()
	goodDir := filepath.Join(dir, "good")
	if err := os.MkdirAll(goodDir, 0755); err != nil {
		t.Fatal(err)
	}
	filterFile := writableFilter(t)

	results := CheckConfigPaths(goodDir, "/nonexistent/cloudinit", filterFile)
	if !results[0].Passed {
		t.Errorf("images_dir should pass when dir exists and is writable")
	}
	if results[1].Passed {
		t.Errorf("cloudinit_dir should fail when dir doesn't exist")
	}
}

func TestCheckConfigPaths_NotADir(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}
	filterFile := writableFilter(t)

	results := CheckConfigPaths(filePath, filePath, filterFile)
	for _, r := range results[:2] {
		if r.Passed {
			t.Errorf("%s should fail when path is a file, not a directory", r.Name)
		}
		if !strings.Contains(r.Detail, "not a directory") {
			t.Errorf("%s detail = %q, want mention of 'not a directory'", r.Name, r.Detail)
		}
	}
}

func TestCheckFilterFile_Writable(t *testing.T) {
	path := writableFilter(t)
	r := checkFilterFileResult(path)
	if !r.Passed {
		t.Errorf("should pass for writable file, got: %s", r.Detail)
	}
}

func TestCheckFilterFile_NotExist(t *testing.T) {
	r := checkFilterFileResult("/nonexistent/allowed-domains")
	if r.Passed {
		t.Error("should fail for nonexistent file")
	}
	if !strings.Contains(r.Detail, "does not exist") {
		t.Errorf("detail = %q, want mention of 'does not exist'", r.Detail)
	}
}

func TestCheckFilterFile_IsDirectory(t *testing.T) {
	r := checkFilterFileResult(t.TempDir())
	if r.Passed {
		t.Error("should fail when path is a directory")
	}
	if !strings.Contains(r.Detail, "is a directory") {
		t.Errorf("detail = %q, want mention of 'is a directory'", r.Detail)
	}
}

func TestCheckFilterFile_NotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("write permission test unreliable as root")
	}
	path := filepath.Join(t.TempDir(), "readonly-filter")
	if err := os.WriteFile(path, []byte(""), 0444); err != nil {
		t.Fatal(err)
	}

	r := checkFilterFileResult(path)
	if r.Passed {
		t.Error("should fail for read-only file")
	}
	if !strings.Contains(r.Detail, "not writable") {
		t.Errorf("detail = %q, want mention of 'not writable'", r.Detail)
	}
	if !strings.Contains(r.Detail, "check group ownership") {
		t.Error("detail should include permissions guidance")
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
