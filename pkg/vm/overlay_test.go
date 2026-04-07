package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateOverlay_AbsoluteBackingPath(t *testing.T) {
	recs := mockRunner(t)
	imagesDir := "/var/lib/libvirt/images"

	if err := CreateOverlay(imagesDir, "golden.qcow2", "overlay.qcow2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*recs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*recs))
	}
	// -b arg is at index 4
	backingPath := (*recs)[0].args[4]
	if !filepath.IsAbs(backingPath) {
		t.Errorf("backing path %q is not absolute", backingPath)
	}
}

func TestCreateOverlay_ExactArgs(t *testing.T) {
	recs := mockRunner(t)

	if err := CreateOverlay("/images", "golden.qcow2", "overlay.qcow2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := (*recs)[0]
	if rec.name != "qemu-img" {
		t.Errorf("command = %q, want %q", rec.name, "qemu-img")
	}
	wantArgs := []string{"create", "-f", "qcow2", "-b", "/images/golden.qcow2", "-F", "qcow2", "/images/overlay.qcow2"}
	if got := strings.Join(rec.args, " "); got != strings.Join(wantArgs, " ") {
		t.Errorf("args = %q, want %q", got, strings.Join(wantArgs, " "))
	}
}

func TestCreateDataDisk_ExactArgs(t *testing.T) {
	recs := mockRunner(t)

	if err := CreateDataDisk("/images", "data.qcow2", 10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := (*recs)[0]
	if rec.name != "qemu-img" {
		t.Errorf("command = %q, want %q", rec.name, "qemu-img")
	}
	wantArgs := []string{"create", "-f", "qcow2", "/images/data.qcow2", "10G"}
	if got := strings.Join(rec.args, " "); got != strings.Join(wantArgs, " ") {
		t.Errorf("args = %q, want %q", got, strings.Join(wantArgs, " "))
	}
}

func TestCreateDataDisk_SizeFormatting(t *testing.T) {
	tests := []struct {
		sizeGB   int
		wantLast string
	}{
		{1, "1G"},
		{10, "10G"},
		{100, "100G"},
	}
	for _, tt := range tests {
		t.Run(tt.wantLast, func(t *testing.T) {
			recs := mockRunner(t)

			if err := CreateDataDisk("/images", "data.qcow2", tt.sizeGB); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			args := (*recs)[0].args
			got := args[len(args)-1]
			if got != tt.wantLast {
				t.Errorf("size arg = %q, want %q", got, tt.wantLast)
			}
		})
	}
}

func TestResetOverlay_DeletesThenCreates(t *testing.T) {
	tmpDir := t.TempDir()
	overlayPath := filepath.Join(tmpDir, "overlay.qcow2")

	// Create a dummy overlay file
	if err := os.WriteFile(overlayPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	recs := mockRunner(t)

	if err := ResetOverlay(tmpDir, "golden.qcow2", "overlay.qcow2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Old file should be removed
	if _, err := os.Stat(overlayPath); err == nil {
		t.Error("overlay file should have been deleted before qemu-img create")
	}

	// qemu-img create should have been called
	if len(*recs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*recs))
	}
	if (*recs)[0].name != "qemu-img" {
		t.Errorf("command = %q, want %q", (*recs)[0].name, "qemu-img")
	}
}

func TestResetOverlay_MissingOverlayNotError(t *testing.T) {
	recs := mockRunner(t)

	// Overlay doesn't exist — should not error
	if err := ResetOverlay("/images", "golden.qcow2", "nonexistent.qcow2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*recs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*recs))
	}
}

func TestCreateOverlay_RelativePathRejected(t *testing.T) {
	mockRunner(t)

	err := CreateOverlay("relative/path", "golden.qcow2", "overlay.qcow2")
	if err == nil {
		t.Fatal("expected error for relative imagesDir")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %q, want mention of 'absolute'", err.Error())
	}
}

func TestCreateOverlay_RunCmdError(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "qemu-img error", fmt.Errorf("qemu-img failed")
	})

	err := CreateOverlay("/images", "golden.qcow2", "overlay.qcow2")
	if err == nil {
		t.Fatal("expected error from runCmd")
	}
}

func TestCreateDataDisk_RunCmdError(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", fmt.Errorf("qemu-img failed")
	})

	err := CreateDataDisk("/images", "data.qcow2", 10)
	if err == nil {
		t.Fatal("expected error from runCmd")
	}
}
