package vm

import (
	"os"
	"testing"
)

// The pinned bun download is real network and ~50 MB; golden-build tests get a
// stand-in executable instead. ensureBun itself is tested directly with a
// fixture zip.
func TestMain(m *testing.M) {
	fetchBun = func(string) (string, error) {
		f, err := os.CreateTemp("", "motoko-bun-test-*")
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString("fake bun"); err != nil {
			return "", err
		}
		return f.Name(), nil
	}
	os.Exit(m.Run())
}
