package cloudinit

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/antoniocascais/motoko/pkg/config"
	"gopkg.in/yaml.v3"
)

type userData struct {
	WriteFiles []struct {
		Path        string `yaml:"path"`
		Content     string `yaml:"content"`
		Permissions string `yaml:"permissions"`
	} `yaml:"write_files"`
	Runcmd []string `yaml:"runcmd"`
}

func parseUserData(t *testing.T, output string) userData {
	t.Helper()
	var doc userData
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(output, "#cloud-config\n")), &doc); err != nil {
		t.Fatalf("user-data is not valid YAML: %v\n%s", err, output)
	}
	return doc
}

func (d userData) file(t *testing.T, path string) (content, perms string) {
	t.Helper()
	for _, f := range d.WriteFiles {
		if f.Path == path {
			return f.Content, f.Permissions
		}
	}
	t.Fatalf("write_files has no entry for %s", path)
	return "", ""
}

func (d userData) runcmdIndex(substr string) int {
	for i, c := range d.Runcmd {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

// variants covers the cross product the config depends on: Telegram on/off and
// the default and a custom VM user (with characters the project slug rewrites).
type variant struct {
	name     string
	user     string
	telegram bool
}

var variants = []variant{
	{"telegram_default_user", "claude", true},
	{"no_telegram_default_user", "claude", false},
	{"telegram_custom_user", "agent_1", true},
	{"no_telegram_custom_user", "agent_1", false},
}

func (v variant) params(t *testing.T) *InstanceParams {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.VMUser = v.user
	token := ""
	if v.telegram {
		token = testToken
	}
	p, err := NewInstanceParams(cfg, "vm1", "vm1", token, []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest1 u@h"}, "")
	if err != nil {
		t.Fatalf("NewInstanceParams: %v", err)
	}
	return p
}

func TestZeroTouch_ManagedSettings(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			doc := parseUserData(t, renderUserData(t, v.params(t)))
			content, perms := doc.file(t, "/etc/claude-code/managed-settings.json")
			if perms != "0644" {
				t.Errorf("permissions = %q, want 0644", perms)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(content), &got); err != nil {
				t.Fatalf("managed settings are not valid JSON (claude would refuse to start): %v\n%s", err, content)
			}
			want := map[string]any{"skipDangerousModePermissionPrompt": true}
			if v.telegram {
				want["enabledPlugins"] = map[string]any{"telegram@claude-plugins-official": true}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("managed settings = %v, want exactly %v", got, want)
			}
			if strings.Contains(content, testToken) || strings.Contains(content, "vm1") {
				t.Error("managed settings carry instance-specific or secret data")
			}
		})
	}
}

func TestZeroTouch_ClaudeJSONPreseed(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			doc := parseUserData(t, renderUserData(t, v.params(t)))
			content, perms := doc.file(t, "/home/"+v.user+"/.claude.json")
			if perms != "0600" {
				t.Errorf("permissions = %q, want 0600", perms)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(content), &got); err != nil {
				t.Fatalf("not valid JSON: %v\n%s", err, content)
			}
			want := map[string]any{
				"hasCompletedOnboarding": true,
				"projects": map[string]any{
					"/home/" + v.user + "/data": map[string]any{"hasTrustDialogAccepted": true},
				},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf(".claude.json = %v, want %v", got, want)
			}
			if v.user != "claude" && strings.Contains(content, "/home/claude/") {
				t.Error("trust path hardcodes the default user")
			}
		})
	}
}

// The path is data in a JSON document, not template text.
func TestClaudeJSON_EscapesPath(t *testing.T) {
	out, err := claudeJSON("/home/we\"ird\\user/data")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Projects map[string]map[string]bool `json:"projects"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if !got.Projects["/home/we\"ird\\user/data"]["hasTrustDialogAccepted"] {
		t.Errorf("trust key lost: %s", out)
	}
}

// The token may only live in 0600 files the VM user owns; never in a world-
// readable file, a command line (runcmd), the unit, or the alias.
func TestZeroTouch_TokenOnlyInPrivateFiles(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			output := renderUserData(t, v.params(t))
			doc := parseUserData(t, output)

			total := strings.Count(output, testToken)
			inFiles := 0
			for _, f := range doc.WriteFiles {
				n := strings.Count(f.Content, testToken)
				inFiles += n
				if n > 0 && f.Permissions != "0600" {
					t.Errorf("%s holds the token with permissions %q, want 0600", f.Path, f.Permissions)
				}
				if n > 0 && f.Path != "/home/"+v.user+"/.claude-env" {
					t.Errorf("token written to unexpected file %s", f.Path)
				}
			}
			for _, c := range doc.Runcmd {
				if strings.Contains(c, testToken) {
					t.Errorf("token on a runcmd command line: %s", c)
				}
			}
			if total != inFiles {
				t.Errorf("token appears %d times in the document but %d times in write_files contents", total, inFiles)
			}
			wantInFiles := 0
			if v.telegram {
				wantInFiles = 1
			}
			if inFiles != wantInFiles {
				t.Errorf("token occurrences = %d, want %d", inFiles, wantInFiles)
			}
		})
	}
}

func TestZeroTouch_EnvironmentSplit(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			doc := parseUserData(t, renderUserData(t, v.params(t)))
			etc, _ := doc.file(t, "/etc/environment")
			svc, perms := doc.file(t, "/home/"+v.user+"/.claude-env")
			if perms != "0600" {
				t.Errorf("service env permissions = %q, want 0600", perms)
			}
			seed := "CLAUDE_CODE_PLUGIN_SEED_DIR=/opt/claude-seed"
			state := "TELEGRAM_STATE_DIR=/home/" + v.user + "/data/telegram"
			for name, content := range map[string]string{"/etc/environment": etc, "service env": svc} {
				has := func(line string) bool { return strings.Contains("\n"+content, "\n"+line+"\n") }
				if has(seed) != v.telegram || has(state) != v.telegram {
					t.Errorf("%s: seed=%v state=%v, want both %v:\n%s", name, has(seed), has(state), v.telegram, content)
				}
			}
			if strings.Contains(etc, "TELEGRAM_BOT_TOKEN") {
				t.Error("/etc/environment is world-readable and must not carry the token")
			}
			if v.telegram && !strings.Contains(svc, "TELEGRAM_BOT_TOKEN="+testToken+"\n") {
				t.Error("service env lacks the token")
			}
		})
	}
}

// Everything on the data disk is created in runcmd, after the mount; a
// write_files entry there would be hidden by the mount (they run before it).
func TestZeroTouch_DataDiskSetupAfterMount(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			doc := parseUserData(t, renderUserData(t, v.params(t)))
			home := "/home/" + v.user
			for _, f := range doc.WriteFiles {
				if strings.HasPrefix(f.Path, home+"/data/") {
					t.Errorf("write_files writes %s onto the data disk mount point", f.Path)
				}
			}

			mkdata := doc.runcmdIndex("mkdir -p " + home + "/data")
			memory := doc.runcmdIndex("install -d -o " + v.user + " -g " + v.user + " " + home + "/data/memory")
			linkDir := doc.runcmdIndex("install -d -o " + v.user + " -g " + v.user + " " + home + "/.claude/projects/")
			link := doc.runcmdIndex("ln -s " + home + "/data/memory")
			state := doc.runcmdIndex("install -d -m 0700 -o " + v.user + " -g " + v.user + " " + home + "/data/telegram")
			chown := doc.runcmdIndex("chown -R " + v.user + ":" + v.user + " " + home)
			linger := doc.runcmdIndex("loginctl enable-linger")
			enable := doc.runcmdIndex("enable --now claude-assistant.service")

			steps := []struct {
				name string
				idx  int
			}{{"mkdir data", mkdata}, {"memory dir", memory}, {"memory link dir", linkDir}, {"memory link", link}}
			if v.telegram {
				steps = append(steps, struct {
					name string
					idx  int
				}{"telegram state dir", state})
			} else if state != -1 {
				t.Error("telegram state dir created without telegram")
			}
			steps = append(steps, struct {
				name string
				idx  int
			}{"chown home", chown}, struct {
				name string
				idx  int
			}{"enable-linger", linger}, struct {
				name string
				idx  int
			}{"enable service", enable})
			for i, s := range steps {
				if s.idx < 0 {
					t.Fatalf("runcmd lacks %s:\n%s", s.name, strings.Join(doc.Runcmd, "\n"))
				}
				if i > 0 && steps[i-1].idx >= s.idx {
					t.Errorf("%s (#%d) must run before %s (#%d)", steps[i-1].name, steps[i-1].idx, s.name, s.idx)
				}
			}

			// Pairing state and memory must survive a rebuild: nothing may delete them.
			for _, c := range doc.Runcmd {
				if strings.Contains(c, "rm ") && (strings.Contains(c, "data/telegram") || strings.Contains(c, "data/memory")) {
					t.Errorf("runcmd deletes persistent state: %s", c)
				}
			}
		})
	}
}

func TestZeroTouch_MemorySymlink(t *testing.T) {
	tests := []struct {
		user string
		slug string
	}{
		{"claude", "-home-claude-data"},
		{"agent_1", "-home-agent-1-data"},
		{"a-b", "-home-a-b-data"},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			v := variant{"x", tt.user, false}
			p := v.params(t)
			dir := "/home/" + tt.user + "/.claude/projects/" + tt.slug
			if p.MemoryLinkDir != dir || p.MemoryLink != dir+"/memory" {
				t.Errorf("memory link = %s (dir %s), want %s/memory", p.MemoryLink, p.MemoryLinkDir, dir)
			}
			doc := parseUserData(t, renderUserData(t, p))
			// Guarded so an existing memory dir is left alone instead of
			// getting a link created inside it.
			want := "test -e " + p.MemoryLink + " || test -L " + p.MemoryLink + " || ln -s /home/" + tt.user + "/data/memory " + p.MemoryLink
			if doc.runcmdIndex(want) < 0 {
				t.Errorf("runcmd lacks guarded link %q:\n%s", want, strings.Join(doc.Runcmd, "\n"))
			}
		})
	}
}

// Real shell semantics of the guarded link: create when absent, keep a real
// directory's contents, keep an existing link.
func TestZeroTouch_MemoryLinkGuardBehaviour(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	p := variants[0].params(t)
	doc := parseUserData(t, renderUserData(t, p))
	cmd := doc.Runcmd[doc.runcmdIndex("ln -s ")]

	setup := func(t *testing.T) (root string, script string) {
		root = t.TempDir()
		data := filepath.Join(root, "data/memory")
		if err := os.MkdirAll(data, 0755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "projects/slug/memory")
		if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
			t.Fatal(err)
		}
		s := strings.ReplaceAll(cmd, p.MemoryLink, link)
		s = strings.ReplaceAll(s, "/home/claude/data/memory", data)
		return root, s
	}
	run := func(t *testing.T, script string) {
		t.Helper()
		sh, _ := exec.LookPath("sh")
		if out, err := exec.Command(sh, "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	t.Run("absent_creates_link", func(t *testing.T) {
		root, script := setup(t)
		run(t, script)
		target, err := os.Readlink(filepath.Join(root, "projects/slug/memory"))
		if err != nil || target != filepath.Join(root, "data/memory") {
			t.Errorf("link target = %q, err %v", target, err)
		}
	})
	t.Run("existing_dir_untouched", func(t *testing.T) {
		root, script := setup(t)
		dir := filepath.Join(root, "projects/slug/memory")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("keep"), 0644); err != nil {
			t.Fatal(err)
		}
		run(t, script)
		if _, err := os.Lstat(filepath.Join(dir, "memory")); err == nil {
			t.Error("a link was created inside the existing memory dir")
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "note.md")); string(b) != "keep" {
			t.Error("existing memory content changed")
		}
	})
	t.Run("existing_link_kept", func(t *testing.T) {
		root, script := setup(t)
		link := filepath.Join(root, "projects/slug/memory")
		if err := os.Symlink("/elsewhere", link); err != nil {
			t.Fatal(err)
		}
		run(t, script)
		if target, _ := os.Readlink(link); target != "/elsewhere" {
			t.Errorf("existing link replaced, now %q", target)
		}
	})
}

func TestZeroTouch_UnitAndNoDNS(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			output := renderUserData(t, v.params(t))
			if strings.Contains(output, "nameserver") || strings.Contains(output, "resolved.conf") {
				t.Error("DNS configuration introduced; VMs have no DNS")
			}
			doc := parseUserData(t, output)
			unit, _ := doc.file(t, "/home/"+v.user+"/.config/systemd/user/claude-assistant.service")
			if strings.Contains(unit, "ExecStartPost") || strings.Contains(unit, "send-keys") {
				t.Error("unit sends blind keystrokes after start")
			}
			if got := strings.Contains(unit, "--channels"); got != v.telegram {
				t.Errorf("service --channels = %v, want %v", got, v.telegram)
			}
		})
	}
}

func TestZeroTouch_SeedDirMatchesBakedSeed(t *testing.T) {
	p := variants[0].params(t)
	if p.SeedDir != config.SeedDir {
		t.Errorf("SeedDir = %q, want the golden's %q", p.SeedDir, config.SeedDir)
	}
	if !strings.Contains(p.ClaudeRunSh, config.BunPath) || !strings.Contains(p.ClaudeRunSh, config.SeedDir) {
		t.Error("run script's guard does not check the baked bun and seed paths")
	}
}

// --- preflight (the unit's ExecCondition) ---

const (
	claudeGuardLine = "predates the apt-based install"
	seedGuardLine   = "predates zero-touch"
)

// The decision table: claude, bun and seed each present or missing, telegram on
// or off. The expectation is written out independently of the script: the
// instance can start unless claude is missing, or telegram is on and bun or the
// seed is missing. Dropping any one of the three checks fails some row.
func TestPreflight_DecisionTable(t *testing.T) {
	for _, telegram := range []bool{true, false} {
		for _, claude := range []bool{true, false} {
			for _, bun := range []bool{true, false} {
				for _, seed := range []bool{true, false} {
					name := fmt.Sprintf("telegram=%v/claude=%v/bun=%v/seed=%v", telegram, claude, bun, seed)
					t.Run(name, func(t *testing.T) {
						dir := t.TempDir()
						p := variant{"x", "claude", telegram}.params(t)
						res := runScript(t, p, dir, "0", scriptEnv{claude: claude, bun: bun, seed: seed}, "--preflight")

						wantLine := ""
						switch {
						case !claude:
							wantLine = claudeGuardLine
						case telegram && (!bun || !seed):
							wantLine = seedGuardLine
						}
						assertPreflight(t, res, wantLine)
					})
				}
			}
		}
	}
}

// Ways bun or the seed can be present but unusable. Each kills a mutant of the
// check (-x to -e, -d to -e, checking only the seed root).
func TestPreflight_UnusableBunOrSeed(t *testing.T) {
	notExecutable := func(_, bunPath, _ string) {
		if err := os.Chmod(bunPath, 0644); err != nil {
			t.Fatal(err)
		}
	}
	seedIsFile := func(_, _, seedDir string) {
		if err := os.RemoveAll(seedDir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(seedDir, []byte("not a dir"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	seedWithoutCache := func(_, _, seedDir string) {
		if err := os.RemoveAll(filepath.Join(seedDir, "cache")); err != nil {
			t.Fatal(err)
		}
	}
	cacheIsFile := func(_, _, seedDir string) {
		cache := filepath.Join(seedDir, "cache")
		if err := os.RemoveAll(cache); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	claudeNotExecutable := func(claudePath, _, _ string) {
		if err := os.Chmod(claudePath, 0644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name     string
		telegram bool
		setup    func(claude, bun, seed string)
		wantLine string
	}{
		{"bun_not_executable", true, notExecutable, seedGuardLine},
		{"seed_path_is_a_file", true, seedIsFile, seedGuardLine},
		{"seed_without_cache_dir", true, seedWithoutCache, seedGuardLine},
		{"cache_is_a_file", true, cacheIsFile, seedGuardLine},
		{"claude_not_executable", true, claudeNotExecutable, claudeGuardLine},
		{"claude_not_executable_telegram_off", false, claudeNotExecutable, claudeGuardLine},
		{"bun_not_executable_telegram_off_is_fine", false, notExecutable, ""},
		{"seed_cache_is_a_file_telegram_off_is_fine", false, cacheIsFile, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := variant{"x", "claude", tt.telegram}.params(t)
			env := scriptEnv{claude: true, bun: true, seed: true, setup: tt.setup}
			assertPreflight(t, runScript(t, p, dir, "0", env, "--preflight"), tt.wantLine)
		})
	}
}

// wantLine "" = the instance can start: exit 0, nothing logged. Otherwise the
// start must be refused with a status systemd treats as "condition false"
// (1-254), exactly one log record carrying the hint, and neither claude nor
// tmux touched.
func assertPreflight(t *testing.T, res scriptResult, wantLine string) {
	t.Helper()
	if res.tmuxRan {
		t.Error("preflight ran tmux")
	}
	if res.claudeArgs != "" {
		t.Errorf("preflight started claude (%q)", res.claudeArgs)
	}
	if wantLine == "" {
		if res.code != 0 {
			t.Errorf("exit = %d, want 0", res.code)
		}
		if res.log != "" {
			t.Errorf("a passing preflight wrote to the log:\n%s", res.log)
		}
		return
	}
	if res.code < 1 || res.code > 254 {
		t.Errorf("exit = %d, want 1-254 (systemd: condition false, no restart)", res.code)
	}
	if n := strings.Count(res.log, "=== "); n != 1 {
		t.Errorf("log has %d records, want exactly 1:\n%s", n, res.log)
	}
	if !strings.Contains(res.log, wantLine) {
		t.Errorf("log lacks %q:\n%s", wantLine, res.log)
	}
	other := seedGuardLine
	if wantLine == seedGuardLine {
		other = claudeGuardLine
	}
	if strings.Contains(res.log, other) {
		t.Errorf("log carries the other guard's line too:\n%s", res.log)
	}
	if wantLine == seedGuardLine && !strings.Contains(res.log, "motoko build") {
		t.Errorf("guard line does not say how to fix it:\n%s", res.log)
	}
}

// Two start attempts leave two records, one each.
func TestPreflight_OneRecordPerAttempt(t *testing.T) {
	dir := t.TempDir()
	p := variant{"x", "claude", true}.params(t)
	env := scriptEnv{claude: true}
	runScript(t, p, dir, "0", env, "--preflight")
	res := runScript(t, p, dir, "0", env, "--preflight")
	if n := strings.Count(res.log, "=== "); n != 2 {
		t.Errorf("log has %d records after 2 attempts, want 2:\n%s", n, res.log)
	}
}

// The guards live in one script: each message appears once in the rendered
// claude-run.sh, and there is no second preflight script among the files.
func TestPreflight_SingleSourceOfTruth(t *testing.T) {
	p := variant{"x", "claude", true}.params(t)
	for _, line := range []string{claudeGuardLine, seedGuardLine} {
		if n := strings.Count(p.ClaudeRunSh, line); n != 1 {
			t.Errorf("%q appears %d times in claude-run.sh, want 1", line, n)
		}
	}
	doc := parseUserData(t, renderUserData(t, p))
	for _, f := range doc.WriteFiles {
		if strings.Contains(f.Content, claudeGuardLine) && f.Path != "/home/claude/bin/claude-run.sh" {
			t.Errorf("guard duplicated in %s", f.Path)
		}
	}
}

// Normal mode does not re-run the guards: a started claude is not second-guessed.
func TestClaudeRun_NormalModeSkipsPreflight(t *testing.T) {
	dir := t.TempDir()
	p := variant{"x", "claude", true}.params(t)
	res := runScript(t, p, dir, "0", scriptEnv{claude: true}, "--dangerously-skip-permissions")
	if res.code != 0 || res.claudeArgs == "" || strings.Contains(res.log, seedGuardLine) {
		t.Errorf("normal mode ran the guards: %+v", res)
	}
}

// Interactive shells must never see the token: the plugin server started by an
// interactive session would take over the bot from the service's poller.
func TestZeroTouch_TokenAbsentFromInteractiveSurfaces(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			doc := parseUserData(t, renderUserData(t, v.params(t)))
			home := "/home/" + v.user
			for _, path := range []string{
				"/etc/environment",
				"/etc/claude-code/managed-settings.json",
				home + "/.bash_aliases",
				home + "/.claude.json",
			} {
				content, _ := doc.file(t, path)
				if strings.Contains(content, "TELEGRAM_BOT_TOKEN") || strings.Contains(content, testToken) {
					t.Errorf("%s exposes the token to interactive sessions", path)
				}
			}
			for _, f := range doc.WriteFiles {
				base := filepath.Base(f.Path)
				if strings.HasPrefix(base, ".en"+"v") || strings.Contains(f.Path, "/telegram/") ||
					strings.Contains(f.Path, "/.claude/channels/") {
					t.Errorf("write_files writes a plugin state file %s; the token belongs only in the service env file", f.Path)
				}
			}
		})
	}
}

// Type=forking around tmux masks claude's exit status, so the guards sit in an
// ExecCondition that runs before ExecStart. A skipped start is not restarted
// (systemd service_shall_restart: Restart=always returns false for
// SERVICE_SKIP_CONDITION), whereas RestartPreventExitStatus could never fire.
func TestZeroTouch_UnitGatesStartWithPreflight(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			doc := parseUserData(t, renderUserData(t, v.params(t)))
			unit, _ := doc.file(t, "/home/"+v.user+"/.config/systemd/user/claude-assistant.service")
			lines := strings.Split(unit, "\n")
			indexOf := func(prefix string) int {
				for i, l := range lines {
					if strings.HasPrefix(l, prefix) {
						return i
					}
				}
				return -1
			}
			cond, start := indexOf("ExecCondition="), indexOf("ExecStart=")
			if cond < 0 || start < 0 || cond > start {
				t.Fatalf("ExecCondition (line %d) must exist and precede ExecStart (line %d):\n%s", cond, start, unit)
			}
			script := "/home/" + v.user + "/bin/claude-run.sh"
			if want := "ExecCondition=" + script + " --preflight"; lines[cond] != want {
				t.Errorf("got %q, want %q", lines[cond], want)
			}
			if _, perms := doc.file(t, script); perms != "0755" {
				t.Errorf("%s permissions = %q, want 0755", script, perms)
			}
			if strings.Contains(unit, "RestartPreventExitStatus") {
				t.Error("RestartPreventExitStatus is dead under Type=forking and must not return")
			}
			found := false
			for _, l := range lines {
				found = found || l == "Restart=always"
			}
			if !found {
				t.Error("claude crashes must still restart: Restart=always missing")
			}
			if !strings.Contains(unit, "Type=forking") {
				t.Error("unit type changed; the ExecCondition rationale assumes forking")
			}
		})
	}
}

func TestManagedSettings_TrailingNewline(t *testing.T) {
	for _, telegram := range []bool{false, true} {
		out, err := managedSettings(telegram)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(out, "\n") {
			t.Errorf("telegram=%v: missing trailing newline", telegram)
		}
	}
}
