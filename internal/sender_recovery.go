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
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"

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

// senderRecoveryWorkerOverride is N42_SENDER_RECOVER_WORKERS, 0 when unset
// (1 disables the fan-out).
var senderRecoveryWorkerOverride = envSenderRecoveryWorkers()

func envSenderRecoveryWorkers() int {
	if v := os.Getenv("N42_SENDER_RECOVER_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return 0
}

// senderRecoveryFanout sizes the recovery pool for THIS node.
//
// Two things it must not do, both of which the previous fixed
// `min(NumCPU/4, 8)` did:
//
//   - Read NumCPU. A validator host commonly runs several nodes side by side,
//     and `--pprof.maxcpu` is how the operator states this node's share of it.
//     NumCPU ignores that and reports the whole machine.
//   - Be captured in a package var. GOMAXPROCS is applied from that flag
//     during startup, well after package init, so a value computed at init is
//     always the machine's, never the node's.
//
// And the ceiling of 8 made the fan-out a constant on any host bigger than 32
// threads. Measured on the 7-node 480M rig (22,857 transactions per block,
// ~50 us per secp256k1 recovery = 1.14 core-seconds of work): the importer's
// `recov` phase sat at 137-149 ms, which is that 1.14 s over exactly 8
// workers, while the node had a 37-thread budget and was using 0.7 of a core.
//
// Recovery is a barrier ahead of execution — none of this node's other
// critical-path work runs during it — so it takes most of the budget, keeping
// a quarter back for gossip, consensus and RPC.
func senderRecoveryFanout() int {
	if senderRecoveryWorkerOverride > 0 {
		return senderRecoveryWorkerOverride
	}
	n := runtime.GOMAXPROCS(0)
	if n > 2 {
		n -= n / 4
	}
	if n < 2 {
		return 2
	}
	return n
}

// applySenderHints copies pool-recovered senders onto wire-decoded transactions
// ahead of recoverBlockSenders, returning how many it filled.
//
// Equivalence: hints.GetTx is keyed by transaction hash, and two transactions
// with the same hash are the same signature bytes, so Sender(signer, poolCopy)
// derives exactly what Sender(signer, wireCopy) would — while hitting the pool
// copy's memo instead of redoing the curve math. The signer passed here is the
// block's own signer; Sender re-checks a memo's signer before trusting it, so
// a pool memo made under a different fork's rules re-derives rather than
// leaks. A nil source, a miss, or a recovery error just leaves the transaction
// for the worker-pool pass.
func applySenderHints(hints SenderHintSource, signer transaction.Signer, txs []*transaction.Transaction) int {
	if hints == nil || signer == nil {
		return 0
	}
	filled := 0
	for _, tx := range txs {
		if tx == nil || tx.From() != nil {
			continue
		}
		ptx := hints.GetTx(tx.Hash())
		if ptx == nil {
			continue
		}
		if addr, err := transaction.Sender(signer, ptx); err == nil {
			tx.SetFrom(addr)
			filled++
		}
	}
	return filled
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
	workers := senderRecoveryFanout()
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

// verifyBlockSenders enforces that every wire-declared sender matches the
// transaction's signature. It is a CONSENSUS-SAFETY gate, not an
// optimization: the native tx codec carries `From` on the wire and the
// TxRoot commits to it (Transactions.EncodeIndex marshals the full
// record), but the tx hash covers only the signed fields — so `From` is
// not bound to the signature by the hash. On import, AsMessage trusts a
// non-nil `From` and skips recovery; without this check a byzantine
// leader could stamp `From = victim` on an unsigned/foreign-signed tx,
// have every follower execute it identically (roots agree, QC forms) and
// drain any account with a single faulty leader — collapsing the ≤f
// fault model to "all leaders honest" for fund safety.
//
// For each tx carrying `From`, recover the signer from V/R/S (bypassing
// the cached From via signer.Sender, but reusing the process-wide
// senderCache for pool-seen txs so honest blocks stay fast) and compare.
// Any mismatch or unrecoverable signature rejects the whole block.
func verifyBlockSenders(signer transaction.Signer, txs []*transaction.Transaction) error {
	if signer == nil {
		return nil
	}
	type mismatch struct {
		idx int
		err error
	}
	var (
		found []mismatch
		mu    sync.Mutex
	)
	report := func(i int, err error) {
		mu.Lock()
		found = append(found, mismatch{idx: i, err: err})
		mu.Unlock()
	}
	check := func(i int) {
		tx := txs[i]
		if tx == nil {
			return
		}
		declared := tx.From()
		if declared == nil {
			// No wire From: the execution loop recovers and trusts the
			// signature directly — nothing to forge.
			return
		}
		// A declared sender with no recoverable signature material cannot be
		// checked, and that is precisely what this gate exists to refuse —
		// trusting it would reopen the hole. Guard explicitly: the signer
		// dereferences V/R/S without a nil check and would PANIC here, which
		// on the non-worker path below escapes Process and kills the node.
		// (Legacy records whose signature lives in the unverifiable `Sign`
		// blob land here too; they are rejected with a named cause rather
		// than silently trusted.)
		if v, r, sv := tx.RawSignatureValues(); v == nil || r == nil || sv == nil {
			report(i, fmt.Errorf("tx %d declares sender %s but carries no signature values (V/R/S)", i, declared.Hex()))
			return
		}
		recovered, err := transaction.RecoverSenderFromSig(signer, tx)
		if err != nil {
			report(i, fmt.Errorf("tx %d declares sender %s but signature does not recover: %w", i, declared.Hex(), err))
			return
		}
		if recovered != *declared {
			report(i, fmt.Errorf("tx %d declares sender %s but signature recovers %s", i, declared.Hex(), recovered.Hex()))
		}
	}

	workers := senderRecoveryFanout()
	if len(txs) < senderRecoveryMinTxs || workers > len(txs) {
		workers = 1
	}
	if workers < 2 {
		// Same panic containment as the worker branch below: a gate must be
		// able to reject a block, never to take the process down with it.
		func() {
			defer func() {
				if r := recover(); r != nil {
					report(0, fmt.Errorf("sender verify panic: %v", r))
				}
			}()
			for i := range txs {
				check(i)
			}
		}()
	} else {
		done := make(chan struct{}, workers)
		for w := 0; w < workers; w++ {
			go func(start int) {
				cur := start
				defer func() {
					if r := recover(); r != nil {
						// cur, not start: reporting the stripe origin made
						// every panic look like tx 0 and hid the offender.
						report(cur, fmt.Errorf("sender verify panic: %v", r))
					}
					done <- struct{}{}
				}()
				for i := start; i < len(txs); i += workers {
					cur = i
					check(i)
				}
			}(w)
		}
		for w := 0; w < workers; w++ {
			<-done
		}
	}
	if len(found) == 0 {
		return nil
	}
	// Deterministic error: report the lowest-index offender.
	best := found[0]
	for _, m := range found[1:] {
		if m.idx < best.idx {
			best = m
		}
	}
	return best.err
}
