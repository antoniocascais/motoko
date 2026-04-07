package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testState() *InstanceState {
	return &InstanceState{
		Name:             "test-vm",
		CreatedAt:        time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
		TelegramTokenEnv: "TEST_TOKEN",
		PersonaPath:      "/tmp/persona.md",
		SSHKeyPath:       "/home/user/.config/motoko/keys/test-vm/id_ed25519",
		OverlayName:      "motoko-test-vm-overlay.qcow2",
		DataDiskName:     "motoko-test-vm-data.qcow2",
		CloudInitISO:     "/var/lib/libvirt/cloud-init/motoko-test-vm-cloud-init.iso",
	}
}

func TestSave_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "myvm", testState()); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "instances", "myvm", "state.json")); err != nil {
		t.Fatalf("state.json not created: %v", err)
	}
}

func TestSave_DirPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "myvm", testState()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "instances", "myvm"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("dir perm = %o, want 0700", perm)
	}
}

func TestSave_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "myvm", testState()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "instances", "myvm", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}
}

func TestLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	orig := testState()
	if err := Save(dir, "myvm", orig); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, "myvm")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.Name != orig.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, orig.Name)
	}
	if !loaded.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", loaded.CreatedAt, orig.CreatedAt)
	}
	if loaded.TelegramTokenEnv != orig.TelegramTokenEnv {
		t.Errorf("TelegramTokenEnv = %q, want %q", loaded.TelegramTokenEnv, orig.TelegramTokenEnv)
	}
	if loaded.PersonaPath != orig.PersonaPath {
		t.Errorf("PersonaPath = %q, want %q", loaded.PersonaPath, orig.PersonaPath)
	}
	if loaded.SSHKeyPath != orig.SSHKeyPath {
		t.Errorf("SSHKeyPath = %q, want %q", loaded.SSHKeyPath, orig.SSHKeyPath)
	}
	if loaded.OverlayName != orig.OverlayName {
		t.Errorf("OverlayName = %q, want %q", loaded.OverlayName, orig.OverlayName)
	}
	if loaded.DataDiskName != orig.DataDiskName {
		t.Errorf("DataDiskName = %q, want %q", loaded.DataDiskName, orig.DataDiskName)
	}
	if loaded.CloudInitISO != orig.CloudInitISO {
		t.Errorf("CloudInitISO = %q, want %q", loaded.CloudInitISO, orig.CloudInitISO)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing state")
	}
	if !strings.Contains(err.Error(), "reading instance state") {
		t.Errorf("error = %q, want 'reading instance state' prefix", err.Error())
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	sd := filepath.Join(dir, "instances", "broken")
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "state.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir, "broken")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parsing instance state") {
		t.Errorf("error = %q, want 'parsing instance state' prefix", err.Error())
	}
}

func TestDelete_RemovesDir(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "myvm", testState()); err != nil {
		t.Fatal(err)
	}
	if err := Delete(dir, "myvm"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "instances", "myvm")); !os.IsNotExist(err) {
		t.Error("instance dir should be removed after Delete")
	}
}

func TestDelete_NonexistentOK(t *testing.T) {
	dir := t.TempDir()
	if err := Delete(dir, "nonexistent"); err != nil {
		t.Fatalf("Delete() should not error for missing dir: %v", err)
	}
}

func TestList_Empty(t *testing.T) {
	dir := t.TempDir()
	names, err := List(dir)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected empty list, got %v", names)
	}
}

func TestList_MultipleInstances(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if err := Save(dir, name, testState()); err != nil {
			t.Fatal(err)
		}
	}
	names, err := List(dir)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
	if names[0] != "alpha" || names[1] != "bravo" || names[2] != "charlie" {
		t.Errorf("expected sorted [alpha bravo charlie], got %v", names)
	}
}

func TestList_IgnoresNonDirs(t *testing.T) {
	dir := t.TempDir()
	instancesDir := filepath.Join(dir, "instances")
	if err := os.MkdirAll(instancesDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instancesDir, "stray-file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	names, err := List(dir)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected empty list, got %v", names)
	}
}

func TestList_IgnoresDirsWithoutState(t *testing.T) {
	dir := t.TempDir()
	instancesDir := filepath.Join(dir, "instances")
	// dir with state.json
	if err := Save(dir, "real", testState()); err != nil {
		t.Fatal(err)
	}
	// dir without state.json
	if err := os.MkdirAll(filepath.Join(instancesDir, "orphan"), 0700); err != nil {
		t.Fatal(err)
	}
	names, err := List(dir)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(names) != 1 || names[0] != "real" {
		t.Errorf("expected [real], got %v", names)
	}
}

func TestSave_Overwrite(t *testing.T) {
	dir := t.TempDir()
	s := testState()
	if err := Save(dir, "myvm", s); err != nil {
		t.Fatal(err)
	}
	s.TelegramTokenEnv = "UPDATED_TOKEN"
	if err := Save(dir, "myvm", s); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, "myvm")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TelegramTokenEnv != "UPDATED_TOKEN" {
		t.Errorf("TelegramTokenEnv = %q, want UPDATED_TOKEN", loaded.TelegramTokenEnv)
	}
}

func TestLoad_OmitsEmptyOptionalFields(t *testing.T) {
	dir := t.TempDir()
	s := &InstanceState{
		Name:         "minimal",
		CreatedAt:    time.Now(),
		SSHKeyPath:   "/tmp/key",
		OverlayName:  "overlay.qcow2",
		DataDiskName: "data.qcow2",
		CloudInitISO: "/tmp/cloud-init.iso",
	}
	if err := Save(dir, "minimal", s); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, "minimal")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TelegramTokenEnv != "" {
		t.Errorf("TelegramTokenEnv should be empty, got %q", loaded.TelegramTokenEnv)
	}
	if loaded.PersonaPath != "" {
		t.Errorf("PersonaPath should be empty, got %q", loaded.PersonaPath)
	}
}

func TestStateDir_PathConstruction(t *testing.T) {
	got := stateDir("/base", "myvm")
	want := filepath.Join("/base", "instances", "myvm")
	if got != want {
		t.Errorf("stateDir = %q, want %q", got, want)
	}
}

func TestStatePath_PathConstruction(t *testing.T) {
	got := statePath("/base", "myvm")
	want := filepath.Join("/base", "instances", "myvm", "state.json")
	if got != want {
		t.Errorf("statePath = %q, want %q", got, want)
	}
}

func TestSave_ErrorMessage(t *testing.T) {
	// Write to an impossible path to trigger mkdir error
	_, err := Load("/nonexistent/deep/path", "vm")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "reading instance state") {
		t.Errorf("error = %q, want 'reading instance state' prefix", err.Error())
	}
}

func TestLoad_ExtraJSONFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	sd := filepath.Join(dir, "instances", "fwd")
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	// JSON with an extra field not in the struct
	data := `{"name":"fwd","ssh_key_path":"/tmp/key","overlay_name":"o.qcow2","data_disk_name":"d.qcow2","cloudinit_iso":"/tmp/ci.iso","future_field":"value"}`
	if err := os.WriteFile(filepath.Join(sd, "state.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, "fwd")
	if err != nil {
		t.Fatalf("extra JSON fields should be ignored: %v", err)
	}
	if loaded.Name != "fwd" {
		t.Errorf("Name = %q, want fwd", loaded.Name)
	}
}
