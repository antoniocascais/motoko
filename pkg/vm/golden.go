package vm

import (
	"bufio"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
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
	if _, _, err := runCmd("virt-customize", libvirtArgs(args...)...); err != nil {
		return fmt.Errorf("customizing image: %w", err)
	}

	if _, _, err := runCmd("guestfish", "-a", tmpPath, "run", ":", "e2fsck-f", "/dev/sda1"); err != nil {
		return fmt.Errorf("filesystem check: %w", err)
	}

	// Re-check at the moment of replacement, not just before the build: the bake
	// takes minutes, and an instance created meanwhile would otherwise be left
	// backed by a different image than the one its overlay was written against.
	refs, err := GoldenReferences(cfg.ImagesDir, cfg.GoldenName)
	if err != nil {
		return fmt.Errorf("checking golden image references: %w", err)
	}
	if len(refs) > 0 {
		return &GoldenInUseError{Golden: filepath.Join(cfg.ImagesDir, cfg.GoldenName), Refs: refs}
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

// GoldenRef is an image whose qcow2 backing file is the golden image.
type GoldenRef struct {
	Path string
	// Unverified marks an image whose backing file could not be determined. It
	// is counted as a reference so a scan failure never permits replacing the
	// golden image.
	Unverified bool
}

// GoldenInUseError reports that the golden image cannot be replaced because
// existing images are backed by it.
type GoldenInUseError struct {
	Golden string
	Refs   []GoldenRef
}

func (e *GoldenInUseError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "golden image %s is the backing file of %d image(s):", e.Golden, len(e.Refs))
	for _, r := range e.Refs {
		b.WriteString("\n  " + r.Path)
		if r.Unverified {
			b.WriteString(" (backing file unreadable, assumed in use)")
		}
	}
	b.WriteString("\nReplacing or recreating it would corrupt them. To roll out a new golden image: set a new " +
		"golden_image.name in the config, run `motoko build`, then `motoko rebuild <name>` for each instance.")
	return b.String()
}

// GoldenReferences lists the images in imagesDir that use the golden image as
// their qcow2 backing file. Overlays record the backing file by absolute path,
// so replacing the golden in place makes every stopped overlay read its deltas
// on top of different base blocks — silent root-filesystem corruption on next
// boot. The overlays themselves are scanned rather than instance state, because
// orphaned and pre-state instances exist. A missing golden is still scanned:
// overlays left pointing at a deleted golden fail loudly at boot, but building a
// new one at the same path would make them boot silently corrupted.
func GoldenReferences(imagesDir, goldenName string) ([]GoldenRef, error) {
	goldenPath := filepath.Join(imagesDir, goldenName)
	goldenInfo, err := os.Stat(goldenPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking golden image: %w", err)
	}

	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, fmt.Errorf("reading images dir: %w", err)
	}

	var refs []GoldenRef
	for _, e := range entries {
		name := e.Name()
		// Hidden files are in-progress bakes/downloads, never instance images.
		if e.IsDir() || name == goldenName || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".qcow2") {
			continue
		}
		p := filepath.Join(imagesDir, name)
		backing, err := backingFile(p)
		if err != nil {
			refs = append(refs, GoldenRef{Path: p, Unverified: true})
			continue
		}
		if backing == "" {
			continue
		}
		if !filepath.IsAbs(backing) {
			backing = filepath.Join(imagesDir, backing)
		}
		// Compare by file identity too, so a backing path that reaches the golden
		// through a symlink or a different spelling still counts. Only possible
		// while the golden exists; a missing one is matched by path alone.
		if filepath.Clean(backing) == goldenPath {
			refs = append(refs, GoldenRef{Path: p})
		} else if goldenInfo == nil {
			continue
		} else if bi, err := os.Stat(backing); err == nil && os.SameFile(bi, goldenInfo) {
			refs = append(refs, GoldenRef{Path: p})
		}
	}
	return refs, nil
}

// backingFile returns the image's qcow2 backing file path, or "" if it has none.
// -U (force share) is required: running VMs hold a write lock on their overlay.
func backingFile(path string) (string, error) {
	stdout, _, err := runCmd("qemu-img", "info", "-U", "--output=json", path)
	if err != nil {
		return "", err
	}
	var info struct {
		Backing     string `json:"backing-filename"`
		FullBacking string `json:"full-backing-filename"`
	}
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		return "", fmt.Errorf("parsing qemu-img info for %s: %w", path, err)
	}
	if info.FullBacking != "" {
		return info.FullBacking, nil
	}
	return info.Backing, nil
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
