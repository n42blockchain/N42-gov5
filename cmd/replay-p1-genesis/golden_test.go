package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// goldenNoFlagsSHA256 is the hash of the genesis the tool wrote before the
// prelude flags existed (default test validators, no source, no prelude).
const goldenNoFlagsSHA256 = "9cb46e618c1599c4685e5587cd5c730ee9e71a95d6ea74a2e7a52b61015b4887"

func TestMain(m *testing.M) {
	if os.Getenv("REPLAY_P1_GENESIS_AS_MAIN") == "1" {
		os.Args = append([]string{"replay-p1-genesis"}, os.Args[1:]...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestGenesisByteIdenticalWithoutPreludeFlags runs the tool without the
// prelude flags and compares the output with the pre-flag golden hash.
func TestGenesisByteIdenticalWithoutPreludeFlags(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "g.json")
	cmd := exec.Command(os.Args[0], "--std", "../../internal/testdata/profile1/standard_genesis.json", "--out", out, "--keys", filepath.Join(dir, "k.json"))
	cmd.Env = append(os.Environ(), "REPLAY_P1_GENESIS_AS_MAIN=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != goldenNoFlagsSHA256 {
		t.Errorf("genesis changed without prelude flags: sha256 %s", got)
	}
}
