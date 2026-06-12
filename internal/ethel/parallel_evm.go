// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// parallel_evm.go — real EVM integration for the Block-STM parallel
// executor. Wraps vm.EVM + IntraBlockState behind the state.ParallelEVM
// interface by reusing the existing internal.ApplyTransaction pipeline,
// swapping in MVStateReader + MVStateWriter as the state-access points.
//
// Design:
//   - One ParallelEVM instance per block (holds header + block context).
//   - Each Execute() call builds a fresh IntraBlockState with an
//     MVStateReader backed by the per-tx view. Writes flow through a
//     fresh MVStateWriter into the same view. No sharing between
//     concurrent tx executions.
//   - Each tx gets its own GasPool seeded with the full block gas
//     limit (parallel txs cannot share a gas counter; commit phase
//     sums per-tx GasUsed and enforces the block limit).
//   - Abort-retry: if the view observed an MVEstimate read, we return
//     state.AbortRetry() without flushing. Scheduler re-queues.

package ethel

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	internalpkg "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	vm2 "github.com/n42blockchain/N42/internal/vm"
)

// RealParallelEVM adapts n42's real EVM (vm.EVM + IntraBlockState +
// internal.ApplyTransaction) to the state.ParallelEVM interface so
// Block-STM workers can run mainnet-real transactions in parallel.
type RealParallelEVM struct {
	chainCfg      *params.ChainConfig
	engine        consensus.Engine
	cfg           vm2.Config
	header        *block.Header
	blockHashFunc func(uint64) types.Hash
	author        *types.Address

	// skipCoinbase, when non-zero, drops the per-tx coinbase balance write from
	// the MV write-set (lazy-coinbase): the coinbase credit is a COMMUTATIVE delta
	// (final balance = Σ tips, order-independent), so the per-tx write is a FALSE
	// conflict that serializes every tx in Block-STM. Dropping it removes that
	// universal conflict. NOTE: only sound when no tx observes the intermediate
	// coinbase balance (reads it / transacts with it) — for the timing benchmark
	// the result is discarded so correctness is moot; production lazy-coinbase
	// (P1-C) must aggregate the tip via FinalizeBlock + handle the rare read.
	skipCoinbase types.Address
}

// SetSkipCoinbase enables lazy-coinbase (see the field doc). Pass the block's
// coinbase address; zero disables.
func (e *RealParallelEVM) SetSkipCoinbase(addr types.Address) { e.skipCoinbase = addr }

// NewRealParallelEVM constructs an adapter for one block. Reusable
// across the block's txs; not safe to call Execute concurrently on
// the SAME adapter (each goroutine needs its own).
//
// Implementation note: the adapter itself is stateless after
// construction — all per-tx state is derived from the (tx, view)
// arguments of Execute. So sharing is technically safe, but for
// clarity we document it as per-worker.
func NewRealParallelEVM(
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	cfg vm2.Config,
	header *block.Header,
	blockHashFunc func(uint64) types.Hash,
	author *types.Address,
) *RealParallelEVM {
	return &RealParallelEVM{
		chainCfg:      chainCfg,
		engine:        engine,
		cfg:           cfg,
		header:        header,
		blockHashFunc: blockHashFunc,
		author:        author,
	}
}

// Execute satisfies state.ParallelEVM. Runs one transaction against
// the supplied per-tx view and returns the commit-phase summary.
func (e *RealParallelEVM) Execute(
	tx *transaction.Transaction,
	sender types.Address,
	view *state.EVMStateView,
	_ *state.BlockContext, // unused — we use the raw *block.Header we were constructed with
) (state.TxOutput, error) {
	// Propagate the pre-recovered sender only when it's non-zero.
	// Zero is the "no pre-recovery available" sentinel used by callers
	// that don't have a senderStore/senderFreezer configured. In that
	// case, skip SetFrom so AsMessage runs ecrecover via the signer —
	// matches ProcessBlock's "if precomputedSenders != nil { SetFrom }"
	// guard. Silently SetFrom(zero) here previously caused every tx to
	// execute as if sent from 0x0000...0000, corrupting state.
	if sender != (types.Address{}) {
		tx.SetFrom(sender)
	}

	reader := state.NewMVStateReader(view)
	var writer state.StateWriter = state.NewMVStateWriter(view)
	var cbw *coinbaseSkipWriter
	// Lazy-coinbase is sound ONLY when the tx's coinbase change is a pure tip
	// credit (commutative add). When the tx's sender IS the coinbase (an MEV
	// builder sending its own tx — common at 25M), the coinbase's update bundles
	// nonce++ + balance changes from sending + tip; those non-tip changes are
	// NOT commutative and MUST reach the MV so the builder's next tx sees the
	// correct nonce. Otherwise every builder tx after the first reads its own
	// stale nonce and fails "nonce too high" — exactly the convergence bug
	// VTRACE pinpointed (18.7k reads, 0 writes for builder coinbases). Enable
	// the skip ONLY when sender != coinbase.
	if e.skipCoinbase != (types.Address{}) && sender != e.skipCoinbase {
		cbw = &coinbaseSkipWriter{StateWriter: writer, coinbase: e.skipCoinbase}
		writer = cbw
	}

	// Fresh IntraBlockState per tx. The existing IntraBlockState
	// isn't thread-safe; one-per-tx avoids ANY shared-state races.
	// Cost: ~1 allocation per tx for the IBS struct. EVM + blockCtx
	// will also reallocate. Over ~200 tx/block this is ~200 allocs —
	// negligible compared to the per-tx EVM work.
	ibs := state.New(reader)

	// Observer guard: detect any tx that OBSERVES the coinbase balance during
	// execution (BALANCE/SELFBALANCE/CanTransfer via GetBalance, and the
	// EIP-161 Empty() check via stateObject.empty()). Under lazy-coinbase the
	// MV holds no coinbase entry for the deferred tips, so such a read sees a
	// stale (prefix-tip-missing) balance — the block must re-run non-lazy.
	//
	// The hook is installed for EVERY tx when lazy is enabled, NOT only those
	// with cbw: a sender==coinbase (MEV builder) tx is NOT skip-wrapped, but
	// it can still read the coinbase balance and see the same stale value
	// (other txs' tips were dropped from the MV). The finalize-time
	// balanceInc materialization goes through ReadAccountData (not GetBalance/
	// Empty), so an ordinary tip-only tx never trips the hook.
	var coinbaseObserved bool
	if e.skipCoinbase != (types.Address{}) {
		cb := e.skipCoinbase
		ibs.SetBalanceReadHook(func(addr types.Address) {
			if addr == cb {
				coinbaseObserved = true
			}
		})
	}

	// Set tx context on IBS (thash, bhash, txIndex) matching the
	// sequential ProcessBlock.Prepare() call. blockHash = header hash.
	// We don't have a canonical txIdx here — the scheduler knows it
	// but didn't pass it. Leave blank; AddLog records it as 0 which
	// is fine for parallel execution (the commit phase re-assigns
	// log indices in tx-order anyway).
	ibs.Prepare(tx.Hash(), e.header.Hash(), 0)

	// Per-tx gas pool: seed with FULL block gas limit. Parallel txs
	// cannot share a gas counter (races). Commit phase sums per-tx
	// GasUsed and verifies totalUsed ≤ header.GasLimit.
	gp := new(common.GasPool)
	gp.AddGas(e.header.GasLimit)

	var usedGas uint64
	receipt, _, err := internalpkg.ApplyTransaction(
		e.chainCfg,
		e.blockHashFunc,
		e.engine,
		e.author,
		gp,
		ibs,
		writer,
		e.header,
		tx,
		&usedGas,
		e.cfg,
	)

	// If any read during execution observed an MVEstimate, the
	// result is speculative-garbage — tell scheduler to retry.
	// Check AbortPending BEFORE surfacing err so a reader-hit-estimate
	// doesn't leak as a terminal tx error (and so a garbage run's
	// coinbase observation doesn't fail the block spuriously).
	if view.Inner().AbortPending() {
		return state.TxOutput{}, state.AbortRetry()
	}

	// Lazy-coinbase soundness gate: an observed coinbase balance (any tx,
	// including a sender==coinbase builder tx) or a non-commutative coinbase
	// mutation (skip-wrapped txs only) means the deferred Σtip model is wrong
	// for this BLOCK — surface the typed error so the caller re-runs without
	// lazy-coinbase.
	if e.skipCoinbase != (types.Address{}) && (coinbaseObserved || (cbw != nil && cbw.unsound)) {
		return state.TxOutput{Err: ErrCoinbaseObserved}, ErrCoinbaseObserved
	}

	if err != nil {
		// Terminal tx failure (not an abort-retry). Keep the partial
		// GasUsed but mark error; commit still builds a failed receipt.
		return state.TxOutput{
			GasUsed: usedGas,
			Status:  0,
			Err:     err,
		}, err
	}

	// Convert receipt logs → state.Log. state.Log's TxIndex/Index are
	// filled by the commit phase from the block-global log counter.
	var logs []state.Log
	if receipt != nil {
		for _, l := range receipt.Logs {
			topics := make([]types.Hash, len(l.Topics))
			copy(topics, l.Topics)
			data := make([]byte, len(l.Data))
			copy(data, l.Data)
			logs = append(logs, state.Log{
				Address: l.Address,
				Topics:  topics,
				Data:    data,
			})
		}
	}

	// Coinbase tip handling depends on the lazy-coinbase mode:
	//   - Default (skipCoinbase unset): the coinbase credit flowed through the
	//     normal MVStateWriter into the MV write-set (a regular account update),
	//     so we must NOT also return CoinbaseTip or FinalizeBlock would
	//     double-count. Every tx writes the coinbase → universal conflict.
	//   - Lazy (skipCoinbase set): coinbaseSkipWriter DROPPED the coinbase write
	//     from the MV write-set (removing the universal false conflict) and
	//     CAPTURED the exact credited delta. We return it as CoinbaseTip so
	//     FinalizeBlock aggregates Σtips into CoinbaseDelta and Apply credits the
	//     coinbase ONCE. (Sound when no tx observes the intermediate coinbase
	//     balance; a coinbase-reading tx needs prefix-sum materialization or a
	//     serial fallback at the caller — handled in the staged integration.)
	status := uint8(1)
	if receipt != nil {
		status = uint8(receipt.Status)
	}

	out := state.TxOutput{
		GasUsed: usedGas,
		Status:  status,
		Logs:    logs,
	}
	if cbw != nil && !cbw.tip.IsZero() {
		tip := cbw.tip
		out.CoinbaseTip = &tip
	}
	return out, nil
}

// Compile-time interface assertion.
var _ state.ParallelEVM = (*RealParallelEVM)(nil)

// coinbaseSkipWriter wraps a StateWriter and drops account writes for the
// coinbase address (lazy-coinbase). The coinbase receives only a balance credit
// via UpdateAccountData, so dropping it removes the coinbase write-set entry and
// the universal per-tx Block-STM conflict on that key. See RealParallelEVM.skipCoinbase.
// ErrCoinbaseObserved is the terminal tx error for a block that cannot run
// under lazy-coinbase: a tx OBSERVED the coinbase state in a way the deferred
// Σtip credit cannot reproduce — an explicit balance read (BALANCE /
// SELFBALANCE / CanTransfer on the coinbase would see the block-start value
// instead of the serial prefix value), or a non-tip coinbase mutation
// (nonce/code/incarnation change, balance decrease, account deletion) that is
// not a commutative credit. The caller must discard the parallel result and
// re-run the block with lazy-coinbase DISABLED (plain parallel is correct,
// just slower) or serially.
var ErrCoinbaseObserved = errors.New("lazy-coinbase: tx observed coinbase state; re-run block non-lazy")

type coinbaseSkipWriter struct {
	state.StateWriter
	coinbase types.Address
	tip      uint256.Int // captured coinbase balance delta (new-old) = this tx's tip
	unsound  bool        // coinbase mutation was NOT a pure balance credit
}

func (w *coinbaseSkipWriter) UpdateAccountData(address types.Address, original, acct *account.StateAccount) error {
	if address == w.coinbase {
		// Drop the write (no MV conflict) but CAPTURE the exact credited delta
		// (acct.Balance - original.Balance) — that's this tx's coinbase tip,
		// taken from the value ApplyTransaction actually computed (no formula
		// re-derivation, so it can't drift from the serial path).
		if acct != nil {
			// Lazy-coinbase models the update as a COMMUTATIVE balance add.
			// Anything else changing — nonce bump, code deployed AT the
			// coinbase address, incarnation — cannot be deferred: flag it so
			// Execute fails the block over to the non-lazy path.
			if original != nil &&
				(acct.Nonce != original.Nonce || acct.CodeHash != original.CodeHash) {
				w.unsound = true
			}
			if original == nil && acct.Nonce != 0 {
				w.unsound = true
			}
			newBal := acct.Balance
			var oldBal uint256.Int
			if original != nil {
				oldBal = original.Balance
			}
			switch newBal.Cmp(&oldBal) {
			case 1:
				var d uint256.Int
				d.Sub(&newBal, &oldBal)
				w.tip.Add(&w.tip, &d)
			case -1:
				// A balance DECREASE is not a tip credit (coinbase spending
				// without being the tx sender — e.g. coinbase-as-contract
				// pushing value out).
				w.unsound = true
			}
		}
		return nil
	}
	return w.StateWriter.UpdateAccountData(address, original, acct)
}

func (w *coinbaseSkipWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if address == w.coinbase {
		// Deleting the coinbase is not expressible as a deferred credit.
		w.unsound = true
		return nil
	}
	return w.StateWriter.DeleteAccount(address, original)
}
