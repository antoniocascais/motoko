package vm

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

func shRun(t *testing.T, env []string, script string) (stderr string, err error) {
	t.Helper()
	sh, lerr := exec.LookPath("sh")
	if lerr != nil {
		t.Skip("sh not available")
	}
	cmd := exec.Command(sh, "-c", script)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
	var buf bytes.Buffer
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

// --- pins ---

func TestPinnedBun_Wellformed(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(pinnedBun.SHA256) {
		t.Errorf("bun sha256 %q is not 64 lowercase hex", pinnedBun.SHA256)
	}
	if !strings.Contains(pinnedBun.URL, "bun-v"+pinnedBun.Version+"/") {
		t.Errorf("URL %q does not belong to version %s", pinnedBun.URL, pinnedBun.Version)
	}
	if !strings.HasSuffix(pinnedBun.URL, "bun-linux-x64-baseline.zip") {
		t.Errorf("URL %q is not the baseline build (virt-install sets no --cpu)", pinnedBun.URL)
	}
	if pinnedBun.Entry != "bun-linux-x64-baseline/bun" {
		t.Errorf("zip entry = %q", pinnedBun.Entry)
	}
}

func TestTelegramManifest_Wellformed(t *testing.T) {
	line := regexp.MustCompile(`^[0-9a-f]{64}  [^\s/][^\s]*$`)
	var paths []string
	for _, l := range strings.Split(strings.TrimSuffix(telegramManifest, "\n"), "\n") {
		if !line.MatchString(l) {
			t.Fatalf("manifest line %q is not `<sha256>  <relative path>`", l)
		}
		p := l[66:]
		if strings.HasPrefix(p, "node_modules/") || strings.HasPrefix(p, ".git/") || p == ".in_use" {
			t.Errorf("manifest must not list %s", p)
		}
		paths = append(paths, p)
	}
	if !sort.StringsAreSorted(paths) {
		t.Error("manifest is not sorted by path")
	}
	for i := 1; i < len(paths); i++ {
		if paths[i] == paths[i-1] {
			t.Errorf("duplicate manifest path %s", paths[i])
		}
	}
	for _, must := range []string{".mcp.json", "package.json", "bun.lock", "server.ts", ".claude-plugin/plugin.json"} {
		found := false
		for _, p := range paths {
			found = found || p == must
		}
		if !found {
			t.Errorf("manifest lacks %s", must)
		}
	}
}

// --- seed argv ---

func TestSeedArgs_Exact(t *testing.T) {
	plugin := "/opt/claude-seed/cache/claude-plugins-official/telegram/0.0.7"
	env := "env HOME=/var/tmp/motoko-seed-home PATH=/usr/local/bin:/usr/bin:/bin CLAUDE_CODE_PLUGIN_CACHE_DIR=/opt/claude-seed"
	want := []string{
		"--mkdir", "/var/tmp/motoko-seed-home",
		"--write", "/var/tmp/motoko-telegram.sha256:" + telegramManifest,
		"--run-command", `{ ` + env + ` claude plugin marketplace add anthropics/claude-plugins-official; } || { echo "golden seed step failed: marketplace-add" >> /var/tmp/motoko-seed.info; echo "golden seed step failed: marketplace-add" >&2; exit 1; }`,
		"--run-command", `{ ` + env + ` claude plugin install telegram@claude-plugins-official; } || { echo "golden seed step failed: plugin-install" >> /var/tmp/motoko-seed.info; echo "golden seed step failed: plugin-install" >&2; exit 1; }`,
		"--run-command", `{ [ -d ` + plugin + `/node_modules/grammy ] && [ -d ` + plugin + `/node_modules/@modelcontextprotocol/sdk ]; } || { echo "golden seed step failed: plugin-deps" >> /var/tmp/motoko-seed.info; echo "golden seed step failed: plugin-deps" >&2; exit 1; }`,
		"--run-command", `{ [ "$(ls -A /opt/claude-seed/cache)" = "claude-plugins-official" ] && [ "$(ls -A /opt/claude-seed/cache/claude-plugins-official/telegram)" = "0.0.7" ] && cd ` + plugin + ` && sha256sum -c /var/tmp/motoko-telegram.sha256 >/dev/null && [ "$(find . -path ./node_modules -prune -o ! -type d ! -path ./.in_use -print | sort)" = "$(sed 's|^[0-9a-f]*  |./|' /var/tmp/motoko-telegram.sha256 | sort)" ]; } || { echo "golden seed step failed: plugin-manifest" >> /var/tmp/motoko-seed.info; echo "golden seed step failed: plugin-manifest" >&2; exit 1; }`,
		"--run-command", `echo "motoko-seed marketplace-commit: $(grep -o '"gitCommitSha": *"[0-9a-f]*"' /opt/claude-seed/installed_plugins.json | head -n 1)" >> /var/tmp/motoko-seed.info`,
		"--run-command", `{ chown -R root:root /opt/claude-seed && chmod -R u=rwX,go=rX /opt/claude-seed && [ -z "$(find /opt/claude-seed ! -type l -perm /022)" ]; } || { echo "golden seed step failed: seed-permissions" >> /var/tmp/motoko-seed.info; echo "golden seed step failed: seed-permissions" >&2; exit 1; }`,
		"--delete", "/var/tmp/motoko-seed-home",
		"--delete", "/var/tmp/motoko-telegram.sha256",
	}
	if got := seedArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("seed argv mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func TestBunArgs_Exact(t *testing.T) {
	want := []string{
		"--upload", "/tmp/x:/usr/local/bin/bun",
		"--chmod", "0755:/usr/local/bin/bun",
		"--run-command", `{ [ "$(/usr/local/bin/bun --version)" = "1.4.4" ]; } || { echo "golden seed step failed: bun-version" >> /var/tmp/motoko-seed.info; echo "golden seed step failed: bun-version" >&2; exit 1; }`,
		"--run-command", `echo "motoko-seed bun-version: $(/usr/local/bin/bun --version)" >> /var/tmp/motoko-seed.info`,
	}
	if got := bunArgs("/tmp/x", pinnedBun); !reflect.DeepEqual(got, want) {
		t.Errorf("bun argv mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

// virtCustomizeArgv runs a build and returns the virt-customize argv.
func virtCustomizeArgv(t *testing.T, mutate func(*BuildGoldenConfig)) []string {
	t.Helper()
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	recs := goldenMockRunner(t)
	cfg := goldenBuildConfig(dir, checksum)
	mutate(&cfg)
	if err := BuildGoldenImage(cfg); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, r := range *recs {
		if r.name == "virt-customize" {
			return r.args
		}
	}
	t.Fatal("virt-customize not called")
	return nil
}

func indexOf(argv []string, pred func(string) bool) int {
	for i, a := range argv {
		if pred(a) {
			return i
		}
	}
	return -1
}

// Everything between the bun version gate and the apt cleanup is the seed; it
// must not vary with anything from config.
func TestBuildGoldenImage_SeedIgnoresConfig(t *testing.T) {
	// From the seed's --mkdir through its last --delete.
	seedPart := func(argv []string) []string {
		from := indexOf(argv, func(a string) bool { return a == seedHomeDir })
		end := -1
		for i, a := range argv {
			if a == telegramManifestGuestPath {
				end = i + 1
			}
		}
		if from < 1 || end <= from {
			t.Fatalf("seed segment not found in %q", argv)
		}
		return argv[from-1 : end]
	}
	base := virtCustomizeArgv(t, func(*BuildGoldenConfig) {})
	hostile := virtCustomizeArgv(t, func(c *BuildGoldenConfig) {
		c.VMUser = "a_b-c9"
		c.Packages = []string{"vim; rm -rf /", "$(reboot)", "a b", "`id`", "x\ny"}
		c.ClaudeChannel = "latest"
		c.ClaudeVersion = "2.1.310"
	})
	if !reflect.DeepEqual(seedPart(base), seedPart(hostile)) {
		t.Errorf("seed steps changed with config:\nbase:    %q\nhostile: %q", seedPart(base), seedPart(hostile))
	}
	if !reflect.DeepEqual(seedPart(base), seedArgs()) {
		t.Errorf("built seed differs from seedArgs()")
	}
}

func TestBuildGoldenImage_BunAndSeedOrdering(t *testing.T) {
	argv := virtCustomizeArgv(t, func(*BuildGoldenConfig) {})
	pos := func(pred func(string) bool, what string) int {
		i := indexOf(argv, pred)
		if i < 0 {
			t.Fatalf("no %s in argv %q", what, argv)
		}
		return i
	}
	install := pos(func(a string) bool { return a == "--install" }, "--install")
	upload := pos(func(a string) bool { return strings.HasSuffix(a, ":"+bunGuestPath) }, "bun upload")
	chmod := pos(func(a string) bool { return a == "0755:"+bunGuestPath }, "bun chmod")
	gate := pos(func(a string) bool { return strings.Contains(a, "seed step failed: bun-version") }, "bun gate")
	market := pos(func(a string) bool { return strings.Contains(a, "claude plugin marketplace add") }, "marketplace add")
	plugin := pos(func(a string) bool { return strings.Contains(a, "claude plugin install") }, "plugin install")
	manifest := pos(func(a string) bool { return strings.Contains(a, "seed step failed: plugin-manifest") }, "manifest gate")
	cleanup := pos(func(a string) bool {
		return strings.HasSuffix(a, "claude-code.list") && strings.HasPrefix(a, "/etc/apt/sources")
	}, "apt source cleanup")
	useradd := pos(func(a string) bool { return strings.HasPrefix(a, "useradd") }, "useradd")

	order := []struct {
		name string
		idx  int
	}{{"install", install}, {"bun upload", upload}, {"bun chmod", chmod}, {"bun gate", gate}, {"marketplace add", market}, {"plugin install", plugin}, {"manifest gate", manifest}}
	for i := 1; i < len(order); i++ {
		if order[i-1].idx >= order[i].idx {
			t.Errorf("%s (%d) must come before %s (%d)", order[i-1].name, order[i-1].idx, order[i].name, order[i].idx)
		}
	}
	if manifest >= cleanup || manifest >= useradd {
		t.Error("seed must finish before the apt cleanup and useradd")
	}
	for _, a := range argv {
		if strings.Contains(a, "bun install") || strings.Contains(a, "curl") || strings.Contains(a, "unzip") {
			t.Errorf("appliance must not fetch or unpack bun itself: %q", a)
		}
	}
}

func TestBuildGoldenImage_NoNodeJS(t *testing.T) {
	argv := virtCustomizeArgv(t, func(*BuildGoldenConfig) {})
	i := indexOf(argv, func(a string) bool { return a == "--install" })
	if got := argv[i+1]; strings.Contains(got, "nodejs") || strings.Contains(got, "npm") {
		t.Errorf("--install %q still lists Node.js", got)
	}
	// An explicit user choice still works; it is just a package.
	argv = virtCustomizeArgv(t, func(c *BuildGoldenConfig) { c.Packages = []string{"nodejs", "npm", "git"} })
	i = indexOf(argv, func(a string) bool { return a == "--install" })
	if got := argv[i+1]; got != "nodejs,npm,git,claude-code" {
		t.Errorf("--install = %q, want user packages kept without duplicating git", got)
	}
}

// --- version gates and step naming (executed for real) ---

func TestBunVersionCheck(t *testing.T) {
	tests := []struct {
		out    string
		wantOK bool
	}{
		{"1.4.4", true},
		{"1.4.4\n", true},
		{"1.4.40", false},
		{"1.4.", false},
		{"1.4", false},
		{"v1.4.4", false},
		{"1.4.4 extra", false},
		{"1.4.4\nextra", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.out), func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "bun")
			stub := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", tt.out)
			if err := os.WriteFile(bin, []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			stderr, err := shRun(t, nil, seedStep("bun-version", bunVersionCheck(bin, "1.4.4")))
			if tt.wantOK && err != nil {
				t.Errorf("gate failed for %q: %v (%s)", tt.out, err, stderr)
			}
			if !tt.wantOK {
				if err == nil {
					t.Errorf("gate passed for %q, want failure", tt.out)
				}
				if failedSeedStep(stderr) != "bun-version" {
					t.Errorf("stderr %q does not name the failing step", stderr)
				}
			}
		})
	}
	t.Run("missing_binary", func(t *testing.T) {
		_, err := shRun(t, nil, seedStep("bun-version", bunVersionCheck(filepath.Join(t.TempDir(), "absent"), "1.4.4")))
		if err == nil {
			t.Error("gate passed without a bun binary")
		}
	})
}

func TestSeedStep(t *testing.T) {
	stderr, err := shRun(t, nil, seedStep("ok-step", "true"))
	if err != nil || stderr != "" {
		t.Errorf("passing step: err=%v stderr=%q, want silent success", err, stderr)
	}
	stderr, err = shRun(t, nil, seedStep("bad-step", "echo noise >&2; false"))
	if err == nil {
		t.Fatal("failing step exited 0")
	}
	if got := failedSeedStep(stderr); got != "bad-step" {
		t.Errorf("failedSeedStep(%q) = %q, want bad-step", stderr, got)
	}
	if failedSeedStep("nothing relevant") != "" {
		t.Error("failedSeedStep matched unrelated output")
	}
}

func TestBuildGoldenImage_NamesFailingSeedStep(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		switch name {
		case "cp":
			_ = os.WriteFile(args[len(args)-1], []byte("fake"), 0644)
		case "qemu-img":
			return `{"format": "qcow2"}`, "", nil
		case "virt-customize":
			return "", "virt-customize: error\n" + seedStepMarker + "plugin-install\n", fmt.Errorf("virt-customize failed")
		}
		return "", "", nil
	})
	err := BuildGoldenImage(goldenBuildConfig(dir, checksum))
	if err == nil || !strings.Contains(err.Error(), `golden seed step "plugin-install" failed`) {
		t.Errorf("error = %v, want it to name the plugin-install seed step", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "golden.qcow2")); !os.IsNotExist(serr) {
		t.Error("golden replaced after a failed seed")
	}
}

func TestLogSeedLines(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	logSeedLines("[ 1.0] noise\nmotoko-seed bun-version: 1.4.4\nmotoko-seedling not ours\nmotoko-seed marketplace-commit: \"gitCommitSha\": \"abc\"")
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	want := "motoko-seed bun-version: 1.4.4\nmotoko-seed marketplace-commit: \"gitCommitSha\": \"abc\"\n"
	if string(out) != want {
		t.Errorf("logged %q, want %q", out, want)
	}
}

// --- manifest gate (executed against a real tree) ---

type seedTree struct {
	seed, manifest, plugin string
}

// newSeedTree lays out a seed that satisfies the gate for version 0.0.7.
func newSeedTree(t *testing.T) seedTree {
	t.Helper()
	root := t.TempDir()
	st := seedTree{
		seed:     filepath.Join(root, "seed"),
		manifest: filepath.Join(root, "manifest.sha256"),
	}
	st.plugin = filepath.Join(st.seed, "cache/claude-plugins-official/telegram/0.0.7")
	files := map[string]string{
		".mcp.json":                  `{"a":1}`,
		"package.json":               `{"b":2}`,
		"server.ts":                  "console.log(1)\n",
		"skills/access/SKILL.md":     "# access\n",
		".claude-plugin/plugin.json": `{"name":"telegram"}`,
	}
	var lines []string
	for rel, content := range files {
		p := filepath.Join(st.plugin, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(content))
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+rel)
	}
	sort.Strings(lines)
	if err := os.WriteFile(st.manifest, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Not covered by the manifest.
	if err := os.MkdirAll(filepath.Join(st.plugin, "node_modules/grammy"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.plugin, "node_modules/grammy/x.js"), []byte("dep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.plugin, ".in_use"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	return st
}

// Indexes into telegramGateChecks.
const (
	gatePass     = -1
	gateCache    = 0
	gateVersion  = 1
	gateCD       = 2
	gateHashes   = 3
	gateFileSet  = 4
	gateNumTotal = 5
)

func runGateChecks(checks []string) error {
	sh, _ := exec.LookPath("sh")
	cmd := exec.Command(sh, "-c", strings.Join(checks, " && "))
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	return cmd.Run()
}

func (st seedTree) checks() []string {
	return telegramGateChecks(st.seed, st.manifest, telegramPluginVersion)
}

func (st seedTree) gate() error { return runGateChecks(st.checks()) }

// assertRejectedAt checks that exactly check number failAt rejects the tree:
// every earlier check passes on its own and the chain up to failAt fails. A
// typo anywhere in the gate (a wrong path in cd, say) therefore cannot make a
// negative case pass for the wrong reason. gatePass means the whole gate passes.
func assertRejectedAt(t *testing.T, st seedTree, failAt int) {
	t.Helper()
	checks := st.checks()
	if len(checks) != gateNumTotal {
		t.Fatalf("gate has %d checks, test knows %d", len(checks), gateNumTotal)
	}
	if failAt == gatePass {
		if err := st.gate(); err != nil {
			t.Errorf("gate rejected an acceptable seed: %v", err)
		}
		return
	}
	for i := 0; i < failAt; i++ {
		if err := runGateChecks(checks[:i+1]); err != nil {
			t.Errorf("check %d failed but the tree should only be rejected at check %d: %v", i, failAt, err)
		}
	}
	if err := runGateChecks(checks[:failAt+1]); err == nil {
		t.Errorf("check %d accepted a tree that must be rejected there", failAt)
	}
	if err := st.gate(); err == nil {
		t.Error("gate passed on a tree that differs from the manifest")
	}
}

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0755))
	must(t, os.WriteFile(path, []byte(content), 0644))
}

func TestTelegramGate(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	extra := func(rel string) func(*testing.T, seedTree) {
		return func(t *testing.T, st seedTree) { writeAt(t, filepath.Join(st.plugin, rel), "x") }
	}
	tests := []struct {
		name   string
		mutate func(t *testing.T, st seedTree)
		failAt int
	}{
		{"exact_match", func(*testing.T, seedTree) {}, gatePass},

		// Exempt: anything below the top-level node_modules, and the top-level marker.
		{"top_level_node_modules_and_in_use_ignored", func(t *testing.T, st seedTree) {
			writeAt(t, filepath.Join(st.plugin, "node_modules/grammy/y.js"), "more")
			writeAt(t, filepath.Join(st.plugin, "node_modules/a/b/c/d/e.js"), "deep")
			writeAt(t, filepath.Join(st.plugin, "node_modules/.bin/tool"), "x")
		}, gatePass},

		{"one_byte_changed", func(t *testing.T, st seedTree) {
			writeAt(t, filepath.Join(st.plugin, "server.ts"), "console.log(2)\n")
		}, gateHashes},
		{"one_byte_appended_to_config", func(t *testing.T, st seedTree) {
			f, err := os.OpenFile(filepath.Join(st.plugin, ".mcp.json"), os.O_APPEND|os.O_WRONLY, 0)
			must(t, err)
			_, err = f.WriteString(" ")
			must(t, err)
			must(t, f.Close())
		}, gateHashes},
		{"missing_file", func(t *testing.T, st seedTree) {
			must(t, os.Remove(filepath.Join(st.plugin, "package.json")))
		}, gateHashes},

		{"extra_file", extra("evil.js"), gateFileSet},
		{"extra_hidden_file", extra(".hooks"), gateFileSet},
		{"extra_file_in_subdir", extra("skills/access/extra.md"), gateFileSet},
		{"extra_symlink", func(t *testing.T, st seedTree) {
			must(t, os.Symlink("/etc/passwd", filepath.Join(st.plugin, "link")))
		}, gateFileSet},

		// The exemptions must not be loosened: look-alikes of node_modules and
		// of the marker, and nested copies of either, are all extra files.
		{"node_modules2_dir", extra("node_modules2/x.js"), gateFileSet},
		{"node_modules_js_file", extra("node_modules.js"), gateFileSet},
		{"nested_node_modules", extra("skills/node_modules/evil.js"), gateFileSet},
		{"in_use2", extra(".in_use2"), gateFileSet},
		{"nested_in_use", extra("sub/.in_use"), gateFileSet},

		// Names a sloppy shell comparison would mangle.
		{"name_with_space", extra("a b"), gateFileSet},
		{"name_star", extra("*"), gateFileSet},
		{"name_dash_rf", extra("-rf"), gateFileSet},
		{"name_with_newline", extra("x\ny"), gateFileSet},
		{"name_brackets", extra("[a]"), gateFileSet},

		{"wrong_version_dir", func(t *testing.T, st seedTree) {
			must(t, os.Rename(st.plugin, filepath.Join(filepath.Dir(st.plugin), "0.0.8")))
		}, gateVersion},
		{"second_version_dir", func(t *testing.T, st seedTree) {
			must(t, os.MkdirAll(filepath.Join(filepath.Dir(st.plugin), "0.0.8"), 0755))
		}, gateVersion},
		{"second_marketplace", func(t *testing.T, st seedTree) {
			must(t, os.MkdirAll(filepath.Join(st.seed, "cache/other"), 0755))
		}, gateCache},
		{"empty_seed", func(t *testing.T, st seedTree) {
			must(t, os.RemoveAll(filepath.Join(st.seed, "cache")))
			must(t, os.MkdirAll(filepath.Join(st.seed, "cache"), 0755))
		}, gateCache},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newSeedTree(t)
			// Same tree, unmutated: the gate accepts it, so a rejection below is
			// caused by the mutation and not by the fixture or a broken gate.
			if err := st.gate(); err != nil {
				t.Fatalf("unmutated fixture rejected: %v", err)
			}
			tt.mutate(t, st)
			assertRejectedAt(t, st, tt.failAt)
		})
	}
}

// The real manifest's paths (dot directories included) must go through the
// sha256sum + sed comparison: a tree holding every listed file with wrong
// content fails the hash check; the same tree with a manifest copy rewritten
// to the content's hashes passes the whole gate.
func TestTelegramGate_EmbeddedManifest(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	st := seedTree{
		seed:     filepath.Join(root, "seed"),
		manifest: filepath.Join(root, "m.sha256"),
		plugin:   filepath.Join(root, "seed/cache/claude-plugins-official/telegram/"+telegramPluginVersion),
	}
	must(t, os.WriteFile(st.manifest, []byte(telegramManifest), 0644))

	const content = "not the real file\n"
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(telegramManifest), "\n") {
		_, rel, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("malformed manifest line %q", line)
		}
		writeAt(t, filepath.Join(st.plugin, rel), content)
		paths = append(paths, rel)
	}
	if !slices.Contains(paths, ".claude-plugin/plugin.json") {
		t.Fatalf("embedded manifest has no dot-directory path to exercise: %v", paths)
	}

	assertRejectedAt(t, st, gateHashes)

	sum := sha256.Sum256([]byte(content))
	var rewritten strings.Builder
	for _, rel := range paths {
		rewritten.WriteString(hex.EncodeToString(sum[:]) + "  " + rel + "\n")
	}
	must(t, os.WriteFile(st.manifest, []byte(rewritten.String()), 0644))
	assertRejectedAt(t, st, gatePass)
}

// --- permissions ---

func TestSeedPermsCheck(t *testing.T) {
	tests := []struct {
		name   string
		mode   os.FileMode
		wantOK bool
	}{
		{"dir_0755", 0755, true},
		{"dir_0775_group_writable", 0775, false},
		{"dir_0757_world_writable", 0757, false},
		{"dir_0777", 0777, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "seed")
			must(t, os.MkdirAll(filepath.Join(dir, "sub"), 0755))
			must(t, os.Chmod(dir, 0755))
			must(t, os.WriteFile(filepath.Join(dir, "sub/f"), []byte("x"), 0644))
			must(t, os.Chmod(filepath.Join(dir, "sub"), tt.mode))
			_, err := shRun(t, nil, seedPermsCheck(dir))
			if tt.wantOK != (err == nil) {
				t.Errorf("check ok=%v, want %v", err == nil, tt.wantOK)
			}
		})
	}
	t.Run("file_0664", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "f")
		must(t, os.WriteFile(f, []byte("x"), 0600))
		must(t, os.Chmod(f, 0664))
		if _, err := shRun(t, nil, seedPermsCheck(dir)); err == nil {
			t.Error("check passed with a group-writable file")
		}
	})
	t.Run("symlink_ignored", func(t *testing.T) {
		dir := t.TempDir()
		must(t, os.Chmod(dir, 0755))
		must(t, os.Symlink("/etc", filepath.Join(dir, "l")))
		if _, err := shRun(t, nil, seedPermsCheck(dir)); err != nil {
			t.Errorf("check rejected a symlink: %v", err)
		}
	})
}

// The fix step's chmod keeps executables executable and never grants write.
func TestSeedPermsFix_ChmodSemantics(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seed")
	must(t, os.MkdirAll(filepath.Join(dir, "d"), 0777))
	must(t, os.WriteFile(filepath.Join(dir, "d/plain"), []byte("x"), 0666))
	must(t, os.WriteFile(filepath.Join(dir, "d/exec"), []byte("x"), 0777))
	must(t, os.Chmod(filepath.Join(dir, "d"), 0777))
	must(t, os.Chmod(filepath.Join(dir, "d/plain"), 0666))
	must(t, os.Chmod(filepath.Join(dir, "d/exec"), 0777))
	// chown needs root; run only the chmod half, as it appears in the step.
	chmod := strings.SplitN(seedPermsFix(dir), " && ", 2)[1]
	if _, err := shRun(t, nil, chmod); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	for path, want := range map[string]os.FileMode{"d": 0755, "d/plain": 0644, "d/exec": 0755} {
		st, err := os.Stat(filepath.Join(dir, path))
		must(t, err)
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	if _, err := shRun(t, nil, seedPermsCheck(dir)); err != nil {
		t.Errorf("fixed tree fails the check: %v", err)
	}
	if !strings.HasPrefix(seedPermsFix(dir), "chown -R root:root ") {
		t.Error("fix step does not make the seed root-owned")
	}
}

// --- bun download ---

func makeZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		must(t, err)
		_, err = w.Write(data)
		must(t, err)
	}
	must(t, zw.Close())
	return buf.Bytes()
}

func fixturePin(zipBytes []byte) bunPin {
	sum := sha256.Sum256(zipBytes)
	return bunPin{
		Version: "9.9.9",
		URL:     "https://example.com/bun-v9.9.9/bun-linux-x64-baseline.zip",
		SHA256:  hex.EncodeToString(sum[:]),
		Entry:   "bun-linux-x64-baseline/bun",
	}
}

// serve answers every request with status and body and counts requests.
func serve(t *testing.T, status int, body []byte) *int {
	t.Helper()
	n := new(int)
	mockHTTPClient(t, func(*http.Request) *http.Response {
		*n++
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}
	})
	return n
}

func TestEnsureBun(t *testing.T) {
	bunBytes := []byte("#!bun-binary")
	good := makeZip(t, map[string][]byte{"bun-linux-x64-baseline/bun": bunBytes})
	bad := []byte("not the pinned zip")
	pin := fixturePin(good)

	cachePath := func(dir string) string {
		return filepath.Join(dir, "bun-"+pin.Version+"-bun-linux-x64-baseline.zip")
	}

	type result struct {
		wantErr    string
		wantHTTP   int
		wantCached []byte // content of the cached zip afterwards; nil = absent
	}
	tests := []struct {
		name   string
		cached []byte // nil = no cached file
		status int
		body   []byte
		want   result
	}{
		{"cached_good", good, 500, nil, result{wantHTTP: 0, wantCached: good}},
		{"cached_bad_redownload_good", bad, 200, good, result{wantHTTP: 1, wantCached: good}},
		{"cached_bad_redownload_bad", bad, 200, bad, result{wantErr: "checksum mismatch", wantHTTP: 1, wantCached: nil}},
		{"fresh_good", nil, 200, good, result{wantHTTP: 1, wantCached: good}},
		{"fresh_bad", nil, 200, bad, result{wantErr: "checksum mismatch", wantHTTP: 1, wantCached: nil}},
		{"http_404", nil, 404, nil, result{wantErr: "HTTP 404", wantHTTP: 1, wantCached: nil}},
		{"http_500_after_stale_cache", bad, 500, nil, result{wantErr: "HTTP 500", wantHTTP: 1, wantCached: nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.cached != nil {
				must(t, os.WriteFile(cachePath(dir), tt.cached, 0644))
			}
			n := serve(t, tt.status, tt.body)

			path, err := ensureBun(dir, pin)
			if tt.want.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.want.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				defer func() { _ = os.Remove(path) }()
				got, rerr := os.ReadFile(path)
				must(t, rerr)
				if !bytes.Equal(got, bunBytes) {
					t.Errorf("extracted %q, want %q", got, bunBytes)
				}
			}
			if *n != tt.want.wantHTTP {
				t.Errorf("HTTP requests = %d, want %d", *n, tt.want.wantHTTP)
			}
			cached, cerr := os.ReadFile(cachePath(dir))
			switch {
			case tt.want.wantCached == nil && cerr == nil:
				t.Errorf("a bad zip was left cached: %q", cached)
			case tt.want.wantCached != nil && !bytes.Equal(cached, tt.want.wantCached):
				t.Errorf("cached zip = %q (err %v), want the good zip", cached, cerr)
			}
			leftovers, _ := filepath.Glob(filepath.Join(dir, ".download-*"))
			if len(leftovers) > 0 {
				t.Errorf("temp downloads left behind: %v", leftovers)
			}
		})
	}
}

func TestEnsureBun_BadZips(t *testing.T) {
	full := makeZip(t, map[string][]byte{"bun-linux-x64-baseline/bun": []byte("ok")})
	tests := []struct {
		name    string
		zip     []byte
		wantErr string
	}{
		{"missing_entry", makeZip(t, map[string][]byte{"other/bun": []byte("x")}), "no bun-linux-x64-baseline/bun entry"},
		{"similar_entry_name", makeZip(t, map[string][]byte{"bun-linux-x64-baseline/bun.sig": []byte("x")}), "no bun-linux-x64-baseline/bun entry"},
		{"truncated", full[:len(full)/2], "opening"},
		{"empty", nil, "opening"},
		{"not_a_zip", []byte("PK but not really"), "opening"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			pin := fixturePin(tt.zip) // the checksum matches: the zip itself is bad
			serve(t, 200, tt.zip)
			path, err := ensureBun(dir, pin)
			if err == nil {
				_ = os.Remove(path)
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestEnsureBun_OversizedEntry(t *testing.T) {
	orig := maxBunBytes
	maxBunBytes = 8
	t.Cleanup(func() { maxBunBytes = orig })

	atLimit := makeZip(t, map[string][]byte{"bun-linux-x64-baseline/bun": bytes.Repeat([]byte("a"), 8)})
	serve(t, 200, atLimit)
	path, err := ensureBun(t.TempDir(), fixturePin(atLimit))
	if err != nil {
		t.Fatalf("entry at the limit rejected: %v", err)
	}
	_ = os.Remove(path)

	over := makeZip(t, map[string][]byte{"bun-linux-x64-baseline/bun": bytes.Repeat([]byte("a"), 9)})
	serve(t, 200, over)
	if path, err := ensureBun(t.TempDir(), fixturePin(over)); err == nil {
		_ = os.Remove(path)
		t.Error("entry over the limit accepted")
	}
}

// A failed bun fetch must stop the build before anything touches the image or
// libvirt, and leave the existing golden image alone.
func TestBuildGoldenImage_BunFailureStopsBuild(t *testing.T) {
	good := makeZip(t, map[string][]byte{"bun-linux-x64-baseline/bun": []byte("ok")})
	badZip := []byte("garbage")
	pin := fixturePin(good)
	tests := []struct {
		name   string
		status int
		body   []byte
	}{
		{"fresh_checksum_mismatch", 200, badZip},
		{"http_error", 503, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			checksum := writeBaseImage(t, dir)
			golden := filepath.Join(dir, "golden.qcow2")
			must(t, os.WriteFile(golden, []byte("previous golden"), 0444))
			recs := goldenMockRunner(t)
			serve(t, tt.status, tt.body)

			orig := fetchBun
			fetchBun = func(d string) (string, error) { return ensureBun(d, pin) }
			t.Cleanup(func() { fetchBun = orig })

			if err := BuildGoldenImage(goldenBuildConfig(dir, checksum)); err == nil {
				t.Fatal("expected error")
			}
			for _, r := range *recs {
				if r.name == "virt-customize" || r.name == "cp" || r.name == "qemu-img" {
					t.Errorf("%s ran despite the bun failure", r.name)
				}
			}
			got, _ := os.ReadFile(golden)
			if string(got) != "previous golden" {
				t.Errorf("golden was replaced: %q", got)
			}
			assertNoBakeLeft(t, dir)
		})
	}
}

func TestBuildGoldenImage_BunTempRemoved(t *testing.T) {
	var path string
	orig := fetchBun
	fetchBun = func(string) (string, error) {
		f, err := os.CreateTemp("", "motoko-bun-test-*")
		must(t, err)
		_ = f.Close()
		path = f.Name()
		return path, nil
	}
	t.Cleanup(func() { fetchBun = orig })

	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	goldenMockRunner(t)
	must(t, BuildGoldenImage(goldenBuildConfig(dir, checksum)))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("extracted bun %s left behind (stat err: %v)", path, err)
	}
}

func TestWithPackages(t *testing.T) {
	tests := []struct {
		base, extra, want []string
	}{
		{nil, []string{"git", "claude-code"}, []string{"git", "claude-code"}},
		{[]string{"git"}, []string{"git", "claude-code"}, []string{"git", "claude-code"}},
		{[]string{"vim", "git"}, []string{"git", "claude-code"}, []string{"vim", "git", "claude-code"}},
		{[]string{"claude-code"}, []string{"git", "claude-code"}, []string{"claude-code", "git"}},
	}
	for _, tt := range tests {
		got := withPackages(tt.base, tt.extra...)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("withPackages(%v, %v) = %v, want %v", tt.base, tt.extra, got, tt.want)
		}
	}
	base := []string{"a"}
	_ = withPackages(base, "b")
	if len(base) != 1 {
		t.Error("withPackages modified its input")
	}
}

// --- reading the bake's info back from the image ---

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

type infoBake struct {
	info       string // virt-cat stdout
	catErr     error
	firstErr   error  // first virt-customize
	firstStder string // first virt-customize stderr
	scrubErr   error
}

// run builds with the mocked tools and returns the error, recorded calls and
// what the build logged to stderr.
func (b infoBake) run(t *testing.T) ([]execRecord, string, string, error) {
	t.Helper()
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	customizeCalls := 0
	recs := mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		switch name {
		case "cp":
			_ = os.WriteFile(args[len(args)-1], []byte("fake"), 0644)
		case "qemu-img":
			return `{"format": "qcow2"}`, "", nil
		case "virt-customize":
			customizeCalls++
			if customizeCalls == 1 {
				return "", b.firstStder, b.firstErr
			}
			return "", "", b.scrubErr
		case "virt-cat":
			return b.info, "", b.catErr
		}
		return "", "", nil
	})
	var err error
	logged := captureStderr(t, func() { err = BuildGoldenImage(goldenBuildConfig(dir, checksum)) })
	return *recs, logged, dir, err
}

func customizeCalls(recs []execRecord) int {
	n := 0
	for _, r := range recs {
		if r.name == "virt-customize" {
			n++
		}
	}
	return n
}

func TestBuildGoldenImage_SeedInfoReadBack(t *testing.T) {
	info := "motoko-seed bun-version: 1.4.4\nmotoko-seed marketplace-commit: \"gitCommitSha\": \"abc\"\n"

	t.Run("success_logs_lines_and_scrubs", func(t *testing.T) {
		recs, logged, dir, err := infoBake{info: info}.run(t)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(logged, "motoko-seed bun-version: 1.4.4") || !strings.Contains(logged, "marketplace-commit") {
			t.Errorf("info lines not logged: %q", logged)
		}
		var names []string
		var cat, scrub []string
		for _, r := range recs {
			names = append(names, r.name)
			if r.name == "virt-cat" {
				cat = r.args
			}
		}
		last := -1
		for i, r := range recs {
			if r.name == "virt-customize" {
				last = i
			}
		}
		scrub = recs[last].args
		wantNames := []string{"cp", "qemu-img", "virt-customize", "virt-cat", "virt-customize", "guestfish", "qemu-img"}
		if strings.Join(names, " ") != strings.Join(wantNames, " ") {
			t.Fatalf("calls = %v, want %v", names, wantNames)
		}
		if len(cat) != 5 || cat[0] != "--connect" || cat[2] != "-a" || !strings.Contains(filepath.Base(cat[3]), ".golden-bake-") || cat[4] != "/var/tmp/motoko-seed.info" {
			t.Errorf("virt-cat argv = %q", cat)
		}
		wantTail := []string{"--delete", "/var/tmp/motoko-seed.info", "--delete", "/tmp/builder.log"}
		if !reflect.DeepEqual(scrub[len(scrub)-4:], wantTail) {
			t.Errorf("scrub argv = %q, want tail %q", scrub, wantTail)
		}
		if scrub[3] != cat[3] {
			t.Errorf("scrub works on %q, virt-cat read %q: must be the same bake image", scrub[3], cat[3])
		}
		if _, serr := os.Stat(filepath.Join(dir, "golden.qcow2")); serr != nil {
			t.Errorf("golden not installed: %v", serr)
		}
	})

	// Every row is a build that must not install a golden, must not leave a
	// temp image behind and must not go on to the filesystem check.
	failures := []struct {
		name       string
		bake       infoBake
		wantErr    string
		notErr     string
		wantLogged string
		wantScrub  bool // the scrub virt-customize pass ran
	}{
		{
			name: "step_named_from_info_file",
			// virt-customize's own stderr carries no marker; the file does.
			bake:       infoBake{info: info + seedStepMarker + "plugin-manifest\n", firstErr: fmt.Errorf("virt-customize failed")},
			wantErr:    `golden seed step "plugin-manifest" failed`,
			wantLogged: "bun-version: 1.4.4",
		},
		{
			name: "stderr_marker_wins_over_info_file",
			bake: infoBake{
				info:       info + seedStepMarker + "plugin-manifest\n",
				firstStder: seedStepMarker + "plugin-install\n",
				firstErr:   fmt.Errorf("virt-customize failed"),
			},
			wantErr: `golden seed step "plugin-install" failed`,
			notErr:  "plugin-manifest",
		},
		{
			name:    "no_marker_anywhere",
			bake:    infoBake{info: info, firstErr: fmt.Errorf("virt-customize failed")},
			wantErr: "customizing image: virt-customize failed",
			notErr:  "golden seed step",
		},
		{
			name:    "bake_failure_and_unreadable_info_keeps_original_error",
			bake:    infoBake{catErr: fmt.Errorf("virt-cat: no such file"), firstErr: fmt.Errorf("virt-customize failed")},
			wantErr: "virt-customize failed",
			notErr:  "virt-cat",
		},
		{
			name:    "bake_ok_but_info_unreadable",
			bake:    infoBake{catErr: fmt.Errorf("virt-cat: boom")},
			wantErr: "reading seed info",
		},
		{
			name:      "scrub_failure",
			bake:      infoBake{info: info, scrubErr: fmt.Errorf("scrub failed")},
			wantErr:   "removing build logs",
			wantScrub: true,
		},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			recs, logged, dir, err := tt.bake.run(t)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if tt.notErr != "" && strings.Contains(err.Error(), tt.notErr) {
				t.Errorf("error %q must not contain %q", err, tt.notErr)
			}
			if tt.wantLogged != "" && !strings.Contains(logged, tt.wantLogged) {
				t.Errorf("info lines not logged: %q", logged)
			}
			wantCalls := 1
			if tt.wantScrub {
				wantCalls = 2
			}
			if got := customizeCalls(recs); got != wantCalls {
				t.Errorf("virt-customize ran %d times, want %d (scrub only after a good bake)", got, wantCalls)
			}
			for _, r := range recs {
				if r.name == "guestfish" {
					t.Error("build went on to the filesystem check")
				}
			}
			if _, serr := os.Stat(filepath.Join(dir, "golden.qcow2")); !os.IsNotExist(serr) {
				t.Error("golden installed")
			}
			assertNoBakeLeft(t, dir)
		})
	}
}

// --- bun zip cache pruning ---

func TestEnsureBun_PrunesOtherVersions(t *testing.T) {
	good := makeZip(t, map[string][]byte{"bun-linux-x64-baseline/bun": []byte("ok")})
	pin := fixturePin(good)
	asset := filepath.Base(pin.URL)
	current := "bun-" + pin.Version + "-" + asset

	dir := t.TempDir()
	prune := []string{"bun-1.0.0-" + asset, "bun-1.3.12-" + asset}
	keep := []string{
		current,
		"debian-12-claude-golden.qcow2",
		"bun-1.0.0-" + asset + ".bak", // suffix: not an exact asset match
		"xbun-1.0.0-" + asset,         // prefix
		"bun-1.0.0-other-asset.zip",   // different asset
		"bun-" + asset,                // no version
		"bun--" + asset,               // empty version
		"my-bun-1.0.0-" + asset,       // prefix
	}
	for _, n := range append(append([]string{}, prune...), keep...) {
		must(t, os.WriteFile(filepath.Join(dir, n), good, 0644))
	}
	// A directory that matches the pattern is not a cached zip.
	must(t, os.Mkdir(filepath.Join(dir, "bun-0.9.0-"+asset), 0755))
	keep = append(keep, "bun-0.9.0-"+asset)
	// A symlink that matches must not be followed or removed as a file target.
	outside := filepath.Join(t.TempDir(), "precious")
	must(t, os.WriteFile(outside, []byte("data"), 0644))
	must(t, os.Symlink(outside, filepath.Join(dir, "bun-0.8.0-"+asset)))

	serve(t, 500, nil)
	path, err := ensureBun(dir, pin)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)

	for _, n := range prune {
		if _, err := os.Lstat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s was not pruned", n)
		}
	}
	for _, n := range keep {
		if _, err := os.Lstat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s must survive: %v", n, err)
		}
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "data" {
		t.Error("symlink target was touched")
	}
}

func TestEnsureBun_FailureDoesNotPrune(t *testing.T) {
	good := makeZip(t, map[string][]byte{"bun-linux-x64-baseline/bun": []byte("ok")})
	pin := fixturePin(good)
	dir := t.TempDir()
	older := filepath.Join(dir, "bun-1.0.0-"+filepath.Base(pin.URL))
	must(t, os.WriteFile(older, good, 0644))
	serve(t, 404, nil)
	if path, err := ensureBun(dir, pin); err == nil {
		_ = os.Remove(path)
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(older); err != nil {
		t.Error("an older zip was removed although the new one could not be fetched")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
