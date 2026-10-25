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
			// An interactive session with --channels would take the bot from the
			// service's poller; interactive use attaches to the service's tmux.
			wantAlias := "alias claude='/usr/bin/claude --dangerously-skip-permissions'\n"
			if aliases != wantAlias {
				t.Errorf("alias:\ngot:  %q\nwant: %q", aliases, wantAlias)
			}

			if strings.Contains(output, "/usr/local/bin/claude") {
				t.Error("user-data still references the npm install path /usr/local/bin/claude")
			}
		})
	}
}

// The blind Enter presses answered whatever prompt appeared first, and picked
// "No, exit" on the folder-trust dialog. Prompts are pre-answered in config now.
func TestRenderUserData_NoBlindKeystrokes(t *testing.T) {
	for _, params := range []*InstanceParams{testParams(t), testParamsNoTelegram(t)} {
		output := renderUserData(t, params)
		unit, _ := writeFile(t, output, "/home/claude/.config/systemd/user/claude-assistant.service")
		if strings.Contains(unit, "ExecStartPost") || strings.Contains(unit, "send-keys") {
			t.Error("unit still sends blind keystrokes after start")
		}
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

// scriptEnv says what exists on the "instance" a claude-run.sh test runs on.
// setup runs after the stubs are created and may break them (given by path).
type scriptEnv struct {
	claude, bun, seed bool
	setup             func(claudePath, bunPath, seedDir string)
	// tokenFile is the raw content of the token file. nil = a well-formed file
	// holding testToken; noTokenFile = no file at all. Ignored without telegram.
	tokenFile   *string
	noTokenFile bool
}

var allPresent = scriptEnv{claude: true, bun: true, seed: true}

// scriptResult is what a claude-run.sh run left behind.
type scriptResult struct {
	code       int
	claudeArgs string // "" = claude was not started
	// tokenSet/tokenSeen: whether claude had TELEGRAM_BOT_TOKEN in its
	// environment, and its exact value.
	tokenSet  bool
	tokenSeen string
	argv      []string // claude's arguments, one per element
	output    string   // the script's own stdout and stderr
	log       string   // exit log; "" = nothing written
	tmuxRan   bool
}

// runScript executes the rendered claude-run.sh for params with claude, bun,
// the seed and tmux replaced by stubs under dir, and the log redirected there.
// In --preflight mode PATH holds only the tools the script legitimately needs
// (no tmux), so a preflight that reached for tmux would fail.
func runScript(t *testing.T, params *InstanceParams, dir, claudeExit string, env scriptEnv, args ...string) scriptResult {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	stubClaude := filepath.Join(dir, "claude")
	stubTmux := filepath.Join(dir, "tmux")
	stubBun := filepath.Join(dir, "bun")
	seedDir := filepath.Join(dir, "seed")
	writeStub := func(path, body string) {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if env.claude {
		writeStub(stubClaude, `echo "$@" > "`+filepath.Join(dir, "args")+`"
printf '%s\0' "$@" > "`+filepath.Join(dir, "argv")+`"
if [ -n "${TELEGRAM_BOT_TOKEN+x}" ]; then printf '%s' "$TELEGRAM_BOT_TOKEN" > "`+filepath.Join(dir, "token-seen")+`"; fi
exit `+claudeExit+"\n")
	}
	writeStub(stubTmux, `touch "`+filepath.Join(dir, "tmux-ran")+`"; echo FINAL-SCREEN`+"\n")
	if env.bun {
		writeStub(stubBun, "exit 0\n")
	}
	if env.seed {
		if err := os.MkdirAll(filepath.Join(seedDir, "cache"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	tokenPath := filepath.Join(dir, "telegram-token")
	if !env.noTokenFile {
		content := testToken + "\n"
		if env.tokenFile != nil {
			content = *env.tokenFile
		}
		if err := os.WriteFile(tokenPath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if env.setup != nil {
		env.setup(stubClaude, stubBun, seedDir)
	}

	script := params.ClaudeRunSh
	script = strings.ReplaceAll(script, "/usr/local/bin/bun", stubBun)
	script = strings.ReplaceAll(script, "/opt/claude-seed", seedDir)
	script = strings.ReplaceAll(script, params.TokenFile, tokenPath)
	script = strings.Replace(script, "/usr/bin/claude", stubClaude, 1)
	script = strings.Replace(script, "/usr/bin/tmux", stubTmux, 1)
	script = strings.Replace(script, "/home/claude/data/claude-exit.log", filepath.Join(dir, "exit.log"), 1)
	scriptPath := filepath.Join(dir, "claude-run.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(sh, append([]string{scriptPath}, args...)...)
	// A token inherited from the developer's shell would fake a pass.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TELEGRAM_BOT_TOKEN=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if len(args) > 0 && args[0] == "--preflight" {
		bin := filepath.Join(dir, "path")
		if err := os.MkdirAll(bin, 0755); err != nil {
			t.Fatal(err)
		}
		for _, tool := range []string{"date", "stat", "mv", "cat"} {
			real, err := exec.LookPath(tool)
			if err != nil {
				t.Skipf("%s not available", tool)
			}
			if err := os.Symlink(real, filepath.Join(bin, tool)); err != nil && !os.IsExist(err) {
				t.Fatal(err)
			}
		}
		cmd.Env = []string{"PATH=" + bin}
	}
	out, err := cmd.CombinedOutput()
	res := scriptResult{output: string(out)}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running script: %v", err)
	}
	gotArgs, _ := os.ReadFile(filepath.Join(dir, "args"))
	res.claudeArgs = strings.TrimSpace(string(gotArgs))
	if seen, err := os.ReadFile(filepath.Join(dir, "token-seen")); err == nil {
		res.tokenSet, res.tokenSeen = true, string(seen)
	}
	if argv, err := os.ReadFile(filepath.Join(dir, "argv")); err == nil && len(argv) > 0 {
		res.argv = strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00")
	}
	log, _ := os.ReadFile(filepath.Join(dir, "exit.log"))
	res.log = string(log)
	_, statErr := os.Stat(filepath.Join(dir, "tmux-ran"))
	res.tmuxRan = statErr == nil
	return res
}

// runClaudeRun runs claude-run.sh on a healthy instance (telegram on).
func runClaudeRun(t *testing.T, dir string, claudeExit string, args ...string) (int, string) {
	t.Helper()
	res := runScript(t, testParams(t), dir, claudeExit, allPresent, args...)
	return res.code, res.claudeArgs
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

// The preflight's guard line is what an old golden image writes on every start
// attempt, so it must honour the size cap too.
func TestClaudeRun_PreflightRotatesLargeLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "exit.log")
	big := strings.Repeat("x", 1048577)
	if err := os.WriteFile(logPath, []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
	res := runScript(t, testParams(t), dir, "0", scriptEnv{bun: true, seed: true}, "--preflight")
	if res.code == 0 {
		t.Fatal("preflight passed without claude")
	}

	rotated, err := os.ReadFile(logPath + ".1")
	if err != nil || string(rotated) != big {
		t.Errorf("oversized log not rotated to .1 intact (err: %v)", err)
	}
	if !strings.Contains(res.log, "predates the apt-based install") || strings.Contains(res.log, "xxx") {
		t.Errorf("new log should hold only the new record, got %d bytes", len(res.log))
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
