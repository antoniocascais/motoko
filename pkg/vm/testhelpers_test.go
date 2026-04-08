package vm

import (
	"crypto/sha512"
	"encoding/hex"
	"net/http"
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

// roundTripFunc adapts a function to http.RoundTripper for test transport injection.
type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

// mockHTTPClient replaces the package-level httpClient with one using the given
// transport function. Original client is restored via t.Cleanup.
func mockHTTPClient(t *testing.T, fn roundTripFunc) {
	t.Helper()
	orig := httpClient
	httpClient = &http.Client{Transport: fn}
	t.Cleanup(func() { httpClient = orig })
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
