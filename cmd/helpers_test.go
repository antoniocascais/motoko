package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antoniocascais/motoko/pkg/preflight"
)

// ---------- sshArgs ----------

func TestSSHArgs_BuildsExpectedArguments(t *testing.T) {
	got := sshArgs("/keys/id_ed25519", "claude", "192.168.122.10")
	want := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", "/keys/id_ed25519",
		"claude@192.168.122.10",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSSHArgs_EmptyKeyAndUser(t *testing.T) {
	// Documents behavior: no validation, empty strings flow through.
	got := sshArgs("", "", "10.0.0.1")
	if got[5] != "" {
		t.Errorf("expected empty key path, got %q", got[5])
	}
	if got[6] != "@10.0.0.1" {
		t.Errorf("expected '@10.0.0.1' with empty user, got %q", got[6])
	}
}

// ---------- requireEnv ----------

func TestRequireEnv_PresentReturnsValue(t *testing.T) {
	const key = "MOTOKO_TEST_VAR"
	t.Setenv(key, "expected-value")
	v, err := requireEnv(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "expected-value" {
		t.Errorf("value = %q, want 'expected-value'", v)
	}
}

func TestRequireEnv_MissingReturnsError(t *testing.T) {
	const key = "MOTOKO_TEST_DEFINITELY_UNSET"
	_ = os.Unsetenv(key)
	v, err := requireEnv(key)
	if err == nil {
		t.Fatal("expected error for unset env var")
	}
	if v != "" {
		t.Errorf("value = %q, want empty on error", v)
	}
	if !strings.Contains(err.Error(), key) {
		t.Errorf("error = %q, want variable name %q in message", err, key)
	}
}

func TestRequireEnv_EmptyValueTreatedAsMissing(t *testing.T) {
	// Boundary: env var set but empty. requireEnv treats empty as missing.
	const key = "MOTOKO_TEST_EMPTY"
	t.Setenv(key, "")
	_, err := requireEnv(key)
	if err == nil {
		t.Fatal("expected error when env var is set to empty string")
	}
}

// ---------- configDir ----------

func TestConfigDir_UsesCfgFileParent(t *testing.T) {
	orig := cfgFile
	cfgFile = "/etc/motoko/some-config.yml"
	t.Cleanup(func() { cfgFile = orig })
	got := configDir()
	if got != "/etc/motoko" {
		t.Errorf("configDir() = %q, want '/etc/motoko'", got)
	}
}

func TestConfigDir_EmptyCfgFileFallsBackToDefault(t *testing.T) {
	orig := cfgFile
	cfgFile = ""
	t.Cleanup(func() { cfgFile = orig })
	got := configDir()
	if got == "" {
		t.Error("configDir() returned empty for default case")
	}
	if !strings.Contains(got, "motoko") {
		t.Errorf("configDir() = %q, want path containing 'motoko'", got)
	}
}

// ---------- warnRemove ----------

func TestWarnRemove_RemovesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tmpfile")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	warnRemove(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after warnRemove: err=%v", err)
	}
}

func TestWarnRemove_MissingFileNoOp(t *testing.T) {
	// Should not warn, should not error — ENOENT is the "happy path" for cleanup.
	warnRemove("/nonexistent/path/to/file/that/does/not/exist")
	// If no panic / no fatal, success. Coverage records the IsNotExist branch.
}

// ---------- loadOperatorKeyAndPersona ----------

func TestLoadOperatorKeyAndPersona_BothEmpty(t *testing.T) {
	key, persona, err := loadOperatorKeyAndPersona("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "" {
		t.Errorf("key = %q, want empty", key)
	}
	if persona != "" {
		t.Errorf("persona = %q, want empty", persona)
	}
}

func TestLoadOperatorKeyAndPersona_KeyOnly(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519.pub")
	const keyContent = "ssh-ed25519 AAAA... user@host\n"
	if err := os.WriteFile(keyPath, []byte(keyContent), 0644); err != nil {
		t.Fatal(err)
	}
	key, persona, err := loadOperatorKeyAndPersona(keyPath, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// TrimSpace strips trailing newline.
	if key != strings.TrimSpace(keyContent) {
		t.Errorf("key = %q, want trimmed content", key)
	}
	if persona != "" {
		t.Errorf("persona = %q, want empty", persona)
	}
}

func TestLoadOperatorKeyAndPersona_PersonaOnly(t *testing.T) {
	dir := t.TempDir()
	personaPath := filepath.Join(dir, "persona.md")
	const personaContent = "# Persona\n\nYou are a helpful gym coach.\n"
	if err := os.WriteFile(personaPath, []byte(personaContent), 0644); err != nil {
		t.Fatal(err)
	}
	key, persona, err := loadOperatorKeyAndPersona("", personaPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "" {
		t.Errorf("key = %q, want empty", key)
	}
	// Persona is NOT trimmed — preserves trailing newline (relevant for YAML embedding).
	if persona != personaContent {
		t.Errorf("persona = %q, want untrimmed content", persona)
	}
}

func TestLoadOperatorKeyAndPersona_MissingKey(t *testing.T) {
	_, _, err := loadOperatorKeyAndPersona("/nonexistent/key.pub", "")
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
	if !strings.Contains(err.Error(), "operator SSH key") {
		t.Errorf("error = %q, want mention of 'operator SSH key'", err)
	}
}

func TestLoadOperatorKeyAndPersona_MissingPersona(t *testing.T) {
	_, _, err := loadOperatorKeyAndPersona("", "/nonexistent/persona.md")
	if err == nil {
		t.Fatal("expected error for missing persona file")
	}
	if !strings.Contains(err.Error(), "persona") {
		t.Errorf("error = %q, want mention of 'persona'", err)
	}
}

// ---------- printResults / progress (smoke) ----------

// These functions only write to stderr; smoke-testing ensures coverage and absence of panic.

func TestProgress_DoesNotPanic(t *testing.T) {
	progress(3, 10, "Doing something")
}

func TestPrintResults_PassedAndFailedBranches(t *testing.T) {
	// Exercise both branches: Passed=true (OK label) and Passed=false (fail label).
	results := []preflight.CheckResult{
		{Name: "good", Passed: true, Detail: ""},
		{Name: "bad", Passed: false, Detail: "missing binary"},
	}
	printResults(results, "FAIL")
}

func TestPrintResults_EmptyList(t *testing.T) {
	printResults(nil, "WARN")
}

// ---------- confirmAction ----------

// withStdin pipes the given input as os.Stdin for the duration of the test.
func withStdin(t *testing.T, input string) {
	t.Helper()
	orig := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
}

func TestConfirmAction_Accepts(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase y", "y\n", true},
		{"uppercase Y", "Y\n", true},
		{"y with trailing whitespace", "  y  \n", true},
		{"no", "n\n", false},
		{"empty input", "\n", false},
		{"yes word rejected (only 'y' counts)", "yes\n", false},
		{"unrelated text", "maybe\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStdin(t, tt.input)
			got := confirmAction("Proceed?")
			if got != tt.want {
				t.Errorf("confirmAction(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestConfirmAction_EOF(t *testing.T) {
	// scanner.Scan returns false on EOF (no input). Function returns false.
	withStdin(t, "")
	if got := confirmAction("Proceed?"); got != false {
		t.Errorf("confirmAction with EOF = %v, want false", got)
	}
}
