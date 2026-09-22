// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S32 (docs/QS_BLOCK_TIME_BUDGET.md 6di/6dj): a follower's block-push
// receive path RLP-decodes every transaction of a pushed block even though
// ~99.4% of them already sit in the node's own pool, fully decoded, with
// their sender cached (6df's own (5') finding). DecodeRLPReusePool decodes
// a block exactly like DecodeRLP, except each transaction is looked up by
// hash BEFORE it is decoded -- computed directly from the raw bytes found
// in the block's own RLP, never from any pool object's own cached
// encoding -- and a pool hit reuses that object instead of decoding fresh.

package block

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/rlp"
)

// TxLookup answers "do you already hold a decoded transaction, with sender
// cached, for this hash?" during a reuse-aware block decode. A nil return
// (including a nil TxLookup itself) means: decode this transaction fresh.
type TxLookup func(hash types.Hash) *transaction.Transaction

// reuseSafeTxType lists the transaction types whose in-block encoding
// (blockRLP.TxData[i], i.e. tx.EthEncoded()) is a value a REUSED pool
// object can be trusted to keep producing later (re-serialization, tx
// root, etc.) -- the SAME list transaction.Transaction.Hash's own
// (unexported) hashFromEncoding uses to decide when a cached tx.enc is a
// safe hash preimage. Blob and SetCode are deliberately excluded: a pool
// Blob transaction's own cached encoding may carry its EIP-4844 sidecar
// (the pool/gossip network wrapper), which the block's own encoding never
// does (common/transaction/ethereum_rlp.go's EncodeEthereumTransaction vs
// EncodeEthereumPooledTransaction) -- reusing that OBJECT downstream could
// diverge from what this block actually contains even though its hash
// still matches. This step reuses that existing safety boundary rather
// than asserting a new one; both excluded types simply decode fresh here,
// exactly as if they had missed the pool.
func reuseSafeTxType(txType uint8) bool {
	switch txType {
	case transaction.LegacyTxType, transaction.AccessListTxType, transaction.DynamicFeeTxType:
		return true
	}
	return false
}

// DecodeRLPReusePool decodes a block exactly like DecodeRLP, except each
// transaction is looked up via lookup by its own hash (computed from its
// raw bytes in the block) before being decoded; a hit whose type is
// reuse-safe (see reuseSafeTxType) reuses the pool's object instead of
// decoding it fresh. The resulting Transactions list is element-for-element
// equal to what DecodeRLP alone would produce. lookup may be nil, in which
// case this behaves exactly like DecodeRLP (reused == 0).
func (b *Block) DecodeRLPReusePool(s *rlp.Stream, lookup TxLookup) (reused, decoded int, err error) {
	var dec blockRLP
	if err := s.Decode(&dec); err != nil {
		return 0, 0, err
	}
	txs, reused, decoded, err := decodeBlockTxsReuse(dec.TxData, lookup)
	if err != nil {
		return 0, 0, err
	}
	b.header = dec.Header
	b.body = &Body{Txs: txs, Verifiers: dec.Verifiers, Rewards: dec.Rewards, ZkProof: dec.ZkProof}
	b.hash = atomic.Value{}
	b.size = atomic.Value{}
	b.decodeReused, b.decodeDecoded = reused, decoded
	return reused, decoded, nil
}

// decodeBlockTxsReuse mirrors decodeBlockTxs' own parallel-fan-out shape
// (same worker count and threshold) but resolves each transaction via
// lookup first. The error, if any, is the one of the lowest failing
// index, matching decodeBlockTxs' own contract.
func decodeBlockTxsReuse(data [][]byte, lookup TxLookup) (txs []*transaction.Transaction, reused, decoded int, err error) {
	if lookup == nil {
		txs, err = decodeBlockTxs(data)
		return txs, 0, len(txs), err
	}
	txs = make([]*transaction.Transaction, len(data))
	wasReused := make([]bool, len(data))

	decodeOne := func(i int) error {
		h := crypto.Keccak256Hash(data[i])
		if tx := lookup(h); tx != nil && reuseSafeTxType(tx.Type()) {
			txs[i] = tx
			wasReused[i] = true
			return nil
		}
		tx, derr := transaction.DecodeEthereumTransaction(data[i])
		if derr != nil {
			return derr
		}
		txs[i] = tx
		return nil
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16
	}
	if len(data) < parallelTxDecodeMin || workers < 2 {
		for i := range data {
			if err := decodeOne(i); err != nil {
				return nil, 0, 0, err
			}
		}
	} else {
		chunk := (len(data) + workers - 1) / workers
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			errAt = -1
			first error
		)
		for w := 0; w < workers; w++ {
			lo, hi := w*chunk, (w+1)*chunk
			if hi > len(data) {
				hi = len(data)
			}
			if lo >= hi {
				break
			}
			wg.Add(1)
			go func(lo, hi int) {
				defer wg.Done()
				for i := lo; i < hi; i++ {
					if err := decodeOne(i); err != nil {
						mu.Lock()
						if errAt < 0 || i < errAt {
							errAt, first = i, err
						}
						mu.Unlock()
						return
					}
				}
			}(lo, hi)
		}
		wg.Wait()
		if first != nil {
			return nil, 0, 0, first
		}
	}

	for _, r := range wasReused {
		if r {
			reused++
		} else {
			decoded++
		}
	}
	return txs, reused, decoded, nil
}
