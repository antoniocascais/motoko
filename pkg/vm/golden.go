package vm

import (
	"bufio"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// httpClient is the shared HTTP client for image downloads and checksum fetches.
// Tests replace this via mockHTTPClient to avoid real network calls.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

// validVMUser matches safe Unix usernames for interpolation into virt-customize --run-command.
var validVMUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// BuildGoldenConfig holds parameters for building a golden base image.
type BuildGoldenConfig struct {
	ImagesDir    string
	BaseURL      string
	BaseChecksum string   // "" = fetch SHA512SUMS dynamically
	BaseFilename string
	GoldenName   string
	RootDiskGB   int
	Packages     []string // nodejs, npm appended automatically
	VMUser       string
}

// GoldenImageExists checks whether the golden image file exists.
func GoldenImageExists(imagesDir, goldenName string) (bool, error) {
	_, err := os.Stat(filepath.Join(imagesDir, goldenName))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// BuildGoldenImage downloads a base cloud image, customizes it, and produces a read-only golden image.
func BuildGoldenImage(cfg BuildGoldenConfig) error {
	if !validVMUser.MatchString(cfg.VMUser) {
		return fmt.Errorf("invalid vm_user %q: must match [a-z_][a-z0-9_-]*", cfg.VMUser)
	}

	basePath := filepath.Join(cfg.ImagesDir, cfg.BaseFilename)

	checksum := cfg.BaseChecksum
	if checksum == "" {
		var err error
		checksum, err = FetchDebianChecksum(cfg.BaseURL, cfg.BaseFilename)
		if err != nil {
			return fmt.Errorf("fetching checksum: %w", err)
		}
	}

	if err := downloadBaseImage(cfg.BaseURL, basePath, checksum); err != nil {
		return fmt.Errorf("downloading base image: %w", err)
	}

	tmpPath := filepath.Join(cfg.ImagesDir, fmt.Sprintf(".golden-bake-%d.qcow2", time.Now().Unix()))
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, _, err := runCmd("cp", "--reflink=auto", basePath, tmpPath); err != nil {
		return fmt.Errorf("copying base image: %w", err)
	}

	if _, _, err := runCmd("qemu-img", "resize", tmpPath, fmt.Sprintf("%dG", cfg.RootDiskGB)); err != nil {
		return fmt.Errorf("resizing image: %w", err)
	}

	packages := append(append([]string{}, cfg.Packages...), "nodejs", "npm")
	args := []string{
		"-a", tmpPath,
		"--run-command", "growpart /dev/sda 1 && resize2fs /dev/sda1",
		"--update",
		"--install", strings.Join(packages, ","),
		"--run-command", "npm install -g @anthropic-ai/claude-code",
		"--run-command", fmt.Sprintf("useradd -m -s /bin/bash %s", cfg.VMUser),
	}
	if _, _, err := runCmd("virt-customize", args...); err != nil {
		return fmt.Errorf("customizing image: %w", err)
	}

	if _, _, err := runCmd("guestfish", "-a", tmpPath, "run", ":", "e2fsck-f", "/dev/sda1"); err != nil {
		return fmt.Errorf("filesystem check: %w", err)
	}

	goldenPath := filepath.Join(cfg.ImagesDir, cfg.GoldenName)
	if err := os.Rename(tmpPath, goldenPath); err != nil {
		return fmt.Errorf("moving golden image: %w", err)
	}
	cleanup = false

	if err := os.Chmod(goldenPath, 0444); err != nil {
		return fmt.Errorf("setting golden image permissions: %w", err)
	}

	return nil
}

// FetchDebianChecksum downloads SHA512SUMS from the same directory as baseURL
// and returns the checksum for the given filename as "sha512:<hex>".
func FetchDebianChecksum(baseURL, filename string) (string, error) {
	// Derive SHA512SUMS URL from base image URL
	dir := baseURL[:strings.LastIndex(baseURL, "/")+1]
	checksumsURL := dir + "SHA512SUMS"

	resp, err := httpClient.Get(checksumsURL)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", checksumsURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: HTTP %d", checksumsURL, resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: "<hash>  <filename>" (two spaces between hash and filename)
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) == filename {
			return "sha512:" + parts[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading checksums: %w", err)
	}

	return "", fmt.Errorf("checksum for %q not found in %s", filename, checksumsURL)
}

// downloadBaseImage fetches the image from url to destPath if it doesn't already exist.
// Verifies the checksum after download.
func downloadBaseImage(url, destPath, checksum string) error {
	if _, err := os.Stat(destPath); err == nil {
		// Already downloaded — verify checksum
		return verifyChecksum(destPath, checksum)
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}

	// Write to temp file in same directory for atomic rename
	dir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing download: %w", err)
	}

	if err := verifyChecksum(tmpPath, checksum); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("moving download: %w", err)
	}

	return nil
}

// verifyChecksum checks a file against a "sha256:<hex>" or "sha512:<hex>" checksum.
func verifyChecksum(path, checksum string) error {
	parts := strings.SplitN(checksum, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid checksum format %q: expected algo:hex", checksum)
	}

	algo, expected := parts[0], parts[1]

	var h hash.Hash
	switch algo {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	default:
		return fmt.Errorf("unsupported checksum algorithm %q", algo)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening file for checksum: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("reading file for checksum: %w", err)
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", filepath.Base(path), actual, expected)
	}

	return nil
}
