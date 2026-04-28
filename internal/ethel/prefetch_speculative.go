// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// prefetch_speculative.go — reth-style "prewarm" cache populator.
//
// Walks block N+1's transactions while block N is executing and runs each
// tx through the EVM with a NoopWriter, so every account/storage read
// performed during speculative execution warms PlainStateBuffer's read
// cache. Unlike the static AccessList-based path in prefetch.go, this
// covers state reached via internal CALL, dynamically-computed SLOAD
// keys, and contract creation paths that the AL alone cannot describe.
//
// Speculation safety:
//   - Reads use prefetchStateReader, which skips PlainStateBuffer's active
//     write maps (mutated by the executor goroutine) and instead reads
//     InFlight snapshot → S3-FIFO LRU → MDBX RoTx. The S3-FIFO Put is
//     concurrency-safe.
//   - Writes go through state.NewNoopWriter, so SSTORE/balance changes
//     never touch the production buffer or MDBX. They DO accumulate in
//     the speculative IBS's stateObjects map for the duration of the
//     block — this is what lets later txs in the same block observe
//     state set by earlier ones.
//   - Speculative EVM has CheckNonce=false (`SetCheckNonce(false)` after
//     NormalizeExecutionMessage) and gasBailout=true (in ApplyMessage),
//     so stale state (MDBX commit interval behind real exec) doesn't
//     cause spurious failures.

package ethel

import (
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	iinternal "github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// SmallBlockTxThreshold is the per-block transaction count below which
// speculative prefetch is skipped — setup overhead exceeds benefit on
// short blocks. Matches reth's SMALL_BLOCK_TX_THRESHOLD (mod.rs:80).
const SmallBlockTxThreshold = 5

// prefetchStateReader is a goroutine-safe state.StateReader for use by
// the speculative prefetcher. It reads only from concurrency-safe
// tiers:
//
//  1. InFlight snapshot — atomic pointer, immutable once installed.
//  2. read LRU (S3-FIFO/byteLRU) — its Put/Get are mutex-protected.
//  3. MDBX via the supplied RoTx — independent from the executor's tx.
//
// On MDBX hit we publish into the LRU via *IfAbsent — never overwriting
// an existing entry. This is tombstone-safe: even if our pinned RoTx
// snapshot lags behind a commit + RefreshLRUForSnapshot tombstone, our
// stale value cannot displace whatever the canonical post-flush refresh
// just wrote. And for cold slots (LRU miss), we still get to fill them
// with the prewarmed value, which is the whole point of speculation.
type prefetchStateReader struct {
	buf   *state.PlainStateBuffer
	tx    kv.Tx
	epoch uint64 // captured BEFORE BeginRo; see Cache*IfAbsentEpoch.
}

func newPrefetchStateReader(buf *state.PlainStateBuffer, tx kv.Tx) *prefetchStateReader {
	// Test-friendly fallback: epoch=0 means "older than any wipe", so
	// Cache*IfAbsentEpoch falls back to the same conservative behavior
	// as PutIfAbsent without the wipe-stamp guard. Production callers
	// (runSpeculative) use newPrefetchStateReaderEpoch with the epoch
	// captured before BeginRo.
	return &prefetchStateReader{buf: buf, tx: tx, epoch: 0}
}

func newPrefetchStateReaderEpoch(buf *state.PlainStateBuffer, tx kv.Tx, epoch uint64) *prefetchStateReader {
	return &prefetchStateReader{buf: buf, tx: tx, epoch: epoch}
}

func (r *prefetchStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if snap := r.buf.InFlightSnapshot(); snap != nil {
		if enc, ok := snap.LookupAccount(address); ok {
			if len(enc) == 0 {
				return nil, nil
			}
			var a account.StateAccount
			if err := a.DecodeForStorage(enc); err != nil {
				return nil, err
			}
			return &a, nil
		}
	}
	if v, present := r.buf.LookupReadAccount(address); present {
		if len(v) == 0 {
			return nil, nil
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(v); err != nil {
			return nil, err
		}
		return &a, nil
	}
	enc, err := r.tx.GetOne(modules.Account, address[:])
	if err != nil {
		return nil, err
	}
	// Skip negative cache: our RoTx may pre-date a flush whose snap
	// created this account; caching nil here poisons the next-interval
	// bufReader read once active buf rotates out. Only cache positive
	// hits, which are tombstone-safe via PutIfAbsentEpoch.
	if len(enc) == 0 {
		return nil, nil
	}
	// CacheAccountIfAbsentEpoch's flush-epoch guard rejects the put if
	// any flush has stamped after r.epoch was captured — closes the
	// stale-RoTx race that hit block 2520771 pre-2026-04-28.
	r.buf.CacheAccountIfAbsentEpoch(address, enc, r.epoch)
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *prefetchStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	if snap := r.buf.InFlightSnapshot(); snap != nil {
		if v, ok := snap.LookupStorage(address, *key); ok {
			return v, nil
		}
	}
	if v, present := r.buf.LookupReadStorage(address, *key); present {
		if len(v) == 0 {
			return nil, nil
		}
		return v, nil
	}
	compositeKey := modules.PlainGenerateCompositeStorageKey(address[:], key[:])
	enc, err := r.tx.GetOne(modules.Storage, compositeKey)
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		// Skip negative cache — same reasoning as ReadAccountData.
		return nil, nil
	}
	cached := make([]byte, len(enc))
	copy(cached, enc)
	r.buf.CacheStorageIfAbsentEpoch(address, *key, cached, r.epoch)
	return cached, nil
}

func (r *prefetchStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if v, present := r.buf.LookupReadCode(codeHash); present {
		return v, nil
	}
	code, err := r.tx.GetOne(modules.Code, codeHash[:])
	if err != nil {
		return nil, err
	}
	if len(code) == 0 {
		return nil, nil
	}
	cached := make([]byte, len(code))
	copy(cached, code)
	// Tombstone-safe LRU populate — see type doc.
	r.buf.CacheCodeIfAbsent(codeHash, cached)
	return cached, nil
}

func (r *prefetchStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	return len(code), err
}

// runSpeculative executes block N's transactions with a NoopWriter so
// every read warms the cache. Returns counts for diagnostics. Bails out
// early if the executor catches up (currentBlockNum >= blockNum).
func (p *prefetcher) runSpeculative(blockNum uint64, header *block.Header, body *GethBodyResult, senders []types.Address) (attempted, succeeded int) {
	if p.engine == nil || p.currentBlockNum == nil {
		return 0, 0
	}
	if p.cancelled(blockNum) {
		return 0, 0
	}

	// Capture the flush epoch BEFORE BeginRo so the reader's
	// Cache*IfAbsentEpoch calls reject any value for an address wiped
	// by a flush that landed between this capture and our writes — the
	// LRU prefix-sweep race that PutIfAbsent alone cannot block.
	prefetcherEpoch := p.stateBuf.CurrentFlushEpoch()
	roTx, err := p.db.BeginRo(p.ctx)
	if err != nil {
		return 0, 0
	}
	defer roTx.Rollback()

	reader := newPrefetchStateReaderEpoch(p.stateBuf, roTx, prefetcherEpoch)
	ibs := state.New(reader)
	noopWriter := state.NewNoopWriter()

	gp := new(common.GasPool)
	gp.AddGas(header.GasLimit)

	rules := p.chainCfg.Rules(header.Number.Uint64())
	signer := transaction.MakeSignerWithTimestamp(p.chainCfg, header.Number.ToBig(), header.Time)
	blockHash := header.Hash()
	blockHashFunc := func(uint64) types.Hash { return types.Hash{} }
	var usedGas uint64

	// EIP-4788 (parent beacon root) + EIP-2935 (history contract) +
	// Prague system contracts. Speculative call lets the EVM touch the
	// fixed system-contract slots so the LRU is hot when real exec hits
	// them. Errors here are tolerated — they don't affect tx speculation
	// below; real exec retries under correct preconditions.
	_ = iinternal.ProcessExecutionBlockStart(header.ParentBeaconRoot, p.chainCfg, ibs, header, p.engine)

	for i, tx := range body.Transactions {
		if p.cancelled(blockNum) {
			return attempted, succeeded
		}
		attempted++

		if i < len(senders) {
			tx.SetFrom(senders[i])
		}
		ibs.Prepare(tx.Hash(), blockHash, i)
		if err := p.applySpeculativeTx(ibs, header, tx, signer, blockHashFunc, gp, rules, noopWriter, &usedGas); err != nil {
			// Speculative failure: discard IBS to avoid carrying partial
			// dirty state into the next tx. Reads done before the failure
			// already warmed the cache, which is the goal.
			ibs = state.New(reader)
			gp = new(common.GasPool)
			gp.AddGas(header.GasLimit)
			usedGas = 0
			continue
		}
		succeeded++
	}

	// EIP-4895 validator withdrawals. AddBalance for an untouched account
	// only journals an increment (no state read), but for accounts already
	// promoted by tx execution the cache hit is already there. The win is
	// modest — included for parity with real exec semantics so the IBS
	// state mirrors what the executor will see.
	if !p.cancelled(blockNum) {
		applyEthelWithdrawals(p.chainCfg, header, ibs, body.Withdrawals)
	}
	return attempted, succeeded
}

// applySpeculativeTx mirrors internal.applyTransaction but with
// CheckNonce=false (set after NormalizeExecutionMessage flips it back to
// true) and gasBailout=true. Side effects go to noopWriter.
func (p *prefetcher) applySpeculativeTx(
	ibs *state.IntraBlockState,
	header *block.Header,
	tx *transaction.Transaction,
	signer transaction.Signer,
	blockHashFunc func(uint64) types.Hash,
	gp *common.GasPool,
	rules *params.Rules,
	noopWriter state.StateWriter,
	usedGas *uint64,
) error {
	msg, err := tx.AsMessage(signer, header.BaseFee)
	if err != nil {
		return err
	}
	iinternal.NormalizeExecutionMessage(&msg, false, p.engine != nil)
	msg.SetCheckNonce(false)

	txContext := iinternal.NewEVMTxContext(msg)
	blockContext := iinternal.NewEVMBlockContext(header, blockHashFunc, p.engine, p.chainCfg, nil)
	evm := vm2.NewEVM(blockContext, txContext, ibs, p.chainCfg, vm2.Config{NoReceipts: true})

	result, err := iinternal.ApplyMessage(evm, msg, gp, true, true)
	if err != nil {
		return err
	}
	if err := ibs.FinalizeTx(rules, noopWriter); err != nil {
		return err
	}
	*usedGas += result.UsedGas
	return nil
}

// cancelled returns true if the executor has caught up to or past the
// block we're prefetching.
func (p *prefetcher) cancelled(prefetchingBlockNum uint64) bool {
	if p.currentBlockNum == nil {
		return false
	}
	return p.currentBlockNum.Load() >= prefetchingBlockNum
}
