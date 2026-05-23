package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr to a pipe for the duration of fn and
// returns whatever was written. Used to assert on user-facing messages.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

// stubVM swaps the vm.State and vm.UpdateResources hooks for testing.
// Returns a pointer to a counter that increments each time UpdateResources is called.
// Originals restored via t.Cleanup.
func stubVM(t *testing.T, state string, stateErr, updateErr error) *int {
	t.Helper()
	var calls int
	origState := vmStateFn
	origUpdate := vmUpdateResourcesFn
	vmStateFn = func(string) (string, error) { return state, stateErr }
	vmUpdateResourcesFn = func(string, int, int) error {
		calls++
		return updateErr
	}
	t.Cleanup(func() {
		vmStateFn = origState
		vmUpdateResourcesFn = origUpdate
	})
	return &calls
}

// setUpdateFlags assigns package-level flag vars and restores them after the test.
// Required because RunE reads them directly rather than via cobra's flag set.
func setUpdateFlags(t *testing.T, ram, cpus int) {
	t.Helper()
	origRAM := updateRAMFlag
	origCPUs := updateCPUsFlag
	updateRAMFlag = ram
	updateCPUsFlag = cpus
	t.Cleanup(func() {
		updateRAMFlag = origRAM
		updateCPUsFlag = origCPUs
	})
}

func runUpdate(t *testing.T, name string) error {
	t.Helper()
	return updateCmd.RunE(updateCmd, []string{name})
}

// ---------- name validation ----------

func TestUpdate_RejectsInvalidName(t *testing.T) {
	tests := []struct {
		name    string
		argName string
	}{
		{"path traversal", "../etc"},
		{"slash", "foo/bar"},
		{"empty", ""},
		{"uppercase", "BAD"},
		{"starts with dash", "-bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubVM(t, "shut off", nil, nil)
			setUpdateFlags(t, 4096, 0)
			err := runUpdate(t, tt.argName)
			if err == nil {
				t.Fatalf("expected error for name=%q", tt.argName)
			}
			if *calls != 0 {
				t.Errorf("UpdateResources should not be called when name invalid, got %d calls", *calls)
			}
		})
	}
}

// ---------- "at least one flag" rule ----------

func TestUpdate_RejectsWhenNoFlagsProvided(t *testing.T) {
	calls := stubVM(t, "shut off", nil, nil)
	setUpdateFlags(t, 0, 0)
	err := runUpdate(t, "test")
	if err == nil {
		t.Fatal("expected error when neither --ram nor --cpus provided")
	}
	if !strings.Contains(err.Error(), "at least one of --ram or --cpus") {
		t.Errorf("error = %q, want mention of 'at least one of --ram or --cpus'", err)
	}
	if *calls != 0 {
		t.Errorf("UpdateResources should not be called, got %d calls", *calls)
	}
}

// ---------- RAM boundary ----------

func TestUpdate_RAMBoundary(t *testing.T) {
	tests := []struct {
		name      string
		ram       int
		wantError bool
		errSubstr string
	}{
		{"just below min", 511, true, "at least 512"},
		{"one mb", 1, true, "at least 512"},
		{"at min", 512, false, ""},
		{"typical 4G", 4096, false, ""},
		{"large 65G", 65536, false, ""},
		// Negative values get an explicit "must be positive" — not the misleading
		// "at least 512" that surfaces when the lower-bound check runs first.
		{"negative ram", -1, true, "must be positive"},
		{"large negative", -9999, true, "must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubVM(t, "shut off", nil, nil)
			setUpdateFlags(t, tt.ram, 0)
			err := runUpdate(t, "test")
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error for ram=%d", tt.ram)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error = %q, want substring %q", err, tt.errSubstr)
				}
				if *calls != 0 {
					t.Errorf("UpdateResources should not be called on validation failure")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for ram=%d: %v", tt.ram, err)
				}
				if *calls != 1 {
					t.Errorf("UpdateResources call count = %d, want 1", *calls)
				}
			}
		})
	}
}

// ---------- CPU boundary ----------

func TestUpdate_CPUBoundary(t *testing.T) {
	tests := []struct {
		name      string
		cpus      int
		wantError bool
		errSubstr string
	}{
		{"negative", -1, true, "must be positive"},
		{"large negative", -100, true, "must be positive"},
		{"at min", 1, false, ""},
		{"typical 4", 4, false, ""},
		{"large 64", 64, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubVM(t, "shut off", nil, nil)
			setUpdateFlags(t, 0, tt.cpus)
			err := runUpdate(t, "test")
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error for cpus=%d", tt.cpus)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error = %q, want substring %q", err, tt.errSubstr)
				}
				if *calls != 0 {
					t.Errorf("UpdateResources should not be called on validation failure")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for cpus=%d: %v", tt.cpus, err)
				}
				if *calls != 1 {
					t.Errorf("UpdateResources call count = %d, want 1", *calls)
				}
			}
		})
	}
}

// ---------- VM state gating ----------

func TestUpdate_RejectsNonShutOffStates(t *testing.T) {
	// Decision table: every non-"shut off" state should be rejected with hint.
	states := []string{"running", "paused", "in shutdown", "crashed", "pmsuspended", ""}
	for _, st := range states {
		t.Run(fmt.Sprintf("state=%q", st), func(t *testing.T) {
			calls := stubVM(t, st, nil, nil)
			setUpdateFlags(t, 8192, 0)
			err := runUpdate(t, "test")
			if err == nil {
				t.Fatalf("expected error for state=%q", st)
			}
			if !strings.Contains(err.Error(), "motoko stop test") {
				t.Errorf("error = %q, want hint 'motoko stop test'", err)
			}
			if !strings.Contains(err.Error(), st) && st != "" {
				t.Errorf("error = %q, want state %q in message", err, st)
			}
			// Message must read cleanly even for empty state — no double spaces
			// or stray punctuation that betray a printf with an empty %s slot.
			if strings.Contains(err.Error(), "  ") {
				t.Errorf("error has double space (empty-state interpolation?): %q", err)
			}
			if *calls != 0 {
				t.Errorf("UpdateResources should not be called for non-shut-off VM, got %d calls", *calls)
			}
		})
	}
}

func TestUpdate_AcceptsShutOff(t *testing.T) {
	calls := stubVM(t, "shut off", nil, nil)
	setUpdateFlags(t, 4096, 2)
	if err := runUpdate(t, "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *calls != 1 {
		t.Errorf("UpdateResources call count = %d, want 1", *calls)
	}
}

// ---------- error propagation ----------

func TestUpdate_StateErrorPropagates(t *testing.T) {
	underlying := errors.New("domain not found")
	calls := stubVM(t, "", underlying, nil)
	setUpdateFlags(t, 4096, 0)
	err := runUpdate(t, "test")
	if err == nil {
		t.Fatal("expected error from State()")
	}
	if !strings.Contains(err.Error(), "checking VM state") {
		t.Errorf("error = %q, want prefix 'checking VM state'", err)
	}
	if !errors.Is(err, underlying) {
		t.Errorf("error = %v, want errors.Is == underlying %v", err, underlying)
	}
	if *calls != 0 {
		t.Errorf("UpdateResources should not be called when State fails")
	}
}

func TestUpdate_UpdateResourcesErrorPropagates(t *testing.T) {
	underlying := errors.New("setmaxmem rejected by libvirt")
	stubVM(t, "shut off", nil, underlying)
	setUpdateFlags(t, 8192, 0)
	err := runUpdate(t, "test")
	if err == nil {
		t.Fatal("expected error from UpdateResources()")
	}
	if !errors.Is(err, underlying) {
		// RunE returns err directly (no wrapping), so identity should match.
		t.Errorf("error = %v, want errors.Is == underlying", err)
	}
}

// ---------- output ordering: messages must reflect what actually committed ----------

func TestUpdate_DoesNotAnnounceChangeBeforeOperationRuns(t *testing.T) {
	// Failure mode: operator sees "Setting RAM to 8192 MiB" followed by an error
	// and reasonably assumes the change was partially applied. In fact nothing
	// changed. No announcement should appear if vmUpdateResourcesFn errors.
	stubVM(t, "shut off", nil, errors.New("libvirt boom"))
	setUpdateFlags(t, 8192, 4)
	stderr := captureStderr(t, func() {
		_ = runUpdate(t, "test")
	})
	if strings.Contains(stderr, "Setting RAM") {
		t.Errorf("stderr announced RAM change despite failed update:\n%s", stderr)
	}
	if strings.Contains(stderr, "Setting vCPUs") {
		t.Errorf("stderr announced vCPU change despite failed update:\n%s", stderr)
	}
}

func TestUpdate_AnnouncesChangeOnSuccess(t *testing.T) {
	// Happy path still surfaces what was set so the operator has a paper trail.
	stubVM(t, "shut off", nil, nil)
	setUpdateFlags(t, 4096, 2)
	stderr := captureStderr(t, func() {
		if err := runUpdate(t, "test"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	for _, want := range []string{"4096", "2", "test", "motoko start"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
}

// ---------- flag forwarding ----------

func TestUpdate_ForwardsFlagsToVMUpdate(t *testing.T) {
	tests := []struct {
		name     string
		ram      int
		cpus     int
		wantRAM  int
		wantCPUs int
	}{
		{"ram only", 8192, 0, 8192, 0},
		{"cpus only", 0, 4, 0, 4},
		{"both", 4096, 2, 4096, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotName string
			var gotRAM, gotCPUs int
			origState := vmStateFn
			origUpdate := vmUpdateResourcesFn
			vmStateFn = func(string) (string, error) { return "shut off", nil }
			vmUpdateResourcesFn = func(name string, ramMB, vcpus int) error {
				gotName = name
				gotRAM = ramMB
				gotCPUs = vcpus
				return nil
			}
			t.Cleanup(func() {
				vmStateFn = origState
				vmUpdateResourcesFn = origUpdate
			})

			setUpdateFlags(t, tt.ram, tt.cpus)
			if err := runUpdate(t, "aitley"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotName != "aitley" {
				t.Errorf("name = %q, want 'aitley'", gotName)
			}
			if gotRAM != tt.wantRAM {
				t.Errorf("ram = %d, want %d", gotRAM, tt.wantRAM)
			}
			if gotCPUs != tt.wantCPUs {
				t.Errorf("cpus = %d, want %d", gotCPUs, tt.wantCPUs)
			}
		})
	}
}

// ---------- validation order invariant ----------

// Validation order matters: name → flag-presence → flag-bounds → state.
// State should NOT be checked if earlier validation already failed (avoids
// surfacing "VM not found" when the real issue is invalid input).
func TestUpdate_ValidationShortCircuitsBeforeStateCheck(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"invalid name with valid flags", func(t *testing.T) {
			setUpdateFlags(t, 4096, 2)
		}},
		{"valid name, no flags", func(t *testing.T) {
			setUpdateFlags(t, 0, 0)
		}},
		{"valid name, ram below min", func(t *testing.T) {
			setUpdateFlags(t, 100, 0)
		}},
		{"valid name, cpus below min", func(t *testing.T) {
			setUpdateFlags(t, 0, -1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateCalled := false
			origState := vmStateFn
			origUpdate := vmUpdateResourcesFn
			vmStateFn = func(string) (string, error) {
				stateCalled = true
				return "shut off", nil
			}
			vmUpdateResourcesFn = func(string, int, int) error { return nil }
			t.Cleanup(func() {
				vmStateFn = origState
				vmUpdateResourcesFn = origUpdate
			})

			tt.setup(t)
			argName := "test"
			if tt.name == "invalid name with valid flags" {
				argName = "BAD/NAME"
			}
			_ = runUpdate(t, argName)
			if stateCalled {
				t.Errorf("vm.State should not be called when input validation fails")
			}
		})
	}
}
