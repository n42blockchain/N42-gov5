// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// ErrDeferredParentNotApplied: the parent is stored but not (yet) this
// node's applied head, so its post-state cannot be read for the
// includability check; retry after the next import.
var ErrDeferredParentNotApplied = errors.New("deferred execution: parent is not the applied head yet")

// CheckDeferredBlock is the follower's vote check under deferred execution,
// run when a proposed block arrives and before it executes: the header's
// execution fields are this node's result of the parent, and the block's
// transactions are includable against that post-state (sender recovered,
// nonces contiguous from the state, worst-case cost within the balance,
// intrinsic gas within the gas limit, block gas within the header's
// limit) -- the rule that keeps execution from ever failing a committed
// block. Returns checked=false when the block is before the fork.
// ErrDeferredResultUnknown / ErrDeferredParentNotApplied ask the caller to
// retry once the parent is applied.
func (bc *BlockChain) CheckDeferredBlock(blk block.IBlock) (checked, retry bool, err error) {
	hdr, ok := blk.Header().(*block.Header)
	if !ok || bc.chainConfig == nil || !bc.chainConfig.IsDeferredExecution(hdr.Time) {
		return false, false, nil
	}
	number := hdr.Number.Uint64()
	if number == 0 {
		return false, false, nil
	}
	tx, err := bc.ChainDB.BeginRo(context.Background())
	if err != nil {
		return true, true, err
	}
	defer tx.Rollback()
	parent := rawdb.ReadHeader(tx, hdr.ParentHash, number-1)
	if parent == nil {
		return true, true, fmt.Errorf("%w: parent %x of block %d not stored", ErrDeferredResultUnknown, hdr.ParentHash[:8], number)
	}
	if err := checkDeferredHeader(bc.chainConfig, tx, hdr, parent); err != nil {
		return true, errors.Is(err, ErrDeferredResultUnknown), err
	}
	if !bc.AppliedHeadIsExactly(parent.Hash(), number-1) {
		return true, true, ErrDeferredParentNotApplied
	}
	return true, false, bc.checkIncludable(tx, hdr, blk.Transactions())
}

// checkIncludable verifies txs against the applied head's post-state (the
// parent of hdr).
func (bc *BlockChain) checkIncludable(tx kv.Tx, hdr *block.Header, txs []*transaction.Transaction) error {
	if len(txs) == 0 {
		return nil
	}
	number := hdr.Number.Uint64()
	signer := transaction.MakeSignerWithTimestamp(bc.chainConfig, hdr.Number.ToBig(), hdr.Time)
	if _, err := verifyBlockSendersHinted(signer, txs, bc.senderHints); err != nil {
		return fmt.Errorf("deferred execution: block %d senders: %w", number, err)
	}
	rules := bc.chainConfig.Rules(number)
	var blockGas uint64
	type senderTxs struct {
		first int
		txs   []*transaction.Transaction
	}
	bySender := make(map[types.Address]*senderTxs, len(txs)/8+1)
	var order []types.Address
	for i, t := range txs {
		from := t.From()
		if from == nil {
			return fmt.Errorf("deferred execution: block %d tx %d has no sender", number, i)
		}
		blockGas += t.Gas()
		if blockGas > hdr.GasLimit {
			return fmt.Errorf("deferred execution: block %d exceeds its gas limit at tx %d", number, i)
		}
		create := t.To() == nil
		value := t.Value()
		hasValue := value != nil && !value.IsZero()
		selfTransfer := !create && t.To() != nil && *t.To() == *from
		ig, err := IntrinsicGas(t.Data(), t.AccessList(), t.AuthList(), create, rules.IsHomestead, rules.IsIstanbul, rules.IsShanghai, rules.IsPrague, rules.IsGlamsterdam, hasValue, selfTransfer)
		if err != nil {
			return fmt.Errorf("deferred execution: block %d tx %d: %w", number, i, err)
		}
		if ig > t.Gas() {
			return fmt.Errorf("deferred execution: block %d tx %d: intrinsic gas %d exceeds gas limit %d", number, i, ig, t.Gas())
		}
		st := bySender[*from]
		if st == nil {
			st = &senderTxs{first: i}
			bySender[*from] = st
			order = append(order, *from)
		}
		st.txs = append(st.txs, t)
	}
	// Nonce and worst-case cost against the parent post-state, the senders
	// read across goroutines with their own store readers.
	workers := 16
	if workers > len(order) {
		workers = len(order)
	}
	if workers < 1 {
		workers = 1
	}
	mode := commitment.QMDBStateReadMode()
	useQMDB := mode != commitment.QMDBReadOff && bc.qmdbEnabled && bc.qmdbRootComputer != nil
	errs := make([]error, workers)
	per := (len(order) + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > len(order) {
			hi = len(order)
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			rtx, err := bc.ChainDB.BeginRo(context.Background())
			if err != nil {
				errs[w] = err
				return
			}
			defer rtx.Rollback()
			var reader state.StateReader = state.NewPlainStateReader(rtx)
			if useQMDB {
				reader = commitment.NewQMDBStateReader(commitment.NewLookupSourceLocked(bc.qmdbRootComputer, rtx), reader, mode)
			}
			cost := new(uint256.Int)
			gasCost := new(uint256.Int)
			for i := lo; i < hi; i++ {
				from := order[i]
				st := bySender[from]
				acc, err := reader.ReadAccountData(from)
				if err != nil {
					errs[w] = err
					return
				}
				var nonce uint64
				balance := new(uint256.Int)
				if acc != nil {
					nonce = acc.Nonce
					balance.Set(&acc.Balance)
				}
				cost.Clear()
				for _, t := range st.txs {
					if t.Nonce() != nonce {
						errs[w] = fmt.Errorf("deferred execution: block %d sender %x nonce %d, state expects %d", number, from[:4], t.Nonce(), nonce)
						return
					}
					nonce++
					feeCap := t.GasFeeCap()
					if feeCap == nil {
						feeCap = t.GasPrice()
					}
					gasCost.SetUint64(t.Gas())
					if feeCap != nil {
						gasCost.Mul(gasCost, feeCap)
					}
					cost.Add(cost, gasCost)
					if v := t.Value(); v != nil {
						cost.Add(cost, v)
					}
				}
				if cost.Gt(balance) {
					errs[w] = fmt.Errorf("deferred execution: block %d sender %x worst-case cost %s exceeds balance %s", number, from[:4], cost.String(), balance.String())
					return
				}
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
