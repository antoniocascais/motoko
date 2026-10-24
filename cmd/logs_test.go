package cmd

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type sshCall struct {
	argv []string
}

// stubLogsSSH replaces logsSSHFn; results are returned in call order.
func stubLogsSSH(t *testing.T, results ...sshResult) *[]sshCall {
	t.Helper()
	var calls []sshCall
	orig := logsSSHFn
	logsSSHFn = func(argv []string, stdout, stderr io.Writer) (int, error) {
		calls = append(calls, sshCall{argv: append([]string{}, argv...)})
		if len(calls) > len(results) {
			t.Fatalf("unexpected ssh call %d: %v", len(calls), argv)
		}
		r := results[len(calls)-1]
		_, _ = io.WriteString(stderr, r.stderr)
		return r.code, r.err
	}
	t.Cleanup(func() { logsSSHFn = orig })
	return &calls
}

type sshResult struct {
	code   int
	err    error
	stderr string
}

var logsBase = []string{"-i", "key", "claude@10.0.0.5"}

func TestShowLogs(t *testing.T) {
	capture := append(append([]string{}, logsBase...), "tmux", "capture-pane", "-p", "-t", "claude", "-S", "-100")
	tail := append(append([]string{}, logsBase...), "tail", "-n", "300", "data/claude-exit.log")
	exitErr := errors.New("exit status")

	tests := []struct {
		name      string
		results   []sshResult
		wantCalls [][]string
		wantErr   bool
		wantHint  bool
	}{
		{"session_running", []sshResult{{0, nil, ""}}, [][]string{capture}, false, false},
		{"session_absent_shows_exit_log", []sshResult{{1, exitErr, ""}, {0, nil, ""}}, [][]string{capture, tail}, false, true},
		{"session_absent_no_log", []sshResult{{1, exitErr, ""}, {1, exitErr, ""}}, [][]string{capture, tail}, true, true},
		// ssh itself failed: the remote state is unknown, so no fallback.
		{"ssh_connection_failed", []sshResult{{255, exitErr, ""}}, [][]string{capture}, true, false},
		{"ssh_not_started", []sshResult{{0, exitErr, ""}}, [][]string{capture}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubLogsSSH(t, tt.results...)
			var stdout, stderr bytes.Buffer

			err := showLogs(logsBase, &stdout, &stderr)

			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			var got [][]string
			for _, c := range *calls {
				got = append(got, c.argv)
			}
			if !reflect.DeepEqual(got, tt.wantCalls) {
				t.Errorf("ssh calls:\ngot:  %q\nwant: %q", got, tt.wantCalls)
			}
			if hint := strings.Contains(stderr.String(), "session not running"); hint != tt.wantHint {
				t.Errorf("fallback hint printed = %v, want %v (stderr: %q)", hint, tt.wantHint, stderr.String())
			}
		})
	}
}

// The fallback must not mutate the caller's base argv (append aliasing).
func TestShowLogs_BaseUnchanged(t *testing.T) {
	stubLogsSSH(t, sshResult{1, errors.New("x"), ""}, sshResult{0, nil, ""})
	base := make([]string, len(logsBase), len(logsBase)+16)
	copy(base, logsBase)
	if err := showLogs(base, io.Discard, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(base, logsBase) {
		t.Errorf("base argv mutated: %v", base)
	}
}

// The first attempt's stderr is noise once the fallback explains the missing
// session, but it is the only diagnostic when there is no fallback.
func TestShowLogs_CaptureStderr(t *testing.T) {
	exitErr := errors.New("exit status")
	const tmuxErr = "no server running on /tmp/tmux-1000/default\n"
	tests := []struct {
		name     string
		results  []sshResult
		wantTmux bool
	}{
		{"fallback_hides_it", []sshResult{{1, exitErr, tmuxErr}, {0, nil, ""}}, false},
		{"ssh_failure_shows_it", []sshResult{{255, exitErr, "ssh: connect to host: Connection refused\n"}}, true},
		{"success_shows_it", []sshResult{{0, nil, tmuxErr}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubLogsSSH(t, tt.results...)
			var stdout, stderr bytes.Buffer
			_ = showLogs(logsBase, &stdout, &stderr)
			first := tt.results[0].stderr
			if got := strings.Contains(stderr.String(), first); got != tt.wantTmux {
				t.Errorf("first attempt's stderr shown = %v, want %v (stderr: %q)", got, tt.wantTmux, stderr.String())
			}
		})
	}
}
