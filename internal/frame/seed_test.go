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

package frame_test

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
)

// TestV2FrameSeedsTxHashes: frames built by the gateway builder verify under
// v2, their entry hashes are the transaction hashes, and decoding with them
// as seeds publishes them as the hashes, an undecodable entry in between
// included.
func TestV2FrameSeedsTxHashes(t *testing.T) {
	defer frame.SetHashV2ForTest(true)()
	for _, pool := range []bool{true, false} {
		restore := frame.SetTxDecodePoolForTest(pool)
		keys := frametest.Keys(t, 5, 3)
		b := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
		raws, hashes, txs := b.Frames(t, 3, 40)
		k := 0
		for i, raw := range raws {
			f, err := frame.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			h, err := f.Verify()
			if err != nil || h != hashes[i] {
				t.Fatalf("frame %d: verify %v", i, err)
			}
			got, dropped, err := frame.DecodeTxsSeeded(raw, f.TxHashes)
			if err != nil || dropped != 0 || len(got) != len(f.TxHashes) {
				t.Fatalf("decode: %v %d", err, dropped)
			}
			for j, tx := range got {
				if f.TxHashes[j] != txs[k].Hash() || tx.Hash() != txs[k].Hash() {
					t.Fatalf("frame %d tx %d: hash mismatch", i, j)
				}
				k++
			}
			// The seeds are what the decode publishes: bogus seeds show up
			// (a test-only misuse; admission computes them over raw).
			bogus := make([]types.Hash, len(f.TxHashes))
			for j := range bogus {
				bogus[j] = types.Hash{byte(j + 1)}
			}
			got, _, _ = frame.DecodeTxsSeeded(raw, bogus)
			for j, tx := range got {
				if tx.Hash() != bogus[j] {
					t.Fatalf("pool=%v frame %d tx %d: seed not used", pool, i, j)
				}
			}
		}
		restore()
	}

	// An undecodable entry between two transactions: seeds stay aligned
	// with entries, not with the decoded transactions.
	keys := frametest.Keys(t, 2, 4)
	b := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
	t0, t1 := b.Tx(t, 0), b.Tx(t, 1)
	e0, _ := transaction.EncodeEthereumTransaction(t0)
	e1, _ := transaction.EncodeEthereumTransaction(t1)
	entries := [][]byte{e0, {0xc1, 0x80}, e1}
	raw, _, err := frame.Encode(keys[0], 0, frame.TxTypeEthereum, entries)
	if err != nil {
		t.Fatal(err)
	}
	seeds, err := frame.EntryHashesOf(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		if seeds[i] != crypto.Keccak256Hash(e) {
			t.Fatalf("entry hash %d", i)
		}
	}
	per, dropped, err := frame.DecodeFramesEachSeeded([][]byte{raw}, func(int) []types.Hash { return seeds }, 1, nil)
	if err != nil || dropped != 1 || len(per[0]) != 2 {
		t.Fatalf("decode: %v dropped %d", err, dropped)
	}
	if per[0][0].Hash() != t0.Hash() || per[0][1].Hash() != t1.Hash() {
		t.Fatal("seeds misaligned past a dropped entry")
	}
}
