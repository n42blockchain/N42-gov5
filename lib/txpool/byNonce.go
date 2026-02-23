/*
   Copyright 2022 The Erigon contributors

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

package txpool

import (
	"fmt"
	"math"

	"github.com/google/btree"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

// BySenderAndNonce - designed to perform most expensive operation in TxPool:
// "recalculate all ephemeral fields of all transactions" by algo
//   - for all senders - iterate over all transactions in nonce growing order
//
// Performances decisions:
//   - All senders stored inside 1 large BTree - because iterate over 1 BTree is faster than over map[senderId]BTree
//   - sortByNonce used as non-pointer wrapper - because iterate over BTree of pointers is 2x slower
type BySenderAndNonce struct {
	tree              *btree.BTreeG[*metaTx]
	search            *metaTx
	senderIDTxnCount  map[uint64]int    // count of sender's txns in the pool - may differ from nonce
	senderIDBlobCount map[uint64]uint64 // count of sender's total number of blobs in the pool
}

func (b *BySenderAndNonce) nonce(senderID uint64) (nonce uint64, ok bool) {
	s := b.search
	s.Tx.SenderID = senderID
	s.Tx.Nonce = math.MaxUint64

	b.tree.DescendLessOrEqual(s, func(mt *metaTx) bool {
		if mt.Tx.SenderID == senderID {
			nonce = mt.Tx.Nonce
			ok = true
		}
		return false
	})
	return nonce, ok
}

func (b *BySenderAndNonce) ascendAll(f func(*metaTx) bool) {
	b.tree.Ascend(func(mt *metaTx) bool {
		return f(mt)
	})
}

func (b *BySenderAndNonce) ascend(senderID uint64, f func(*metaTx) bool) {
	s := b.search
	s.Tx.SenderID = senderID
	s.Tx.Nonce = 0
	b.tree.AscendGreaterOrEqual(s, func(mt *metaTx) bool {
		if mt.Tx.SenderID != senderID {
			return false
		}
		return f(mt)
	})
}

func (b *BySenderAndNonce) descend(senderID uint64, f func(*metaTx) bool) {
	s := b.search
	s.Tx.SenderID = senderID
	s.Tx.Nonce = math.MaxUint64
	b.tree.DescendLessOrEqual(s, func(mt *metaTx) bool {
		if mt.Tx.SenderID != senderID {
			return false
		}
		return f(mt)
	})
}

func (b *BySenderAndNonce) count(senderID uint64) int {
	return b.senderIDTxnCount[senderID]
}

func (b *BySenderAndNonce) blobCount(senderID uint64) uint64 {
	return b.senderIDBlobCount[senderID]
}

func (b *BySenderAndNonce) hasTxs(senderID uint64) bool {
	has := false
	b.ascend(senderID, func(*metaTx) bool {
		has = true
		return false
	})
	return has
}

func (b *BySenderAndNonce) get(senderID, txNonce uint64) *metaTx {
	s := b.search
	s.Tx.SenderID = senderID
	s.Tx.Nonce = txNonce
	if found, ok := b.tree.Get(s); ok {
		return found
	}
	return nil
}

// nolint
func (b *BySenderAndNonce) has(mt *metaTx) bool {
	return b.tree.Has(mt)
}

func (b *BySenderAndNonce) delete(mt *metaTx, reason txpoolcfg.DiscardReason, logger log.Logger) {
	if _, ok := b.tree.Delete(mt); ok {
		if mt.Tx.Traced {
			logger.Info("TX TRACING: Deleted tx by nonce", "idHash", fmt.Sprintf("%x", mt.Tx.IDHash), "sender", mt.Tx.SenderID, "nonce", mt.Tx.Nonce, "reason", reason)
		}

		senderID := mt.Tx.SenderID
		count := b.senderIDTxnCount[senderID]
		if count > 1 {
			b.senderIDTxnCount[senderID] = count - 1
		} else {
			delete(b.senderIDTxnCount, senderID)
		}

		if mt.Tx.Type == types.BlobTxType && mt.Tx.Blobs != nil {
			accBlobCount := b.senderIDBlobCount[senderID]
			txnBlobCount := len(mt.Tx.Blobs)
			if txnBlobCount > 1 {
				b.senderIDBlobCount[senderID] = accBlobCount - uint64(txnBlobCount)
			} else {
				delete(b.senderIDBlobCount, senderID)
			}
		}
	}
}

func (b *BySenderAndNonce) replaceOrInsert(mt *metaTx, logger log.Logger) *metaTx {
	it, ok := b.tree.ReplaceOrInsert(mt)

	if ok {
		if mt.Tx.Traced {
			logger.Info("TX TRACING: Replaced tx by nonce", "idHash", fmt.Sprintf("%x", mt.Tx.IDHash), "sender", mt.Tx.SenderID, "nonce", mt.Tx.Nonce)
		}
		return it
	}

	if mt.Tx.Traced {
		logger.Info("TX TRACING: Inserted tx by nonce", "idHash", fmt.Sprintf("%x", mt.Tx.IDHash), "sender", mt.Tx.SenderID, "nonce", mt.Tx.Nonce)
	}

	b.senderIDTxnCount[mt.Tx.SenderID]++
	if mt.Tx.Type == types.BlobTxType && mt.Tx.Blobs != nil {
		b.senderIDBlobCount[mt.Tx.SenderID] += uint64(len(mt.Tx.Blobs))
	}
	return nil
}
