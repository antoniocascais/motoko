package vm

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoldenImageExists_True(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "golden.qcow2"), []byte("image"), 0644); err != nil {
		t.Fatal(err)
	}

	exists, err := GoldenImageExists(dir, "golden.qcow2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("GoldenImageExists() = false, want true")
	}
}

func TestGoldenImageExists_False(t *testing.T) {
	exists, err := GoldenImageExists(t.TempDir(), "nonexistent.qcow2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("GoldenImageExists() = true, want false")
	}
}

// goldenMockRunner creates a mock that writes the temp file when cp is called,
// so os.Rename succeeds in BuildGoldenImage.
func goldenMockRunner(t *testing.T) *[]execRecord {
	t.Helper()
	return mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		if name == "cp" && len(args) >= 2 {
			_ = os.WriteFile(args[len(args)-1], []byte("fake"), 0644)
		}
		return "", "", nil
	})
}

func TestBuildGoldenImage_CommandSequence(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	recs := goldenMockRunner(t)

	cfg := BuildGoldenConfig{
		ImagesDir:    dir,
		BaseURL:      "https://example.com/images/base.qcow2",
		BaseChecksum: checksum,
		BaseFilename: "base.qcow2",
		GoldenName:   "golden.qcow2",
		RootDiskGB:   8,
		Packages:     []string{"python3", "curl", "git"},
		VMUser:       "claude",
	}

	if err := BuildGoldenImage(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*recs) != 4 {
		names := make([]string, len(*recs))
		for i, r := range *recs {
			names[i] = r.name
		}
		t.Fatalf("expected 4 commands, got %d: %v", len(*recs), names)
	}

	// Verify command order
	wantCmds := []string{"cp", "qemu-img", "virt-customize", "guestfish"}
	for i, want := range wantCmds {
		if (*recs)[i].name != want {
			t.Errorf("command %d = %q, want %q", i, (*recs)[i].name, want)
		}
	}
}

func TestBuildGoldenImage_VirtCustomizeArgs(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	recs := goldenMockRunner(t)

	cfg := BuildGoldenConfig{
		ImagesDir:    dir,
		BaseURL:      "https://example.com/base.qcow2",
		BaseChecksum: checksum,
		BaseFilename: "base.qcow2",
		GoldenName:   "golden.qcow2",
		RootDiskGB:   8,
		Packages:     []string{"python3", "curl", "git"},
		VMUser:       "claude",
	}

	if err := BuildGoldenImage(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find virt-customize call
	var vcArgs []string
	for _, rec := range *recs {
		if rec.name == "virt-customize" {
			vcArgs = rec.args
			break
		}
	}
	if vcArgs == nil {
		t.Fatal("virt-customize not called")
	}

	args := strings.Join(vcArgs, " ")

	// Packages should include user-specified + nodejs + npm
	if !strings.Contains(args, "--install python3,curl,git,nodejs,npm") {
		t.Errorf("missing expected --install args in: %s", args)
	}

	// Should have useradd command
	if !strings.Contains(args, "useradd -m -s /bin/bash claude") {
		t.Errorf("missing useradd in: %s", args)
	}

	// Should have growpart
	if !strings.Contains(args, "growpart /dev/sda 1 && resize2fs /dev/sda1") {
		t.Errorf("missing growpart in: %s", args)
	}

	// Should have claude-code install
	if !strings.Contains(args, "npm install -g @anthropic-ai/claude-code") {
		t.Errorf("missing claude-code install in: %s", args)
	}
}

func TestBuildGoldenImage_ResizeArg(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	recs := goldenMockRunner(t)

	cfg := BuildGoldenConfig{
		ImagesDir:    dir,
		BaseURL:      "https://example.com/base.qcow2",
		BaseChecksum: checksum,
		BaseFilename: "base.qcow2",
		GoldenName:   "golden.qcow2",
		RootDiskGB:   16,
		Packages:     []string{"git"},
		VMUser:       "claude",
	}

	if err := BuildGoldenImage(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// qemu-img resize should use "16G"
	for _, rec := range *recs {
		if rec.name == "qemu-img" {
			last := rec.args[len(rec.args)-1]
			if last != "16G" {
				t.Errorf("qemu-img resize size = %q, want %q", last, "16G")
			}
			return
		}
	}
	t.Fatal("qemu-img not called")
}

func TestBuildGoldenImage_VMUserValidation(t *testing.T) {
	tests := []struct {
		name    string
		vmUser  string
		wantErr bool
	}{
		{"valid", "claude", false},
		{"underscore_prefix", "_svc", false},
		{"with_hyphen", "claude-user", false},
		{"shell_injection", "root; rm -rf /", true},
		{"empty", "", true},
		{"starts_with_number", "0user", true},
		{"uppercase", "Claude", true},
		{"spaces", "claude user", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			checksum := writeBaseImage(t, dir)
			goldenMockRunner(t)

			cfg := BuildGoldenConfig{
				ImagesDir:    dir,
				BaseURL:      "https://example.com/base.qcow2",
				BaseChecksum: checksum,
				BaseFilename: "base.qcow2",
				GoldenName:   "golden.qcow2",
				RootDiskGB:   8,
				Packages:     []string{"git"},
				VMUser:       tt.vmUser,
			}

			err := BuildGoldenImage(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("BuildGoldenImage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildGoldenImage_CleansUpOnError(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)

	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		if name == "virt-customize" {
			return "", "virt-customize error", fmt.Errorf("virt-customize failed")
		}
		return "", "", nil
	})

	cfg := BuildGoldenConfig{
		ImagesDir:    dir,
		BaseURL:      "https://example.com/base.qcow2",
		BaseChecksum: checksum,
		BaseFilename: "base.qcow2",
		GoldenName:   "golden.qcow2",
		RootDiskGB:   8,
		Packages:     []string{"git"},
		VMUser:       "claude",
	}

	err := BuildGoldenImage(cfg)
	if err == nil {
		t.Fatal("expected error")
	}

	// Temp file should be cleaned up (any file matching .golden-bake-*)
	entries, _ := filepath.Glob(filepath.Join(dir, ".golden-bake-*"))
	if len(entries) > 0 {
		t.Errorf("temp file not cleaned up: %v", entries)
	}
}

func TestFetchDebianChecksum_HTTPMock(t *testing.T) {
	sha512sums := "abc123def456  other-file.qcow2\n" +
		"deadbeef1234  debian-12-generic-amd64.qcow2\n" +
		"ffffff999999  another-file.img\n"

	mockHTTPClient(t, func(req *http.Request) *http.Response {
		if req.URL.Path != "/images/SHA512SUMS" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sha512sums))}
	})

	checksum, err := FetchDebianChecksum("http://example.com/images/debian-12-generic-amd64.qcow2", "debian-12-generic-amd64.qcow2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "sha512:deadbeef1234"
	if checksum != want {
		t.Errorf("checksum = %q, want %q", checksum, want)
	}
}

func TestFetchDebianChecksum_FileNotFound(t *testing.T) {
	sha512sums := "abc123def456  other-file.qcow2\n"

	mockHTTPClient(t, func(req *http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sha512sums))}
	})

	_, err := FetchDebianChecksum("http://example.com/images/missing.qcow2", "missing.qcow2")
	if err == nil {
		t.Fatal("expected error for missing filename")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want mention of 'not found'", err.Error())
	}
}

func TestFetchDebianChecksum_URLConstruction(t *testing.T) {
	var requestedPath string
	mockHTTPClient(t, func(req *http.Request) *http.Response {
		requestedPath = req.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("aaa111  file.qcow2\n"))}
	})

	_, _ = FetchDebianChecksum("http://example.com/cloud/bookworm/latest/file.qcow2", "file.qcow2")

	if requestedPath != "/cloud/bookworm/latest/SHA512SUMS" {
		t.Errorf("requested path = %q, want %q", requestedPath, "/cloud/bookworm/latest/SHA512SUMS")
	}
}

func TestVerifyChecksum_SHA512Match(t *testing.T) {
	dir := t.TempDir()
	content := []byte("test content for checksum")
	path := filepath.Join(dir, "testfile")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	h := sha512.Sum512(content)
	checksum := "sha512:" + hex.EncodeToString(h[:])

	if err := verifyChecksum(path, checksum); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyChecksum_SHA512Mismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	err := verifyChecksum(path, "sha512:0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %q, want mention of 'mismatch'", err.Error())
	}
}

func TestVerifyChecksum_InvalidFormat(t *testing.T) {
	err := verifyChecksum("/dev/null", "nocolon")
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestVerifyChecksum_UnsupportedAlgorithm(t *testing.T) {
	err := verifyChecksum("/dev/null", "md5:abc123")
	if err == nil {
		t.Fatal("expected error for unsupported algorithm")
	}
}

func TestVerifyChecksum_SHA256Match(t *testing.T) {
	dir := t.TempDir()
	content := []byte("sha256 test content")
	path := filepath.Join(dir, "testfile")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(content)
	checksum := "sha256:" + hex.EncodeToString(h[:])

	if err := verifyChecksum(path, checksum); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyChecksum_SHA256Mismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	err := verifyChecksum(path, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %q, want mention of 'mismatch'", err.Error())
	}
}

func TestVerifyChecksum_FileNotFound(t *testing.T) {
	err := verifyChecksum("/nonexistent/path/file", "sha512:abc123")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFetchDebianChecksum_HTTPError(t *testing.T) {
	mockHTTPClient(t, func(req *http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(""))}
	})

	_, err := FetchDebianChecksum("http://example.com/images/file.qcow2", "file.qcow2")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want mention of HTTP status", err.Error())
	}
}

func TestBuildGoldenImage_EmptyPackages(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	recs := goldenMockRunner(t)

	cfg := BuildGoldenConfig{
		ImagesDir:    dir,
		BaseURL:      "https://example.com/base.qcow2",
		BaseChecksum: checksum,
		BaseFilename: "base.qcow2",
		GoldenName:   "golden.qcow2",
		RootDiskGB:   8,
		Packages:     []string{},
		VMUser:       "claude",
	}

	if err := BuildGoldenImage(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// virt-customize --install should still have nodejs,npm
	for _, rec := range *recs {
		if rec.name == "virt-customize" {
			args := strings.Join(rec.args, " ")
			if !strings.Contains(args, "--install nodejs,npm") {
				t.Errorf("empty packages should still install nodejs,npm, got: %s", args)
			}
			return
		}
	}
	t.Fatal("virt-customize not called")
}

func TestBuildGoldenImage_GuestfishFailure(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)

	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		if name == "cp" && len(args) >= 2 {
			_ = os.WriteFile(args[len(args)-1], []byte("fake"), 0644)
		}
		if name == "guestfish" {
			return "", "", fmt.Errorf("e2fsck failed")
		}
		return "", "", nil
	})

	cfg := BuildGoldenConfig{
		ImagesDir:    dir,
		BaseURL:      "https://example.com/base.qcow2",
		BaseChecksum: checksum,
		BaseFilename: "base.qcow2",
		GoldenName:   "golden.qcow2",
		RootDiskGB:   8,
		Packages:     []string{"git"},
		VMUser:       "claude",
	}

	err := BuildGoldenImage(cfg)
	if err == nil {
		t.Fatal("expected error from guestfish failure")
	}
	if !strings.Contains(err.Error(), "filesystem check") {
		t.Errorf("error = %q, want mention of 'filesystem check'", err.Error())
	}

	// Temp file should still be cleaned up
	entries, _ := filepath.Glob(filepath.Join(dir, ".golden-bake-*"))
	if len(entries) > 0 {
		t.Errorf("temp file not cleaned up after guestfish failure: %v", entries)
	}
}

// --- downloadBaseImage tests ---

func TestDownloadBaseImage_AlreadyExistsValidChecksum(t *testing.T) {
	dir := t.TempDir()
	content := []byte("existing image data")
	destPath := filepath.Join(dir, "base.qcow2")
	if err := os.WriteFile(destPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	h := sha512.Sum512(content)
	checksum := "sha512:" + hex.EncodeToString(h[:])

	httpCalled := false
	mockHTTPClient(t, func(req *http.Request) *http.Response {
		httpCalled = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}
	})

	if err := downloadBaseImage("http://example.com/base.qcow2", destPath, checksum); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if httpCalled {
		t.Error("HTTP request made when file already exists with valid checksum")
	}
}

func TestDownloadBaseImage_AlreadyExistsBadChecksum(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "base.qcow2")
	if err := os.WriteFile(destPath, []byte("image data"), 0644); err != nil {
		t.Fatal(err)
	}

	err := downloadBaseImage("http://example.com/base.qcow2", destPath, "sha512:0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %q, want mention of 'mismatch'", err.Error())
	}
}

func TestDownloadBaseImage_FreshDownload(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "base.qcow2")
	content := []byte("downloaded image content")

	h := sha512.Sum512(content)
	checksum := "sha512:" + hex.EncodeToString(h[:])

	mockHTTPClient(t, func(req *http.Request) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(content)),
		}
	})

	if err := downloadBaseImage("http://example.com/base.qcow2", destPath, checksum); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file landed at destPath with correct content
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Error("downloaded file content does not match")
	}

	// No temp files should remain (atomic rename)
	entries, _ := filepath.Glob(filepath.Join(dir, ".download-*"))
	if len(entries) > 0 {
		t.Errorf("temp file not cleaned up after successful download: %v", entries)
	}
}

func TestDownloadBaseImage_HTTPError(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "base.qcow2")

	mockHTTPClient(t, func(req *http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("error"))}
	})

	err := downloadBaseImage("http://example.com/base.qcow2", destPath, "sha512:abc")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want mention of HTTP status", err.Error())
	}

	// No file should exist at destPath
	if _, err := os.Stat(destPath); !os.IsNotExist(err) {
		t.Error("file should not exist after HTTP error")
	}
}

func TestDownloadBaseImage_ChecksumMismatchCleansUp(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "base.qcow2")
	content := []byte("image with wrong checksum")

	mockHTTPClient(t, func(req *http.Request) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(content)),
		}
	})

	err := downloadBaseImage("http://example.com/base.qcow2", destPath, "sha512:0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %q, want mention of 'mismatch'", err.Error())
	}

	// Temp file should be cleaned up
	entries, _ := filepath.Glob(filepath.Join(dir, ".download-*"))
	if len(entries) > 0 {
		t.Errorf("temp file not cleaned up: %v", entries)
	}

	// No file at destPath
	if _, err := os.Stat(destPath); !os.IsNotExist(err) {
		t.Error("file should not exist after checksum mismatch")
	}
}

