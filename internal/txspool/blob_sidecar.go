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

package txspool

import (
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/kzg"
	"github.com/n42blockchain/N42/log"
)

// convertSidecar is the conversion the pool applies, a variable for tests.
var convertSidecar = kzg.SidecarToV1

// maybeConvertLegacySidecars queues, at the first reset onto an Osaka head,
// the conversion of every version-0 blob sidecar in the pool to version 1.
// The payload builder skips version-0 transactions from Osaka, so without
// it they would sit in the pool until they expired.
//
// Conversion once is enough: validateTx refuses version-0 sidecars from the
// moment the chain head is in Osaka, which is before this reset runs, and
// every transaction admitted earlier is in the pool by now, since admission
// and the reset both hold pool.mu.
func (pool *TxsPool) maybeConvertLegacySidecars(head block.IBlock) {
	if pool.chainconfig == nil || head == nil || !pool.chainconfig.IsOsaka(head.Time()) {
		return
	}
	if !pool.legacySidecarsConverted.CompareAndSwap(false, true) {
		return
	}
	var legacy []*transaction.Transaction
	pool.all.Range(func(_ types.Hash, tx *transaction.Transaction, _ bool) bool {
		if sc := tx.BlobTxSidecar(); sc != nil && sc.Version == transaction.BlobSidecarVersion0 {
			legacy = append(legacy, tx)
		}
		return true
	}, true, true)
	if len(legacy) == 0 {
		return
	}
	pool.wg.Add(1)
	go func() {
		defer pool.wg.Done()
		pool.convertLegacySidecars(legacy)
	}()
}

// convertLegacySidecars computes the cell proofs of each transaction's blobs
// outside the pool lock, then swaps the converted transactions in under it.
// A transaction whose sidecar does not convert is dropped, as no block can
// include it any more.
func (pool *TxsPool) convertLegacySidecars(txs []*transaction.Transaction) {
	start := time.Now()
	var (
		olds, news []*transaction.Transaction
		failed     []types.Hash
	)
	for _, tx := range txs {
		if pool.ctx.Err() != nil {
			return
		}
		sc := tx.BlobTxSidecar()
		// The blobs and commitments are shared with the original, which is
		// never modified: the conversion only replaces the proofs.
		v1 := &transaction.BlobTxSidecar{Version: sc.Version, Blobs: sc.Blobs, Commitments: sc.Commitments, Proofs: sc.Proofs}
		if err := convertSidecar(v1); err != nil {
			log.Debug("Failed to convert legacy blob sidecar", "hash", tx.Hash(), "err", err)
			failed = append(failed, tx.Hash())
			continue
		}
		olds = append(olds, tx)
		news = append(news, tx.WithBlobTxSidecar(v1))
	}

	pool.mu.Lock()
	converted := 0
	for i, tx := range news {
		if pool.replaceTxLocked(olds[i], tx) {
			converted++
		}
	}
	for _, hash := range failed {
		pool.removeTx(hash, true)
	}
	if converted > 0 {
		pool.priced.Reheap()
	}
	pool.publishPendingSnapshot()
	pool.mu.Unlock()

	log.Info("Converted legacy blob sidecars", "converted", converted, "dropped", len(failed),
		"queued", len(txs), "elapsed", time.Since(start))
}

// replaceTxLocked swaps tx in for old, the same transaction with another
// sidecar, wherever the pool holds old. It does nothing when old has left
// the pool meanwhile. The caller holds pool.mu and reheaps the priced list.
func (pool *TxsPool) replaceTxLocked(old, tx *transaction.Transaction) bool {
	if pool.all.Get(old.Hash()) != old || !pool.all.replace(old, tx) {
		return false
	}
	if from := tx.From(); from != nil {
		for _, lists := range []map[types.Address]*txsList{pool.pending, pool.queue} {
			if list := lists[*from]; list != nil {
				list.txs.replace(old, tx)
			}
		}
	}
	return true
}

// replace swaps tx in for old under old's hash, keeping it local or remote.
func (t *txLookup) replace(old, tx *transaction.Transaction) bool {
	t.lock.Lock()
	defer t.lock.Unlock()

	hash := old.Hash()
	set := t.locals
	if set[hash] != old {
		set = t.remotes
	}
	if set[hash] != old {
		return false
	}
	set[hash] = tx
	t.slots += numSlots(tx) - numSlots(old)
	return true
}

// replace swaps tx in for old at its nonce. The flattened cache is dropped,
// not edited, because published pending snapshots share it.
func (m *txsSortedMap) replace(old, tx *transaction.Transaction) {
	if m.items[old.Nonce()] != old {
		return
	}
	m.items[old.Nonce()] = tx
	m.cache = nil
}
