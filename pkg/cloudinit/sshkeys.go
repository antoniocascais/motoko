package cloudinit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ValidInstanceName matches lowercase alphanumeric names with hyphens, 1-63 chars.
var validInstanceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validHostname matches RFC 1123 hostnames: lowercase alphanumeric, hyphens, dots, 1-253 chars.
var validHostname = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// validTelegramToken matches Telegram Bot API token format: numeric bot ID, colon, alphanumeric secret.
var validTelegramToken = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{35,}$`)

// ValidateInstanceName checks that an instance name is safe for use in filesystem paths and shell arguments.
func ValidateInstanceName(name string) error {
	if !validInstanceName.MatchString(name) {
		return fmt.Errorf("invalid instance name %q: must match [a-z0-9][a-z0-9-]{0,62}", name)
	}
	return nil
}

// ValidateHostname checks that a hostname is safe for YAML interpolation and conforms to RFC 1123.
func ValidateHostname(hostname string) error {
	if !validHostname.MatchString(hostname) {
		return fmt.Errorf("invalid hostname %q: must match RFC 1123 (lowercase alphanumeric, hyphens, dots, 1-253 chars)", hostname)
	}
	return nil
}

// ValidateTelegramToken checks that a token matches the Telegram Bot API format.
func ValidateTelegramToken(token string) error {
	if !validTelegramToken.MatchString(token) {
		return fmt.Errorf("invalid telegram token: must match <numeric_id>:<secret> format")
	}
	return nil
}

func instanceKeyPaths(configDir, instanceName string) (keyDir, privPath, pubPath string, err error) {
	if err := ValidateInstanceName(instanceName); err != nil {
		return "", "", "", err
	}
	keyDir = filepath.Join(configDir, "keys", instanceName)
	privPath = filepath.Join(keyDir, "id_ed25519")
	pubPath = filepath.Join(keyDir, "id_ed25519.pub")
	return keyDir, privPath, pubPath, nil
}

// GenerateInstanceKey creates a new ED25519 keypair at
// <configDir>/keys/<instanceName>/id_ed25519 (and .pub).
// Returns the public key string and private key file path.
func GenerateInstanceKey(configDir, instanceName string) (pubkey, privkeyPath string, err error) {
	keyDir, privkeyPath, pubkeyPath, err := instanceKeyPaths(configDir, instanceName)
	if err != nil {
		return "", "", err
	}

	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return "", "", fmt.Errorf("creating key directory: %w", err)
	}

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating ed25519 key: %w", err)
	}

	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return "", "", fmt.Errorf("converting public key: %w", err)
	}
	pubKeyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPubKey)))

	privPEM, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return "", "", fmt.Errorf("marshaling private key: %w", err)
	}
	privKeyBytes := pem.EncodeToMemory(privPEM)

	// O_EXCL ensures atomic "create if not exists" — avoids TOCTOU with a separate Stat check
	f, err := os.OpenFile(privkeyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return "", "", fmt.Errorf("key already exists: %s", privkeyPath)
		}
		return "", "", fmt.Errorf("writing private key: %w", err)
	}
	if _, err := f.Write(privKeyBytes); err != nil {
		_ = f.Close()
		return "", "", fmt.Errorf("writing private key: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", "", fmt.Errorf("writing private key: %w", err)
	}

	if err := os.WriteFile(pubkeyPath, []byte(pubKeyStr+"\n"), 0644); err != nil {
		return "", "", fmt.Errorf("writing public key: %w", err)
	}

	return pubKeyStr, privkeyPath, nil
}

// LoadInstanceKey reads an existing keypair from
// <configDir>/keys/<instanceName>/id_ed25519.
func LoadInstanceKey(configDir, instanceName string) (pubkey, privkeyPath string, err error) {
	_, privkeyPath, pubkeyPath, err := instanceKeyPaths(configDir, instanceName)
	if err != nil {
		return "", "", err
	}

	if _, err := os.Stat(privkeyPath); err != nil {
		return "", "", fmt.Errorf("private key missing: %w", err)
	}

	pubKeyBytes, err := os.ReadFile(pubkeyPath)
	if err != nil {
		return "", "", fmt.Errorf("reading public key: %w", err)
	}

	return strings.TrimSpace(string(pubKeyBytes)), privkeyPath, nil
}

// EnsureInstanceKey loads existing keys or generates new ones.
func EnsureInstanceKey(configDir, instanceName string) (pubkey, privkeyPath string, err error) {
	pubkey, privkeyPath, err = LoadInstanceKey(configDir, instanceName)
	if err == nil {
		return pubkey, privkeyPath, nil
	}
	return GenerateInstanceKey(configDir, instanceName)
}
