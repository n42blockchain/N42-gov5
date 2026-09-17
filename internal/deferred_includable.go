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
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
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
// block. Returns checked=false when the block is before the fork; retry
// asks the caller to run the check again once the parent is applied.
func (bc *BlockChain) CheckDeferredBlock(blk block.IBlock) (checked, retry bool, err error) {
	hdr, ok := blk.Header().(*block.Header)
	if !ok || bc.chainConfig == nil || !bc.chainConfig.IsDeferredExecution(hdr.Time) {
		return false, false, nil
	}
	number := hdr.Number.Uint64()
	if number == 0 {
		return false, false, nil
	}
	// Already executed here (a catch-up range import or the gossip copy landed
	// first): the state the includability check would read is this block's own
	// post-state, where its transactions read as nonces already used. Nothing
	// to check -- a block this node imported has passed full validation, so the
	// vote may proceed (35zzq: a follower one block behind imported 13654279
	// from the range fetch, then the direct push arrived and the check failed
	// it, "nonce 0, state expects 4096", and the round was aborted).
	if bc.HasAppliedBlock(blk.Hash(), number) {
		return true, false, nil
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
	// Senders and the stateless rules first, outside the tree's readers lock.
	// A block decoded from the wire carries no sender -- the import's hint
	// pass sets From later -- so the check recovers them itself, through the
	// per-object memo and the process-wide sender cache (which the import
	// then hits), and never writes or reads the From field the import
	// writes: the check can run beside the import of a child without a race.
	plan, err := deferredTxPlan(bc.chainConfig, hdr, blk.Transactions())
	if err != nil {
		return true, false, err
	}
	// The applied-head check and the state reads below must see one state:
	// the tree's readers lock keeps the import from moving the applied head
	// (and the tree) between them.
	if bc.qmdbEnabled && bc.qmdbRootComputer != nil {
		unlock := bc.qmdbRootComputer.LockReaders()
		defer unlock()
	}
	if !bc.AppliedHeadIsExactly(parent.Hash(), number-1) {
		return true, true, ErrDeferredParentNotApplied
	}
	// The applied marker moves AFTER the tree takes a block's appends, so the
	// marker naming the parent is not proof that the tree is at the parent's
	// post-state: an import in flight can already have written its rows. Under
	// deferred execution the header carries that post-state (header N holds
	// N-1's root), so the tree's root is the exact test.
	if bc.qmdbEnabled && bc.qmdbRootComputer != nil {
		if got := bc.qmdbRootComputer.RootLocked(); got != hdr.Root {
			return true, true, fmt.Errorf("%w: the tree is at %x, the parent's post-state is %x",
				ErrDeferredParentNotApplied, got[:6], hdr.Root[:6])
		}
	}
	return true, false, bc.checkSenderStates(number, plan)
}

// deferredSender is one sender's transactions in block order.
type deferredSender struct {
	addr types.Address
	txs  []*transaction.Transaction
}

// deferredTxPlan recovers every transaction's sender (across goroutines),
// applies the rules that need no state -- block gas, blob transactions, fee
// cap against the base fee and the tip, intrinsic gas under the block
// timestamp's fork rules -- and groups the transactions by sender in first
// appearance order.
func deferredTxPlan(config *params.ChainConfig, hdr *block.Header, txs []*transaction.Transaction) ([]*deferredSender, error) {
	if len(txs) == 0 {
		return nil, nil
	}
	number := hdr.Number.Uint64()
	signer := transaction.MakeSignerWithTimestamp(config, hdr.Number.ToBig(), hdr.Time)
	senders := make([]types.Address, len(txs))
	var (
		errMu  sync.Mutex
		errAt  = -1
		errVal error
	)
	fail := func(i int, err error) {
		errMu.Lock()
		if errAt < 0 || i < errAt {
			errAt, errVal = i, err
		}
		errMu.Unlock()
	}
	workers := senderRecoveryFanout()
	if len(txs) < senderRecoveryMinTxs || workers < 2 {
		workers = 1
	}
	if workers > len(txs) {
		workers = len(txs)
	}
	chunk := (len(txs) + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*chunk, (w+1)*chunk
		if hi > len(txs) {
			hi = len(txs)
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				t := txs[i]
				if t == nil {
					fail(i, fmt.Errorf("deferred execution: block %d tx %d is nil", number, i))
					return
				}
				addr, err := transaction.Sender(signer, t)
				if err != nil {
					fail(i, fmt.Errorf("deferred execution: block %d tx %d sender: %w", number, i, err))
					return
				}
				senders[i] = addr
			}
		}(lo, hi)
	}
	wg.Wait()
	if errVal != nil {
		return nil, errVal
	}

	rules := config.RulesWithTimestamp(number, hdr.Time)
	baseFee := hdr.BaseFee
	var blockGas uint64
	bySender := make(map[types.Address]*deferredSender, len(txs)/8+1)
	var plan []*deferredSender
	for i, t := range txs {
		from := senders[i]
		blockGas += t.Gas()
		if blockGas > hdr.GasLimit {
			return nil, fmt.Errorf("deferred execution: block %d exceeds its gas limit at tx %d", number, i)
		}
		if len(t.BlobHashes()) > 0 {
			// Blob fees and their balance charge are not modelled here.
			return nil, fmt.Errorf("deferred execution: block %d tx %d: blob transactions are not includable under deferred execution", number, i)
		}
		feeCap, tip := t.GasFeeCap(), t.GasTipCap()
		if feeCap == nil {
			feeCap = t.GasPrice()
		}
		if tip == nil {
			tip = feeCap
		}
		if feeCap == nil {
			return nil, fmt.Errorf("deferred execution: block %d tx %d has no fee cap", number, i)
		}
		if err := CheckEip1559TxGasFeeCap(from, feeCap, tip, baseFee, false); err != nil {
			return nil, fmt.Errorf("deferred execution: block %d tx %d: %w", number, i, err)
		}
		create := t.To() == nil
		value := t.Value()
		hasValue := value != nil && !value.IsZero()
		selfTransfer := !create && *t.To() == from
		ig, err := IntrinsicGas(t.Data(), t.AccessList(), t.AuthList(), create, rules.IsHomestead, rules.IsIstanbul, rules.IsShanghai, rules.IsPrague, rules.IsGlamsterdam, hasValue, selfTransfer)
		if err != nil {
			return nil, fmt.Errorf("deferred execution: block %d tx %d: %w", number, i, err)
		}
		if ig > t.Gas() {
			return nil, fmt.Errorf("deferred execution: block %d tx %d: intrinsic gas %d exceeds gas limit %d", number, i, ig, t.Gas())
		}
		st := bySender[from]
		if st == nil {
			st = &deferredSender{addr: from}
			bySender[from] = st
			plan = append(plan, st)
		}
		st.txs = append(st.txs, t)
	}
	return plan, nil
}

// checkSenderStates verifies each sender's nonces and worst-case cost
// against the applied head's post-state (the parent), reading the senders
// across goroutines with their own read transactions. The caller holds the
// tree's readers lock.
func (bc *BlockChain) checkSenderStates(number uint64, plan []*deferredSender) error {
	if len(plan) == 0 {
		return nil
	}
	workers := 16
	if workers > len(plan) {
		workers = len(plan)
	}
	mode := commitment.QMDBStateReadMode()
	useQMDB := mode != commitment.QMDBReadOff && bc.qmdbEnabled && bc.qmdbRootComputer != nil
	errs := make([]error, workers)
	per := (len(plan) + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > len(plan) {
			hi = len(plan)
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
				st := plan[i]
				from := st.addr
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
					// EIP-3607: a sender with code cannot originate.
					if acc.CodeHash != (types.Hash{}) && acc.CodeHash != crypto.EmptyCodeHash {
						errs[w] = fmt.Errorf("deferred execution: block %d sender %x has code", number, from[:4])
						return
					}
				}
				cost.Clear()
				for _, t := range st.txs {
					if t.Nonce() != nonce {
						errs[w] = fmt.Errorf("deferred execution: block %d sender %x nonce %d, state expects %d", number, from[:4], t.Nonce(), nonce)
						return
					}
					if nonce == ^uint64(0) {
						errs[w] = fmt.Errorf("deferred execution: block %d sender %x nonce at its maximum", number, from[:4])
						return
					}
					nonce++
					feeCap := t.GasFeeCap()
					if feeCap == nil {
						feeCap = t.GasPrice()
					}
					gasCost.SetUint64(t.Gas())
					if feeCap != nil {
						if _, over := gasCost.MulOverflow(gasCost, feeCap); over {
							errs[w] = fmt.Errorf("deferred execution: block %d sender %x gas cost overflows", number, from[:4])
							return
						}
					}
					if _, over := cost.AddOverflow(cost, gasCost); over {
						errs[w] = fmt.Errorf("deferred execution: block %d sender %x cost overflows", number, from[:4])
						return
					}
					if v := t.Value(); v != nil {
						if _, over := cost.AddOverflow(cost, v); over {
							errs[w] = fmt.Errorf("deferred execution: block %d sender %x cost overflows", number, from[:4])
							return
						}
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
