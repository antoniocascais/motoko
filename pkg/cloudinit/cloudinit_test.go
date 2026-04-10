package cloudinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antoniocascais/motoko/pkg/config"
	"gopkg.in/yaml.v3"
)

// testToken is a valid Telegram Bot API token format for tests.
const testToken = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijk"

func testConfig() *config.Config {
	return config.DefaultConfig()
}

func testParams(t *testing.T) *InstanceParams {
	t.Helper()
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "test-vm", "test-vm", testToken, []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest1 user@host",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest2 user2@host2",
	}, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	return params
}

func testParamsNoTelegram(t *testing.T) *InstanceParams {
	t.Helper()
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "test-vm", "test-vm", "", []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest1 user@host",
	}, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	return params
}

func renderUserData(t *testing.T, params *InstanceParams) string {
	t.Helper()
	data, err := RenderUserData(params)
	if err != nil {
		t.Fatalf("RenderUserData() error = %v", err)
	}
	return string(data)
}

// --- NewInstanceParams tests ---

func TestNewInstanceParams_RejectsInvalidHostname(t *testing.T) {
	cfg := testConfig()
	_, err := NewInstanceParams(cfg, "vm1", "has\nnewline", testToken, nil, "")
	if err == nil {
		t.Error("expected error for hostname with newline")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("error should mention hostname, got: %v", err)
	}
}

func TestNewInstanceParams_RejectsInvalidToken(t *testing.T) {
	cfg := testConfig()
	_, err := NewInstanceParams(cfg, "vm1", "vm1", "not-a-token", nil, "")
	if err == nil {
		t.Error("expected error for invalid telegram token")
	}
	if !strings.Contains(err.Error(), "telegram token") {
		t.Errorf("error should mention telegram token, got: %v", err)
	}
}

func TestNewInstanceParams_SingleCharHostname(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "a", testToken, nil, "")
	if err != nil {
		t.Fatalf("single char hostname should be valid, got: %v", err)
	}
	output := renderUserData(t, params)
	if !strings.Contains(output, "hostname: a") {
		t.Error("user-data missing single-char hostname")
	}
}

func TestNewInstanceParams_MaxLengthHostname(t *testing.T) {
	cfg := testConfig()
	// 253 chars: max RFC 1123 hostname length
	hostname := strings.Repeat("a", 253)
	params, err := NewInstanceParams(cfg, "vm1", hostname, testToken, nil, "")
	if err != nil {
		t.Fatalf("253-char hostname should be valid, got: %v", err)
	}
	output := renderUserData(t, params)

	yamlContent := strings.TrimPrefix(output, "#cloud-config\n")
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlContent), &parsed); err != nil {
		t.Fatalf("user-data with max-length hostname is not valid YAML: %v", err)
	}
	if !strings.Contains(output, "hostname: "+hostname) {
		t.Error("user-data missing max-length hostname")
	}
}

func TestNewInstanceParams_OverMaxLengthHostname(t *testing.T) {
	cfg := testConfig()
	hostname := strings.Repeat("a", 254)
	_, err := NewInstanceParams(cfg, "vm1", hostname, testToken, nil, "")
	if err == nil {
		t.Error("254-char hostname should be rejected")
	}
}

func TestNewInstanceParams_ProxyURL(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	want := "http://192.168.122.1:3128"
	if params.ProxyURL != want {
		t.Errorf("ProxyURL = %q, want %q", params.ProxyURL, want)
	}
}

func TestNewInstanceParams_ClaudeMDWithPersona(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "You are a pirate.")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	if !strings.Contains(params.ClaudeMD, "# Active Persona") {
		t.Error("ClaudeMD missing '# Active Persona' header")
	}
	if !strings.Contains(params.ClaudeMD, "You are a pirate.") {
		t.Error("ClaudeMD missing persona content")
	}
}

func TestNewInstanceParams_NoPersona(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	if strings.Contains(params.ClaudeMD, "# Active Persona") {
		t.Error("ClaudeMD should not contain '# Active Persona' when no persona provided")
	}
}

func TestNewInstanceParams_ClaudeMDContainsProxy(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	if !strings.Contains(params.ClaudeMD, "192.168.122.1:3128") {
		t.Error("ClaudeMD missing proxy address")
	}
}

func TestNewInstanceParams_ScriptsRendered(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	if !strings.Contains(params.ClaudeRestartSh, "/home/claude") {
		t.Error("claude-restart.sh missing VMUser home path")
	}
	if !strings.Contains(params.CmdQueueSh, "/home/claude") {
		t.Error("cmd-queue.sh missing VMUser home path")
	}
}

func TestNewInstanceParams_ScriptsCustomUser(t *testing.T) {
	cfg := testConfig()
	cfg.VMUser = "agent"
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	if !strings.Contains(params.ClaudeRestartSh, "/home/agent") {
		t.Error("claude-restart.sh not using custom VMUser")
	}
	if !strings.Contains(params.CmdQueueSh, "/home/agent") {
		t.Error("cmd-queue.sh not using custom VMUser")
	}
}

func TestNewInstanceParams_FieldPropagation(t *testing.T) {
	cfg := testConfig()
	cfg.VMDefaults.Locale = "de_DE.UTF-8"
	cfg.VMDefaults.Timezone = "Europe/Berlin"
	params, err := NewInstanceParams(cfg, "myvm", "myhost", testToken, []string{"key1"}, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	if params.Name != "myvm" {
		t.Errorf("Name = %q, want %q", params.Name, "myvm")
	}
	if params.Hostname != "myhost" {
		t.Errorf("Hostname = %q, want %q", params.Hostname, "myhost")
	}
	if params.Locale != "de_DE.UTF-8" {
		t.Errorf("Locale = %q, want %q", params.Locale, "de_DE.UTF-8")
	}
	if params.Timezone != "Europe/Berlin" {
		t.Errorf("Timezone = %q, want %q", params.Timezone, "Europe/Berlin")
	}
	if params.TelegramBotToken != testToken {
		t.Errorf("TelegramBotToken = %q, want %q", params.TelegramBotToken, testToken)
	}
	if len(params.SSHAuthorizedKeys) != 1 || params.SSHAuthorizedKeys[0] != "key1" {
		t.Errorf("SSHAuthorizedKeys = %v, want [key1]", params.SSHAuthorizedKeys)
	}
}

func TestNewInstanceParams_LocaleTimezoneInUserData(t *testing.T) {
	cfg := testConfig()
	cfg.VMDefaults.Locale = "de_DE.UTF-8"
	cfg.VMDefaults.Timezone = "Europe/Berlin"
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	output := renderUserData(t, params)
	if !strings.Contains(output, "locale: de_DE.UTF-8") {
		t.Error("user-data missing custom locale")
	}
	if !strings.Contains(output, "timezone: Europe/Berlin") {
		t.Error("user-data missing custom timezone")
	}
}

// --- RenderUserData tests ---

func TestRenderUserData_CloudConfigHeader(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.HasPrefix(output, "#cloud-config\n") {
		t.Error("user-data must start with '#cloud-config'")
	}
}

func TestRenderUserData_ValidYAML(t *testing.T) {
	output := renderUserData(t, testParams(t))
	// Strip the #cloud-config header for YAML parsing
	yamlContent := strings.TrimPrefix(output, "#cloud-config\n")
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlContent), &parsed); err != nil {
		t.Fatalf("user-data is not valid YAML: %v\n\ncontent:\n%s", err, output)
	}
}

func TestRenderUserData_ContainsProxyConfig(t *testing.T) {
	output := renderUserData(t, testParams(t))
	checks := []string{
		`Acquire::http::Proxy "http://192.168.122.1:3128"`,
		"https_proxy=http://192.168.122.1:3128",
		"192.168.122.1 proxy",
	}
	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("user-data missing proxy config: %q", check)
		}
	}
}

func TestRenderUserData_ContainsSSHKeys(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest1") {
		t.Error("user-data missing first SSH key")
	}
	if !strings.Contains(output, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest2") {
		t.Error("user-data missing second SSH key")
	}
}

func TestRenderUserData_DisablesDNS(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "chattr +i /etc/resolv.conf") {
		t.Error("user-data missing DNS disable (chattr +i)")
	}
	if !strings.Contains(output, "systemctl disable --now systemd-resolved") {
		t.Error("user-data missing systemd-resolved disable")
	}
}

func TestRenderUserData_TelegramToken(t *testing.T) {
	output := renderUserData(t, testParams(t))
	count := strings.Count(output, "TELEGRAM_BOT_TOKEN="+testToken)
	if count != 2 {
		t.Errorf("telegram token should appear in exactly 2 locations, found %d", count)
	}
}

func TestRenderUserData_ContainsCLAUDEmd(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "Motoko Sandbox VM") {
		t.Error("user-data missing CLAUDE.md content")
	}
	if !strings.Contains(output, "/CLAUDE.md") {
		t.Error("user-data missing CLAUDE.md write_files path")
	}
}

func TestRenderUserData_ContainsScripts(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "bin/claude-restart.sh") {
		t.Error("user-data missing claude-restart.sh")
	}
	if !strings.Contains(output, "bin/cmd-queue.sh") {
		t.Error("user-data missing cmd-queue.sh")
	}
	// Both should have executable permissions
	if !strings.Contains(output, `permissions: "0755"`) {
		t.Error("user-data missing 0755 permissions for scripts")
	}
}

func TestRenderUserData_NoCalendarCheck(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if strings.Contains(output, "calendar-check") {
		t.Error("user-data should not contain calendar-check (user-specific, removed)")
	}
}

func TestRenderUserData_DiskSetup(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "/dev/vdb") {
		t.Error("user-data missing /dev/vdb disk setup")
	}
	if !strings.Contains(output, "ext4") {
		t.Error("user-data missing ext4 filesystem")
	}
}

func TestRenderUserData_HostsEntry(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "192.168.122.1 proxy") {
		t.Error("user-data missing proxy hosts entry")
	}
}

func TestRenderUserData_HostnameInCloudConfig(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "hostname: test-vm") {
		t.Error("user-data missing hostname directive")
	}
	// hostname also appears in /etc/hosts
	if !strings.Contains(output, "127.0.1.1 test-vm") {
		t.Error("user-data missing hostname in /etc/hosts")
	}
}

func TestRenderUserData_PromptInjectionCleanup(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "rm -rf /home/claude/data/.claude") {
		t.Error("user-data missing .claude cleanup from data volume")
	}
	if !strings.Contains(output, "rm -f /home/claude/data/CLAUDE.md") {
		t.Error("user-data missing CLAUDE.md cleanup from data volume")
	}
}

func TestRenderUserData_CloudInitCacheWipe(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "rm -rf /var/lib/cloud/instances /run/cloud-init") {
		t.Error("user-data missing cloud-init cache wipe")
	}
}

func TestRenderUserData_ZeroSSHKeys(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	output := renderUserData(t, params)

	// Must still be valid YAML even with no keys
	yamlContent := strings.TrimPrefix(output, "#cloud-config\n")
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlContent), &parsed); err != nil {
		t.Fatalf("user-data with zero SSH keys is not valid YAML: %v", err)
	}
}

func TestRenderUserData_SingleSSHKey(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIonly one@host",
	}, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	output := renderUserData(t, params)
	if !strings.Contains(output, "only one@host") {
		t.Error("single SSH key not found in output")
	}
}

func TestRenderUserData_PersonaWithYAMLSpecialChars(t *testing.T) {
	cfg := testConfig()
	persona := "role: pirate\nnotes: |+\n  line1\n  line2\nkey: value # comment with: colons"
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, persona)
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	output := renderUserData(t, params)

	yamlContent := strings.TrimPrefix(output, "#cloud-config\n")
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlContent), &parsed); err != nil {
		t.Fatalf("user-data with YAML-special persona chars is not valid YAML: %v\n\ncontent:\n%s", err, output)
	}
	if !strings.Contains(output, "# Active Persona") {
		t.Error("persona header missing")
	}
}

func TestRenderUserData_WriteFilesCount(t *testing.T) {
	output := renderUserData(t, testParams(t))
	// Expected: sysctl, apt proxy, environment, hosts, systemd service,
	// telegram .env, claude-env, bash_aliases, CLAUDE.md, claude-restart.sh, cmd-queue.sh
	count := strings.Count(output, "  - path: /")
	if count != 11 {
		t.Errorf("write_files should have 11 entries, got %d", count)
	}
}

func TestRenderUserData_RuncmdOrder(t *testing.T) {
	output := renderUserData(t, testParams(t))
	// DNS disable must come before cloud-init wipe
	dnsPos := strings.Index(output, "chattr +i /etc/resolv.conf")
	wipePos := strings.Index(output, "rm -rf /var/lib/cloud/instances")
	if dnsPos < 0 || wipePos < 0 {
		t.Fatal("missing expected runcmd entries")
	}
	if dnsPos > wipePos {
		t.Error("DNS disable must come before cloud-init cache wipe in runcmd")
	}
}

func TestNewInstanceParams_CustomProxyPort(t *testing.T) {
	cfg := testConfig()
	cfg.Proxy.Port = 8080
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	if params.ProxyURL != "http://192.168.122.1:8080" {
		t.Errorf("ProxyURL = %q, want http://192.168.122.1:8080", params.ProxyURL)
	}
}

func TestNewInstanceParams_CustomBridgeIP(t *testing.T) {
	cfg := testConfig()
	cfg.Network.BridgeIP = "10.0.0.1"
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, nil, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	if params.ProxyURL != "http://10.0.0.1:3128" {
		t.Errorf("ProxyURL = %q, want http://10.0.0.1:3128", params.ProxyURL)
	}
	if !strings.Contains(params.ClaudeMD, "10.0.0.1:3128") {
		t.Error("ClaudeMD should contain custom bridge IP")
	}
}

func TestIndent_ZeroSpaces(t *testing.T) {
	fn := funcMap["indent"].(func(int, string) string)
	result := fn(0, "line1\nline2")
	if result != "line1\nline2" {
		t.Errorf("indent(0) should be no-op, got %q", result)
	}
}

func TestIndent_SingleLine(t *testing.T) {
	fn := funcMap["indent"].(func(int, string) string)
	result := fn(4, "hello")
	if result != "    hello" {
		t.Errorf("indent single line = %q, want %q", result, "    hello")
	}
}

func TestIndent_EmptyString(t *testing.T) {
	fn := funcMap["indent"].(func(int, string) string)
	result := fn(4, "")
	if result != "" {
		t.Errorf("indent of empty string should be empty, got %q", result)
	}
}

func TestIndent_MultipleLines(t *testing.T) {
	fn := funcMap["indent"].(func(int, string) string)
	result := fn(4, "line1\nline2\nline3")
	expected := "    line1\n    line2\n    line3"
	if result != expected {
		t.Errorf("indent result:\n%q\nwant:\n%q", result, expected)
	}
}

func TestIndent_NegativeSpaces(t *testing.T) {
	fn := funcMap["indent"].(func(int, string) string)
	// Negative indent should not panic — treat as zero
	result := fn(-1, "hello")
	if result != "hello" {
		t.Errorf("indent(-1) should be no-op, got %q", result)
	}
}

func TestIndent_PreservesEmptyLines(t *testing.T) {
	fn := funcMap["indent"].(func(int, string) string)
	result := fn(4, "line1\n\nline3")
	// Empty lines should NOT get indentation (avoids trailing whitespace)
	expected := "    line1\n\n    line3"
	if result != expected {
		t.Errorf("indent result:\n%q\nwant:\n%q", result, expected)
	}
}

func TestRenderUserData_VMUserInAllPaths(t *testing.T) {
	cfg := testConfig()
	cfg.VMUser = "agent"
	params, err := NewInstanceParams(cfg, "vm1", "vm1", testToken, []string{"ssh-ed25519 AAAA key"}, "")
	if err != nil {
		t.Fatalf("NewInstanceParams() error = %v", err)
	}
	output := renderUserData(t, params)

	// Should NOT contain /home/claude when VMUser is agent
	if strings.Contains(output, "/home/claude") {
		t.Error("output contains /home/claude but VMUser is 'agent'")
	}
	// Should contain /home/agent in write_files and runcmd
	if !strings.Contains(output, "/home/agent") {
		t.Error("output missing /home/agent paths")
	}
	// users section should use agent
	if !strings.Contains(output, "name: agent") {
		t.Error("users section missing 'name: agent'")
	}
}

// --- RenderMetaData tests ---

func TestRenderMetaData_Content(t *testing.T) {
	data, err := RenderMetaData("test-vm", "test-host")
	if err != nil {
		t.Fatalf("RenderMetaData() error = %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "instance-id: test-vm") {
		t.Error("meta-data missing instance-id")
	}
	if !strings.Contains(output, "local-hostname: test-host") {
		t.Error("meta-data missing local-hostname")
	}
}

func TestRenderMetaData_ValidYAML(t *testing.T) {
	data, err := RenderMetaData("test-vm", "test-host")
	if err != nil {
		t.Fatalf("RenderMetaData() error = %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("meta-data is not valid YAML: %v", err)
	}
}

func TestRenderMetaData_EmptyStrings(t *testing.T) {
	data, err := RenderMetaData("", "")
	if err != nil {
		t.Fatalf("RenderMetaData() error = %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("meta-data with empty strings is not valid YAML: %v", err)
	}
}

func TestRenderMetaData_FieldCount(t *testing.T) {
	data, err := RenderMetaData("vm1", "host1")
	if err != nil {
		t.Fatalf("RenderMetaData() error = %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Errorf("meta-data should have exactly 2 fields, got %d: %v", len(parsed), parsed)
	}
}

// --- Optional Telegram tests ---

func TestNewInstanceParams_AcceptsEmptyToken(t *testing.T) {
	cfg := testConfig()
	params, err := NewInstanceParams(cfg, "vm1", "vm1", "", nil, "")
	if err != nil {
		t.Fatalf("empty token should be accepted, got error: %v", err)
	}
	if params.TelegramBotToken != "" {
		t.Errorf("TelegramBotToken = %q, want empty", params.TelegramBotToken)
	}
}

func TestRenderUserData_NoTelegram_OmitsTelegramEnvFiles(t *testing.T) {
	output := renderUserData(t, testParamsNoTelegram(t))
	if strings.Contains(output, "TELEGRAM_BOT_TOKEN") {
		t.Error("user-data should not contain TELEGRAM_BOT_TOKEN when token is empty")
	}
	if strings.Contains(output, "channels/telegram/.env") {
		t.Error("user-data should not write telegram .env when token is empty")
	}
}

func TestRenderUserData_NoTelegram_ServiceWithoutChannels(t *testing.T) {
	output := renderUserData(t, testParamsNoTelegram(t))
	if strings.Contains(output, "--channels") {
		t.Error("claude-assistant service should not include --channels when no telegram token")
	}
	if !strings.Contains(output, "claude --dangerously-skip-permissions") {
		t.Error("claude-assistant service should still start claude without --channels")
	}
}

func TestRenderUserData_NoTelegram_AliasWithoutChannels(t *testing.T) {
	output := renderUserData(t, testParamsNoTelegram(t))
	if !strings.Contains(output, "alias claude=") {
		t.Fatal("bash alias for claude should be present")
	}
	if strings.Contains(output, "--channels") {
		t.Error("bash alias should not include --channels when no telegram token")
	}
}

func TestRenderUserData_NoTelegram_WriteFilesCount(t *testing.T) {
	output := renderUserData(t, testParamsNoTelegram(t))
	// Without telegram: sysctl, apt proxy, environment, hosts, systemd service,
	// claude-env (proxy only), bash_aliases, CLAUDE.md, claude-restart.sh, cmd-queue.sh = 10
	// (telegram .env is the only one dropped)
	count := strings.Count(output, "  - path: /")
	if count != 10 {
		t.Errorf("write_files without telegram should have 10 entries, got %d", count)
	}
}

func TestRenderUserData_NoTelegram_ValidYAML(t *testing.T) {
	output := renderUserData(t, testParamsNoTelegram(t))
	yamlContent := strings.TrimPrefix(output, "#cloud-config\n")
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlContent), &parsed); err != nil {
		t.Fatalf("user-data without telegram is not valid YAML: %v\n\ncontent:\n%s", err, output)
	}
}

func TestRenderUserData_WithTelegram_StillHasChannels(t *testing.T) {
	output := renderUserData(t, testParams(t))
	if !strings.Contains(output, "--channels plugin:telegram@claude-plugins-official") {
		t.Error("service should include --channels when telegram token is set")
	}
}

// --- BuildISO tests ---

func TestBuildISO_MissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := BuildISO([]byte("#cloud-config\n"), []byte("instance-id: x\n"), "/tmp/test.iso")
	if err == nil {
		t.Fatal("expected error for missing cloud-localds")
	}
	if !strings.Contains(err.Error(), "cloud-localds") {
		t.Errorf("error should mention cloud-localds, got: %v", err)
	}
}

func TestBuildISO_OverwriteExisting(t *testing.T) {
	if _, err := exec.LookPath("cloud-localds"); err != nil {
		t.Skip("cloud-localds not in PATH")
	}

	outPath := filepath.Join(t.TempDir(), "test.iso")
	userdata := []byte("#cloud-config\nhostname: test\n")
	metadata := []byte("instance-id: test\nlocal-hostname: test\n")

	// First build
	if err := BuildISO(userdata, metadata, outPath); err != nil {
		t.Fatalf("first BuildISO() error = %v", err)
	}
	first, _ := os.Stat(outPath)

	// Second build on same path (rebuild scenario)
	if err := BuildISO(userdata, metadata, outPath); err != nil {
		t.Fatalf("second BuildISO() error = %v", err)
	}
	second, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("ISO file missing after overwrite: %v", err)
	}
	if second.ModTime().Before(first.ModTime()) {
		t.Error("ISO file was not overwritten")
	}
	if perm := second.Mode().Perm(); perm != 0600 {
		t.Errorf("overwritten ISO permissions = %o, want 0600", perm)
	}
}

func TestBuildISO_Integration(t *testing.T) {
	if _, err := exec.LookPath("cloud-localds"); err != nil {
		t.Skip("cloud-localds not in PATH")
	}

	outPath := filepath.Join(t.TempDir(), "test.iso")
	userdata := []byte("#cloud-config\nhostname: test\n")
	metadata := []byte("instance-id: test\nlocal-hostname: test\n")

	if err := BuildISO(userdata, metadata, outPath); err != nil {
		t.Fatalf("BuildISO() error = %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("ISO file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("ISO file is empty")
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("ISO permissions = %o, want 0600", perm)
	}
}
