package vm

import (
	"bufio"
	"crypto/sha256"
	"crypto/sha512"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// httpClient is the shared HTTP client for image downloads and checksum fetches.
// Tests replace this via mockHTTPClient to avoid real network calls.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

// validVMUser matches safe Unix usernames for interpolation into virt-customize --run-command.
var validVMUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// Channel and version are interpolated into apt files and a --run-command;
// config validation already enforces these, this is the last gate before use.
var (
	validClaudeChannel = regexp.MustCompile(`^(stable|latest)$`)
	validClaudeVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// claudeCodeKey is Anthropic's Claude Code release signing key
// (fingerprint 31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE). Embedded
// rather than fetched at build time so apt trusts the key motoko shipped, not
// whatever the network returns.
//
//go:embed keys/claude-code.asc
var claudeCodeKey []byte

const (
	claudeRepoBase    = "https://downloads.claude.ai/claude-code/apt"
	claudeKeyringPath = "/etc/apt/keyrings/claude-code.gpg"
	claudeSourcesPath = "/etc/apt/sources.list.d/claude-code.list"
	claudePinPath     = "/etc/apt/preferences.d/claude-code"
)

// BuildGoldenConfig holds parameters for building a golden base image.
type BuildGoldenConfig struct {
	ImagesDir    string
	BaseURL      string
	BaseChecksum string // "" = fetch SHA512SUMS dynamically
	BaseFilename string
	GoldenName   string
	RootDiskGB   int
	Packages     []string // git and claude-code are appended automatically if absent
	VMUser       string
	// ClaudeChannel is the apt suite ("stable" or "latest") Claude Code is installed from.
	ClaudeChannel string
	// ClaudeVersion pins an exact upstream version (X.Y.Z); "" = newest in channel.
	ClaudeVersion string
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
	if !validClaudeChannel.MatchString(cfg.ClaudeChannel) {
		return fmt.Errorf("invalid claude_channel %q: must be stable or latest", cfg.ClaudeChannel)
	}
	if cfg.ClaudeVersion != "" && !validClaudeVersion.MatchString(cfg.ClaudeVersion) {
		return fmt.Errorf("invalid claude_version %q: must be X.Y.Z", cfg.ClaudeVersion)
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

	// Fetched and verified before the bake starts: a bad download must not cost
	// minutes of building, and must leave the existing golden untouched.
	bunPath, err := fetchBun(cfg.ImagesDir)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(bunPath) }()

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

	keyPath, err := writeClaudeKey()
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(keyPath) }()

	// Claude Code is a self-contained binary and the Telegram plugin's
	// dependencies are installed by bun, so no Node.js is needed. git is required
	// by claude's marketplace clone during the seed.
	packages := withPackages(cfg.Packages, "git", "claude-code")
	// The keyring and source entry must exist before --update so apt indexes the
	// repo and verifies its signature with that key only (signed-by), never via
	// apt's global trust.
	args := []string{
		"-a", tmpPath,
		"--run-command", "growpart /dev/sda 1 && resize2fs /dev/sda1",
		"--mkdir", "/etc/apt/keyrings",
		"--upload", keyPath + ":" + claudeKeyringPath,
		// --upload keeps the temp file's 0600; apt verifies as the _apt user.
		"--chmod", "0644:" + claudeKeyringPath,
		"--write", claudeSourcesPath + ":" + claudeSourceLine(cfg.ClaudeChannel),
	}
	if cfg.ClaudeVersion != "" {
		args = append(args, "--write", claudePinPath+":"+claudePin(cfg.ClaudeVersion))
	}
	args = append(args,
		"--update",
		"--install", strings.Join(packages, ","),
	)
	if cfg.ClaudeVersion != "" {
		args = append(args, "--run-command", claudeVersionCheck(cfg.ClaudeVersion))
	}
	// Software only: bun and the plugin seed are identical for every instance.
	// All per-instance config (managed settings, tokens, trust) is cloud-init's.
	args = append(args, bunArgs(bunPath, pinnedBun)...)
	args = append(args, seedArgs()...)
	// The source and key are for the bake only. Left in the image, every in-VM
	// apt-get update would hit a proxy-blocked host, and allowlisting that host
	// would let apt move claude off the version baked here.
	args = append(args,
		"--delete", claudeSourcesPath,
		"--delete", claudeKeyringPath,
		"--run-command", fmt.Sprintf("useradd -m -s /bin/bash %s", cfg.VMUser),
	)
	_, stderr, err := runCmd("virt-customize", libvirtArgs(args...)...)
	// virt-customize shows a command's output only when the command fails, so
	// the seed's info lines are read back from the image, success or not.
	info, infoErr := readSeedInfo(tmpPath)
	if infoErr == nil {
		logSeedLines(info)
	}
	if err != nil {
		step := failedSeedStep(stderr + "\n" + err.Error())
		if step == "" && infoErr == nil {
			step = failedSeedStep(info)
		}
		if step != "" {
			return fmt.Errorf("customizing image: golden seed step %q failed: %w", step, err)
		}
		return fmt.Errorf("customizing image: %w", err)
	}
	if infoErr != nil {
		return fmt.Errorf("reading seed info from the image: %w", infoErr)
	}
	if err := scrubBakeArtifacts(tmpPath); err != nil {
		return fmt.Errorf("removing build logs from the image: %w", err)
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

// withPackages returns base plus any of extra not already listed, in order.
func withPackages(base []string, extra ...string) []string {
	out := append([]string{}, base...)
	for _, e := range extra {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// writeClaudeKey writes the embedded signing key, dearmored, to a temp file for
// virt-customize --upload. The caller removes it.
func writeClaudeKey() (string, error) {
	key, err := dearmor(claudeCodeKey)
	if err != nil {
		return "", fmt.Errorf("decoding signing key: %w", err)
	}
	f, err := os.CreateTemp("", "motoko-claude-code-*.gpg")
	if err != nil {
		return "", fmt.Errorf("creating signing key file: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("writing signing key file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("writing signing key file: %w", err)
	}
	return path, nil
}

// dearmor converts an ASCII-armored OpenPGP block to the binary form gpgv
// reads directly. Debian 12's apt would otherwise convert an armored signed-by
// key itself at every verification, in the deprecated apt-key script.
func dearmor(armored []byte) ([]byte, error) {
	const begin, end = "-----BEGIN PGP PUBLIC KEY BLOCK-----", "-----END PGP PUBLIC KEY BLOCK-----"
	lines := strings.Split(strings.ReplaceAll(string(armored), "\r\n", "\n"), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) != begin {
		i++
	}
	if i == len(lines) {
		return nil, fmt.Errorf("no %s line", begin)
	}
	// Armor headers (if any) end at the first blank line.
	for i++; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
	}
	var body, sum string
	for i++; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == end:
			data, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				return nil, fmt.Errorf("decoding armor body: %w", err)
			}
			if len(data) == 0 {
				return nil, fmt.Errorf("empty armor body")
			}
			if sum != "" {
				want, err := base64.StdEncoding.DecodeString(sum)
				if err != nil || len(want) != 3 {
					return nil, fmt.Errorf("malformed armor checksum %q", sum)
				}
				if got := crc24(data); got != uint32(want[0])<<16|uint32(want[1])<<8|uint32(want[2]) {
					return nil, fmt.Errorf("armor checksum mismatch")
				}
			}
			return data, nil
		case strings.HasPrefix(line, "="):
			sum = line[1:]
		default:
			body += line
		}
	}
	return nil, fmt.Errorf("no %s line", end)
}

// crc24 is the OpenPGP armor checksum (RFC 4880 section 6.1).
func crc24(data []byte) uint32 {
	crc := uint32(0xB704CE)
	for _, b := range data {
		crc ^= uint32(b) << 16
		for range 8 {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= 0x1864CFB
			}
		}
	}
	return crc & 0xFFFFFF
}

// claudeSourceLine is the apt source entry for a channel. Each channel is its
// own repo path whose suite has the same name.
func claudeSourceLine(channel string) string {
	return fmt.Sprintf("deb [signed-by=%s] %s/%s %s main\n", claudeKeyringPath, claudeRepoBase, channel, channel)
}

// claudePin makes apt prefer the pinned upstream version over newer ones,
// whatever its Debian revision. It also keeps an in-VM `apt upgrade` from
// moving the version away from what the golden image was built with.
func claudePin(version string) string {
	return fmt.Sprintf("Package: claude-code\nPin: version %s-*\nPin-Priority: 1001\n", version)
}

// claudeVersionCheck fails the bake unless the pinned version was installed.
// A pin on a version the channel does not carry does not make apt fail; it
// silently installs the newest one instead.
func claudeVersionCheck(version string) string {
	return fmt.Sprintf(`v=$(dpkg-query -W -f='${Version}' claude-code); case "$v" in %s-*) ;; *) echo "claude-code $v installed, want %s" >&2; exit 1 ;; esac`, version, version)
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
