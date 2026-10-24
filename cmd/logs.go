package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

// sshConnFailed is ssh's own exit status for connection and auth failures; any
// other non-zero status came from the remote command.
const sshConnFailed = 255

// exitLogPath is relative to the VM user's home, where ssh sessions start.
// claude-run.sh writes it on the data disk.
const exitLogPath = "data/claude-exit.log"

// exitLogTail covers about four exit records: each is a header plus up to 50
// scrollback lines and the 24-line visible pane.
const exitLogTail = "300"

// Seam for testability — tests swap this to fake ssh.
var logsSSHFn = runSSH

var logsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show Claude Code output from the instance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, st, ip, knownHosts, err := resolveInstance(args[0])
		if err != nil {
			return err
		}
		return showLogs(sshArgs(st.SSHKeyPath, cfg.VMUser, ip, knownHosts), os.Stdout, os.Stderr)
	},
}

// showLogs prints the live tmux pane. When the session is gone, the assistant
// is down and the pane cannot say why, so it prints the exit log instead.
func showLogs(base []string, stdout, stderr io.Writer) error {
	withCmd := func(remote ...string) []string {
		return append(append([]string{}, base...), remote...)
	}

	// Held back: when the session is gone, tmux's "no server running" only
	// clutters the explanation printed below.
	var captureErr bytes.Buffer
	code, err := logsSSHFn(withCmd("tmux", "capture-pane", "-p", "-t", "claude", "-S", "-100"), stdout, &captureErr)
	if err == nil || code == 0 || code == sshConnFailed {
		_, _ = stderr.Write(captureErr.Bytes())
		return err
	}

	_, _ = fmt.Fprintf(stderr, "claude tmux session not running; recent exits from ~/%s:\n", exitLogPath)
	_, err = logsSSHFn(withCmd("tail", "-n", exitLogTail, exitLogPath), stdout, stderr)
	return err
}

// runSSH runs ssh with argv and returns its exit status. A non-nil error with
// status 0 means ssh could not be started at all.
func runSSH(argv []string, stdout, stderr io.Writer) (int, error) {
	c := exec.Command("ssh", argv...)
	c.Stdout = stdout
	c.Stderr = stderr
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), err
	}
	return 0, err
}

func init() {
	rootCmd.AddCommand(logsCmd)
}
