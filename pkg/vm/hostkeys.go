package vm

import (
	"fmt"
	"strings"
)

// parseHostKeys converts a guest's `cat /etc/ssh/ssh_host_*_key.pub` output into
// known_hosts file content keyed by ip. The keys are captured by WaitForSSHReady
// on the same authenticated connection that asserts the guest hostname, so the
// pinned keys provably belong to this instance — not blind trust-on-first-use.
func parseHostKeys(catOutput, ip string) (string, error) {
	var lines []string
	for _, line := range strings.Split(catOutput, "\n") {
		// Each pubkey line is "<keytype> <base64> [comment]"; a known_hosts entry
		// is "<host> <keytype> <base64>" — drop the trailing comment.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Skip DSA: Debian still writes ssh_host_dsa_key but sshd never presents it
		// (disabled by default since OpenSSH 7.0), so pinning it is dead weight.
		if fields[0] == "ssh-dss" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", ip, fields[0], fields[1]))
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("no host keys found at %s", ip)
	}
	return strings.Join(lines, "\n") + "\n", nil
}
