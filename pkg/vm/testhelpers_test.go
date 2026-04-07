package vm

import (
	"crypto/sha512"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

type execRecord struct {
	name string
	args []string
}

// mockRunner replaces runCmd with a recorder that returns success for all calls.
// Returns a pointer to the recorded calls. Original runCmd is restored via t.Cleanup.
func mockRunner(t *testing.T) *[]execRecord {
	t.Helper()
	return mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", nil
	})
}

// mockRunnerWithFunc replaces runCmd with a recorder that delegates to fn.
// The fn return values are passed through; all calls are also recorded.
func mockRunnerWithFunc(t *testing.T, fn runFunc) *[]execRecord {
	t.Helper()
	var records []execRecord
	original := runCmd
	runCmd = func(name string, args ...string) (string, string, error) {
		records = append(records, execRecord{name: name, args: append([]string{}, args...)})
		return fn(name, args...)
	}
	t.Cleanup(func() { runCmd = original })
	return &records
}

// writeBaseImage creates a fake base image in dir and returns its sha512 checksum string.
func writeBaseImage(t *testing.T, dir string) string {
	t.Helper()
	content := []byte("fake base image")
	if err := os.WriteFile(filepath.Join(dir, "base.qcow2"), content, 0644); err != nil {
		t.Fatal(err)
	}
	h := sha512.Sum512(content)
	return "sha512:" + hex.EncodeToString(h[:])
}
