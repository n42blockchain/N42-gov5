package mobileverify

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestIdentityAndConstruction(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := hex.DecodeString(key); err != nil || len(decoded) != 32 {
		t.Fatal("invalid secret seed format")
	}
	public, err := PublicKey(key)
	if err != nil || len(public) != 96 {
		t.Fatal("invalid public key")
	}
	miner, err := NewMiner("http://127.0.0.1:21100", key, "mainnet_qmdb_staggered")
	if err != nil {
		t.Fatal(err)
	}
	if miner.Running() || miner.PubkeyHex() != public || !json.Valid([]byte(miner.Status())) {
		t.Fatal("invalid stopped miner state")
	}
	miner.Stop()
	session, err := NewSession("http://127.0.0.1:21100", key, "mainnet_qmdb_staggered")
	if err != nil {
		t.Fatal(err)
	}
	if session.PubkeyHex() != public {
		t.Fatal("session identity differs")
	}
	session.Close()
	if _, err := session.Latest(); err == nil {
		t.Fatal("closed session accepted a request")
	}
}

func TestInvalidAndZeroValueHandles(t *testing.T) {
	if _, err := NewMiner("http://127.0.0.1:21100", "invalid", "mainnet_qmdb_staggered"); err == nil {
		t.Fatal("invalid private key accepted")
	}
	var miner Miner
	miner.Start()
	miner.Stop()
	if miner.Running() || !json.Valid([]byte(miner.Status())) {
		t.Fatal("invalid empty miner state")
	}
	var session Session
	session.Close()
	for _, call := range []func() (string, error){session.Register, session.Latest, session.Health,
		func() (string, error) { return session.VerifyBlock("0x00") },
		func() (string, error) { return session.Cert("0x00") }} {
		if _, err := call(); err == nil {
			t.Fatal("empty session accepted a request")
		}
	}
}
