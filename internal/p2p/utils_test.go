package p2p

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivKeyFromFileAllowsSurroundingWhitespace(t *testing.T) {
	const want = "1111111111111111111111111111111111111111111111111111111111111111"
	path := filepath.Join(t.TempDir(), "network-keys")
	if err := os.WriteFile(path, []byte(" \t"+want+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := privKeyFromFile(path)
	if err != nil {
		t.Fatalf("privKeyFromFile: %v", err)
	}
	if got := hex.EncodeToString(key.D.FillBytes(make([]byte, 32))); got != want {
		t.Fatalf("private key = %s, want %s", got, want)
	}
}
