package cloudinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The bot token reaches exactly one process: the claude that claude-run.sh
// starts. These tests run the rendered script against stubs and look at what
// the stub claude received.

const tokenGuardLine = "token file"

var runArgs = []string{"--dangerously-skip-permissions", "--channels", "plugin:telegram@claude-plugins-official"}

func TestClaudeRun_PassesTokenToClaudeOnly(t *testing.T) {
	// The value must be exactly the token whatever surrounds it in the file.
	tests := []struct{ name, content string }{
		{"trailing_newline", testToken + "\n"},
		{"no_newline", testToken},
		{"crlf", testToken + "\r\n"},
		{"trailing_spaces", testToken + "  \t\n"},
		{"extra_lines_ignored", testToken + "\nsecond line\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := variant{"x", "claude", true}.params(t)
			env := allPresent
			content := tt.content
			env.tokenFile = &content
			res := runScript(t, p, dir, "0", env, runArgs...)

			if !res.tokenSet || res.tokenSeen != testToken {
				t.Errorf("claude saw token set=%v value %q, want exactly %q", res.tokenSet, res.tokenSeen, testToken)
			}
			if strings.Join(res.argv, " ") != strings.Join(runArgs, " ") || len(res.argv) != len(runArgs) {
				t.Errorf("claude argv = %q, want %q", res.argv, runArgs)
			}
			for _, a := range res.argv {
				if strings.Contains(a, testToken) {
					t.Errorf("token on claude's command line: %q", a)
				}
			}
			if strings.Contains(res.output, testToken) {
				t.Errorf("script printed the token:\n%s", res.output)
			}
			if strings.Contains(res.log, testToken) {
				t.Errorf("exit log carries the token:\n%s", res.log)
			}
		})
	}
}

// The file is parsed, never executed: shell syntax in it must stay inert.
func TestClaudeRun_TokenFileIsDataNotCode(t *testing.T) {
	dir := t.TempDir()
	p := variant{"x", "claude", true}.params(t)
	marker := filepath.Join(dir, "pwned")
	evil := "$(touch " + marker + ")`touch " + marker + "`\n"
	env := allPresent
	env.tokenFile = &evil
	runScript(t, p, dir, "0", env, runArgs...)
	if _, err := os.Stat(marker); err == nil {
		t.Error("token file content was executed by the shell")
	}
}

// Without Telegram the script behaves as before: no token, same argv, even if
// a stray token file is lying around.
func TestClaudeRun_NoTelegramNoToken(t *testing.T) {
	for _, withFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_file", true: "stray_file"}[withFile], func(t *testing.T) {
			dir := t.TempDir()
			p := variant{"x", "claude", false}.params(t)
			if p.TokenFile == "" {
				t.Fatal("TokenFile unset")
			}
			env := allPresent
			env.noTokenFile = !withFile
			args := []string{"--dangerously-skip-permissions"}
			res := runScript(t, p, dir, "0", env, args...)
			if res.tokenSet {
				t.Errorf("claude saw a token: %q", res.tokenSeen)
			}
			if len(res.argv) != 1 || res.argv[0] != args[0] || res.code != 0 {
				t.Errorf("argv = %q, code %d, want %q and 0", res.argv, res.code, args)
			}
			if strings.Contains(p.ClaudeRunSh, "TELEGRAM_BOT_TOKEN") || strings.Contains(p.ClaudeRunSh, "read_token") {
				t.Error("no-telegram script still carries token handling")
			}
		})
	}
}

// Telegram on but no usable token: a preflight failure with one log record, the
// same way a missing bun/seed is handled, rather than a claude that starts and
// leaves the bot silently dead. Telegram off never looks for the file.
func TestPreflight_TokenFile(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name     string
		telegram bool
		env      scriptEnv
		wantLine string
	}{
		{"valid", true, allPresent, ""},
		{"valid_crlf", true, scriptEnv{claude: true, bun: true, seed: true, tokenFile: str(testToken + "\r\n")}, ""},
		{"missing", true, scriptEnv{claude: true, bun: true, seed: true, noTokenFile: true}, tokenGuardLine},
		{"empty", true, scriptEnv{claude: true, bun: true, seed: true, tokenFile: str("")}, tokenGuardLine},
		{"only_newline", true, scriptEnv{claude: true, bun: true, seed: true, tokenFile: str("\n")}, tokenGuardLine},
		{"only_whitespace", true, scriptEnv{claude: true, bun: true, seed: true, tokenFile: str("  \r\n")}, tokenGuardLine},
		{"telegram_off_no_file", false, scriptEnv{claude: true, noTokenFile: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := variant{"x", "claude", tt.telegram}.params(t)
			res := runScript(t, p, t.TempDir(), "0", tt.env, "--preflight")
			assertPreflight(t, res, tt.wantLine)
			if strings.Contains(res.log+res.output, testToken) {
				t.Error("preflight leaked the token")
			}
		})
	}
}

// What tmux does with the environment is the whole reason the token is not in
// the unit: tmux copies the environment it starts with into its global
// environment, which every later window or session inherits. This starts a real
// server on a private socket the way ExecStart does, with only the unit's
// EnvironmentFile as environment.
func TestTmuxServerNeverSeesToken(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}
	dir := t.TempDir()
	p := variant{"x", "claude", true}.params(t)
	doc := parseUserData(t, renderUserData(t, p))

	unitEnv, _ := doc.file(t, "/home/claude/.claude-env")
	var env []string
	for _, line := range strings.Split(unitEnv, "\n") {
		if strings.Contains(line, "=") {
			env = append(env, line)
		}
	}
	env = append(env, "PATH="+os.Getenv("PATH"), "HOME="+dir, "TMUX_TMPDIR="+dir)

	stubClaude := filepath.Join(dir, "claude")
	seen := filepath.Join(dir, "claude-saw")
	body := "#!/bin/sh\nprintf '%s' \"$TELEGRAM_BOT_TOKEN\" > " + seen + "\nsleep 30\n"
	if err := os.WriteFile(stubClaude, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "telegram-token")
	if err := os.WriteFile(tokenPath, []byte(testToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(p.ClaudeRunSh, "/usr/bin/claude", stubClaude, 1)
	script = strings.ReplaceAll(script, p.TokenFile, tokenPath)
	script = strings.Replace(script, "/home/claude/data/claude-exit.log", filepath.Join(dir, "exit.log"), 1)
	scriptPath := filepath.Join(dir, "claude-run.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		cmd := exec.Command(tmux, append([]string{"-L", "tokentest", "-f", "/dev/null"}, args...)...)
		cmd.Env = env
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	t.Cleanup(func() { run("kill-server") })

	if out := run("new-session", "-d", "-s", "claude", scriptPath+" --dangerously-skip-permissions"); out != "" {
		t.Skipf("cannot start tmux here: %s", out)
	}

	// The service's own claude does get the token (wait for the stub to write it).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, _ := os.ReadFile(seen); string(b) == testToken {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(seen)
			t.Fatalf("service claude saw %q, want the token", b)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if out := run("show-environment", "-g"); strings.Contains(out, "TELEGRAM_BOT_TOKEN") || strings.Contains(out, testToken) {
		t.Errorf("tmux global environment carries the token:\n%s", out)
	}
	if out := run("show-environment", "-t", "claude"); strings.Contains(out, "TELEGRAM_BOT_TOKEN") || strings.Contains(out, testToken) {
		t.Errorf("tmux session environment carries the token:\n%s", out)
	}

	// `tmux attach`, then C-b c: a new window must not inherit it.
	window := filepath.Join(dir, "window-saw")
	run("new-window", "-t", "claude", "printenv TELEGRAM_BOT_TOKEN > "+window+"; touch "+window+".done")
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(window + ".done"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new window never ran")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b, _ := os.ReadFile(window); len(b) != 0 {
		t.Errorf("a new tmux window inherited the token: %q", b)
	}
}
