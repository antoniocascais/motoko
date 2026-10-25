package vm

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// claudeKeySHA256 pins the embedded signing key file. Changing the key must be
// a deliberate act: verify the new key's fingerprint against Anthropic's docs
// (`gpg --show-keys pkg/vm/keys/claude-code.asc`), then update this constant.
const claudeKeySHA256 = "bd70a5e4a268002704024ceba7f8446024114e94f3f0bdd11c23a9e592be81c6" // gitleaks:allow (public key hash)

// claudeKeyDearmoredSHA256 is the sha256 of `gpg --dearmor < claude-code.asc`,
// so dearmor is checked against gpg's own output.
const claudeKeyDearmoredSHA256 = "0e122272125dd4bed96be0034cd95c84e9db07b4cf9bcddbe7c3ae01f3580646" // gitleaks:allow (public key hash)

func TestClaudeCodeKey_Pinned(t *testing.T) {
	sum := sha256.Sum256(claudeCodeKey)
	if got := hex.EncodeToString(sum[:]); got != claudeKeySHA256 {
		t.Errorf("embedded key sha256 = %s, want %s", got, claudeKeySHA256)
	}
	if !bytes.HasPrefix(claudeCodeKey, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		t.Error("embedded key is not an ASCII-armored public key")
	}
}

// claudeBuild runs BuildGoldenImage with the given channel/version and returns
// the virt-customize argv with the bake path and key temp path normalized, plus
// the key file content as it was at the moment virt-customize ran.
func claudeBuild(t *testing.T, channel, version string) (argv []string, keyContent []byte, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		switch {
		case name == "cp" && len(args) >= 2:
			_ = os.WriteFile(args[len(args)-1], []byte("fake"), 0644)
		case name == "qemu-img" && len(args) > 0 && args[0] == "info":
			return `{"format": "qcow2"}`, "", nil
		case name == "virt-customize" && argv == nil:
			argv = append([]string{}, args...)
			for i, a := range argv {
				if i > 0 && argv[i-1] == "--upload" && strings.HasSuffix(a, ":"+claudeKeyringPath) {
					keyPath = strings.TrimSuffix(a, ":"+claudeKeyringPath)
					keyContent, _ = os.ReadFile(keyPath)
					argv[i] = "<key>:" + claudeKeyringPath
				}
				if i > 0 && argv[i-1] == "--upload" && strings.HasSuffix(a, ":"+bunGuestPath) {
					argv[i] = "<bun>:" + bunGuestPath
				}
				if i > 0 && argv[i-1] == "-a" {
					if !strings.HasPrefix(filepath.Base(a), ".golden-bake-") {
						t.Errorf("-a target = %q, want the temp bake file", a)
					}
					argv[i] = "<bake>"
				}
			}
		}
		return "", "", nil
	})

	cfg := BuildGoldenConfig{
		ImagesDir:     dir,
		BaseURL:       "https://example.com/base.qcow2",
		BaseChecksum:  checksum,
		BaseFilename:  "base.qcow2",
		GoldenName:    "golden.qcow2",
		RootDiskGB:    8,
		Packages:      []string{"git", "tmux"},
		VMUser:        "claude",
		ClaudeChannel: channel,
		ClaudeVersion: version,
	}
	if err := BuildGoldenImage(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return argv, keyContent, keyPath
}

// expandSeedPlaceholder splices the seed steps in for the "--SEED" marker; the
// seed's own exact content is asserted in golden_seed_test.go.
func expandSeedPlaceholder(want []string) []string {
	var out []string
	for _, a := range want {
		if a == "--SEED" {
			out = append(out, seedArgs()...)
			continue
		}
		out = append(out, a)
	}
	return out
}

func TestBuildGoldenImage_ClaudeAptArgs(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		version string
		want    []string
	}{
		{
			name:    "stable_unpinned",
			channel: "stable",
			want: []string{
				"--connect", "qemu:///system",
				"-a", "<bake>",
				"--run-command", "growpart /dev/sda 1 && resize2fs /dev/sda1",
				"--mkdir", "/etc/apt/keyrings",
				"--upload", "<key>:/etc/apt/keyrings/claude-code.gpg",
				"--chmod", "0644:/etc/apt/keyrings/claude-code.gpg",
				"--write", "/etc/apt/sources.list.d/claude-code.list:deb [signed-by=/etc/apt/keyrings/claude-code.gpg] https://downloads.claude.ai/claude-code/apt/stable stable main\n",
				"--update",
				"--install", "git,tmux,claude-code",
				"--upload", "<bun>:/usr/local/bin/bun",
				"--chmod", "0755:/usr/local/bin/bun",
				"--run-command", seedStep("bun-version", bunVersionCheck("/usr/local/bin/bun", "1.4.4")),
				"--run-command", `echo "motoko-seed bun-version: $(/usr/local/bin/bun --version)" >> /var/tmp/motoko-seed.info`,
				"--SEED",
				"--delete", "/etc/apt/sources.list.d/claude-code.list",
				"--delete", "/etc/apt/keyrings/claude-code.gpg",
				"--run-command", "useradd -m -s /bin/bash claude",
			},
		},
		{
			name:    "latest_pinned",
			channel: "latest",
			version: "2.1.310",
			want: []string{
				"--connect", "qemu:///system",
				"-a", "<bake>",
				"--run-command", "growpart /dev/sda 1 && resize2fs /dev/sda1",
				"--mkdir", "/etc/apt/keyrings",
				"--upload", "<key>:/etc/apt/keyrings/claude-code.gpg",
				"--chmod", "0644:/etc/apt/keyrings/claude-code.gpg",
				"--write", "/etc/apt/sources.list.d/claude-code.list:deb [signed-by=/etc/apt/keyrings/claude-code.gpg] https://downloads.claude.ai/claude-code/apt/latest latest main\n",
				"--write", "/etc/apt/preferences.d/claude-code:Package: claude-code\nPin: version 2.1.310-*\nPin-Priority: 1001\n",
				"--update",
				"--install", "git,tmux,claude-code",
				"--run-command", claudeVersionCheck("2.1.310"),
				"--upload", "<bun>:/usr/local/bin/bun",
				"--chmod", "0755:/usr/local/bin/bun",
				"--run-command", seedStep("bun-version", bunVersionCheck("/usr/local/bin/bun", "1.4.4")),
				"--run-command", `echo "motoko-seed bun-version: $(/usr/local/bin/bun --version)" >> /var/tmp/motoko-seed.info`,
				"--SEED",
				"--delete", "/etc/apt/sources.list.d/claude-code.list",
				"--delete", "/etc/apt/keyrings/claude-code.gpg",
				"--run-command", "useradd -m -s /bin/bash claude",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, _, _ := claudeBuild(t, tt.channel, tt.version)
			want := expandSeedPlaceholder(tt.want)
			if !reflect.DeepEqual(argv, want) {
				t.Errorf("virt-customize argv mismatch\ngot:  %q\nwant: %q", argv, want)
			}
		})
	}
}

// The uploaded file must be the embedded key in binary form (Debian 12's apt
// cannot read an armored signed-by key without gnupg), and must not outlive
// the build.
func TestBuildGoldenImage_ClaudeKeyUpload(t *testing.T) {
	_, content, path := claudeBuild(t, "stable", "")
	sum := sha256.Sum256(content)
	if got := hex.EncodeToString(sum[:]); got != claudeKeyDearmoredSHA256 {
		t.Errorf("uploaded key sha256 = %s, want %s (gpg --dearmor output)", got, claudeKeyDearmoredSHA256)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("key temp file %s still exists after build (stat err: %v)", path, err)
	}
}

func TestBuildGoldenImage_ClaudeKeyRemovedOnFailure(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	var keyPath string
	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		if name == "cp" && len(args) >= 2 {
			_ = os.WriteFile(args[len(args)-1], []byte("fake"), 0644)
		}
		if name == "virt-customize" {
			for i, a := range args {
				if i > 0 && args[i-1] == "--upload" && strings.HasSuffix(a, ":"+claudeKeyringPath) {
					keyPath = strings.TrimSuffix(a, ":"+claudeKeyringPath)
				}
			}
			return "", "E: The repository is not signed.", fmt.Errorf("virt-customize failed")
		}
		return "", "", nil
	})

	cfg := goldenBuildConfig(dir, checksum)
	if err := BuildGoldenImage(cfg); err == nil {
		t.Fatal("expected error from failed virt-customize")
	}
	if keyPath == "" {
		t.Fatal("virt-customize called without --upload")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Errorf("key temp file %s still exists after failed build (stat err: %v)", keyPath, err)
	}
}

// Bad values must be refused before anything runs: they are interpolated into
// apt files and a shell --run-command.
func TestBuildGoldenImage_ClaudeValidation(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		version string
		wantErr bool
	}{
		{"stable", "stable", "", false},
		{"latest_pinned", "latest", "2.1.310", false},
		{"empty_channel", "", "", true},
		{"unknown_channel", "beta", "", true},
		{"channel_injection", "stable main\ndeb http://evil/ x", "", true},
		{"two_part_version", "stable", "2.1", true},
		{"debian_revision", "stable", "2.1.308-1", true},
		{"version_injection", "stable", "2.1.308; reboot", true},
		{"version_glob", "stable", "2.1.*", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			checksum := writeBaseImage(t, dir)
			recs := goldenMockRunner(t)
			cfg := goldenBuildConfig(dir, checksum)
			cfg.ClaudeChannel = tt.channel
			cfg.ClaudeVersion = tt.version

			err := BuildGoldenImage(cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected validation error")
				}
				if len(*recs) != 0 {
					t.Errorf("commands ran despite invalid input: %v", *recs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// claudeVersionCheck runs inside the image as a shell snippet; execute it
// against a stub dpkg-query to prove it gates on the exact upstream version.
func TestClaudeVersionCheck(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	tests := []struct {
		name      string
		installed string
		wantOK    bool
	}{
		{"exact_revision_1", "2.1.308-1", true},
		{"other_revision", "2.1.308-2", true},
		{"newer_version", "2.1.310-1", false},
		{"older_version", "2.1.30-1", false},
		{"longer_patch_prefix", "2.1.3080-1", false},
		{"not_installed", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			stub := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", tt.installed)
			if err := os.WriteFile(filepath.Join(bin, "dpkg-query"), []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(sh, "-c", claudeVersionCheck("2.1.308"))
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			if tt.wantOK && err != nil {
				t.Errorf("check failed for %q: %v (%s)", tt.installed, err, stderr.String())
			}
			if !tt.wantOK {
				if err == nil {
					t.Errorf("check passed for %q, want failure", tt.installed)
				} else if !strings.Contains(stderr.String(), "want 2.1.308") {
					t.Errorf("stderr = %q, want mention of the wanted version", stderr.String())
				}
			}
		})
	}
}

func TestDearmor(t *testing.T) {
	data := []byte("motoko dearmor test payload")
	body := base64.StdEncoding.EncodeToString(data)
	c := crc24(data)
	sum := base64.StdEncoding.EncodeToString([]byte{byte(c >> 16), byte(c >> 8), byte(c)})
	armor := func(headers, body, sum string) []byte {
		s := "-----BEGIN PGP PUBLIC KEY BLOCK-----\n" + headers + "\n" + body + "\n"
		if sum != "" {
			s += "=" + sum + "\n"
		}
		return []byte(s + "-----END PGP PUBLIC KEY BLOCK-----\n")
	}
	badSum := base64.StdEncoding.EncodeToString([]byte{byte(c>>16) ^ 1, byte(c >> 8), byte(c)})

	tests := []struct {
		name    string
		in      []byte
		wantErr bool
	}{
		{"plain", armor("", body, sum), false},
		{"headers_skipped", armor("Comment: x\nVersion: y\n", body, sum), false},
		{"no_checksum", armor("", body, ""), false},
		{"crlf", bytes.ReplaceAll(armor("", body, sum), []byte("\n"), []byte("\r\n")), false},
		{"wrapped_body", armor("", body[:8]+"\n"+body[8:], sum), false},
		{"checksum_mismatch", armor("", body, badSum), true},
		{"malformed_checksum", armor("", body, "!!"), true},
		{"bad_base64", armor("", "%%%%", ""), true},
		{"empty_body", armor("", "", ""), true},
		{"no_begin", []byte(body + "\n-----END PGP PUBLIC KEY BLOCK-----\n"), true},
		{"no_end", []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n" + body + "\n"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dearmor(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("dearmor succeeded with %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("dearmor: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Errorf("dearmor = %q, want %q", got, data)
			}
		})
	}
}
