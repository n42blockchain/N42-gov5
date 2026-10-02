/*
   Copyright 2022-2026 The N42 Authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/
//
// Synthetic tests for BySenderAndNonce helpers in by_nonce.go: descend order,
// blob accounting, hasTxs/has membership checks.

package txpool

import (
	"testing"

	"github.com/google/btree"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

func newTestBySenderAndNonce() *BySenderAndNonce {
	return &BySenderAndNonce{
		tree:              btree.NewG[*metaTx](32, SortByNonceLess),
		search:            &metaTx{Tx: &types.TxSlot{}},
		senderIDTxnCount:  map[uint64]int{},
		senderIDBlobCount: map[uint64]uint64{},
	}
}

func newByNonceMetaTx(senderID, nonce uint64) *metaTx {
	return newMetaTx(&types.TxSlot{SenderID: senderID, Nonce: nonce}, false, 0)
}

func TestBySenderAndNonceDescendOrder(t *testing.T) {
	b := newTestBySenderAndNonce()
	logger := log.New()
	for _, n := range []uint64{0, 1, 2, 5} {
		b.replaceOrInsert(newByNonceMetaTx(1, n), logger)
	}
	// Another sender interleaved; descend must skip it.
	b.replaceOrInsert(newByNonceMetaTx(2, 3), logger)

	var seen []uint64
	b.descend(1, func(mt *metaTx) bool {
		seen = append(seen, mt.Tx.Nonce)
		return true
	})
	want := []uint64{5, 2, 1, 0}
	if len(seen) != len(want) {
		t.Fatalf("expected %d nonces, got %d (%v)", len(want), len(seen), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("descend order mismatch at %d: got %d want %d (%v)", i, seen[i], want[i], seen)
		}
	}
}

func TestBySenderAndNonceDescendEarlyStop(t *testing.T) {
	b := newTestBySenderAndNonce()
	logger := log.New()
	for _, n := range []uint64{0, 1, 2} {
		b.replaceOrInsert(newByNonceMetaTx(1, n), logger)
	}
	var seen []uint64
	b.descend(1, func(mt *metaTx) bool {
		seen = append(seen, mt.Tx.Nonce)
		return false // stop after first
	})
	if len(seen) != 1 || seen[0] != 2 {
		t.Fatalf("expected descend to stop after first (highest) nonce, got %v", seen)
	}
}

func TestBySenderAndNonceBlobCount(t *testing.T) {
	b := newTestBySenderAndNonce()
	logger := log.New()

	if got := b.blobCount(7); got != 0 {
		t.Fatalf("expected 0 blob count for unknown sender, got %d", got)
	}

	blobTx := newMetaTx(&types.TxSlot{
		SenderID: 7, Nonce: 0, Type: types.BlobTxType, Blobs: [][]byte{{1}, {2}, {3}},
	}, false, 0)
	b.replaceOrInsert(blobTx, logger)

	if got := b.blobCount(7); got != 3 {
		t.Fatalf("expected blob count 3, got %d", got)
	}

	b.delete(blobTx, txpoolcfg.Success, logger)
	if got := b.blobCount(7); got != 0 {
		t.Fatalf("expected blob count 0 after delete, got %d", got)
	}
}

func TestBySenderAndNonceHasTxsAndHas(t *testing.T) {
	b := newTestBySenderAndNonce()
	logger := log.New()

	if b.hasTxs(42) {
		t.Fatal("expected hasTxs false before insertion")
	}

	mt := newByNonceMetaTx(42, 0)
	if b.has(mt) {
		t.Fatal("expected has false before insertion")
	}

	b.replaceOrInsert(mt, logger)

	if !b.hasTxs(42) {
		t.Fatal("expected hasTxs true after insertion")
	}
	if !b.has(mt) {
		t.Fatal("expected has true after insertion")
	}

	b.delete(mt, txpoolcfg.Success, logger)
	if b.hasTxs(42) {
		t.Fatal("expected hasTxs false after delete")
	}
}
