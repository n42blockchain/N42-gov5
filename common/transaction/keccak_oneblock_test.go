// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"crypto/ecdsa"
	"math/big"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// withOneBlock runs f with N42_KECCAK_ONEBLOCK forced on or off.
func withOneBlock(on bool, f func()) {
	prev := crypto.KeccakOneBlockEnabled
	crypto.KeccakOneBlockEnabled = on
	defer func() { crypto.KeccakOneBlockEnabled = prev }()
	f()
}

func randU256(rng *rand.Rand) *uint256.Int {
	switch rng.Intn(5) {
	case 0:
		return nil
	case 1:
		return uint256.NewInt(uint64(rng.Intn(256)))
	case 2:
		return uint256.NewInt(rng.Uint64())
	default:
		var b [32]byte
		rng.Read(b[:rng.Intn(33)])
		return new(uint256.Int).SetBytes(b[:rng.Intn(33)])
	}
}

// randLegacyTx builds a random EIP-155 legacy transaction signed by key on
// chainID. Data lengths 0..300 straddle the single-block bound of both the
// signing payload and the encoding.
func randLegacyTx(t testing.TB, rng *rand.Rand, key *ecdsa.PrivateKey, chainID *big.Int) *Transaction {
	var to *types.Address
	if rng.Intn(4) != 0 {
		a := types.Address{}
		rng.Read(a[:])
		to = &a
	}
	data := make([]byte, rng.Intn(301))
	rng.Read(data)
	gp := randU256(rng)
	if gp == nil {
		gp = uint256.NewInt(1)
	}
	val := randU256(rng)
	if val == nil {
		val = uint256.NewInt(0)
	}
	inner := &LegacyTx{Nonce: rng.Uint64() >> uint(rng.Intn(64)), GasPrice: gp, Gas: rng.Uint64() >> uint(rng.Intn(64)),
		To: to, Value: val, Data: data}
	tx, err := SignTx(NewTx(inner), NewEIP155Signer(chainID), key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	return dec
}

// TestKeccakOneBlockLegacyEquivalence: for random legacy transactions the
// signing hash, the transaction hash and the recovered sender are identical
// with N42_KECCAK_ONEBLOCK on and off, and equal to the reflective RLP
// signing hash and the re-encoded transaction hash.
func TestKeccakOneBlockLegacyEquivalence(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey)
	rng := rand.New(rand.NewSource(7))
	shortSig, shortEnc := 0, 0
	for i := 0; i < 1500; i++ {
		chainID := big.NewInt([]int64{1, 127, 128, 1142, 1 << 40}[i%5])
		tx := randLegacyTx(t, rng, key, chainID)
		signer := NewLondonSigner(chainID)
		refSig := hash.RlpHash([]interface{}{tx.Nonce(), tx.GasPrice(), tx.Gas(), tx.To(), tx.Value(), tx.Data(), chainID, uint(0), uint(0)})
		refHash := tx.inner.hash()
		var sigOn, sigOff, hOn, hOff types.Hash
		var aOn, aOff types.Address
		var eOn, eOff error
		withOneBlock(true, func() {
			sigOn = legacySigningHash(tx, chainID)
			hOn = tx.computeHash()
			aOn, eOn = signer.Sender(tx)
		})
		withOneBlock(false, func() {
			sigOff = legacySigningHash(tx, chainID)
			hOff = tx.computeHash()
			aOff, eOff = signer.Sender(tx)
		})
		if sigOn != refSig || sigOff != refSig {
			t.Fatalf("tx %d: signing hash on %x off %x ref %x", i, sigOn, sigOff, refSig)
		}
		if hOn != refHash || hOff != refHash {
			t.Fatalf("tx %d: tx hash on %x off %x ref %x", i, hOn, hOff, refHash)
		}
		if eOn != nil || eOff != nil || aOn != want || aOff != want {
			t.Fatalf("tx %d: sender on (%x, %v) off (%x, %v) want %x", i, aOn, eOn, aOff, eOff, want)
		}
		if len(tx.Data()) < 40 {
			shortSig++
		}
		if p := tx.enc.Load(); p != nil && len(*p) < crypto.Keccak256OneBlockMax {
			shortEnc++
		}
	}
	if shortSig == 0 || shortEnc == 0 {
		t.Fatalf("no single-block inputs exercised (sig %d, enc %d)", shortSig, shortEnc)
	}
}
