package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	body := `
a1b2c3  flashcart_linux_amd64
d4e5f6  flashcart_linux_arm64
`
	got := ParseChecksums(body)
	if got["flashcart_linux_amd64"] != "a1b2c3" {
		t.Errorf("amd64 = %q", got["flashcart_linux_amd64"])
	}
	if len(got) != 2 {
		t.Errorf("parsed %d entries, want 2", len(got))
	}
}

func TestVerifyAndSwapReplacesBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flashcart")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	payload := []byte("new binary")
	sum := sha256.Sum256(payload)

	if err := VerifyAndSwap(path, payload, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("VerifyAndSwap: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("binary content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("replaced binary is not executable")
	}
}

// A mismatched checksum must leave the running binary untouched.
func TestVerifyAndSwapRefusesBadChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flashcart")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := VerifyAndSwap(path, []byte("tampered"), strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("VerifyAndSwap accepted a mismatched checksum")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Error("the existing binary was replaced despite a checksum mismatch")
	}
	// No debris left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %d entries", len(entries))
	}
}
