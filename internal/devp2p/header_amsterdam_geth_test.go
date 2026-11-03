// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package devp2p

import (
	"math/big"
	"testing"

	gethcommon "github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// An Amsterdam header (EIP-7928 + EIP-7843) must hash exactly as geth hashes
// it. Before SlotNumber existed, position 23 held N42's MobileRegistryRoot, so
// a real Amsterdam header mis-decoded and hashed differently.
//
// The test lives here rather than in common/block because linking
// go-ethereum's types next to N42's secp256k1 only works in packages that
// already import geth.
func TestHeaderAmsterdamHashMatchesGeth(t *testing.T) {
	h := func(b byte) types.Hash { return types.Hash{b} }
	wh, pbr, rh, bal := h(7), h(8), h(9), h(10)
	blobUsed, excess, slot := uint64(131072), uint64(393216), uint64(13_456_789)
	n42 := &block.Header{
		ParentHash:          h(1),
		UncleHash:           types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:            types.Address{0xaa},
		Root:                h(2),
		TxHash:              h(3),
		ReceiptHash:         h(4),
		Difficulty:          uint256.NewInt(0),
		Number:              uint256.NewInt(24_000_000),
		GasLimit:            60_000_000,
		GasUsed:             12_345_678,
		Time:                1_800_000_000,
		Extra:               []byte("n42"),
		MixDigest:           h(5),
		BaseFee:             uint256.NewInt(1_000_000_007),
		WithdrawalsHash:     &wh,
		BlobGasUsed:         &blobUsed,
		ExcessBlobGas:       &excess,
		ParentBeaconRoot:    &pbr,
		RequestsHash:        &rh,
		BlockAccessListHash: &bal,
		SlotNumber:          &slot,
	}
	gh := func(x types.Hash) *gethcommon.Hash { v := gethcommon.Hash(x); return &v }
	geth := &gethtypes.Header{
		ParentHash:          gethcommon.Hash(n42.ParentHash),
		UncleHash:           gethcommon.Hash(n42.UncleHash),
		Coinbase:            gethcommon.Address(n42.Coinbase),
		Root:                gethcommon.Hash(n42.Root),
		TxHash:              gethcommon.Hash(n42.TxHash),
		ReceiptHash:         gethcommon.Hash(n42.ReceiptHash),
		Difficulty:          big.NewInt(0),
		Number:              big.NewInt(24_000_000),
		GasLimit:            n42.GasLimit,
		GasUsed:             n42.GasUsed,
		Time:                n42.Time,
		Extra:               n42.Extra,
		MixDigest:           gethcommon.Hash(n42.MixDigest),
		BaseFee:             big.NewInt(1_000_000_007),
		WithdrawalsHash:     gh(wh),
		BlobGasUsed:         &blobUsed,
		ExcessBlobGas:       &excess,
		ParentBeaconRoot:    gh(pbr),
		RequestsHash:        gh(rh),
		BlockAccessListHash: gh(bal),
		SlotNumber:          &slot,
	}
	if got, want := n42.Hash(), types.Hash(geth.Hash()); got != want {
		t.Fatalf("Amsterdam header hash %s, geth %s", got.Hex(), want.Hex())
	}

	// A geth-encoded Amsterdam header decodes into the right fields.
	enc, err := rlp.EncodeToBytes(geth)
	if err != nil {
		t.Fatal(err)
	}
	var back block.Header
	if err := rlp.DecodeBytes(enc, &back); err != nil {
		t.Fatalf("decode geth header: %v", err)
	}
	if back.SlotNumber == nil || *back.SlotNumber != slot || back.MobileRegistryRoot != nil {
		t.Fatalf("decoded SlotNumber=%v MobileRegistryRoot=%v, want %d and nil", back.SlotNumber, back.MobileRegistryRoot, slot)
	}
	if back.Hash() != n42.Hash() {
		t.Fatalf("decoded hash %s, want %s", back.Hash().Hex(), n42.Hash().Hex())
	}
}
