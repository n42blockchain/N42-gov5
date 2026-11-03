package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/conf"
)

// A secret that exists but cannot be read is an operator problem, not a first
// run: replacing it silently locks the consensus client out.
func TestObtainJWTSecretKeepsAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	file := filepath.Join(t.TempDir(), "jwt.hex")
	const secret = "0x0101010101010101010101010101010101010101010101010101010101010101"
	if err := os.WriteFile(file, []byte(secret), 0o200); err != nil {
		t.Fatal(err)
	}
	n := &Node{config: &conf.Config{}}
	if _, err := n.obtainJWTSecret(file); err == nil {
		t.Fatal("unreadable secret was replaced instead of reported")
	}
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != secret {
		t.Fatal("existing secret was overwritten")
	}
}

func TestObtainJWTSecretDoesNotPutTheFileInItsError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "jwt.hex")
	const secret = "not-hex-but-still-the-operators-secret"
	if err := os.WriteFile(file, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Node{config: &conf.Config{}}
	_, err := n.obtainJWTSecret(file)
	if err == nil {
		t.Fatal("malformed secret was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposes the file contents: %v", err)
	}
}
