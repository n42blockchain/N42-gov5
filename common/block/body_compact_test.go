// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func sampleBody(t *testing.T, txCount int) *Body {
	t.Helper()
	r := rand.New(rand.NewSource(7))
	chainID := uint256.NewInt(94)
	signer := transaction.LatestSignerForChainID(chainID.ToBig())

	body := new(Body)
	for i := 0; i < txCount; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		var to types.Address
		r.Read(to[:])
		data := make([]byte, 16+i*8)
		r.Read(data)
		tx, err := transaction.SignNewTx(key, signer, &transaction.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: uint256.NewInt(uint64(1 + i)),
			GasFeeCap: uint256.NewInt(1e10),
			Gas:       21000 + uint64(i),
			To:        &to,
			Value:     uint256.NewInt(uint64(1e15 + i)),
			Data:      data,
		})
		if err != nil {
			t.Fatal(err)
		}
		body.Txs = append(body.Txs, tx)
	}

	for i := 0; i < 3; i++ {
		v := new(Verify)
		r.Read(v.Address[:])
		r.Read(v.PublicKey[:])
		body.Verifiers = append(body.Verifiers, v)
	}
	for i := 0; i < 2; i++ {
		rw := &Reward{Amount: uint256.NewInt(uint64(2e18 + i))}
		r.Read(rw.Address[:])
		body.Rewards = append(body.Rewards, rw)
	}
	body.ZkProof = make([]byte, 64)
	r.Read(body.ZkProof)
	return body
}

func assertBodiesEqual(t *testing.T, want, got *Body) {
	t.Helper()
	if len(got.Txs) != len(want.Txs) {
		t.Fatalf("tx count %d != %d", len(got.Txs), len(want.Txs))
	}
	for i := range want.Txs {
		if got.Txs[i].Hash() != want.Txs[i].Hash() {
			t.Errorf("tx %d hash %s != %s", i, got.Txs[i].Hash(), want.Txs[i].Hash())
		}
	}
	if len(got.Verifiers) != len(want.Verifiers) {
		t.Fatalf("verifier count %d != %d", len(got.Verifiers), len(want.Verifiers))
	}
	for i := range want.Verifiers {
		if got.Verifiers[i].Address != want.Verifiers[i].Address {
			t.Errorf("verifier %d address differs", i)
		}
		if got.Verifiers[i].PublicKey != want.Verifiers[i].PublicKey {
			t.Errorf("verifier %d public key differs", i)
		}
	}
	if len(got.Rewards) != len(want.Rewards) {
		t.Fatalf("reward count %d != %d", len(got.Rewards), len(want.Rewards))
	}
	for i := range want.Rewards {
		if got.Rewards[i].Address != want.Rewards[i].Address {
			t.Errorf("reward %d address differs", i)
		}
		if got.Rewards[i].Amount.Cmp(want.Rewards[i].Amount) != 0 {
			t.Errorf("reward %d amount %s != %s", i, got.Rewards[i].Amount, want.Rewards[i].Amount)
		}
	}
	if !bytes.Equal(got.ZkProof, want.ZkProof) {
		t.Error("ZK proof differs")
	}
}

// TestCompactBodyRoundTrip pins every member of a body across the codec the
// freezer now serializes with. Transaction identity is checked by hash, which
// is what a body is ultimately verified against.
func TestCompactBodyRoundTrip(t *testing.T) {
	for _, txCount := range []int{0, 1, 5, 40} {
		body := sampleBody(t, txCount)

		encoded, err := body.MarshalCompact()
		if err != nil {
			t.Fatalf("MarshalCompact(%d txs): %v", txCount, err)
		}
		if !IsCompactBody(encoded) {
			t.Fatalf("MarshalCompact(%d txs) output not recognised", txCount)
		}

		decoded := new(Body)
		if err := decoded.UnmarshalCompact(encoded); err != nil {
			t.Fatalf("UnmarshalCompact(%d txs): %v", txCount, err)
		}
		assertBodiesEqual(t, body, decoded)
	}
}

// TestCompactBodySizeVsProto records what the change buys on the freezer's
// serialized bodies. Full-entropy signatures and addresses, as a real body has.
func TestCompactBodySizeVsProto(t *testing.T) {
	for _, txCount := range []int{1, 10, 100} {
		body := sampleBody(t, txCount)

		protoBytes, err := marshalBodyProto(body)
		if err != nil {
			t.Fatalf("proto marshal: %v", err)
		}
		compactBytes, err := body.MarshalCompact()
		if err != nil {
			t.Fatalf("MarshalCompact: %v", err)
		}
		t.Logf("%3d txs: proto %6d B, compact %6d B -> %.1f%% smaller",
			txCount, len(protoBytes), len(compactBytes),
			float64(len(protoBytes)-len(compactBytes))*100/float64(len(protoBytes)))
		if len(compactBytes) >= len(protoBytes) {
			t.Errorf("%d txs: compact (%d B) is not smaller than proto (%d B)",
				txCount, len(compactBytes), len(protoBytes))
		}
	}
}

// TestCompactBodyRejectsMalformed checks the decoder fails rather than
// allocating on a corrupt length or accepting a truncated record.
func TestCompactBodyRejectsMalformed(t *testing.T) {
	body := sampleBody(t, 4)
	valid, err := body.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"empty":             {},
		"marker only":       {compactBodyMarker},
		"wrong version":     {compactBodyMarker, 0x99, 0x00},
		"truncated mid-tx":  valid[:len(valid)/2],
		"trailing bytes":    append(append([]byte(nil), valid...), 0x00),
		"tx count too high": {compactBodyMarker, compactBodyVersion, 0xff, 0xff, 0xff, 0x7f},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			decoded := new(Body)
			if err := decoded.UnmarshalCompact(data); err == nil {
				t.Errorf("expected an error, decoded %d txs", len(decoded.Txs))
			}
		})
	}
}

// marshalBodyProto produces the old freezer encoding, for the size comparison.
func marshalBodyProto(b *Body) ([]byte, error) {
	return protoMarshalForTest(b.ToProtoMessage())
}
