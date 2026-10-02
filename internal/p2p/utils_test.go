package p2p

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/proto/sync_pb"
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

func TestPrivKeyFromFileRejectsBadHex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-keys")
	if err := os.WriteFile(path, []byte("not-hex-at-all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := privKeyFromFile(path); err == nil {
		t.Fatal("expected an error decoding invalid hex")
	}
}

func TestPrivKeyFromFileMissing(t *testing.T) {
	if _, err := privKeyFromFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error reading a missing file")
	}
}

func TestSerializeENRNilRecord(t *testing.T) {
	if _, err := SerializeENR(nil); err == nil {
		t.Fatal("expected an error serializing a nil record")
	}
}

func TestSeqNumberRoundTrip(t *testing.T) {
	cfg := &conf.P2PConfig{DataDir: t.TempDir()}

	// No file yet: starts at zero.
	ping, err := getSeqNumber(cfg)
	if err != nil {
		t.Fatalf("getSeqNumber (no file): %v", err)
	}
	if ping.SeqNumber != 0 {
		t.Fatalf("SeqNumber = %d, want 0", ping.SeqNumber)
	}

	if err := saveSeqNumber(cfg, &sync_pb.Ping{SeqNumber: 42}); err != nil {
		t.Fatalf("saveSeqNumber: %v", err)
	}

	ping, err = getSeqNumber(cfg)
	if err != nil {
		t.Fatalf("getSeqNumber (after save): %v", err)
	}
	if ping.SeqNumber != 42 {
		t.Fatalf("SeqNumber = %d, want 42", ping.SeqNumber)
	}
}

func TestGetSeqNumberRejectsShortFile(t *testing.T) {
	cfg := &conf.P2PConfig{DataDir: t.TempDir()}
	path := filepath.Join(cfg.DataDir, "network-seq")
	if err := os.WriteFile(path, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	ping, err := getSeqNumber(cfg)
	if err != nil {
		t.Fatalf("getSeqNumber: %v", err)
	}
	if ping.SeqNumber != 0 {
		t.Fatalf("SeqNumber = %d, want 0 for a malformed file", ping.SeqNumber)
	}
}

func TestPrivKeyGeneratesWhenNoFileAndNoStaticID(t *testing.T) {
	cfg := &conf.P2PConfig{DataDir: t.TempDir()}
	key, err := privKey(cfg)
	if err != nil {
		t.Fatalf("privKey: %v", err)
	}
	if key == nil {
		t.Fatal("expected a generated key")
	}
	// Non-static: nothing should be persisted to disk.
	if _, err := os.Stat(filepath.Join(cfg.DataDir, keyPath)); !os.IsNotExist(err) {
		t.Fatalf("expected no key file to be written, stat err = %v", err)
	}
}

func TestPrivKeyPersistsAndReusesWithStaticPeerID(t *testing.T) {
	cfg := &conf.P2PConfig{DataDir: t.TempDir(), StaticPeerID: true}
	first, err := privKey(cfg)
	if err != nil {
		t.Fatalf("privKey (first): %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, keyPath)); err != nil {
		t.Fatalf("expected a persisted key file: %v", err)
	}

	second, err := privKey(cfg)
	if err != nil {
		t.Fatalf("privKey (second): %v", err)
	}
	if first.D.Cmp(second.D) != 0 {
		t.Fatal("second call should reuse the persisted key, got a different one")
	}
}

func TestPrivKeyUsesExplicitFileOverDataDir(t *testing.T) {
	const want = "2222222222222222222222222222222222222222222222222222222222222222"
	explicit := filepath.Join(t.TempDir(), "explicit-key")
	if err := os.WriteFile(explicit, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &conf.P2PConfig{DataDir: t.TempDir(), PrivateKey: explicit}
	key, err := privKey(cfg)
	if err != nil {
		t.Fatalf("privKey: %v", err)
	}
	if got := hex.EncodeToString(key.D.FillBytes(make([]byte, 32))); got != want {
		t.Fatalf("key = %s, want %s", got, want)
	}
}
