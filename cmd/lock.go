package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// withNetworkLock runs fn while holding an exclusive flock on a host-wide lock
// file. The libvirt network is shared across every instance on the host, so
// concurrent `motoko create`s must serialize their allocate-IP -> add-reservation
// sequence: `virsh net-update add ip-dhcp-host` does not reject duplicate IPs, so
// two unsynchronized creates could reserve the same address and reintroduce the
// DHCP race this is meant to eliminate. The lock only needs to cover allocate+add
// (not the slow boot wait) — once the reservation is written it is visible to any
// concurrent allocation via net-dumpxml.
func withNetworkLock(dir string, fn func() error) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir for lock: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".netlock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening network lock: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring network lock: %w", err)
	}
	return fn()
}
