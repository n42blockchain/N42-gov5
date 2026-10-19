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

// mixedTxs returns random legacy transfers, mixed with dynamic-fee
// transactions, tampered signatures (bad V, zero R, high S), wrong chain
// ids, unprotected legacy transactions, transactions without a cached
// encoding, and nil entries.
func mixedTxs(t *testing.T, rng *rand.Rand, n int, chainID *big.Int) []*Transaction {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	txs := make([]*Transaction, n)
	for i := range txs {
		switch r := rng.Intn(20); {
		case r == 0:
			txs[i] = nil
		case r == 1:
			to := types.Address{1}
			tx, err := SignNewTx(key, NewLondonSigner(chainID), &DynamicFeeTx{ChainID: uint256.MustFromBig(chainID), Nonce: uint64(i),
				GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(2), Gas: 21000, To: &to, Value: uint256.NewInt(3)})
			if err != nil {
				t.Fatal(err)
			}
			txs[i] = tx
		case r == 2:
			tx := randLegacyTx(t, rng, key, chainID)
			v, R, S := tx.RawSignatureValues()
			inner := tx.inner.copy().(*LegacyTx)
			switch rng.Intn(4) {
			case 0:
				inner.V = new(uint256.Int).AddUint64(v, 2) // chain id mismatch
			case 1:
				inner.R = new(uint256.Int)
			case 2:
				inner.S = new(uint256.Int).Not(new(uint256.Int))
			case 3:
				inner.R = new(uint256.Int).AddUint64(R, 1)
				_ = S
			}
			txs[i] = NewTx(inner)
		case r == 3:
			tx, err := SignTx(NewTx(&LegacyTx{Nonce: uint64(i), GasPrice: uint256.NewInt(1), Gas: 21000, Value: uint256.NewInt(1)}), HomesteadSigner{}, key)
			if err != nil {
				t.Fatal(err)
			}
			txs[i] = tx
		case r == 4:
			txs[i] = randLegacyTx(t, rng, key, big.NewInt(chainID.Int64()+1)) // other chain
		case r == 5:
			tx := randLegacyTx(t, rng, key, chainID)
			txs[i] = NewTx(tx.inner.copy()) // no cached encoding
		default:
			tx := randLegacyTx(t, rng, key, chainID)
			if rng.Intn(2) == 0 {
				inner := tx.inner.copy().(*LegacyTx)
				inner.Data = nil
				tx2, err := SignTx(NewTx(inner), NewEIP155Signer(chainID), key)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := EncodeEthereumTransaction(tx2)
				tx, _ = DecodeEthereumTransaction(raw)
			}
			txs[i] = tx
		}
	}
	return txs
}

func cloneTxs(txs []*Transaction) []*Transaction {
	out := make([]*Transaction, len(txs))
	for i, tx := range txs {
		if tx == nil {
			continue
		}
		if p := tx.enc.Load(); p != nil {
			c, err := DecodeEthereumTransaction(append([]byte(nil), *p...))
			if err == nil {
				out[i] = c
				continue
			}
		}
		out[i] = NewTx(tx.inner.copy())
	}
	return out
}

// TestHashBatchAndSendersBatchEquivalence: HashBatch memoises exactly the
// hashes tx.Hash() computes one by one, and SendersBatch returns exactly
// signer.Sender per transaction (address and error), across signers, batch
// lengths and the transaction mix above, with the flag on and off.
func TestHashBatchAndSendersBatchEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	chainID := big.NewInt(1142)
	signers := []Signer{NewLondonSigner(chainID), NewEIP2930Signer(chainID), NewEIP155Signer(chainID), HomesteadSigner{}}
	for round := 0; round < 40; round++ {
		n := rng.Intn(70)
		txs := mixedTxs(t, rng, n, chainID)
		for _, on := range []bool{true, false} {
			withOneBlock(on, func() {
				ref := cloneTxs(txs)
				got := cloneTxs(txs)
				// Pre-hash a few so HashBatch meets ready hashes too.
				for i := range got {
					if got[i] != nil && rng.Intn(10) == 0 {
						got[i].Hash()
					}
				}
				HashBatch(got)
				for i := range ref {
					if ref[i] == nil {
						continue
					}
					if got[i].hashState.Load() != hashReady {
						t.Fatalf("round %d tx %d: hash not memoised", round, i)
					}
					want := ref[i].inner.hash()
					if ref[i].Hash() != want || got[i].Hash() != want {
						t.Fatalf("round %d tx %d on=%v: hash %x / %x want %x", round, i, on, got[i].Hash(), ref[i].Hash(), want)
					}
				}
				for _, sg := range signers {
					addrs := make([]types.Address, len(got))
					errs := make([]error, len(got))
					SendersBatch(sg, got, addrs, errs)
					for i, tx := range ref {
						if tx == nil {
							if addrs[i] != (types.Address{}) || errs[i] != nil {
								t.Fatalf("nil tx %d touched", i)
							}
							continue
						}
						wa, we := sg.Sender(tx)
						if addrs[i] != wa || (we == nil) != (errs[i] == nil) || (we != nil && we.Error() != errs[i].Error()) {
							t.Fatalf("round %d tx %d on=%v %T: batch (%x, %v) single (%x, %v)", round, i, on, sg, addrs[i], errs[i], wa, we)
						}
					}
				}
			})
		}
	}
}
