// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/rlp"
)

// TestProfile1SealedHeaderSurvivesTheBlockWire: a sealed profile-1 block sent
// the way block push and fetch send it (ETH RLP of the block) and decoded the
// way the receiver decodes it (header peek, plain decode, reuse-pool decode)
// keeps its hash and its zero blob gas fields, and the receiver's header check
// accepts it. Regression for the 4-node smoke where every follower refused
// block 1 at the vote gate with "blob gas fields are not zero: absent".
func TestProfile1SealedHeaderSurvivesTheBlockWire(t *testing.T) {
	f := newP1Fixture(t)
	hs := f.chainOf(1)
	sealed := f.seal(f.child(hs[1], 2, nil))
	if err := checkProfile1Fields(sealed); err != nil {
		t.Fatalf("producer header refused: %v", err)
	}
	want := sealed.Hash()
	enc, err := rlp.EncodeToBytes(block.NewBlock(sealed, nil))
	if err != nil {
		t.Fatal(err)
	}

	decoded := map[string]*block.Header{}
	st := rlp.NewStream(bytes.NewReader(enc), uint64(len(enc)))
	if _, err := st.List(); err != nil {
		t.Fatal(err)
	}
	var peek block.Header
	if err := st.Decode(&peek); err != nil {
		t.Fatal(err)
	}
	decoded["peek"] = &peek
	var plain block.Block
	if err := rlp.DecodeBytes(enc, &plain); err != nil {
		t.Fatal(err)
	}
	decoded["block"] = plain.Header().(*block.Header)
	var pooled block.Block
	if _, _, err := pooled.DecodeRLPReusePool(rlp.NewStream(bytes.NewReader(enc), uint64(len(enc))), nil); err != nil {
		t.Fatal(err)
	}
	decoded["reuse"] = pooled.Header().(*block.Header)

	for path, h := range decoded {
		h.ResetHashCache()
		if h.Hash() != want {
			t.Errorf("%s: hash %s, want %s", path, h.Hash().Hex(), want.Hex())
		}
		if h.BlobGasUsed == nil || h.ExcessBlobGas == nil || *h.BlobGasUsed != 0 || *h.ExcessBlobGas != 0 {
			t.Errorf("%s: blobGasUsed %v, excessBlobGas %v, want present zero", path, h.BlobGasUsed, h.ExcessBlobGas)
		}
		if h.SlotNumber == nil || *h.SlotNumber != *sealed.SlotNumber || h.MobileRegistryRoot == nil ||
			h.ParentBeaconRoot == nil || h.RequestsHash == nil || h.WithdrawalsHash == nil {
			t.Errorf("%s: an optional field was lost: %+v", path, h)
		}
		if h.BlockAccessListHash != nil {
			t.Errorf("%s: element 22 decoded present", path)
		}
		if err := checkProfile1Fields(h); err != nil {
			t.Errorf("%s: receiver refuses the header: %v", path, err)
		}
		if err := f.hs.VerifyHeader(f.chain, h, true); err != nil {
			t.Errorf("%s: VerifyHeader: %v", path, err)
		}
	}
}
