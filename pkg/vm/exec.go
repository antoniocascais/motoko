package vm

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

const libvirtURI = "qemu:///system"

// libvirtArgs prepends --connect qemu:///system so all virsh/virt-install/virt-customize
// calls target the system daemon (where networks and VMs live).
func libvirtArgs(args ...string) []string {
	return append([]string{"--connect", libvirtURI}, args...)
}

// runFunc is the signature for command execution. Tests replace runCmd to intercept calls.
type runFunc func(name string, args ...string) (stdout, stderr string, err error)

var runCmd runFunc = run

// run executes a command with explicit args (never sh -c).
// Returns trimmed stdout and stderr. On non-zero exit, err includes stderr.
func run(name string, args ...string) (string, string, error) {
	binary, err := exec.LookPath(name)
	if err != nil {
		return "", "", fmt.Errorf("%s not found in PATH: %w", name, err)
	}

	cmd := exec.Command(binary, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	stdout := strings.TrimSpace(stdoutBuf.String())
	stderr := strings.TrimSpace(stderrBuf.String())

	if err != nil {
		return stdout, stderr, fmt.Errorf("%s failed: %w\nstderr: %s", name, err, stderr)
	}
	return stdout, stderr, nil
}
