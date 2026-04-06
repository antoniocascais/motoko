package cloudinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateInstanceKey_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	pubkey, privPath, err := GenerateInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("GenerateInstanceKey() error = %v", err)
	}

	if _, err := os.Stat(privPath); err != nil {
		t.Errorf("private key file not found: %v", err)
	}
	pubPath := filepath.Join(dir, "keys", "test-vm", "id_ed25519.pub")
	if _, err := os.Stat(pubPath); err != nil {
		t.Errorf("public key file not found: %v", err)
	}
	if pubkey == "" {
		t.Error("public key string is empty")
	}
}

func TestGenerateInstanceKey_PrivateKeyPerms(t *testing.T) {
	dir := t.TempDir()
	_, privPath, err := GenerateInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("GenerateInstanceKey() error = %v", err)
	}

	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("private key perms = %o, want 0600", perm)
	}
}

func TestGenerateInstanceKey_PubKeyFormat(t *testing.T) {
	dir := t.TempDir()
	pubkey, _, err := GenerateInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("GenerateInstanceKey() error = %v", err)
	}

	if !strings.HasPrefix(pubkey, "ssh-ed25519 ") {
		t.Errorf("public key does not start with 'ssh-ed25519 ', got: %s", pubkey)
	}
}

func TestGenerateInstanceKey_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	_, _, err := GenerateInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("first GenerateInstanceKey() error = %v", err)
	}

	_, _, err = GenerateInstanceKey(dir, "test-vm")
	if err == nil {
		t.Error("expected error for existing key, got nil")
	}
}

func TestGenerateInstanceKey_DirCreated(t *testing.T) {
	dir := t.TempDir()
	_, _, err := GenerateInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("GenerateInstanceKey() error = %v", err)
	}

	keyDir := filepath.Join(dir, "keys", "test-vm")
	info, err := os.Stat(keyDir)
	if err != nil {
		t.Fatalf("key directory not found: %v", err)
	}
	if !info.IsDir() {
		t.Error("key path is not a directory")
	}
}

func TestLoadInstanceKey_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	genPub, genPriv, err := GenerateInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("GenerateInstanceKey() error = %v", err)
	}

	loadPub, loadPriv, err := LoadInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("LoadInstanceKey() error = %v", err)
	}

	if genPub != loadPub {
		t.Errorf("pubkey mismatch: generate=%q, load=%q", genPub, loadPub)
	}
	if genPriv != loadPriv {
		t.Errorf("privkey path mismatch: generate=%q, load=%q", genPriv, loadPriv)
	}
}

func TestLoadInstanceKey_MissingKey(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadInstanceKey(dir, "nonexistent")
	if err == nil {
		t.Error("expected error for missing key, got nil")
	}
}

func TestEnsureInstanceKey_NewKey(t *testing.T) {
	dir := t.TempDir()
	pubkey, privPath, err := EnsureInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("EnsureInstanceKey() error = %v", err)
	}
	if pubkey == "" {
		t.Error("public key is empty")
	}
	if _, err := os.Stat(privPath); err != nil {
		t.Errorf("private key file not found: %v", err)
	}
}

func TestEnsureInstanceKey_ExistingKey(t *testing.T) {
	dir := t.TempDir()
	pub1, _, err := EnsureInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("first EnsureInstanceKey() error = %v", err)
	}

	pub2, _, err := EnsureInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("second EnsureInstanceKey() error = %v", err)
	}

	if pub1 != pub2 {
		t.Errorf("pubkey changed between calls: %q != %q", pub1, pub2)
	}
}

func TestValidateInstanceName_Valid(t *testing.T) {
	valid := []string{
		"vm1", "test-vm", "a", "0",
		"my-long-instance-name-123",
		strings.Repeat("a", 63), // max length boundary
	}
	for _, name := range valid {
		if err := ValidateInstanceName(name); err != nil {
			t.Errorf("ValidateInstanceName(%q) unexpected error: %v", name, err)
		}
	}
}

func TestValidateInstanceName_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"-starts-with-dash",
		"has spaces",
		"has/slash",
		"has..dots",
		"../../etc/passwd",
		"UPPERCASE",
		"under_score",
		strings.Repeat("a", 64),
	}
	for _, name := range invalid {
		if err := ValidateInstanceName(name); err == nil {
			t.Errorf("ValidateInstanceName(%q) expected error, got nil", name)
		}
	}
}

func TestGenerateInstanceKey_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	_, _, err := GenerateInstanceKey(dir, "../../etc")
	if err == nil {
		t.Fatal("expected error for path traversal instance name, got nil")
	}
}

func TestLoadInstanceKey_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadInstanceKey(dir, "../../../tmp")
	if err == nil {
		t.Fatal("expected error for path traversal instance name, got nil")
	}
}

func TestEnsureInstanceKey_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	_, _, err := EnsureInstanceKey(dir, "../../.ssh")
	if err == nil {
		t.Fatal("expected error for path traversal instance name, got nil")
	}
}

func TestValidateInstanceName_BoundaryLength(t *testing.T) {
	// 63 chars = valid max, 64 = invalid
	if err := ValidateInstanceName(strings.Repeat("a", 63)); err != nil {
		t.Errorf("63-char name should be valid: %v", err)
	}
	if err := ValidateInstanceName(strings.Repeat("a", 64)); err == nil {
		t.Error("64-char name should be invalid")
	}
}

func TestValidateInstanceName_TrailingDash(t *testing.T) {
	// Regex allows trailing dash — verify this is intentional
	if err := ValidateInstanceName("vm-"); err != nil {
		t.Errorf("trailing dash should be valid per regex: %v", err)
	}
}

func TestGenerateInstanceKey_UniqueKeys(t *testing.T) {
	dir := t.TempDir()
	pub1, _, err := GenerateInstanceKey(dir, "vm1")
	if err != nil {
		t.Fatalf("GenerateInstanceKey(vm1) error = %v", err)
	}
	pub2, _, err := GenerateInstanceKey(dir, "vm2")
	if err != nil {
		t.Fatalf("GenerateInstanceKey(vm2) error = %v", err)
	}
	if pub1 == pub2 {
		t.Error("two different instances generated identical public keys")
	}
}

func TestLoadInstanceKey_MissingPubKey(t *testing.T) {
	dir := t.TempDir()
	// Create the key dir and private key but not the public key
	keyDir := filepath.Join(dir, "keys", "test-vm")
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "id_ed25519"), []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadInstanceKey(dir, "test-vm")
	if err == nil {
		t.Error("expected error when public key file is missing")
	}
}

func TestLoadInstanceKey_EmptyPubKey(t *testing.T) {
	dir := t.TempDir()
	keyDir := filepath.Join(dir, "keys", "test-vm")
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "id_ed25519"), []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "id_ed25519.pub"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	pub, _, err := LoadInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("LoadInstanceKey() error = %v", err)
	}
	// Empty pubkey file → empty string returned (caller must validate)
	if pub != "" {
		t.Errorf("expected empty pubkey string, got %q", pub)
	}
}

func TestEnsureInstanceKey_NewKeyFormat(t *testing.T) {
	dir := t.TempDir()
	pubkey, _, err := EnsureInstanceKey(dir, "test-vm")
	if err != nil {
		t.Fatalf("EnsureInstanceKey() error = %v", err)
	}
	if !strings.HasPrefix(pubkey, "ssh-ed25519 ") {
		t.Errorf("pubkey should start with 'ssh-ed25519 ', got: %s", pubkey)
	}
}

func TestValidateHostname_Valid(t *testing.T) {
	valid := []string{"vm1", "test-vm", "my.host.name", "a", "0"}
	for _, h := range valid {
		if err := ValidateHostname(h); err != nil {
			t.Errorf("ValidateHostname(%q) unexpected error: %v", h, err)
		}
	}
}

func TestValidateHostname_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"-starts-dash",
		"has spaces",
		"UPPERCASE",
		"has\nnewline",
		"trailing-",
		strings.Repeat("a", 254),
	}
	for _, h := range invalid {
		if err := ValidateHostname(h); err == nil {
			t.Errorf("ValidateHostname(%q) expected error, got nil", h)
		}
	}
}

func TestValidateTelegramToken_Valid(t *testing.T) {
	valid := []string{
		"123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijk",
		"1:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn",
	}
	for _, tok := range valid {
		if err := ValidateTelegramToken(tok); err != nil {
			t.Errorf("ValidateTelegramToken(%q) unexpected error: %v", tok, err)
		}
	}
}

func TestValidateTelegramToken_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"not-a-token",
		"123:short",
		"123456:has\nnewline",
		"no-colon-at-all",
		":missing-id",
	}
	for _, tok := range invalid {
		if err := ValidateTelegramToken(tok); err == nil {
			t.Errorf("ValidateTelegramToken(%q) expected error, got nil", tok)
		}
	}
}
