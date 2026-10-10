// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestResolveFramesAttachesAdmissionSenders: a frame body resolved from the
// store carries the senders the store cached at admission (P2a item a), so
// the import's recovery step finds them memoised; a frame without a cache
// is left to that step.
func TestResolveFramesAttachesAdmissionSenders(t *testing.T) {
	keys := frametest.Keys(t, 3, 21)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, hashes, txs := fb.Frames(t, 2, 6)
	signer := transaction.LatestSignerForChainID(senderRecoveryChainID)

	st := store.New(1 << 24)
	for i := range raws {
		st.Put(hashes[i], raws[i])
	}
	// Frame 0 has a cache whose first entry is a marker; frame 1 has none.
	ss, _, err := frame.RecoverSenders(signer, raws[0])
	if err != nil {
		t.Fatal(err)
	}
	marker := types.HexToAddress("0x00000000000000000000000000000000000000bb")
	ss.Addrs[0] = marker
	st.SetSenders(hashes[0], ss)

	bc := &BlockChain{ctx: context.Background(), ChainDB: memdb.NewTestDB(t)}
	bc.SetFrameStore(st)
	hdr := &block.Header{Number: uint256.NewInt(3), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1), GasLimit: 1 << 30}
	blk := block.NewFrameBlockFromReceipt(hdr, hashes, nil, nil, nil)
	if err := bc.ResolveFrames(blk); err != nil {
		t.Fatal(err)
	}
	got := blk.Transactions()
	if len(got) != len(txs) {
		t.Fatalf("resolved %d txs", len(got))
	}
	if a, _ := transaction.Sender(signer, got[0]); a != marker {
		t.Fatalf("admission sender not attached: %s", a)
	}
	want, _ := signer.Sender(txs[7])
	if a, _ := transaction.Sender(signer, got[7]); a != want {
		t.Fatalf("uncached frame sender %s, want %s", a, want)
	}
}
