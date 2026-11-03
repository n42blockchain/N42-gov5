// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestE1LightNoTxIndexCommit pins N42_E1_LIGHT_NO_TXINDEX on both commit
// branches (a new head, then the duplicate of it): a light node with the
// flag writes no TxLookup row and feeds the indexer nothing, with or
// without an indexer attached; the flag on a non-light node, and a light
// node without the flag, keep today's behaviour.
func TestE1LightNoTxIndexCommit(t *testing.T) {
	defer func(v bool) { e1LightNoTxIndex = v }(e1LightNoTxIndex)
	cases := []struct {
		name               string
		flag, light, index bool
		wantSkip           bool
	}{
		{"light+flag, no indexer", true, true, false, true},
		{"light+flag, indexer", true, true, true, true},
		{"executor+flag, no indexer", true, false, false, false},
		{"executor+flag, indexer", true, false, true, false},
		{"light, no flag, no indexer", false, true, false, false},
		{"light, no flag, indexer", false, true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e1LightNoTxIndex = c.flag
			db := newRealignTestDB(t)
			parent := block.NewBlock(&block.Header{
				Number:     uint256.NewInt(8),
				Difficulty: uint256.NewInt(1),
				Root:       types.Hash{0x08},
			}, nil).(*block.Block)
			from := types.HexToAddress("0x100")
			to := types.HexToAddress("0x200")
			txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
			child := block.NewBlock(&block.Header{
				Number:     uint256.NewInt(9),
				ParentHash: parent.Hash(),
				Difficulty: uint256.NewInt(1),
				Root:       types.Hash{0x09},
			}, []*transaction.Transaction{txn}).(*block.Block)
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				if err := rawdb.WriteBlock(tx, parent); err != nil {
					return err
				}
				if err := rawdb.WriteBlock(tx, child); err != nil {
					return err
				}
				if err := rawdb.WriteCanonicalHash(tx, parent.Hash(), 8); err != nil {
					return err
				}
				rawdb.WriteHeadBlockHash(tx, parent.Hash())
				return rawdb.WriteHeadHeaderHash(tx, parent.Hash())
			}); err != nil {
				t.Fatal(err)
			}
			bc := &BlockChain{ChainDB: db, ctx: context.Background()}
			bc.currentBlock.Store(parent)
			if c.light {
				bc.SetE1SourceForTest("http://127.0.0.1:1")
			}
			var ix *testTxIndexer
			if c.index {
				ix = &testTxIndexer{}
				bc.SetTxIndexer(ix)
			}
			if got := bc.skipTxIndex(); got != c.wantSkip {
				t.Fatalf("skipTxIndex = %v, want %v", got, c.wantSkip)
			}
			// New head, then the duplicate commit's branch.
			for i := 0; i < 2; i++ {
				if err := bc.CommitToCanonical(child.Hash()); err != nil {
					t.Fatalf("commit %d: %v", i, err)
				}
			}
			if bc.CurrentBlock().Hash() != child.Hash() {
				t.Fatal("commit did not advance the head")
			}
			if err := db.View(context.Background(), func(tx kv.Tx) error {
				got, err := rawdb.ReadTxLookupEntry(tx, txn.Hash())
				if err != nil {
					return err
				}
				wantRow := !c.wantSkip && !c.index
				if wantRow && (got == nil || *got != 9) {
					t.Fatalf("tx lookup = %v, want 9", got)
				}
				if !wantRow && got != nil {
					t.Fatalf("tx lookup written (%d), want none", *got)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if ix != nil {
				_, fed := ix.added[9]
				if fed == c.wantSkip {
					t.Fatalf("indexer fed = %v, want %v", fed, !c.wantSkip)
				}
			}
		})
	}
}

// TestE1LightNoTxIndexResolveSkipsHashes: a frame body resolved on a light
// node with N42_E1_LIGHT_NO_TXINDEX leaves the transaction hashes
// unmemoised (they are computed on demand, still correct); without the
// flag every hash is memoised during the resolve, as before.
func TestE1LightNoTxIndexResolveSkipsHashes(t *testing.T) {
	defer func(v bool) { e1LightNoTxIndex = v }(e1LightNoTxIndex)
	keys := frametest.Keys(t, 3, 21)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, hashes, txs := fb.Frames(t, 2, 6)
	for _, skip := range []bool{true, false} {
		e1LightNoTxIndex = skip
		st := store.New(1 << 24)
		for i := range raws {
			st.Put(hashes[i], raws[i])
		}
		bc := &BlockChain{ctx: context.Background(), ChainDB: memdb.NewTestDB(t)}
		bc.SetE1SourceForTest("http://127.0.0.1:1")
		bc.SetFrameStore(st)
		hdr := &block.Header{Number: uint256.NewInt(3), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1), GasLimit: 1 << 30}
		blk := block.NewFrameBlockFromReceipt(hdr, hashes, nil, nil, nil)
		if err := bc.ResolveFrames(blk); err != nil {
			t.Fatal(err)
		}
		got := blk.Transactions()
		if len(got) != len(txs) {
			t.Fatalf("skip=%v: resolved %d txs, want %d", skip, len(got), len(txs))
		}
		for i, tx := range got {
			if tx.HashMemoised() == skip {
				t.Fatalf("skip=%v: tx %d memoised=%v", skip, i, tx.HashMemoised())
			}
			if tx.Hash() != txs[i].Hash() {
				t.Fatalf("skip=%v: tx %d hash %s, want %s", skip, i, tx.Hash(), txs[i].Hash())
			}
		}
	}
}
