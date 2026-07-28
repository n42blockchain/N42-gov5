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
//
// Parallel sender recovery for the block-import path (geth senderCacher
// equivalent). Warms each transaction's signature cache from a worker pool
// before the serial execution loop starts, so that AsMessage inside
// ApplyTransaction hits the cache instead of paying for a secp256k1 public
// key recovery per transaction on the critical path.

package internal

import (
	"os"
	"runtime"
	"strconv"

	"github.com/n42blockchain/N42/common/transaction"
)

// Why this exists.
//
// A block arriving over the wire carries transactions in RLP only
// (Block.DecodeRLP -> DecodeEthereumTransaction). The RLP form has no sender
// field, so tx.From() is nil for every transaction of an imported block. The
// per-transaction AsMessage call inside applyTransaction therefore falls
// through to transaction.Sender, i.e. a real secp256k1 recovery (~50us each),
// serialized on the single execution goroutine. On a block of ~14k transfers
// that is most of a second of pure signature math ahead of the EVM.
//
// The block producer never sees this cost: it takes transactions straight from
// the pool, where the sender was already derived and cached at admission time
// (txs_pool validateSender), which is why the miner's fillTx timer and the
// importer's exec timer disagree so wildly for the very same transactions.
//
// Recovering the whole block up front on a worker pool moves that work off the
// critical path without changing a single execution decision — see
// recoverBlockSenders for the equivalence argument.

// senderRecoveryMinTxs is the block size below which the goroutine setup is not
// worth it; those blocks recover inline in the execution loop as before.
const senderRecoveryMinTxs = 8

// senderRecoveryWorkers is the size of the recovery fan-out.
//
// Deliberately a fraction of NumCPU rather than all of it: a validator host
// commonly runs several nodes side by side, and block import is latency- not
// throughput-bound — saturating every core would just move the contention.
// Override with N42_SENDER_RECOVER_WORKERS (1 disables the fan-out).
var senderRecoveryWorkers = defaultSenderRecoveryWorkers()

func defaultSenderRecoveryWorkers() int {
	if v := os.Getenv("N42_SENDER_RECOVER_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	n := runtime.NumCPU() / 4
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return n
}

// recoverBlockSenders pre-populates the signature cache of every transaction in
// txs, using the exact signer the execution loop will use.
//
// Semantics are unchanged by construction:
//
//   - The only side effect is transaction.Sender's memo (tx.from, an
//     atomic.Value keyed by the deriving signer). Nothing else about the
//     transaction, the state or the block is touched.
//   - Recovery failures are dropped on purpose. transaction.Sender caches
//     successes only, so a transaction whose signature does not recover is left
//     exactly as it was found; the execution loop reaches it, calls AsMessage,
//     fails identically and aborts the block at the same index with the same
//     error.
//   - A cache entry can never be consumed by a different signer:
//     transaction.Sender re-checks sigCache.signer.Equal(signer) on every read
//     and re-derives on mismatch. Passing the wrong signer here could therefore
//     only cost time, never change a verdict.
//   - Transactions that already carry an explicit sender (tx.From() != nil,
//     e.g. blocks handed over straight from the local miner) are skipped, since
//     AsMessage short-circuits on that field before ever consulting the cache.
//
// Concurrency safety: each transaction is visited by exactly one worker, the
// memo is an atomic.Value, and the recovery path below it is read-only on the
// transaction (RawSignatureValues copies out, the Keccak hashers come from a
// sync.Pool). The underlying erigontech/secp256k1 binding is built for this —
// it pre-creates one libsecp256k1 context per CPU and its verify-only default
// context is safe to share across threads.
func recoverBlockSenders(signer transaction.Signer, txs []*transaction.Transaction) {
	if signer == nil || len(txs) < senderRecoveryMinTxs {
		return
	}
	workers := senderRecoveryWorkers
	if workers > len(txs) {
		workers = len(txs)
	}
	if workers < 2 {
		return
	}

	// Static striding: signature recovery costs the same for every transaction,
	// so a fixed stride balances as well as a work queue would while needing no
	// synchronisation at all beyond the final join.
	done := make(chan struct{}, workers)
	for w := 0; w < workers; w++ {
		go func(start int) {
			// A panic below would otherwise take down the process from a
			// goroutine the importer cannot see. Swallowing it here only means
			// this worker stops pre-warming; the execution loop still reaches
			// those transactions and reproduces whatever happened, on its own
			// stack, exactly as it would have without this optimisation.
			defer func() {
				_ = recover()
				done <- struct{}{}
			}()
			for i := start; i < len(txs); i += workers {
				tx := txs[i]
				if tx == nil || tx.From() != nil {
					continue
				}
				_, _ = transaction.Sender(signer, tx)
			}
		}(w)
	}
	for w := 0; w < workers; w++ {
		<-done
	}
}
