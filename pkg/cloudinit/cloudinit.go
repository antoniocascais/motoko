package cloudinit

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	CmdQueueSh        string
}

const (
	tmplUserData      = "templates/user-data.yml.tmpl"
	tmplMetaData      = "templates/meta-data.yml.tmpl"
	tmplClaudeMD      = "templates/vm-CLAUDE.md.tmpl"
	tmplClaudeRestart = "scripts/claude-restart.sh.tmpl"
	tmplCmdQueue      = "scripts/cmd-queue.sh.tmpl"
)

var funcMap = template.FuncMap{
	"indent": func(spaces int, s string) string {
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
	if err := ValidateTelegramToken(telegramToken); err != nil {
		return nil, err
	}

	proxyURL := fmt.Sprintf("http://%s:%d", cfg.Network.BridgeIP, cfg.Proxy.Port)

	claudeMD, err := renderClaudeMD(cfg.Network.BridgeIP, cfg.Proxy.Port, persona)
	if err != nil {
		return nil, fmt.Errorf("rendering CLAUDE.md: %w", err)
	}

	scriptData := struct{ VMUser string }{VMUser: cfg.VMUser}

	restartSh, err := renderEmbedded(scriptFS, tmplClaudeRestart, scriptData)
	if err != nil {
		return nil, fmt.Errorf("rendering claude-restart.sh: %w", err)
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
		CmdQueueSh:        queueSh,
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

func renderClaudeMD(bridgeIP string, proxyPort int, persona string) (string, error) {
	data := struct {
		BridgeIP  string
		ProxyPort int
	}{bridgeIP, proxyPort}

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
