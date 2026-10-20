// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// TestResolveFramesIngestRecycleEquivalent: a frame body resolves to the
// same transactions (order, hashes, encodings, attached senders) and the
// same sender-done mark with N42_INGEST_RECYCLE on (decode into the
// block's slice) and off (per frame, concatenated), with every frame's
// senders cached, with none, and with a frame holding an undecodable
// entry (the flat decode's fallback).
func TestResolveFramesIngestRecycleEquivalent(t *testing.T) {
	keys := frametest.Keys(t, 9, 31)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, hashes, txs := fb.Frames(t, 6, 40)
	enc0, err := txs[0].EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	bad, badHash, err := frame.Encode(keys[0], 900, frame.TxTypeEthereum, [][]byte{{0x02, 0x01}, enc0})
	if err != nil {
		t.Fatal(err)
	}
	signer := transaction.LatestSignerForChainID(senderRecoveryChainID)
	type out struct {
		hashes  []types.Hash
		encs    [][]byte
		senders []types.Address
		done    bool
	}
	header := &block.Header{Number: uint256.NewInt(3), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1), GasLimit: 1 << 40, Time: 1}
	run := func(on, withSenders bool, frames [][]byte, fh []types.Hash) out {
		defer frame.SetIngestRecycleForTest(on)()
		st := store.New(1 << 30)
		for i := range frames {
			st.Put(fh[i], frames[i])
			if withSenders {
				ss, _, err := frame.RecoverSenders(signer, frames[i])
				if err != nil {
					t.Fatal(err)
				}
				st.SetSenders(fh[i], ss)
			}
		}
		bc := &BlockChain{ctx: context.Background()}
		bc.SetFrameStore(st)
		blk := block.NewFrameBlockFromReceipt(header, fh, nil, nil, nil).(*block.Block)
		if err := bc.ResolveFrames(blk); err != nil {
			t.Fatal(err)
		}
		var o out
		got := blk.Transactions()
		for _, tx := range got {
			e, err := tx.EthEncoded()
			if err != nil {
				t.Fatal(err)
			}
			o.hashes = append(o.hashes, tx.Hash())
			o.encs = append(o.encs, e)
			from, err := transaction.Sender(signer, tx)
			if err != nil {
				t.Fatal(err)
			}
			o.senders = append(o.senders, from)
		}
		o.done, _ = bc.takeSendersDone(blk.Hash(), signer, got)
		return o
	}
	withBad := append(append([][]byte(nil), raws...), bad)
	withBadHashes := append(append([]types.Hash(nil), hashes...), badHash)
	for _, c := range []struct {
		name    string
		senders bool
		frames  [][]byte
		hashes  []types.Hash
		n       int
	}{
		{"cached", true, raws, hashes, len(txs)},
		{"uncached", false, raws, hashes, len(txs)},
		{"dropped", true, withBad, withBadHashes, len(txs) + 1},
	} {
		off, on := run(false, c.senders, c.frames, c.hashes), run(true, c.senders, c.frames, c.hashes)
		if !reflect.DeepEqual(off, on) {
			t.Fatalf("%s: N42_INGEST_RECYCLE on differs from off", c.name)
		}
		if len(on.hashes) != c.n {
			t.Fatalf("%s: %d txs, want %d", c.name, len(on.hashes), c.n)
		}
		for i := range txs {
			if on.hashes[i] != txs[i].Hash() {
				t.Fatalf("%s: tx %d differs from the built one", c.name, i)
			}
		}
	}
}
