package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWithNetworkLock_RunsFnAndReturnsNil(t *testing.T) {
	dir := t.TempDir()
	ran := false
	err := withNetworkLock(dir, func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Error("fn was not invoked")
	}
}

func TestWithNetworkLock_CreatesLockFile(t *testing.T) {
	dir := t.TempDir()
	if err := withNetworkLock(dir, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".netlock")); err != nil {
		t.Errorf(".netlock not created: %v", err)
	}
}

func TestWithNetworkLock_CreatesMissingDir(t *testing.T) {
	// Lock dir does not exist yet — withNetworkLock must MkdirAll it.
	dir := filepath.Join(t.TempDir(), "nested", "motoko")
	if err := withNetworkLock(dir, func() error { return nil }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".netlock")); err != nil {
		t.Errorf(".netlock not created in nested dir: %v", err)
	}
}

func TestWithNetworkLock_PropagatesFnError(t *testing.T) {
	dir := t.TempDir()
	sentinel := errors.New("boom")
	err := withNetworkLock(dir, func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want %v", err, sentinel)
	}
}

func TestWithNetworkLock_SequentialReacquire(t *testing.T) {
	// The lock must be released after each call so a subsequent acquire in the
	// same process succeeds (otherwise back-to-back creates would self-deadlock).
	dir := t.TempDir()
	count := 0
	for range 2 {
		if err := withNetworkLock(dir, func() error {
			count++
			return nil
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if count != 2 {
		t.Errorf("fn ran %d times, want 2", count)
	}
}

func TestWithNetworkLock_OpenError(t *testing.T) {
	// A path whose parent is a regular file cannot be MkdirAll'd.
	parent := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	err := withNetworkLock(filepath.Join(parent, "sub"), func() error {
		t.Error("fn should not run when lock dir cannot be created")
		return nil
	})
	if err == nil {
		t.Fatal("expected error when lock dir cannot be created")
	}
}
