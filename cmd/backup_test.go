package cmd

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/state"
	"github.com/antoniocascais/motoko/pkg/vm"
)

// ---------- test fixtures ----------

// standardDevs returns the list of block devices a healthy motoko VM
// reports: a vda root overlay, a vdb data disk, and a vdc cdrom ISO. The
// paths are derived from the same images/cloud-init dirs the fixture set up.
// Tests pass a name (matching the fixture) to pin the device list to the
// specific instance.
func standardDevs(cloudinitDir, imagesDir, name string) []vm.BlockDevice {
	dataDisk := filepath.Join(imagesDir, "motoko-"+name+"-data.qcow2")
	iso := filepath.Join(cloudinitDir, "motoko-"+name+"-cloud-init.iso")
	return []vm.BlockDevice{
		{Target: "vda", Source: filepath.Join(imagesDir, "motoko-"+name+"-overlay.qcow2")},
		{Target: "vdb", Source: dataDisk},
		{Target: "vdc", Source: iso},
	}
}

// setupBackupFixture creates a temp dir pre-populated with: a config.yml
// pointing at images/ and cloud-init/ subdirs, a state.json for the instance,
// and a real ed25519 keypair. Returns the config dir, instance name, and a
// helper to write additional files (qcow2, ISO).
func setupBackupFixture(t *testing.T, name string) (configDir, imagesDir, cloudinitDir string) {
	t.Helper()
	dir := t.TempDir()
	imagesDir = filepath.Join(dir, "images")
	cloudinitDir = filepath.Join(dir, "cloud-init")
	for _, d := range []string{imagesDir, cloudinitDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	cfgContent := `images_dir: ` + imagesDir + `
cloudinit_dir: ` + cloudinitDir + `
vm_user: claude
base_image:
  url: https://example.invalid/debian.qcow2
  filename: debian-12-generic-amd64.qcow2
golden_image:
  name: golden.qcow2
  root_disk_gb: 8
network:
  libvirt_network: default
  bridge_ip: 192.168.122.1
  subnet: 192.168.122.0/24
proxy:
  port: 3128
  allowed_domains: ["\\.anthropic\\.com$"]
vm_defaults:
  vcpus: 2
  ram_mb: 2048
  root_disk_gb: 8
  data_disk_gb: 10
`
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(cfgContent), 0600); err != nil {
		t.Fatal(err)
	}
	cfgFile = filepath.Join(dir, "config.yml")
	t.Cleanup(func() { cfgFile = "" })

	// Write a real keypair at <configDir>/keys/<name>/id_ed25519[.pub] so
	// copyInstanceKeys can read it. We use GenerateInstanceKey for
	// realism (a real ed25519 PEM-encoded key).
	if _, _, err := cloudinit.GenerateInstanceKey(dir, name); err != nil {
		t.Fatal(err)
	}

	// Write a state.json so state.Load succeeds.
	dataName := "motoko-" + name + "-data.qcow2"
	isoPath := filepath.Join(cloudinitDir, "motoko-"+name+"-cloud-init.iso")
	if err := state.Save(dir, name, &state.InstanceState{
		Name:         name,
		CreatedAt:    time.Now(),
		SSHKeyPath:   filepath.Join(dir, "keys", name, "id_ed25519"),
		OverlayName:  "motoko-" + name + "-overlay.qcow2",
		DataDiskName: dataName,
		CloudInitISO: isoPath,
	}); err != nil {
		t.Fatal(err)
	}

	return dir, imagesDir, cloudinitDir
}

// stubBackup replaces the backup command's package-level seams with fakes.
// Returns a pointer to a struct recording what was called.
type backupSeamCalls struct {
	State, DumpXML, GetIP int
	BlockDevices          int
	CopyDataDisk          []string // src, dst pairs
	CheckQCOW2            []string // paths checked
	SSHTar                int
}

// stubBackupWith installs fakes. The state/dumpxml/getIP funcs return their
// respective canned values; blockDevices returns the supplied list;
// copyDataDisk and checkQCOW2 both succeed (their args are recorded).
func stubBackupWith(t *testing.T, name string, vmState string, xml string, devs []vm.BlockDevice, ip string) *backupSeamCalls {
	t.Helper()
	calls := &backupSeamCalls{}
	origState, origDump, origDevs, origCopy, origCheck, origGetIP, origTar := backupVMStateFn, backupDumpXMLFn, backupBlockDevicesFn, backupCopyDataDiskFn, backupCheckQCOW2Fn, backupGetIPFn, backupRunSSHTar

	backupVMStateFn = func(string) (string, error) {
		calls.State++
		return vmState, nil
	}
	backupDumpXMLFn = func(string) (string, error) {
		calls.DumpXML++
		return xml, nil
	}
	backupBlockDevicesFn = func(string) ([]vm.BlockDevice, error) {
		calls.BlockDevices++
		return devs, nil
	}
	backupCopyDataDiskFn = func(src, dst string) error {
		calls.CopyDataDisk = append(calls.CopyDataDisk, src, dst)
		// Write a real (empty) qcow2 file so qemu-img check has something
		// to verify, and validateGzipFile path is exercised separately.
		if err := os.WriteFile(dst, []byte("not-a-real-qcow2-but-fine-for-test"), 0600); err != nil {
			return err
		}
		return nil
	}
	backupCheckQCOW2Fn = func(path string) error {
		calls.CheckQCOW2 = append(calls.CheckQCOW2, path)
		return nil
	}
	backupGetIPFn = func(string) (string, error) {
		calls.GetIP++
		return ip, nil
	}
	// Default SSH tar returns a valid gzipped tarball of one file so the
	// validation path in pullDataTarball succeeds.
	backupRunSSHTar = func(sshArgv []string) (io.ReadCloser, error) {
		calls.SSHTar++
		return newFakeTarGZ("hello world\n"), nil
	}
	t.Cleanup(func() {
		backupVMStateFn = origState
		backupDumpXMLFn = origDump
		backupBlockDevicesFn = origDevs
		backupCopyDataDiskFn = origCopy
		backupCheckQCOW2Fn = origCheck
		backupGetIPFn = origGetIP
		backupRunSSHTar = origTar
	})
	return calls
}

// newFakeTarGZ builds a minimal valid gzipped stream. validateGzipFile only
// checks gzip validity + non-empty, so a degenerate gzip envelope with the
// supplied bytes is sufficient — the operator's restore path is what needs a
// real tar; that's out of scope for this command's tests.
func newFakeTarGZ(content string) io.ReadCloser {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(content)); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return io.NopCloser(&buf)
}

// resetBackupFlags clears --output and --yes between tests so a flag set by
// one test does not bleed into the next. RunE reads the package-level vars
// directly (not through cobra's flag set), so callers must set them via
// setBackupFlags rather than passing "--output X" in args.
func resetBackupFlags(t *testing.T) {
	t.Helper()
	origOutput, origYes := backupOutputFlag, backupYesFlag
	backupOutputFlag = ""
	backupYesFlag = false
	t.Cleanup(func() {
		backupOutputFlag = origOutput
		backupYesFlag = origYes
	})
}

// setBackupFlags sets the backup command's output and yes flags, registering
// cleanup to restore the original values. The instance name is passed via
// args (cobra's positional Args are still enforced by RunE).
func setBackupFlags(t *testing.T, output string, yes bool) {
	t.Helper()
	origOutput, origYes := backupOutputFlag, backupYesFlag
	backupOutputFlag = output
	backupYesFlag = yes
	t.Cleanup(func() {
		backupOutputFlag = origOutput
		backupYesFlag = origYes
	})
}

// runBackup invokes backupCmd.RunE. Flags must be set via setBackupFlags
// BEFORE calling runBackup; only the instance name is passed positionally.
// The function does NOT reset flags — that would wipe the values the test
// just set. resetBackupFlags is provided for tests that do not use
// setBackupFlags (it clears and registers restore-on-cleanup).
func runBackup(t *testing.T, name string) error {
	t.Helper()
	return backupCmd.RunE(backupCmd, []string{name})
}

// ---------- name validation ----------

func TestBackup_RejectsInvalidName(t *testing.T) {
	tests := []string{"../etc", "foo/bar", "BAD", "-bad", ""}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			// A bad name must short-circuit BEFORE any state/config loading —
			// so the seams should not be touched.
			dir, _, _ := setupBackupFixture(t, "valid")
			_ = dir
			calls := &backupSeamCalls{}
			origState := backupVMStateFn
			backupVMStateFn = func(string) (string, error) {
				calls.State++
				return "shut off", nil
			}
			t.Cleanup(func() { backupVMStateFn = origState })
			// runBackup doesn't touch the --output/--yes flags itself, so
			// reset them here to avoid a previous test's flag leaking in.
			resetBackupFlags(t)
			if err := runBackup(t, name); err == nil {
				t.Errorf("expected error for name=%q", name)
			}
			if calls.State != 0 {
				t.Errorf("vm.State should not be called for invalid name, got %d calls", calls.State)
			}
		})
	}
}

// ---------- shut-off instance: no tar, no warn ----------

func TestBackup_ShutOff_NoTarball(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "test")
	// The data disk must actually exist on disk so copyInstanceKeys can
	// find it — and the domblklist stub must match its path.
	dataName := "motoko-test-data.qcow2"
	dataPath := filepath.Join(imagesDir, dataName)
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	// ISO must exist for the copy step.
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-test-cloud-init.iso"), []byte("iso-bytes"), 0600); err != nil {
		t.Fatal(err)
	}

	devs := standardDevs(cloudinitDir, imagesDir, "test")
	calls := stubBackupWith(t, "test", "shut off", "<domain/>", devs, "")

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, false)
	if err := runBackup(t, "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Shut-off: no IP lookup, no tar call.
	if calls.GetIP != 0 {
		t.Errorf("GetIP should not be called for shut-off instance, got %d", calls.GetIP)
	}
	if calls.SSHTar != 0 {
		t.Errorf("ssh tar should not run for shut-off instance, got %d", calls.SSHTar)
	}
	// data.tar.gz must NOT exist.
	if _, err := os.Stat(filepath.Join(outDir, "data.tar.gz")); !os.IsNotExist(err) {
		t.Errorf("data.tar.gz should not be written for shut-off backup, got err=%v", err)
	}
	// Other files should exist.
	for _, fname := range []string{"test.xml", "data-disk.qcow2", "motoko-test-cloud-init.iso", "README.txt", "keys/id_ed25519", "keys/id_ed25519.pub"} {
		if _, err := os.Stat(filepath.Join(outDir, fname)); err != nil {
			t.Errorf("expected %s in backup, got: %v", fname, err)
		}
	}
	_ = dir
}

// ---------- running instance: tar IS pulled, IP is resolved ----------

func TestBackup_Running_PullsTarball(t *testing.T) {
	_, imagesDir, cloudinitDir := setupBackupFixture(t, "test")
	dataPath := filepath.Join(imagesDir, "motoko-test-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-test-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "test")
	calls := stubBackupWith(t, "test", "running", "<domain/>", devs, "192.168.122.50")

	// Suppress the interactive confirmation by passing -y.
	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, true)
	if err := runBackup(t, "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.GetIP != 1 {
		t.Errorf("GetIP should be called exactly once, got %d", calls.GetIP)
	}
	if calls.SSHTar != 1 {
		t.Errorf("ssh tar should be called exactly once, got %d", calls.SSHTar)
	}
	if _, err := os.Stat(filepath.Join(outDir, "data.tar.gz")); err != nil {
		t.Errorf("data.tar.gz should exist for running backup: %v", err)
	}
}

// ---------- running instance: interactive prompt must reject without -y ----------

func TestBackup_Running_RequiresConfirmWithoutYes(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "test")
	_ = dir
	_ = imagesDir
	_ = cloudinitDir

	// State returns "running". Don't supply an input to stdin — confirm
	// must be answered no, aborting the backup before any I/O happens.
	withStdin(t, "n\n")
	calls := stubBackupWith(t, "test", "running", "<domain/>", nil, "192.168.122.50")

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, false)
	err := runBackup(t, "test")
	if err != nil {
		t.Fatalf("unexpected error (aborted should return nil): %v", err)
	}
	if calls.DumpXML != 0 {
		t.Errorf("DumpXML should not be called after abort, got %d", calls.DumpXML)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("output dir should not be created on abort, got err=%v", err)
	}
}

func TestBackup_Running_ConfirmYesProceeds(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "test")
	_ = dir
	dataPath := filepath.Join(imagesDir, "motoko-test-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-test-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "test")
	stubBackupWith(t, "test", "running", "<domain/>", devs, "192.168.122.50")
	withStdin(t, "y\n")

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, false)
	if err := runBackup(t, "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "data-disk.qcow2")); err != nil {
		t.Errorf("data-disk.qcow2 should exist after confirmation, got: %v", err)
	}
}

// ---------- ISO not attached ----------

func TestBackup_CloudInitISONotAttached_Fails(t *testing.T) {
	_, imagesDir, _ := setupBackupFixture(t, "iso")
	// Only the data disk in the dev list — no cdrom. (The image dir
	// entries are still created on disk so the data-disk copy step has
	// something to read, but we omit the ISO from domblklist to force
	// the failure path.)
	dataPath := filepath.Join(imagesDir, "motoko-iso-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := []vm.BlockDevice{{Target: "vdb", Source: dataPath}}
	stubBackupWith(t, "iso", "shut off", "<domain/>", devs, "")

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, false)
	err := runBackup(t, "iso")
	if err == nil {
		t.Fatal("expected error when ISO is not attached")
	}
	if !strings.Contains(err.Error(), "cloud-init ISO") {
		t.Errorf("error = %q, want mention of 'cloud-init ISO'", err.Error())
	}
	if !strings.Contains(err.Error(), "not attached") {
		t.Errorf("error = %q, want 'not attached'", err.Error())
	}
}

// ---------- data disk not attached ----------

func TestBackup_DataDiskNotAttached_Fails(t *testing.T) {
	_, imagesDir, cloudinitDir := setupBackupFixture(t, "test")
	_ = imagesDir
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-test-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	// domblklist returns only the overlay and ISO — no data disk.
	devs := []vm.BlockDevice{
		{Target: "vda", Source: "/some/overlay.qcow2"},
		{Target: "vdc", Source: "/some/cidata.iso"},
	}
	stubBackupWith(t, "test", "shut off", "<domain/>", devs, "")

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, false)
	err := runBackup(t, "test")
	if err == nil {
		t.Fatal("expected error when data disk is not attached")
	}
	if !strings.Contains(err.Error(), "not attached") {
		t.Errorf("error = %q, want 'not attached'", err.Error())
	}
}

// ---------- qemu-img check failure: remove the suspect copy and fail ----------

func TestBackup_QemuImgCheckFailure_RemovesCopy(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "test")
	_ = dir
	dataPath := filepath.Join(imagesDir, "motoko-test-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-test-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "test")

	// Override check to fail.
	calls := stubBackupWith(t, "test", "shut off", "<domain/>", devs, "")
	checkCalls := 0
	backupCheckQCOW2Fn = func(string) error {
		checkCalls++
		return errors.New("leaks detected")
	}

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, false)
	err := runBackup(t, "test")
	if err == nil {
		t.Fatal("expected error from failed qemu-img check")
	}
	if !strings.Contains(err.Error(), "qemu-img check") {
		t.Errorf("error = %q, want 'qemu-img check'", err.Error())
	}
	if checkCalls != 1 {
		t.Errorf("CheckQCOW2 should be called once, got %d", checkCalls)
	}
	_ = calls // calls.CheckQCOW2 was recorded by the prior stub before the override

	// The bad copy must have been removed.
	if _, err := os.Stat(filepath.Join(outDir, "data-disk.qcow2")); !os.IsNotExist(err) {
		t.Errorf("data-disk.qcow2 should be removed after failed check, got err=%v", err)
	}
}

// ---------- SSH tar failure: warning emitted, backup still completes ----------

func TestBackup_SSHTarFailure_DegradedButNotAborted(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "test")
	_ = dir
	dataPath := filepath.Join(imagesDir, "motoko-test-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-test-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "test")

	calls := stubBackupWith(t, "test", "running", "<domain/>", devs, "192.168.122.50")
	// Override the SSH tar seam to return an error.
	backupRunSSHTar = func([]string) (io.ReadCloser, error) {
		calls.SSHTar++
		return nil, errors.New("connection refused")
	}

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, true)
	stderr := captureStderr(t, func() {
		if err := runBackup(t, "test"); err != nil {
			t.Fatalf("backup should not abort on tar failure: %v", err)
		}
	})
	if !strings.Contains(stderr, "WARNING") {
		t.Errorf("expected WARNING in stderr, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "degraded") {
		t.Errorf("expected 'degraded' in stderr, got:\n%s", stderr)
	}
	// Disk copy is still on disk.
	if _, err := os.Stat(filepath.Join(outDir, "data-disk.qcow2")); err != nil {
		t.Errorf("data-disk.qcow2 should still exist after tar failure: %v", err)
	}
	// data.tar.gz should NOT exist (the tar step failed before writing).
	if _, err := os.Stat(filepath.Join(outDir, "data.tar.gz")); !os.IsNotExist(err) {
		t.Errorf("data.tar.gz should not exist after failed tar, got err=%v", err)
	}
}

// ---------- file modes ----------

func TestBackup_FilePermissions(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "perms")
	_ = dir
	dataPath := filepath.Join(imagesDir, "motoko-perms-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-perms-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "perms")
	stubBackupWith(t, "perms", "shut off", "<domain/>", devs, "")

	// Pin HOME so any code path that bypasses --output is still predictable.
	t.Setenv("HOME", t.TempDir())
	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, false)
	if err := runBackup(t, "perms"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The directory and every file must be locked down.
	dirInfo, err := os.Stat(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("backup dir perm = %o, want 0700", perm)
	}
	// keys/ is also a directory created with 0700.
	keysInfo, err := os.Stat(filepath.Join(outDir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := keysInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("keys dir perm = %o, want 0700", perm)
	}
	// Every leaf file 0600.
	for _, fname := range []string{"perms.xml", "data-disk.qcow2", "motoko-perms-cloud-init.iso", "README.txt", "keys/id_ed25519", "keys/id_ed25519.pub"} {
		info, err := os.Stat(filepath.Join(outDir, fname))
		if err != nil {
			t.Errorf("expected %s: %v", fname, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("%s perm = %o, want 0600", fname, perm)
		}
	}
}

// ---------- custom --output ----------

func TestBackup_CustomOutput(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "custom")
	_ = dir
	dataPath := filepath.Join(imagesDir, "motoko-custom-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-custom-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "custom")
	stubBackupWith(t, "custom", "shut off", "<domain/>", devs, "")

	custom := filepath.Join(t.TempDir(), "mybackup")
	setBackupFlags(t, custom, false)
	if err := runBackup(t, "custom"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(custom, "custom.xml")); err != nil {
		t.Errorf("expected custom.xml in --output dir: %v", err)
	}
}

// ---------- default output dir is under ~/motoko-backups/<name>-<ts> ----------

func TestBackup_DefaultOutput_UnderMotokoBackups(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "def")
	_ = dir
	dataPath := filepath.Join(imagesDir, "motoko-def-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-def-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "def")
	stubBackupWith(t, "def", "shut off", "<domain/>", devs, "")

	// Pin HOME to a temp dir so the default output is predictable.
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	// No --output flag: should land under $HOME/motoko-backups/def-YYYYMMDD-HHMM/
	resetBackupFlags(t)
	if err := runBackup(t, "def"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(homeDir, "motoko-backups"))
	if err != nil {
		t.Fatalf("expected ~/motoko-backups to exist: %v", err)
	}
	matched := 0
	for _, e := range entries {
		if name := e.Name(); strings.HasPrefix(name, "def-") {
			matched++
		}
	}
	if matched != 1 {
		t.Errorf("expected exactly one def- backup dir, found %d", matched)
	}
}

// ---------- writeSecretFile ----------

func TestWriteSecretFile_CreatesParentAndSetsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "subdir", "file.txt")
	if err := writeSecretFile(path, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want 'hello'", string(data))
	}
}

func TestWriteSecretFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeSecretFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Errorf("content = %q, want 'new'", string(data))
	}
}

// ---------- resolveBackupOutputDir ----------

func TestResolveBackupOutputDir_OverrideIsAbsolute(t *testing.T) {
	rel, err := filepath.Abs("somewhere")
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveBackupOutputDir(rel, "name", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got != rel {
		t.Errorf("got %q, want %q", got, rel)
	}
}

func TestResolveBackupOutputDir_RelativeOverrideMadeAbsolute(t *testing.T) {
	got, err := resolveBackupOutputDir("./mybackups", "name", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("got %q, want absolute path", got)
	}
	if !strings.HasSuffix(got, "mybackups") {
		t.Errorf("got %q, want suffix 'mybackups'", got)
	}
}

func TestResolveBackupOutputDir_DefaultFormat(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	ts := time.Date(2026, 6, 21, 14, 30, 0, 0, time.Local)
	got, err := resolveBackupOutputDir("", "alpha", ts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(homeDir, "motoko-backups", "alpha-20260621-1430")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBackupOutputDir_EmptyName(t *testing.T) {
	if _, err := resolveBackupOutputDir("", "", time.Now()); err == nil {
		t.Error("expected error for empty name")
	}
}

// ---------- findBlockDevice ----------

func TestFindBlockDevice_ExactMatch(t *testing.T) {
	devs := []vm.BlockDevice{
		{Target: "vda", Source: "/a"},
		{Target: "vdb", Source: "/b"},
		{Target: "vdc", Source: "/c"},
	}
	d, ok := findBlockDevice(devs, "/b")
	if !ok {
		t.Fatal("expected match for /b")
	}
	if d.Target != "vdb" {
		t.Errorf("Target = %q, want 'vdb'", d.Target)
	}
}

func TestFindBlockDevice_Missing(t *testing.T) {
	devs := []vm.BlockDevice{{Target: "vda", Source: "/a"}}
	if _, ok := findBlockDevice(devs, "/b"); ok {
		t.Error("expected no match")
	}
}

func TestIsCDROM(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/foo/cidata.iso", true},
		{"/foo/LOUD.ISO", true},
		{"/foo/data.qcow2", false},
		{"/foo/overlay", false},
	}
	for _, tt := range tests {
		if got := isCDROM(vm.BlockDevice{Source: tt.path}); got != tt.want {
			t.Errorf("isCDROM(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// ---------- copyInstanceKeys ----------

func TestCopyInstanceKeys_CopiesBoth(t *testing.T) {
	dir, _, _ := setupBackupFixture(t, "ck")
	outDir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := copyInstanceKeys(dir, "ck", outDir); err != nil {
		t.Fatal(err)
	}
	priv, err := os.ReadFile(filepath.Join(outDir, "keys", "id_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(filepath.Join(outDir, "keys", "id_ed25519.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if len(priv) == 0 {
		t.Error("private key file is empty")
	}
	if len(pub) == 0 {
		t.Error("public key file is empty")
	}
	// Perms: keys/ is 0700, files are 0600.
	keysInfo, _ := os.Stat(filepath.Join(outDir, "keys"))
	if perm := keysInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("keys dir perm = %o, want 0700", perm)
	}
	for _, fname := range []string{"id_ed25519", "id_ed25519.pub"} {
		info, _ := os.Stat(filepath.Join(outDir, "keys", fname))
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("keys/%s perm = %o, want 0600", fname, perm)
		}
	}
}

func TestCopyInstanceKeys_MissingSourceFails(t *testing.T) {
	dir, _, _ := setupBackupFixture(t, "ck")
	outDir := filepath.Join(t.TempDir(), "out")
	if err := copyInstanceKeys(dir, "nonexistent-instance", outDir); err == nil {
		t.Fatal("expected error for missing source keys")
	}
}

// ---------- validateGzipFile ----------

func TestValidateGzipFile_AcceptsValidGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("hello"))
	_ = gz.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateGzipFile(path); err != nil {
		t.Errorf("valid gzip rejected: %v", err)
	}
}

func TestValidateGzipFile_RejectsNonGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.gz")
	if err := os.WriteFile(path, []byte("not gzip"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateGzipFile(path); err == nil {
		t.Error("expected error for non-gzip content")
	}
}

func TestValidateGzipFile_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.gz")
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateGzipFile(path); err == nil {
		t.Error("expected error for empty file")
	}
}

// ---------- README content ----------

func TestBackup_WritesRestoreReadme(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "readme")
	_ = dir
	dataPath := filepath.Join(imagesDir, "motoko-readme-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-readme-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "readme")
	stubBackupWith(t, "readme", "shut off", "<domain/>", devs, "")

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, false)
	if err := runBackup(t, "readme"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(outDir, "README.txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	// Restore instructions should mention both file-level and block-level paths.
	for _, want := range []string{"Restore (file-level)", "Restore (block-level)", "data-disk.qcow2", "keys/", "motoko ssh"} {
		if !strings.Contains(text, want) {
			t.Errorf("README missing %q", want)
		}
	}
}

func TestBackup_RunningReadmeMentionsTarball(t *testing.T) {
	dir, imagesDir, cloudinitDir := setupBackupFixture(t, "rm2")
	_ = dir
	dataPath := filepath.Join(imagesDir, "motoko-rm2-data.qcow2")
	if err := os.WriteFile(dataPath, []byte("fake qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudinitDir, "motoko-rm2-cloud-init.iso"), []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	devs := standardDevs(cloudinitDir, imagesDir, "rm2")
	stubBackupWith(t, "rm2", "running", "<domain/>", devs, "192.168.122.10")

	outDir := filepath.Join(t.TempDir(), "out")
	setBackupFlags(t, outDir, true)
	if err := runBackup(t, "rm2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(outDir, "README.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "data.tar.gz") {
		t.Errorf("running backup README should mention data.tar.gz")
	}
	if !strings.Contains(string(content), "crash-consistent") {
		t.Errorf("running backup README should warn about non-crash-consistent disk copy")
	}
}

// ---------- cmdReader (process-handle wrapper) ----------

func TestCmdReader_CloseKillsProcess(t *testing.T) {
	// Spawn `sleep 60` to give us a real long-running child whose pipe we
	// can close and verify gets killed.
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not in PATH")
	}
	cmd := exec.Command("sleep", "60")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &cmdReader{Reader: stdout, cmd: cmd}
	// Close kills the process and returns the Wait error. Wait on a
	// SIGKILL'd process returns a *exec.ExitError (signal: killed) — the
	// caller is expected to ignore that, but the test asserts we got
	// SOME return value rather than panicking.
	_ = r.Close()
	// Calling Close twice must be safe (idempotent).
	if err := r.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
	if cmd.ProcessState == nil {
		t.Error("process state should be populated after Close")
	}
}

func TestCmdReader_CloseAfterProcessExited(t *testing.T) {
	// If the process has already exited, Close must not error or try to
	// kill a non-existent process.
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	cmd := exec.Command("true")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait for it to exit naturally.
	_ = cmd.Wait()
	r := &cmdReader{Reader: stdout, cmd: cmd}
	if err := r.Close(); err != nil {
		t.Errorf("Close on already-exited process returned error: %v", err)
	}
}

// ---------- command registration ----------

// Verify the command is registered on the root cobra command.
func TestBackup_CommandRegistered(t *testing.T) {
	if backupCmd == nil {
		t.Fatal("backupCmd is nil")
	}
	if backupCmd.Use != "backup <name>" {
		t.Errorf("Use = %q, want 'backup <name>'", backupCmd.Use)
	}
	// Look it up on the root command.
	found := false
	for _, c := range backupCmd.Parent().Commands() {
		if c.Name() == "backup" {
			found = true
			break
		}
	}
	if !found {
		t.Error("backup subcommand is not registered on rootCmd")
	}
}

// ---------- sshTarStatus: tar/ssh exit-code interpretation ----------

func TestSSHTarStatus(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}
	// Real *exec.ExitError values with known codes — one can't be constructed
	// directly, so run a shell that exits with each code.
	run := func(script string) error {
		return exec.Command("sh", "-c", script).Run()
	}

	if err := sshTarStatus(nil); err != nil {
		t.Errorf("sshTarStatus(nil) = %v, want nil", err)
	}
	// tar exits 1 when a file changed while being read — routine on a live VM,
	// archive still complete, so tolerated.
	if err := sshTarStatus(run("exit 1")); err != nil {
		t.Errorf("sshTarStatus(exit 1) = %v, want nil (benign)", err)
	}
	// Fatal tar (>=2) and ssh's own failure (255) must surface as errors.
	if err := sshTarStatus(run("exit 2")); err == nil {
		t.Error("sshTarStatus(exit 2) = nil, want error")
	}
	if err := sshTarStatus(run("exit 255")); err == nil {
		t.Error("sshTarStatus(exit 255) = nil, want error")
	}
	// A non-ExitError (e.g. ssh failed to start) is returned unchanged.
	sentinel := errors.New("ssh not found")
	if err := sshTarStatus(sentinel); !errors.Is(err, sentinel) {
		t.Errorf("sshTarStatus(non-exit err) = %v, want sentinel", err)
	}
}

// ---------- cmdReader: drained-to-EOF path returns the exit status ----------

func TestCmdReader_CloseAfterEOFReturnsExitStatus(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}
	cmd := exec.Command("sh", "-c", "echo hi")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &cmdReader{Reader: stdout, cmd: cmd}
	// Drain to EOF so Close reaps (not kills) and reports the real exit status.
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("draining stream: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close after clean EOF = %v, want nil (exit 0)", err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Error("expected a successful process state after EOF + Close")
	}
}
