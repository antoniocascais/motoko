package vm

import (
	"archive/zip"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/antoniocascais/motoko/pkg/config"
)

// Bumping a pin: see "Updating the baked bun and Telegram plugin" in the README.

// bunPin identifies the bun release baked into the golden image. The zip's
// sha256 comes from bun's release SHASUMS256.txt, whose signature was checked
// against bun's release key (fingerprint F3DCC08A8572C0749B3E18888EAB4D40A7B22B59)
// when the pin was set. The -baseline build is used because virt-install sets
// no --cpu, so SSE4.2 (required by the default build) is not guaranteed.
type bunPin struct {
	Version string
	URL     string
	SHA256  string
	// Entry is the path of the bun executable inside the zip.
	Entry string
}

var pinnedBun = bunPin{
	Version: "1.4.4",
	URL:     "https://github.com/oven-sh/bun/releases/download/bun-v1.4.4/bun-linux-x64-baseline.zip",
	SHA256:  "e0446eb55995772eb9894bebae9a6be368d985a8b9271ee124486f9946eedd76",
	Entry:   "bun-linux-x64-baseline/bun",
}

// maxBunBytes bounds the extracted executable so a corrupt or hostile zip
// cannot fill the host disk; the real binary is ~100 MB.
var maxBunBytes int64 = 512 << 20

const (
	bunGuestPath = config.BunPath
	// seedGuestDir is where the Claude Code plugin seed lives in the image.
	seedGuestDir = config.SeedDir
	// seedHomeDir is a throwaway HOME for the seed commands, so the build user's
	// ~/.claude and ~/.claude.json never end up in the image.
	seedHomeDir = "/var/tmp/motoko-seed-home"
	// telegramManifestGuestPath holds the manifest only while the bake runs.
	telegramManifestGuestPath = "/var/tmp/motoko-telegram.sha256"

	// telegramPluginVersion is the version directory the pinned plugin installs
	// into. It comes from the pin, not a glob, so an upstream version bump fails
	// the bake loudly instead of being baked silently.
	telegramPluginVersion = "0.0.7"
)

// telegramManifest lists every file of the pinned plugin outside node_modules
// as sha256sum lines (hash, two spaces, path relative to the plugin dir).
//
//go:embed seed/telegram-plugin.sha256
var telegramManifest string

// seedStepMarker prefixes the line a failing seed step prints, so the failure
// can be named from the virt-customize output.
const seedStepMarker = "golden seed step failed: "

var seedStepRE = regexp.MustCompile(regexp.QuoteMeta(seedStepMarker) + `([a-z0-9-]+)`)

// failedSeedStep returns the name of the seed step reported in virt-customize
// output, or "".
func failedSeedStep(output string) string {
	if m := seedStepRE.FindStringSubmatch(output); m != nil {
		return m[1]
	}
	return ""
}

// seedStep wraps a fixed shell snippet so that a failure prints which step
// failed and exits non-zero, failing virt-customize and with it the bake.
//
// The marker goes both to stderr and to the info file. virt-customize runs each
// command with output redirected into the guest's /tmp/builder.log and only
// dumps it to the host on failure, so the info file is the reliable channel.
func seedStep(name, snippet string) string {
	return fmt.Sprintf(`{ %s; } || { echo "%s%s" >> %s; echo "%s%s" >&2; exit 1; }`, snippet, seedStepMarker, name, seedInfoPath, seedStepMarker, name)
}

// bunVersionCheck succeeds only when the baked binary reports exactly version.
func bunVersionCheck(bin, version string) string {
	return fmt.Sprintf(`[ "$(%s --version)" = "%s" ]`, bin, version)
}

// seedEnv is the environment of every claude call at seed time. bun must be on
// PATH: claude installs the plugin's locked dependencies with it.
func seedEnv() string {
	return fmt.Sprintf("env HOME=%s PATH=/usr/local/bin:/usr/bin:/bin CLAUDE_CODE_PLUGIN_CACHE_DIR=%s", seedHomeDir, seedGuestDir)
}

// telegramGate fails unless the seeded plugin is exactly the one the manifest
// describes: the seed holds one marketplace with one plugin in exactly the
// pinned version dir, whose files outside node_modules are the manifest's
// files with matching hashes (no missing, no extra). node_modules integrity
// comes from the plugin's frozen bun.lock. .in_use is claude's own marker.
func telegramGate(seed, manifest, version string) string {
	return strings.Join(telegramGateChecks(seed, manifest, version), " && ")
}

// telegramGateChecks are the gate's checks in order; telegramGate chains them.
// Tests run prefixes of this list to pin which check rejects a tree.
func telegramGateChecks(seed, manifest, version string) []string {
	plugin := seed + "/cache/claude-plugins-official/telegram"
	return []string{
		fmt.Sprintf(`[ "$(ls -A %s/cache)" = "claude-plugins-official" ]`, seed),
		fmt.Sprintf(`[ "$(ls -A %s)" = "%s" ]`, plugin, version),
		fmt.Sprintf(`cd %s/%s`, plugin, version),
		fmt.Sprintf(`sha256sum -c %s >/dev/null`, manifest),
		fmt.Sprintf(`[ "$(find . -path ./node_modules -prune -o ! -type d ! -path ./.in_use -print | sort)" = "$(sed 's|^[0-9a-f]*  |./|' %s | sort)" ]`, manifest),
	}
}

// seedPermsFix makes the seed root-owned and read-only to everyone else: dirs
// 0755, files 0644, and files that were executable stay executable.
func seedPermsFix(dir string) string {
	return fmt.Sprintf(`chown -R root:root %[1]s && chmod -R u=rwX,go=rX %[1]s`, dir)
}

// seedPermsCheck succeeds only if nothing under dir (symlinks aside) is group-
// or world-writable, so the VM user can never alter the seed.
func seedPermsCheck(dir string) string {
	return fmt.Sprintf(`[ -z "$(find %s ! -type l -perm /022)" ]`, dir)
}

// bunArgs uploads the host-side bun executable and gates the bake on it
// reporting exactly the pinned version.
func bunArgs(hostBun string, pin bunPin) []string {
	return []string{
		"--upload", hostBun + ":" + bunGuestPath,
		"--chmod", "0755:" + bunGuestPath,
		"--run-command", seedStep("bun-version", bunVersionCheck(bunGuestPath, pin.Version)),
		"--run-command", fmt.Sprintf(`echo "%s bun-version: $(%s --version)" >> %s`, seedLogMarker, bunGuestPath, seedInfoPath),
	}
}

// seedLogMarker prefixes lines the bake records for the build log.
const seedLogMarker = "motoko-seed"

const (
	// seedInfoPath collects the bake's informational lines inside the image;
	// the host reads it back with virt-cat and then deletes it.
	seedInfoPath = "/var/tmp/motoko-seed.info"
	// builderLogPath is where virt-customize logs every --run-command.
	builderLogPath = "/tmp/builder.log"
)

// readSeedInfo returns the info file of the baked, not yet installed image.
func readSeedInfo(image string) (string, error) {
	stdout, _, err := runCmd("virt-cat", libvirtArgs("-a", image, seedInfoPath)...)
	return stdout, err
}

// scrubBakeArtifacts removes the info file and virt-customize's command log so
// the golden image ships no build logs.
func scrubBakeArtifacts(image string) error {
	_, _, err := runCmd("virt-customize", libvirtArgs("-a", image, "--delete", seedInfoPath, "--delete", builderLogPath)...)
	return err
}

// seedArgs is the virt-customize tail that builds the plugin seed. It follows
// the "seed containers" flow in Claude Code's plugin docs. Every string in it
// is a constant: nothing from config or user input is interpolated.
func seedArgs() []string {
	plugin := seedGuestDir + "/cache/claude-plugins-official/telegram/" + telegramPluginVersion
	env := seedEnv()
	return []string{
		"--mkdir", seedHomeDir,
		"--write", telegramManifestGuestPath + ":" + telegramManifest,
		"--run-command", seedStep("marketplace-add", env+" claude plugin marketplace add anthropics/claude-plugins-official"),
		"--run-command", seedStep("plugin-install", env+" claude plugin install telegram@claude-plugins-official"),
		"--run-command", seedStep("plugin-deps", fmt.Sprintf(`[ -d %[1]s/node_modules/grammy ] && [ -d %[1]s/node_modules/@modelcontextprotocol/sdk ]`, plugin)),
		"--run-command", seedStep("plugin-manifest", telegramGate(seedGuestDir, telegramManifestGuestPath, telegramPluginVersion)),
		// Logged, never gated: unrelated upstream commits must not break builds.
		"--run-command", fmt.Sprintf(`echo "%s marketplace-commit: $(grep -o '"gitCommitSha": *"[0-9a-f]*"' %s/installed_plugins.json | head -n 1)" >> %s`, seedLogMarker, seedGuestDir, seedInfoPath),
		"--run-command", seedStep("seed-permissions", seedPermsFix(seedGuestDir)+" && "+seedPermsCheck(seedGuestDir)),
		"--delete", seedHomeDir,
		"--delete", telegramManifestGuestPath,
	}
}

// logSeedLines forwards the bake's informational lines (bun version, marketplace
// commit) to the build log.
func logSeedLines(info string) {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, seedLogMarker+" ") {
			fmt.Fprintln(os.Stderr, line)
		}
	}
}

// fetchBun is replaced in tests so golden-build tests need no zip fixture.
var fetchBun = func(imagesDir string) (string, error) { return ensureBun(imagesDir, pinnedBun) }

// ensureBun returns the path of a temp file holding the pinned bun executable,
// extracted on the host so the image needs no unzip and no network for it. The
// caller removes it. The zip is cached in imagesDir; a cached zip that fails
// its checksum is replaced once, but a fresh download that fails is an error.
func ensureBun(imagesDir string, pin bunPin) (string, error) {
	base := filepath.Base(pin.URL)
	zipPath := filepath.Join(imagesDir, "bun-"+pin.Version+"-"+base)
	checksum := "sha256:" + pin.SHA256

	cachedOK := false
	if _, err := os.Stat(zipPath); err == nil {
		if verifyChecksum(zipPath, checksum) == nil {
			cachedOK = true
		} else if rmErr := os.Remove(zipPath); rmErr != nil {
			return "", fmt.Errorf("removing stale bun download: %w", rmErr)
		}
	}
	// A verified cache hit is not hashed a second time by the download helper.
	if !cachedOK {
		if err := downloadVerified(pin.URL, zipPath, checksum); err != nil {
			return "", fmt.Errorf("fetching bun %s: %w", pin.Version, err)
		}
	}
	pruneOldBunZips(imagesDir, zipPath, base)
	return extractZipEntry(zipPath, pin.Entry)
}

// pruneOldBunZips deletes cached zips of other bun versions. The match is tight
// (bun-<version>-<this asset's name>, regular files only) because imagesDir
// also holds golden images and instance disks. Best effort: a leftover zip
// costs disk space, never correctness.
func pruneOldBunZips(imagesDir, keep, assetName string) {
	re := regexp.MustCompile(`^bun-[0-9][0-9A-Za-z.+-]*-` + regexp.QuoteMeta(assetName) + `$`)
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(imagesDir, e.Name())
		if path == keep || !e.Type().IsRegular() || !re.MatchString(e.Name()) {
			continue
		}
		_ = os.Remove(path)
	}
}

// extractZipEntry copies one named entry of a zip to a new temp file.
func extractZipEntry(zipPath, entry string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", filepath.Base(zipPath), err)
	}
	defer func() { _ = zr.Close() }()

	for _, f := range zr.File {
		if f.Name != entry {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("reading %s from zip: %w", entry, err)
		}
		defer func() { _ = rc.Close() }()

		out, err := os.CreateTemp("", "motoko-bun-*")
		if err != nil {
			return "", fmt.Errorf("creating bun temp file: %w", err)
		}
		path := out.Name()
		n, err := io.Copy(out, io.LimitReader(rc, maxBunBytes+1))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil && n > maxBunBytes {
			err = fmt.Errorf("larger than %d bytes", maxBunBytes)
		}
		if err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("extracting %s: %w", entry, err)
		}
		return path, nil
	}
	return "", fmt.Errorf("%s has no %s entry", filepath.Base(zipPath), entry)
}
