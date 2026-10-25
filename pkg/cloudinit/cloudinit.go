package cloudinit

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/antoniocascais/motoko/pkg/config"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

//go:embed scripts/*.tmpl
var scriptFS embed.FS

// InstanceParams holds all values needed to render cloud-init templates.
type InstanceParams struct {
	Name              string
	Hostname          string
	VMUser            string
	Locale            string
	Timezone          string
	SSHAuthorizedKeys []string
	ProxyURL          string
	BridgeIP          string
	TelegramBotToken  string
	ClaudeMD          string
	ClaudeRestartSh   string
	ClaudeRunSh       string
	CmdQueueSh        string

	// SeedDir is the plugin seed baked into the golden image.
	SeedDir string
	// TokenFile is where cloud-init writes the bot token (see scriptData).
	TokenFile string
	// TelegramStateDir is the plugin's state (access allowlist, inbox) on the
	// data disk, so pairing survives a rebuild.
	TelegramStateDir string
	// ManagedSettings and ClaudeJSON are the rendered JSON bodies of
	// /etc/claude-code/managed-settings.json and ~/.claude.json.
	ManagedSettings string
	ClaudeJSON      string
	// MemoryLink is claude's auto-memory dir for the data dir project; it is a
	// symlink to ~/data/memory so memory survives a rebuild. MemoryLinkDir is
	// its parent.
	MemoryLink    string
	MemoryLinkDir string
}

// telegramPlugin is the plugin id the golden seed provides and channels loads.
const telegramPlugin = "telegram@claude-plugins-official"

// projectSlugRE matches the characters claude replaces with "-" when it names a
// project's directory under ~/.claude/projects.
var projectSlugRE = regexp.MustCompile(`[^a-zA-Z0-9]`)

// managedSettings renders /etc/claude-code/managed-settings.json. It holds
// nothing secret and nothing instance-specific beyond the Telegram toggle.
func managedSettings(telegram bool) (string, error) {
	m := map[string]any{"skipDangerousModePermissionPrompt": true}
	if telegram {
		m["enabledPlugins"] = map[string]bool{telegramPlugin: true}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	return string(b) + "\n", err
}

// claudeJSON renders ~/.claude.json: onboarding done and the working directory
// trusted, so the first start shows no prompt a blind keypress could answer wrong.
func claudeJSON(dataDir string) (string, error) {
	m := map[string]any{
		"hasCompletedOnboarding": true,
		"projects": map[string]any{
			dataDir: map[string]any{"hasTrustDialogAccepted": true},
		},
	}
	b, err := json.MarshalIndent(m, "", "  ")
	return string(b) + "\n", err
}

const (
	tmplUserData      = "templates/user-data.yml.tmpl"
	tmplMetaData      = "templates/meta-data.yml.tmpl"
	tmplClaudeMD      = "templates/vm-CLAUDE.md.tmpl"
	tmplClaudeRestart = "scripts/claude-restart.sh.tmpl"
	tmplClaudeRun     = "scripts/claude-run.sh.tmpl"
	tmplCmdQueue      = "scripts/cmd-queue.sh.tmpl"
)

var funcMap = template.FuncMap{
	"indent": func(spaces int, s string) string {
		if spaces < 0 {
			spaces = 0
		}
		pad := strings.Repeat(" ", spaces)
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			if line != "" {
				lines[i] = pad + line
			}
		}
		return strings.Join(lines, "\n")
	},
}

// NewInstanceParams constructs InstanceParams from config plus instance-specific arguments.
func NewInstanceParams(cfg *config.Config, name, hostname, telegramToken string, sshKeys []string, persona string) (*InstanceParams, error) {
	if err := ValidateHostname(hostname); err != nil {
		return nil, err
	}
	if telegramToken != "" {
		if err := ValidateTelegramToken(telegramToken); err != nil {
			return nil, err
		}
	}

	proxyURL := fmt.Sprintf("http://%s:%d", cfg.Network.BridgeIP, cfg.Proxy.Port)

	claudeMD, err := renderClaudeMD(cfg.Network.BridgeIP, cfg.Proxy.Port, telegramToken != "", persona)
	if err != nil {
		return nil, fmt.Errorf("rendering CLAUDE.md: %w", err)
	}

	telegram := telegramToken != ""
	scriptData := struct {
		VMUser   string
		Telegram bool
		BunPath  string
		SeedDir  string
		// TokenFile holds the bot token alone, read by claude-run.sh. It stays
		// out of the unit's EnvironmentFile so the tmux server never sees it.
		TokenFile string
	}{cfg.VMUser, telegram, config.BunPath, config.SeedDir, "/home/" + cfg.VMUser + "/.telegram-token"}

	home := "/home/" + cfg.VMUser
	dataDir := home + "/data"
	settings, err := managedSettings(telegram)
	if err != nil {
		return nil, fmt.Errorf("rendering managed settings: %w", err)
	}
	claudeJSONBody, err := claudeJSON(dataDir)
	if err != nil {
		return nil, fmt.Errorf("rendering .claude.json: %w", err)
	}
	memoryLinkDir := home + "/.claude/projects/" + projectSlugRE.ReplaceAllString(dataDir, "-")

	restartSh, err := renderEmbedded(scriptFS, tmplClaudeRestart, scriptData)
	if err != nil {
		return nil, fmt.Errorf("rendering claude-restart.sh: %w", err)
	}

	runSh, err := renderEmbedded(scriptFS, tmplClaudeRun, scriptData)
	if err != nil {
		return nil, fmt.Errorf("rendering claude-run.sh: %w", err)
	}

	queueSh, err := renderEmbedded(scriptFS, tmplCmdQueue, scriptData)
	if err != nil {
		return nil, fmt.Errorf("rendering cmd-queue.sh: %w", err)
	}

	return &InstanceParams{
		Name:              name,
		Hostname:          hostname,
		VMUser:            cfg.VMUser,
		Locale:            cfg.VMDefaults.Locale,
		Timezone:          cfg.VMDefaults.Timezone,
		SSHAuthorizedKeys: sshKeys,
		ProxyURL:          proxyURL,
		BridgeIP:          cfg.Network.BridgeIP,
		TelegramBotToken:  telegramToken,
		ClaudeMD:          claudeMD,
		ClaudeRestartSh:   restartSh,
		ClaudeRunSh:       runSh,
		CmdQueueSh:        queueSh,
		SeedDir:           config.SeedDir,
		TokenFile:         scriptData.TokenFile,
		TelegramStateDir:  dataDir + "/telegram",
		ManagedSettings:   settings,
		ClaudeJSON:        claudeJSONBody,
		MemoryLink:        memoryLinkDir + "/memory",
		MemoryLinkDir:     memoryLinkDir,
	}, nil
}

func RenderUserData(params *InstanceParams) ([]byte, error) {
	result, err := renderEmbedded(templateFS, tmplUserData, params)
	if err != nil {
		return nil, err
	}
	return []byte(result), nil
}

// RenderMetaData renders the cloud-init meta-data template.
func RenderMetaData(name, hostname string) ([]byte, error) {
	data := struct {
		Name     string
		Hostname string
	}{name, hostname}
	result, err := renderEmbedded(templateFS, tmplMetaData, data)
	if err != nil {
		return nil, err
	}
	return []byte(result), nil
}

// BuildISO produces a NoCloud ISO from rendered user-data and meta-data.
func BuildISO(userdata, metadata []byte, outPath string) error {
	binary, err := exec.LookPath("cloud-localds")
	if err != nil {
		return fmt.Errorf("cloud-localds not found in PATH (install cloud-image-utils on Debian or cloud-utils on Arch)")
	}

	tmpDir, err := os.MkdirTemp("", "motoko-cloudinit-*")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	userdataPath := filepath.Join(tmpDir, "user-data")
	metadataPath := filepath.Join(tmpDir, "meta-data")

	if err := os.WriteFile(userdataPath, userdata, 0600); err != nil {
		return fmt.Errorf("writing user-data: %w", err)
	}
	if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
		return fmt.Errorf("writing meta-data: %w", err)
	}

	// Remove existing ISO so cloud-localds cp doesn't fail on 0600 files (rebuild path)
	if err := os.Remove(outPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing existing ISO: %w", err)
	}

	cmd := exec.Command(binary, outPath, userdataPath, metadataPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cloud-localds failed: %w\noutput: %s", err, string(output))
	}

	// cloud-localds inherits umask, producing world-readable ISOs containing secrets
	if err := os.Chmod(outPath, 0600); err != nil {
		return fmt.Errorf("securing ISO permissions: %w", err)
	}

	return nil
}

func renderClaudeMD(bridgeIP string, proxyPort int, telegram bool, persona string) (string, error) {
	data := struct {
		BridgeIP  string
		ProxyPort int
		Telegram  bool
	}{bridgeIP, proxyPort, telegram}

	result, err := renderEmbedded(templateFS, tmplClaudeMD, data)
	if err != nil {
		return "", err
	}

	if persona != "" {
		result += "\n# Active Persona\n\n" + persona
	}

	return result, nil
}

func renderEmbedded(fs embed.FS, name string, data any) (string, error) {
	raw, err := fs.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", name, err)
	}

	tmpl, err := template.New(filepath.Base(name)).Funcs(funcMap).Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("parsing %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing %s: %w", name, err)
	}

	return buf.String(), nil
}
