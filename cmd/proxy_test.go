package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFilter(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed-domains")
	if err := os.WriteFile(path, []byte(content), 0664); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFilter(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAddDomain_AppendsPattern(t *testing.T) {
	path := writeFilter(t, `\.anthropic\.com$`+"\n")
	if err := addDomainToFilter(path, `\.example\.com$`); err != nil {
		t.Fatalf("addDomainToFilter() error: %v", err)
	}
	content := readFilter(t, path)
	if !strings.Contains(content, `\.example\.com$`) {
		t.Error("expected new pattern in filter file")
	}
	if !strings.Contains(content, `\.anthropic\.com$`) {
		t.Error("existing pattern should be preserved")
	}
}

func TestAddDomain_Idempotent(t *testing.T) {
	path := writeFilter(t, `\.anthropic\.com$`+"\n")
	if err := addDomainToFilter(path, `\.anthropic\.com$`); err != nil {
		t.Fatal(err)
	}
	content := readFilter(t, path)
	count := strings.Count(content, `\.anthropic\.com$`)
	if count != 1 {
		t.Errorf("pattern appears %d times, want 1 (should be idempotent)", count)
	}
}

func TestAddDomain_FileNotFound(t *testing.T) {
	err := addDomainToFilter("/nonexistent/allowed-domains", `\.test\.com$`)
	if err == nil {
		t.Fatal("expected error for missing filter file")
	}
	if !strings.Contains(err.Error(), "reading filter file") {
		t.Errorf("error = %q, want 'reading filter file' prefix", err.Error())
	}
}

func TestAddDomain_EmptyFile(t *testing.T) {
	path := writeFilter(t, "")
	if err := addDomainToFilter(path, `\.test\.com$`); err != nil {
		t.Fatal(err)
	}
	content := readFilter(t, path)
	if !strings.Contains(content, `\.test\.com$`) {
		t.Error("expected pattern in previously empty file")
	}
}

func TestRemoveDomain_RemovesPattern(t *testing.T) {
	path := writeFilter(t, `\.anthropic\.com$`+"\n"+`\.example\.com$`+"\n")
	if err := removeDomainFromFilter(path, `\.example\.com$`); err != nil {
		t.Fatal(err)
	}
	content := readFilter(t, path)
	if strings.Contains(content, `\.example\.com$`) {
		t.Error("removed pattern should not be in file")
	}
	if !strings.Contains(content, `\.anthropic\.com$`) {
		t.Error("other patterns should be preserved")
	}
}

func TestRemoveDomain_NotFound(t *testing.T) {
	path := writeFilter(t, `\.anthropic\.com$`+"\n")
	before := readFilter(t, path)
	err := removeDomainFromFilter(path, `\.nonexistent\.com$`)
	if err != nil {
		t.Fatalf("removing absent pattern should return nil, got: %v", err)
	}
	after := readFilter(t, path)
	if after != before {
		t.Error("file should be unchanged when pattern not found")
	}
}

func TestRemoveDomain_RemovesAllDuplicates(t *testing.T) {
	path := writeFilter(t, `\.dup\.com$`+"\n"+`\.other\.com$`+"\n"+`\.dup\.com$`+"\n")
	if err := removeDomainFromFilter(path, `\.dup\.com$`); err != nil {
		t.Fatal(err)
	}
	content := readFilter(t, path)
	if strings.Contains(content, `\.dup\.com$`) {
		t.Error("all occurrences of duplicate pattern should be removed")
	}
	if !strings.Contains(content, `\.other\.com$`) {
		t.Error("non-matching patterns should be preserved")
	}
}

func TestRemoveDomain_FileNotFound(t *testing.T) {
	err := removeDomainFromFilter("/nonexistent/allowed-domains", `\.test\.com$`)
	if err == nil {
		t.Fatal("expected error for missing filter file")
	}
	if !strings.Contains(err.Error(), "reading filter file") {
		t.Errorf("error = %q, want 'reading filter file' prefix", err.Error())
	}
}

func TestWriteFilterFile_TrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter")
	if err := os.WriteFile(path, []byte(""), 0664); err != nil {
		t.Fatal(err)
	}
	if err := writeFilterFile(path, []string{`\.a\.com$`, `\.b\.com$`}); err != nil {
		t.Fatal(err)
	}
	content := readFilter(t, path)
	if !strings.HasSuffix(content, "\n") {
		t.Error("file should end with a newline")
	}
	// Exactly one trailing newline
	trimmed := strings.TrimRight(content, "\n")
	if strings.Count(content, "\n")-strings.Count(trimmed, "\n") != 1 {
		t.Errorf("expected exactly one trailing newline, got content: %q", content)
	}
}

func TestWriteFilterFile_EmptyLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter")
	if err := os.WriteFile(path, []byte(""), 0664); err != nil {
		t.Fatal(err)
	}
	if err := writeFilterFile(path, []string{}); err != nil {
		t.Fatal(err)
	}
	content := readFilter(t, path)
	if content != "\n" {
		t.Errorf("empty lines should produce single newline, got %q", content)
	}
}

func TestWriteFilterFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter")
	if err := os.WriteFile(path, []byte("original\n"), 0664); err != nil {
		t.Fatal(err)
	}
	if err := writeFilterFile(path, []string{"replaced"}); err != nil {
		t.Fatal(err)
	}
	content := readFilter(t, path)
	if strings.Contains(content, "original") {
		t.Error("original content should be replaced")
	}
	if !strings.Contains(content, "replaced") {
		t.Error("new content should be written")
	}
}

func TestWriteFilterFile_PreservesExistingPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Set exact permissions (bypass umask)
	if err := os.Chmod(path, 0664); err != nil {
		t.Fatal(err)
	}
	if err := writeFilterFile(path, []string{"new"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// os.WriteFile on existing file preserves permissions
	if perm := info.Mode().Perm(); perm != 0664 {
		t.Errorf("perm = %o, want 0664 (should preserve existing)", perm)
	}
}

func TestWriteFilterFile_UnwritablePath(t *testing.T) {
	err := writeFilterFile("/nonexistent/deep/path/filter", []string{"test"})
	if err == nil {
		t.Fatal("expected error for unwritable path")
	}
	if !strings.Contains(err.Error(), "writing filter file") {
		t.Errorf("error = %q, want 'writing filter file' prefix", err.Error())
	}
}
