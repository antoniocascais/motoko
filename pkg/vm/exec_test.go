package vm

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRun_LookPathMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, _, err := run("nonexistent-binary-xyz")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "nonexistent-binary-xyz") {
		t.Errorf("error = %q, want mention of binary name", err.Error())
	}
}

func TestRun_ReturnsStdout(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo not in PATH")
	}

	stdout, stderr, err := run("echo", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "hello" {
		t.Errorf("stdout = %q, want %q", stdout, "hello")
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("false not in PATH")
	}

	_, _, err := run("false")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "false failed") {
		t.Errorf("error = %q, want mention of 'false failed'", err.Error())
	}
}

func TestRun_TrimOutput(t *testing.T) {
	if _, err := exec.LookPath("printf"); err != nil {
		t.Skip("printf not in PATH")
	}

	stdout, _, err := run("printf", "  padded  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "padded" {
		t.Errorf("stdout = %q, want %q", stdout, "padded")
	}
}

func TestRun_NonZeroExitIncludesStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}

	_, stderr, err := run("sh", "-c", "echo fail >&2; exit 1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fail") {
		t.Errorf("error should include stderr content, got: %q", err.Error())
	}
	if stderr != "fail" {
		t.Errorf("stderr = %q, want %q", stderr, "fail")
	}
}

func TestRun_EmptyOutput(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}

	stdout, stderr, err := run("true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}
