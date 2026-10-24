//go:build apt

package vm

// These tests run real apt against Anthropic's repo using the files
// BuildGoldenImage installs. Unit tests only see virt-customize argv: an
// armored signed-by key passed all of them and failed the first real build.
//
// Run with `make test-apt`, which executes this binary inside debian:12-slim
// (apt 2.6, gpgv but no gnupg, like the cloud image) as a non-root user. It
// needs network access; an unreachable repo fails the run, never skips it.
//
// Not covered: apt runs as the invoking user here, not _apt, so whether the
// --chmod 0644 lands in the image and the post-install deletes are pinned by
// the argv unit tests only. TestApt_HarnessDetectsUnreadableKey shows why the
// chmod matters.

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// debianArchiveKeyring is a valid key that did not sign Anthropic's repo.
const debianArchiveKeyring = "/usr/share/keyrings/debian-archive-keyring.gpg"

var debVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-\S+$`)

// aptRoot is a private apt configuration: its own sources, preferences, lists
// and cache, so apt runs unprivileged and no state leaks between tests.
type aptRoot struct {
	dir  string
	conf string
}

// newAptRoot configures apt with motoko's source line for channel, trusting
// key (written as keyName) and, if pin is set, motoko's pin file.
func newAptRoot(t *testing.T, channel string, keyName string, key []byte, pin string) *aptRoot {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"keyrings", "apt.conf.d", "sources.list.d", "preferences.d", "trusted.gpg.d",
		"state/lists/partial", "cache/archives/partial", "log", "dl"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	keyring := filepath.Join(dir, "keyrings", keyName)
	writeTestFile(t, keyring, key)

	line := claudeSourceLine(channel)
	if strings.Count(line, claudeKeyringPath) != 1 {
		t.Fatalf("source line no longer names %s exactly once: %q", claudeKeyringPath, line)
	}
	writeTestFile(t, filepath.Join(dir, "sources.list"), []byte(strings.Replace(line, claudeKeyringPath, keyring, 1)))
	if pin != "" {
		writeTestFile(t, filepath.Join(dir, "preferences.d", filepath.Base(claudePinPath)), []byte(claudePin(pin)))
	}
	writeTestFile(t, filepath.Join(dir, "state", "status"), nil)

	// APT_CONFIG is read before apt.conf.d, so Dir::Etc::parts here also keeps
	// the image's docker-clean hooks out.
	conf := fmt.Sprintf(`Dir::Etc::parts "%[1]s/apt.conf.d";
Dir::Etc::sourcelist "%[1]s/sources.list";
Dir::Etc::sourceparts "%[1]s/sources.list.d";
Dir::Etc::preferences "%[1]s/none";
Dir::Etc::preferencesparts "%[1]s/preferences.d";
Dir::Etc::trusted "%[1]s/none";
Dir::Etc::trustedparts "%[1]s/trusted.gpg.d";
Dir::State "%[1]s/state";
Dir::State::status "%[1]s/state/status";
Dir::Cache "%[1]s/cache";
Dir::Cache::pkgcache "";
Dir::Cache::srcpkgcache "";
Dir::Log "%[1]s/log";
Debug::NoLocking "true";
Acquire::Languages "none";
Acquire::Retries "3";
Acquire::http::Timeout "30";
Acquire::https::Timeout "30";
Acquire::AllowInsecureRepositories "false";
APT::Update::Error-Mode "any";
`, dir)
	confPath := filepath.Join(dir, "apt.conf")
	writeTestFile(t, confPath, []byte(conf))
	return &aptRoot{dir: dir, conf: confPath}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func (r *aptRoot) run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = filepath.Join(r.dir, "dl")
	cmd.Env = append(os.Environ(), "APT_CONFIG="+r.conf, "LC_ALL=C")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func (r *aptRoot) mustUpdate(t *testing.T) {
	t.Helper()
	if out, err := r.run("apt-get", "update"); err != nil {
		t.Fatalf("apt-get update: %v\n%s", err, out)
	}
}

// candidate is the version apt would install, or "" if none.
func (r *aptRoot) candidate(t *testing.T) string {
	t.Helper()
	out, err := r.run("apt-cache", "policy", "claude-code")
	if err != nil {
		t.Fatalf("apt-cache policy: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^\s*Candidate:\s*(\S+)`).FindStringSubmatch(out)
	if m == nil || m[1] == "(none)" {
		return ""
	}
	return m[1]
}

// versions lists every claude-code version the configured repo offers.
func (r *aptRoot) versions(t *testing.T) []string {
	t.Helper()
	out, err := r.run("apt-cache", "madison", "claude-code")
	if err != nil {
		t.Fatalf("apt-cache madison: %v\n%s", err, out)
	}
	var vs []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Split(line, "|"); len(f) >= 2 {
			vs = append(vs, strings.TrimSpace(f[1]))
		}
	}
	return vs
}

// download fetches the candidate .deb (apt checks it against the signed index)
// and returns its path.
func (r *aptRoot) download(t *testing.T) string {
	t.Helper()
	if out, err := r.run("apt-get", "download", "claude-code"); err != nil {
		t.Fatalf("apt-get download: %v\n%s", err, out)
	}
	debs, _ := filepath.Glob(filepath.Join(r.dir, "dl", "claude-code_*.deb"))
	if len(debs) != 1 {
		t.Fatalf("want exactly one downloaded .deb, got %v", debs)
	}
	return debs[0]
}

func debField(t *testing.T, deb, field string) string {
	t.Helper()
	out, err := exec.Command("dpkg-deb", "-f", deb, field).Output()
	if err != nil {
		t.Fatalf("dpkg-deb -f %s %s: %v", deb, field, err)
	}
	return strings.TrimSpace(string(out))
}

func dearmoredKey(t *testing.T) []byte {
	t.Helper()
	key, err := dearmor(claudeCodeKey)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

var repoOnce sync.Once
var repoErr error

// requireRepo fails (not skips) when the repo is unreachable, and says so, so
// an outage is not mistaken for a verification regression.
func requireRepo(t *testing.T) {
	t.Helper()
	repoOnce.Do(func() {
		url := claudeRepoBase + "/stable/dists/stable/InRelease"
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(url)
		if err != nil {
			repoErr = err
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			repoErr = fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
		}
	})
	if repoErr != nil {
		t.Fatalf("Anthropic apt repo unreachable (outage or no network, not a verification failure): %v", repoErr)
	}
}

func TestApt_ChannelVerifiesAndInstallsSignedPackage(t *testing.T) {
	requireRepo(t)
	for _, channel := range []string{"stable", "latest"} {
		t.Run(channel, func(t *testing.T) {
			r := newAptRoot(t, channel, filepath.Base(claudeKeyringPath), dearmoredKey(t), "")
			r.mustUpdate(t)

			cand := r.candidate(t)
			if !debVersion.MatchString(cand) {
				t.Fatalf("candidate = %q, want X.Y.Z-rev (motoko's pin and version check assume it)", cand)
			}
			deb := r.download(t)
			if got := debField(t, deb, "Package"); got != "claude-code" {
				t.Errorf("downloaded package = %q", got)
			}
			if got := debField(t, deb, "Version"); got != cand {
				t.Errorf("downloaded version %q != candidate %q", got, cand)
			}
		})
	}
}

// A key that did not sign the repo must make update fail, or a passing
// verification test above would prove nothing.
func TestApt_RejectsUnrelatedKey(t *testing.T) {
	requireRepo(t)
	key, err := os.ReadFile(debianArchiveKeyring)
	if err != nil {
		t.Fatalf("reading %s: %v", debianArchiveKeyring, err)
	}
	r := newAptRoot(t, "stable", filepath.Base(claudeKeyringPath), key, "")
	if out, err := r.run("apt-get", "update"); err == nil {
		t.Fatalf("apt-get update succeeded with an unrelated key:\n%s", out)
	}
	if cand := r.candidate(t); cand != "" {
		t.Errorf("candidate %q available from an unverified repo", cand)
	}
}

// The first real build failed this way: virt-customize --upload kept the temp
// key's 0600, and apt verifies as _apt, which could not read it. Here apt runs
// as the test user, so an unreadable mode reproduces it. Validates the
// harness, not motoko.
func TestApt_HarnessDetectsUnreadableKey(t *testing.T) {
	requireRepo(t)
	if os.Geteuid() == 0 {
		t.Fatal("must run as non-root: root reads any file, hiding the failure")
	}
	r := newAptRoot(t, "stable", filepath.Base(claudeKeyringPath), dearmoredKey(t), "")
	if err := os.Chmod(filepath.Join(r.dir, "keyrings", filepath.Base(claudeKeyringPath)), 0); err != nil {
		t.Fatal(err)
	}
	if out, err := r.run("apt-get", "update"); err == nil {
		t.Fatalf("apt-get update succeeded with an unreadable key; harness no longer reproduces the _apt failure:\n%s", out)
	}
}

// Pinning an older version must beat the newest; pinning a missing one is
// silently ignored by apt, which is why the build runs claudeVersionCheck.
func TestApt_PinAndVersionCheck(t *testing.T) {
	requireRepo(t)
	key := dearmoredKey(t)

	base := newAptRoot(t, "stable", filepath.Base(claudeKeyringPath), key, "")
	base.mustUpdate(t)
	newest := base.candidate(t)
	var older string
	for _, v := range base.versions(t) {
		if v != newest {
			older = v
			break
		}
	}
	if older == "" {
		t.Fatalf("stable offers only %q; need an older version to prove the pin wins", newest)
	}
	upstream := older[:strings.LastIndex(older, "-")]
	if !validClaudeVersion.MatchString(upstream) {
		t.Fatalf("repo version %q has upstream %q, which claude_version validation would reject", older, upstream)
	}

	t.Run("existing_version_wins", func(t *testing.T) {
		r := newAptRoot(t, "stable", filepath.Base(claudeKeyringPath), key, upstream)
		r.mustUpdate(t)
		if cand := r.candidate(t); cand != older {
			t.Fatalf("pinned candidate = %q, want %q (newest is %q)", cand, older, newest)
		}
		deb := r.download(t)
		if got := debField(t, deb, "Version"); got != older {
			t.Fatalf("downloaded version %q, want %q", got, older)
		}

		// Run the build's gate against a dpkg database holding the real package.
		admin := fakeDpkgAdmin(t, deb)
		newestUpstream := newest[:strings.LastIndex(newest, "-")]
		for _, tc := range []struct {
			want   string
			wantOK bool
		}{{upstream, true}, {newestUpstream, false}} {
			cmd := exec.Command("sh", "-c", claudeVersionCheck(tc.want))
			cmd.Env = append(os.Environ(), "DPKG_ADMINDIR="+admin)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantOK {
				t.Errorf("version check want=%s on installed %s: err=%v, want ok=%v\n%s", tc.want, older, err, tc.wantOK, out)
			}
		}
	})

	t.Run("missing_version_ignored", func(t *testing.T) {
		r := newAptRoot(t, "stable", filepath.Base(claudeKeyringPath), key, "9.9.9")
		r.mustUpdate(t)
		if cand := r.candidate(t); cand != newest {
			t.Fatalf("candidate with unmatched pin = %q, want newest %q", cand, newest)
		}
	})
}

// fakeDpkgAdmin builds a dpkg database in which deb is installed, so the real
// dpkg-query answers without root.
func fakeDpkgAdmin(t *testing.T, deb string) string {
	t.Helper()
	admin := t.TempDir()
	for _, d := range []string{"info", "updates"} {
		if err := os.MkdirAll(filepath.Join(admin, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	control, err := exec.Command("dpkg-deb", "-f", deb).Output()
	if err != nil {
		t.Fatalf("dpkg-deb -f %s: %v", deb, err)
	}
	status := "Status: install ok installed\n" + strings.TrimRight(string(control), "\n") + "\n"
	writeTestFile(t, filepath.Join(admin, "status"), []byte(status))
	writeTestFile(t, filepath.Join(admin, "available"), nil)
	return admin
}
