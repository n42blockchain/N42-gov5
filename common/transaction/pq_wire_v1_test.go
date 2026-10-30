package transaction

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/falcon"
	"github.com/n42blockchain/N42/params"
)

func walletFalconVector(t *testing.T) (map[string]interface{}, *Transaction, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/falcon512_wallet_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]interface{}
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	raw := hexutil.MustDecode(v["rawTransaction"].(string))
	tx, err := DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v, tx, raw
}
func TestPQWireWalletVector(t *testing.T) {
	v, tx, raw := walletFalconVector(t)
	pq := tx.inner.(*PostQuantumTx)
	if pq.SigningHash().Hex() != v["signingHash"] {
		t.Fatalf("signing hash mismatch: %s", pq.SigningHash().Hex())
	}
	sender, err := Sender(NewFalconTransactionSigner(big.NewInt(94)), tx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(sender.Hex(), v["sender"].(string)) {
		t.Fatalf("sender mismatch: %s", sender.Hex())
	}
	encoded, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Hash() != crypto.Keccak256Hash(raw) {
		t.Fatal("wire hash mismatch")
	}
	protobuf, err := tx.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var stored Transaction
	if err := stored.Unmarshal(protobuf); err != nil {
		t.Fatal(err)
	}
	if stored.Hash() != tx.Hash() {
		t.Fatal("stored hash mismatch")
	}
	if _, err := Sender(NewFalconTransactionSigner(big.NewInt(94)), &stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, encoded) {
		t.Fatal("noncanonical wire roundtrip")
	}
	if len(pq.PQSignature) != 666 || len(pq.PubKeyData) != 897 {
		t.Fatal("wrong padded Falcon sizes")
	}
	if _, err = Sender(NewFalconTransactionSigner(big.NewInt(95)), tx); err == nil {
		t.Fatal("cross-chain sender accepted")
	}
}
func TestPQWireMutationAndBounds(t *testing.T) {
	_, tx, raw := walletFalconVector(t)
	if _, err := DecodeEthereumTransaction(append(raw, 0)); err == nil {
		t.Fatal("trailing bytes accepted")
	}
	pq := tx.inner.(*PostQuantumTx)
	pq.Value = uint256.NewInt(2)
	if _, err := Sender(NewFalconTransactionSigner(big.NewInt(94)), NewTx(pq)); err == nil {
		t.Fatal("changed payment accepted")
	}
	for _, mutate := range []func(*PostQuantumTx){
		func(p *PostQuantumTx) { p.PubKeyMode = 1 },
		func(p *PostQuantumTx) { p.SigAlgo = 1 },
		func(p *PostQuantumTx) { p.PubKeyData = p.PubKeyData[:896] },
		func(p *PostQuantumTx) { p.PQSignature = p.PQSignature[:665] },
		func(p *PostQuantumTx) { p.ChainID = uint256.NewInt(0) },
		func(p *PostQuantumTx) { p.To = nil },
	} {
		_, tx, _ := walletFalconVector(t)
		mutate(tx.inner.(*PostQuantumTx))
		if _, err := EncodeEthereumTransaction(tx); err == nil {
			t.Fatal("invalid wire shape encoded")
		}
	}
}
func TestPQActivationUsesConfiguredBlock(t *testing.T) {
	_, tx, _ := walletFalconVector(t)
	cfg := &params.ChainConfig{ChainID: big.NewInt(94), LondonBlock: big.NewInt(0), PQTxBlock: big.NewInt(10)}
	for _, height := range []int64{0, 9} {
		if _, err := Sender(MakeSignerWithTimestamp(cfg, big.NewInt(height), uint64(height)), tx); err == nil {
			t.Fatalf("PQ accepted before activation at %d", height)
		}
	}
	if _, err := Sender(MakeSignerWithTimestamp(cfg, big.NewInt(10), 10), tx); err != nil {
		t.Fatal(err)
	}
	cfg.PQTxBlock = nil
	if _, err := Sender(MakeSignerWithTimestamp(cfg, big.NewInt(11), 11), tx); err == nil {
		t.Fatal("PQ accepted with unset fork")
	}
}
func TestPQExportGoInteropVector(t *testing.T) {
	path := os.Getenv("N42_PQ_GO_VECTOR_OUTPUT")
	if path == "" {
		t.Skip("optional cross-language fixture export")
	}
	// Public synthetic key material for local tests only. Never fund this address.
	pk, sk, err := falcon.NewKeyFromSeed(bytes.Repeat([]byte{3}, falcon.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	_, source, _ := walletFalconVector(t)
	p := source.inner.(*PostQuantumTx)
	p.PubKeyData = pk.Bytes()
	p.PQSignature = nil
	digest := p.SigningHash()
	signature, err := falcon.Sign(sk, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	p.PQSignature = signature
	tx := NewTx(p)
	raw, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := Sender(NewFalconTransactionSigner(big.NewInt(94)), tx)
	if err != nil {
		t.Fatal(err)
	}
	v := map[string]string{"rawTransaction": "0x" + hex.EncodeToString(raw), "signingHash": digest.Hex(), "sender": sender.Hex()}
	data, _ := json.Marshal(v)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
