// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/proto/types_pb"
	"google.golang.org/protobuf/proto"
)

// Public expected values copied from n42-rs c446f80a38c15a84d2a68b2e3ebf9a16390146a7,
// crates/n42/tx-types/testdata/altsig_vectors.json (sha256
// cea8d623c1f0fddc22943124c16ca3846bb02556eb628d1de06467a718cb0301).
func TestAltSigReferenceVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/alt_sig_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Encoded, Hash, Sender, Signature, SigningHash string }
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 3 {
		t.Fatal("missing reference vectors")
	}
	for i, v := range vectors {
		raw, _ := hex.DecodeString(strings.TrimPrefix(v.Encoded, "0x"))
		tx, err := DecodeAltSigTransactionOffline(raw)
		if err != nil {
			t.Fatal(err)
		}
		inner := tx.inner.(*AltSigTx)
		hash, err := AltSigSigningHash(inner)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(hash.Hex(), v.SigningHash) || !strings.EqualFold(tx.Hash().Hex(), v.Hash) || !strings.EqualFold(AltSigAddress(inner.PublicKey).Hex(), v.Sender) {
			t.Fatalf("vector %d hashes/address: %s %s %s", i, hash.Hex(), tx.Hash().Hex(), AltSigAddress(inner.PublicKey).Hex())
		}

		if !ed25519.Verify(inner.PublicKey[:], hash[:], inner.Signature[:]) {
			t.Fatal("stdlib rejected valid reference signature")
		}
		signed, err := SignAltTx(inner, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(0x32 + i)}, 32)))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := signed.EthEncoded()
		if err != nil || !bytes.Equal(encoded, raw) {
			t.Fatalf("vector %d signing mismatch: %v", i, err)
		}
		if _, err := DecodeEthereumTransaction(raw); err == nil {
			t.Fatal("default ETH decoder admitted offline type")
		}
		if _, err := Sender(LatestSignerForChainID(big.NewInt(94)), tx); err == nil {
			t.Fatal("default signer admitted offline type")
		}
		if _, err := tx.WithSignatureValues(nil, nil, nil); err == nil {
			t.Fatal("VRS bypass")
		}
		if _, err := tx.WithSignature(nil, nil); err == nil {
			t.Fatal("ECDSA signature bypass")
		}
		if tx.MarshalCompactStorage() != nil {
			t.Fatal("unsupported compact type must use proto fallback")
		}
		native, err := tx.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var restored Transaction
		if err := restored.Unmarshal(native); err != nil {
			t.Fatal(err)
		}
		restoredRaw, err := restored.EthEncoded()
		if err != nil || !bytes.Equal(restoredRaw, raw) {
			t.Fatal("native protobuf dropped signature/body")
		}
		// Caller mutation cannot corrupt decoded transaction or native deep copy.
		raw[0] = 0
		if tx.Hash() != restored.Hash() {
			t.Fatal("caller bytes alias")
		}
	}
}
func TestAltSigMalformedCarrier(t *testing.T) {
	tx, err := SignAltTx(&AltSigTx{}, ed25519.NewKeyFromSeed(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	pb := tx.ToProtoMessage().(*types_pb.Transaction)
	for _, modify := range []func(*types_pb.Transaction){
		func(p *types_pb.Transaction) { p.Sign = p.Sign[:95] }, func(p *types_pb.Transaction) { p.To = nil },
		func(p *types_pb.Transaction) { p.From = &types_pb.H160{} }, func(p *types_pb.Transaction) { p.V = &types_pb.H256{} },
		func(p *types_pb.Transaction) { p.FeePerGas = &types_pb.H256{} },
		func(p *types_pb.Transaction) { p.To = &types_pb.H160{} },
		func(p *types_pb.Transaction) { p.AccessList = []*types_pb.AccessTuple{nil} },
		func(p *types_pb.Transaction) { p.PqSignature = []byte{1} }, func(p *types_pb.Transaction) { p.BlobHashes = []*types_pb.H256{{}} },
	} {
		p := proto.Clone(pb).(*types_pb.Transaction)
		modify(p)
		if _, err := FromProtoMessage(p); err == nil {
			t.Fatal("accepted malformed carrier")
		}
	}
	enc, _ := tx.EthEncoded()
	var w altSigSignedRLP
	if err := rlp.DecodeBytes(enc[1:], &w); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*altSigSignedRLP){func(w *altSigSignedRLP) { w.Algorithm = 2 }, func(w *altSigSignedRLP) { w.PublicKey = w.PublicKey[:31] }, func(w *altSigSignedRLP) { w.Signature = w.Signature[:63] }} {
		c := w
		modify(&c)
		b, err := encodeTypedEthereumTransaction(AltSigTxType, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeAltSigTransactionOffline(b); err == nil {
			t.Fatal("malformed wire accepted")
		}
	}
	if _, err := DecodeAltSigTransactionOffline(append(enc, 0)); err == nil {
		t.Fatal("trailing RLP data accepted")
	}
}

func TestAltSigDomainBoundsAndCopies(t *testing.T) {
	tx, err := SignAltTx(&AltSigTx{Data: []byte{1}, AccessList: AccessList{{StorageKeys: []types.Hash{{1}}}}}, ed25519.NewKeyFromSeed(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	inner := tx.inner.(*AltSigTx)
	base, _ := AltSigSigningHash(inner)
	for _, mutate := range []func(*AltSigTx){func(t *AltSigTx) { t.ChainID++ }, func(t *AltSigTx) { t.PublicKey[0] ^= 1 }, func(t *AltSigTx) { t.Nonce++ }, func(t *AltSigTx) { t.AccessList[0].StorageKeys[0][0] ^= 1 }} {
		changed := inner.copy().(*AltSigTx)
		mutate(changed)
		h, _ := AltSigSigningHash(changed)
		if ed25519.Verify(changed.PublicKey[:], h[:], inner.Signature[:]) {
			t.Fatal("signature survived domain mutation")
		}
	}
	if got, _ := AltSigSigningHash(inner); got != base {
		t.Fatal("copy aliases original")
	}
	changed := inner.copy().(*AltSigTx)
	changed.Signature[0] ^= 1
	if h, _ := AltSigSigningHash(changed); h != base {
		t.Fatal("signature included in signing hash")
	}
	changed.GasFeeCap = new(uint256.Int).Lsh(uint256.NewInt(1), 128)
	if _, err := AltSigSigningHash(changed); err == nil {
		t.Fatal("oversize fee accepted")
	}
	if _, err := NewTx(changed).Marshal(); err == nil {
		t.Fatal("native marshal truncated oversized fee")
	}
}

func TestAltSigDisabledBeforeSenderCaches(t *testing.T) {
	tx, err := SignAltTx(&AltSigTx{}, ed25519.NewKeyFromSeed(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	signer := LatestSignerForChainID(big.NewInt(0))
	forged := types.Address{1}
	// Deliberately poison both cache layers using package-private hooks; even an
	// internal caller's mistaken prewarm must not activate this offline type.
	tx.from.Store(sigCache{signer: signer, from: forged})
	senderCachePut(tx.Hash(), signer, forged)
	if _, err := Sender(signer, tx); err == nil {
		t.Fatal("memo bypass enabled offline type")
	}
	if _, err := RecoverSenderFromSig(signer, tx); err == nil {
		t.Fatal("global cache bypass enabled offline type")
	}
	if _, ok := CachedSender(signer, tx); ok {
		t.Fatal("cached sender exposed for offline type")
	}
	tx = NewTx(tx.inner)
	tx.CacheSender(signer, forged)
	tx.SetSenderMemo(signer, forged)
	if tx.from.Load() != nil {
		t.Fatal("disabled type accepted sender memo")
	}
}
