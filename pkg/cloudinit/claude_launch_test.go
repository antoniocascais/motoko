package cloudinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// writeFile returns the write_files entry for path from rendered user-data.
func writeFile(t *testing.T, output, path string) (content, permissions string) {
	t.Helper()
	var doc struct {
		WriteFiles []struct {
			Path        string `yaml:"path"`
			Content     string `yaml:"content"`
			Permissions string `yaml:"permissions"`
		} `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(output, "#cloud-config\n")), &doc); err != nil {
		t.Fatalf("parsing user-data: %v", err)
	}
	for _, f := range doc.WriteFiles {
		if f.Path == path {
			return f.Content, f.Permissions
		}
	}
	t.Fatalf("write_files has no entry for %s", path)
	return "", ""
}

// unitLine returns the service unit line starting with prefix.
func unitLine(t *testing.T, output, prefix string) string {
	t.Helper()
	unit, _ := writeFile(t, output, "/home/claude/.config/systemd/user/claude-assistant.service")
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("unit has no %s line:\n%s", prefix, unit)
	return ""
}

func TestRenderUserData_ClaudeLaunchPaths(t *testing.T) {
	const channels = " --channels plugin:telegram@claude-plugins-official"
	tests := []struct {
		name   string
		params func(*testing.T) *InstanceParams
		flags  string
	}{
		{"telegram", testParams, " --dangerously-skip-permissions" + channels},
		{"no_telegram", testParamsNoTelegram, " --dangerously-skip-permissions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := renderUserData(t, tt.params(t))

			wantStart := "ExecStart=/usr/bin/tmux new-session -d -s claude '/home/claude/bin/claude-run.sh" + tt.flags + "'"
			if got := unitLine(t, output, "ExecStart="); got != wantStart {
				t.Errorf("ExecStart:\ngot:  %s\nwant: %s", got, wantStart)
			}

			aliases, _ := writeFile(t, output, "/home/claude/.bash_aliases")
			wantAlias := "alias claude='/usr/bin/claude" + tt.flags + "'\n"
			if aliases != wantAlias {
				t.Errorf("alias:\ngot:  %q\nwant: %q", aliases, wantAlias)
			}

			if strings.Contains(output, "/usr/local/bin/claude") {
				t.Error("user-data still references the npm install path /usr/local/bin/claude")
			}
		})
	}
}

// A dead tmux session must not turn the keystroke step into the reported
// failure; systemd's "-" prefix ignores its exit status.
func TestRenderUserData_ExecStartPostTolerant(t *testing.T) {
	line := unitLine(t, renderUserData(t, testParams(t)), "ExecStartPost=")
	if !strings.HasPrefix(line, "ExecStartPost=-/bin/bash ") {
		t.Errorf("ExecStartPost lacks the - prefix: %s", line)
	}
}

// When claude exits, tmux exits 0 and systemd sees a clean stop. Only
// Restart=always brings the assistant back; on-failure restarted only because
// ExecStop failed on the dead session. ExecStop's "-" keeps that from marking
// the unit failed, so nothing depends on it any more.
func TestRenderUserData_RestartsOnAnyExit(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if got := unitLine(t, output, "Restart="); got != "Restart=always" {
		t.Errorf("got %q, want Restart=always", got)
	}
	if got := unitLine(t, output, "ExecStop="); got != "ExecStop=-/usr/bin/tmux kill-session -t claude" {
		t.Errorf("ExecStop not tolerant of a dead session: %q", got)
	}
}

// Both the service and interactive logins must refuse to self-update: the
// version in a VM is the one baked into its golden image.
func TestRenderUserData_DisableUpdates(t *testing.T) {
	output := renderUserData(t, testParams(t))
	for _, path := range []string{"/etc/environment", "/home/claude/.claude-env"} {
		content, _ := writeFile(t, output, path)
		if !strings.Contains("\n"+content, "\nDISABLE_UPDATES=1\n") {
			t.Errorf("%s lacks DISABLE_UPDATES=1:\n%s", path, content)
		}
	}
	if strings.Contains(output, "DISABLE_AUTOUPDATER") {
		t.Error("DISABLE_AUTOUPDATER is redundant with DISABLE_UPDATES and should not be set")
	}
}

func TestRenderUserData_ClaudeRunScriptWritten(t *testing.T) {
	params := testParams(t)
	content, perms := writeFile(t, renderUserData(t, params), "/home/claude/bin/claude-run.sh")
	if perms != "0755" {
		t.Errorf("claude-run.sh permissions = %q, want 0755", perms)
	}
	if content != params.ClaudeRunSh {
		t.Errorf("claude-run.sh content mangled by template:\ngot:\n%s\nwant:\n%s", content, params.ClaudeRunSh)
	}
	if !strings.Contains(params.ClaudeRunSh, `LOG="/home/claude/data/claude-exit.log"`) {
		t.Errorf("exit log is not on the data disk:\n%s", params.ClaudeRunSh)
	}
}

// runClaudeRun executes the rendered claude-run.sh with claude and tmux
// replaced by stubs and the log redirected into dir.
func runClaudeRun(t *testing.T, dir string, claudeExit string, args ...string) (int, string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	params := testParams(t)
	stubClaude := filepath.Join(dir, "claude")
	stubTmux := filepath.Join(dir, "tmux")
	writeStub := func(path, body string) {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub(stubClaude, `echo "$@" > "`+filepath.Join(dir, "args")+`"; exit `+claudeExit+"\n")
	writeStub(stubTmux, "echo FINAL-SCREEN\n")

	script := params.ClaudeRunSh
	script = strings.Replace(script, "/usr/bin/claude", stubClaude, 1)
	script = strings.Replace(script, "/usr/bin/tmux", stubTmux, 1)
	script = strings.Replace(script, "/home/claude/data/claude-exit.log", filepath.Join(dir, "exit.log"), 1)
	scriptPath := filepath.Join(dir, "claude-run.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(sh, append([]string{scriptPath}, args...)...)
	err = cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running script: %v", err)
	}
	gotArgs, _ := os.ReadFile(filepath.Join(dir, "args"))
	return code, strings.TrimSpace(string(gotArgs))
}

func TestClaudeRun_RecordsExit(t *testing.T) {
	dir := t.TempDir()
	code, args := runClaudeRun(t, dir, "3", "--dangerously-skip-permissions", "--channels", "plugin:telegram@claude-plugins-official")

	if code != 3 {
		t.Errorf("script exit = %d, want claude's status 3", code)
	}
	if want := "--dangerously-skip-permissions --channels plugin:telegram@claude-plugins-official"; args != want {
		t.Errorf("claude args = %q, want %q", args, want)
	}
	log, err := os.ReadFile(filepath.Join(dir, "exit.log"))
	if err != nil {
		t.Fatalf("exit log not written: %v", err)
	}
	if !strings.Contains(string(log), "claude exited with status 3") {
		t.Errorf("exit log lacks the status:\n%s", log)
	}
	if !strings.Contains(string(log), "FINAL-SCREEN") {
		t.Errorf("exit log lacks the captured pane:\n%s", log)
	}
}

// runClaudeRunMissing executes the rendered claude-run.sh with claude absent
// and the log redirected into dir.
func runClaudeRunMissing(t *testing.T, dir string) error {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	script := testParams(t).ClaudeRunSh
	script = strings.Replace(script, "/usr/bin/claude", filepath.Join(dir, "absent"), 1)
	script = strings.Replace(script, "/home/claude/data/claude-exit.log", filepath.Join(dir, "exit.log"), 1)
	scriptPath := filepath.Join(dir, "claude-run.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return exec.Command(sh, scriptPath).Run()
}

// An instance created from a pre-apt golden image has no /usr/bin/claude; the
// log must say why rather than leave an unexplained restart loop.
func TestClaudeRun_MissingBinary(t *testing.T) {
	dir := t.TempDir()
	err := runClaudeRunMissing(t, dir)
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 127 {
		t.Errorf("exit = %v, want status 127", err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "exit.log"))
	if !strings.Contains(string(log), "predates the apt-based install") {
		t.Errorf("exit log lacks the golden-image hint:\n%s", log)
	}
}

// The missing-binary path is the one an old golden image restart-loops on, so
// it must honour the size cap too.
func TestClaudeRun_MissingBinaryRotatesLargeLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit.log")
	big := strings.Repeat("x", 1048577)
	if err := os.WriteFile(logPath, []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
	_ = runClaudeRunMissing(t, dir)

	rotated, err := os.ReadFile(logPath + ".1")
	if err != nil || string(rotated) != big {
		t.Errorf("oversized log not rotated to .1 intact (err: %v)", err)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "predates the apt-based install") || strings.Contains(string(log), "xxx") {
		t.Errorf("new log should hold only the new record, got %d bytes", len(log))
	}
}

func TestClaudeRun_CleanExitAppends(t *testing.T) {
	dir := t.TempDir()
	runClaudeRun(t, dir, "0")
	code, _ := runClaudeRun(t, dir, "0")
	if code != 0 {
		t.Errorf("script exit = %d, want 0", code)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "exit.log"))
	if n := strings.Count(string(log), "claude exited with status 0"); n != 2 {
		t.Errorf("exit log has %d records, want 2 (append, not overwrite):\n%s", n, log)
	}
}

func TestClaudeRun_RotatesLargeLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit.log")
	big := strings.Repeat("x", 1048577)
	if err := os.WriteFile(logPath, []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
	runClaudeRun(t, dir, "1")

	rotated, err := os.ReadFile(logPath + ".1")
	if err != nil || string(rotated) != big {
		t.Errorf("oversized log not rotated to .1 intact (err: %v)", err)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.HasPrefix(string(log), "=== ") || strings.Contains(string(log), "xxx") {
		t.Errorf("new log should hold only the new record, got %d bytes", len(log))
	}
}

func TestClaudeRun_KeepsLogAtLimit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit.log")
	if err := os.WriteFile(logPath, []byte(strings.Repeat("x", 1048576)), 0644); err != nil {
		t.Fatal(err)
	}
	runClaudeRun(t, dir, "1")
	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Error("log at exactly the limit was rotated; only larger logs should be")
	}
}
