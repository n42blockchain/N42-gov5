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
// TxsPool: main N42 transaction pool. Orchestrates validation,
// per-account sorted lists, promotion between queued and pending
// sub-pools, replay-on-reorg handling and event broadcasts. Exposes
// Add/AddLocal, Pending, ContentFrom and Stop lifecycle APIs used by
// the miner, JSON-RPC and P2P fanout paths.

package txspool

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/contracts/deposit"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/tracing"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
)

// NewTxsPool creates a new transaction pool to gather, sort and filter inbound
// transactions from the network.
func NewTxsPool(ctx context.Context, bc common.IBlockChain, depositContract *deposit.Deposit) (common.ITxsPool, error) {
	c, cancel := context.WithCancel(ctx)

	pool := &TxsPool{
		chainconfig: bc.Config(),
		config:      DefaultTxPoolConfig,
		ctx:         c,
		cancel:      cancel,
		bc:          bc,
		deposit:     depositContract,
		locals:      newAccountSet(),
		pending:     make(map[types.Address]*txsList),
		queue:       make(map[types.Address]*txsList),
		beats:       make(map[types.Address]time.Time),
		all:         newTxLookup(),

		reqResetCh:     make(chan *txspoolResetRequest),
		reqPromoteCh:   make(chan *accountSet),
		queueTxEventCh: make(chan *transaction.Transaction),
		reorgDoneCh:    make(chan chan struct{}),
		gasPrice:       uint256.NewInt(DefaultTxPoolConfig.PriceLimit),
	}

	pool.currentState = StateClient(ctx, bc.DB())
	pool.pendingNonces = newTxNoncer(pool.currentState)
	pool.priced = newTxPricedList(pool.all)
	pool.reset(nil, bc.CurrentBlock())

	// Initialize effective slots to configured maximum.
	pool.effectiveGlobalSlots.Store(pool.config.GlobalSlots)

	// Set before the goroutine starts, not inside it: the flag means "a drainer
	// for reqPromoteCh exists", and the unbuffered send simply waits for the
	// goroutine to be scheduled. Setting it from inside would race with the
	// restore below and skip a legitimate one.
	pool.schedulerLive.Store(true)
	pool.wg.Add(1)
	go pool.scheduleLoop()

	pool.wg.Add(1)
	go pool.blockChangeLoop()

	// Start dynamic sizing monitor if enabled.
	if pool.config.DynamicSizing {
		pool.wg.Add(1)
		go pool.dynamicSizingLoop()
		log.Info("TxPool dynamic sizing enabled",
			"maxSlots", pool.config.GlobalSlots,
			"minSlots", pool.config.MinGlobalSlots,
			"memoryLimitMB", pool.config.MemoryLimitMB)
	}

	// Restore the persisted journal only AFTER scheduleLoop is running. The
	// restore goes through addTxs, which ends in requestPromoteExecutables ->
	// an unbuffered send on reqPromoteCh; nothing reads that channel until
	// scheduleLoop exists. Restoring first therefore blocked NewTxsPool
	// forever the moment the journal was non-empty — a latent wedge that only
	// surfaced once the shutdown flush started producing non-empty journals.
	if loaded := pool.loadFromDB(); loaded > 0 {
		log.Info("Restored persisted transactions", "count", loaded)
	}

	return pool, nil
}

// Stop terminates the transaction pool.
// Persists local and pending transactions to DB before shutdown.
func (pool *TxsPool) Stop() error {
	if err := pool.flushToDB(); err != nil {
		log.Warn("Failed to persist transaction pool", "err", err)
	}
	pool.cancel()
	pool.wg.Wait()
	log.Info("Transaction pool stopped")
	return nil
}

// --- Public entry points ---

// AddLocals enqueues a batch of transactions into the pool if they are valid,
// marking the senders as local ones.
func (pool *TxsPool) AddLocals(txs []*transaction.Transaction) []error {
	return pool.addTxs(txs, !pool.config.NoLocals, true)
}

// AddLocal enqueues a single local transaction into the pool.
func (pool *TxsPool) AddLocal(tx *transaction.Transaction) error {
	return pool.AddLocals([]*transaction.Transaction{tx})[0]
}

// AddRemotes enqueues a batch of transactions into the pool if they are valid.
func (pool *TxsPool) AddRemotes(txs []*transaction.Transaction) []error {
	return pool.addTxs(txs, false, false)
}

// Has returns whether the pool has a transaction cached with the given hash.
func (pool *TxsPool) Has(hash types.Hash) bool {
	return pool.all.Get(hash) != nil
}

// GetTx returns a specific transaction from the pool.
func (pool *TxsPool) GetTx(hash types.Hash) *transaction.Transaction {
	return pool.all.Get(hash)
}

// GetTransaction retrieves all pending transactions as a flat list.
func (pool *TxsPool) GetTransaction() (txs []*transaction.Transaction, err error) {
	pending := pool.Pending(false)
	heads := make([]*transaction.Transaction, 0, len(pending))
	for _, accTxs := range pending {
		heads = append(heads, accTxs...)
	}
	return heads, nil
}

// Pending retrieves all currently processable transactions, grouped by origin
// account and sorted by nonce.
func (pool *TxsPool) Pending(enforceTips bool) map[types.Address][]*transaction.Transaction {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pending := make(map[types.Address][]*transaction.Transaction)
	for addr, list := range pool.pending {
		txs := list.FlattenReadOnly()

		if enforceTips && !pool.locals.contains(addr) {
			for i, tx := range txs {
				if tx.EffectiveGasTipIntCmp(pool.gasPrice, pool.priced.urgent.baseFee) < 0 {
					txs = txs[:i]
					break
				}
			}
		}
		if len(txs) > 0 {
			pending[addr] = txs
		}
	}
	return pending
}

// Content retrieves the pending and queued transactions grouped by account.
func (pool *TxsPool) Content() (map[types.Address][]*transaction.Transaction, map[types.Address][]*transaction.Transaction) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pending := make(map[types.Address][]*transaction.Transaction)
	for addr, list := range pool.pending {
		pending[addr] = list.Flatten()
	}
	queued := make(map[types.Address][]*transaction.Transaction)
	for addr, list := range pool.queue {
		queued[addr] = list.Flatten()
	}
	return pending, queued
}

// Nonce returns the next expected nonce for the given address.
//
// This is a read-only query reachable from RPC (eth_getTransactionCount with
// the "pending" tag) with caller-supplied addresses, so it uses getReadOnly to
// avoid caching unknown addresses — otherwise a flood of queries for random
// addresses would grow the pending-nonce map until the next pool reset.
func (pool *TxsPool) Nonce(addr types.Address) uint64 {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.pendingNonces.getReadOnly(addr)
}

// Stats returns counts of pending/queued addresses and transactions.
func (pool *TxsPool) Stats() (int, int, int, int) {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.statsLocked()
}

// statsLocked returns stats. Caller must hold at least pool.mu.RLock().
func (pool *TxsPool) statsLocked() (int, int, int, int) {
	pendingTxs := 0
	pendingAddresses := len(pool.pending)
	for _, list := range pool.pending {
		pendingTxs += list.Len()
	}
	queuedTxs := 0
	queuedAddresses := len(pool.queue)
	for _, list := range pool.queue {
		queuedTxs += list.Len()
	}
	return pendingAddresses, pendingTxs, queuedAddresses, queuedTxs
}

// StatsPrint logs the current pool stats at debug level.
func (pool *TxsPool) StatsPrint() {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	pendingAddresses, pendingTxs, queuedAddresses, queuedTxs := pool.statsLocked()
	log.Debugf("txs pool: pendingAddresses count: %d pendingTxs count: %d", pendingAddresses, pendingTxs)
	log.Debugf("txs pool: queuedAddresses count: %d queuedTxs count: %d", queuedAddresses, queuedTxs)
}

// ResetState is a no-op placeholder for state reset by block hash.
func (pool *TxsPool) ResetState(blockHash types.Hash) error {
	return nil
}

// --- Internal transaction add/remove ---

// addTxs attempts to queue a batch of transactions if they are valid.
func (pool *TxsPool) addTxs(txs []*transaction.Transaction, local, sync bool) []error {
	// OpenTelemetry: trace transaction ingestion.
	txTracer := tracing.Tracer("txpool")
	_, span := tracing.StartSpan(pool.ctx, txTracer, "txpool.addTxs")
	span.SetAttributes(
		tracing.Int64Attr("txpool.batch_size", int64(len(txs))),
	)
	defer span.End()

	var (
		errs = make([]error, len(txs))
		news = make([]*transaction.Transaction, 0, len(txs))
	)
	for i, tx := range txs {
		hash := tx.Hash()
		if pool.all.Get(hash) != nil {
			errs[i] = ErrAlreadyKnown
			continue
		}
		if !pool.validateSender(tx) {
			errs[i] = ErrInvalidSender
			continue
		}
		news = append(news, tx)
	}
	if len(news) == 0 {
		return errs
	}

	pool.mu.Lock()
	newErrs, dirtyAddrs := pool.addTxsLocked(news, local)
	pool.mu.Unlock()

	nilSlot := 0
	for _, err := range newErrs {
		for errs[nilSlot] != nil {
			nilSlot++
		}
		errs[nilSlot] = err
		nilSlot++
	}
	if local {
		var localTxs []*transaction.Transaction
		for i, err := range errs {
			if err == nil {
				localTxs = append(localTxs, txs[i])
			}
		}
		event.GlobalEvent.Send(common.NewLocalTxsEvent{Txs: localTxs})
	}

	done := pool.requestPromoteExecutables(dirtyAddrs)
	if sync {
		<-done
	}
	return errs
}

// addTxsLocked attempts to queue a batch of transactions if they are valid.
// The transaction pool lock must be held.
func (pool *TxsPool) addTxsLocked(txs []*transaction.Transaction, local bool) ([]error, *accountSet) {
	dirty := newAccountSet()
	errs := make([]error, len(txs))
	for i, tx := range txs {
		replaced, err := pool.add(tx, local)
		errs[i] = err
		if err == nil && !replaced {
			dirty.addTx(tx)
			txpoolAddedTotal.Inc()
		} else if err != nil {
			txpoolRejectedTotal.Inc()
			switch {
			case errors.Is(err, ErrUnderpriced) || errors.Is(err, ErrReplaceUnderpriced):
				txpoolUnderpricedTotal.Inc()
			case errors.Is(err, ErrTxPoolOverflow):
				txpoolOverflowTotal.Inc()
			}
		}
	}
	return errs, dirty
}

// add validates a transaction and inserts it into the non-executable queue for later
// pending promotion and execution.
func (pool *TxsPool) add(tx *transaction.Transaction, local bool) (replaced bool, err error) {
	hash := tx.Hash()
	gasPrice := tx.GasPrice()

	if pool.all.Get(hash) != nil {
		log.Debug("Discarding already known transaction", "hash", hash)
		return false, ErrAlreadyKnown
	}

	isLocal := local || pool.locals.containsTx(tx)

	if err := pool.validateTx(tx, isLocal); err != nil {
		return false, err
	}

	// If the transaction pool is full, discard underpriced transactions.
	// Use effective slots (may be reduced under memory pressure).
	effectiveSlots := pool.EffectiveGlobalSlots()
	if uint64(pool.all.Slots()+numSlots(tx)) > effectiveSlots+pool.config.GlobalQueue {
		if !isLocal && pool.priced.Underpriced(tx) {
			log.Debug("Discarding underpriced transaction", "hash", hash, "gasTipCap", gasPrice, "gasFeeCap", gasPrice)
			return false, ErrUnderpriced
		}
		if pool.changesSinceReorg > int(effectiveSlots/4) {
			return false, ErrTxPoolOverflow
		}

		drop, success := pool.priced.Discard(pool.all.Slots()-int(effectiveSlots+pool.config.GlobalQueue)+numSlots(tx), isLocal)
		if !isLocal && !success {
			log.Debug("Discarding overflown transaction", "hash", hash)
			return false, ErrTxPoolOverflow
		}
		pool.changesSinceReorg += len(drop)
		for _, tx := range drop {
			log.Debug("Discarding freshly underpriced transaction", "hash", hash, "gasTipCap", gasPrice, "gasFeeCap", gasPrice)
			pool.removeTx(tx.Hash(), false)
		}
	}

	// Try to replace an existing transaction in the pending pool
	fromPtr := tx.From()
	if fromPtr == nil {
		return false, errors.New("transaction sender is nil")
	}
	from := *fromPtr

	if list := pool.pending[from]; list != nil && list.Overlaps(tx) {
		inserted, old := list.Add(tx, pool.config.PriceBump)
		if !inserted {
			return false, ErrReplaceUnderpriced
		}
		if old != nil {
			pool.all.Remove(old.Hash())
			pool.priced.Removed(1)
		}
		pool.all.Add(tx, isLocal)
		pool.priced.Put(tx, isLocal)
		pool.queueTxEvent(tx)
		log.Debug("Pooled new executable transaction", "hash", hash, "from", from, "to", tx.To)
		pool.beats[from] = time.Now()
		return old != nil, nil
	}

	// New transaction isn't replacing a pending one, push into queue
	replaced, err = pool.enqueueTx(hash, tx, isLocal, true)
	if err != nil {
		return false, err
	}
	if local && !pool.locals.contains(from) {
		log.Infof("Setting new local account address %v", from)
		pool.locals.add(from)
		pool.priced.Removed(pool.all.RemoteToLocals(pool.locals))
	}
	if isLocal {
		localGauge.Inc()
	}
	return replaced, nil
}

// removeTx removes a single transaction from the queue, moving all subsequent
// transactions back to the future queue.
func (pool *TxsPool) removeTx(hash types.Hash, outofbound bool) {
	tx := pool.all.Get(hash)
	if tx == nil {
		return
	}
	txpoolDroppedTotal.Inc()
	from := tx.From()
	if from == nil {
		return
	}
	addr := *from

	pool.all.Remove(hash)
	if outofbound {
		pool.priced.Removed(1)
	}
	if pool.locals.contains(addr) {
		localGauge.Dec()
	}

	// Remove from pending lists and reset account nonce
	if pending := pool.pending[addr]; pending != nil {
		if removed, invalids := pending.Remove(tx); removed {
			if pending.Empty() {
				delete(pool.pending, addr)
			}
			for _, tx := range invalids {
				pool.enqueueTx(tx.Hash(), tx, false, false)
			}
			pool.pendingNonces.setIfLower(addr, tx.Nonce())
			pendingGauge.Add(-(1 + len(invalids)))
			return
		}
	}

	// Transaction is in the future queue
	if future := pool.queue[addr]; future != nil {
		if removed, _ := future.Remove(tx); removed {
			queuedGauge.Dec()
		}
		if future.Empty() {
			delete(pool.queue, addr)
			delete(pool.beats, addr)
		}
	}
}

// --- Validation ---

// validateTx checks whether a transaction is valid according to the consensus
// rules and adheres to some heuristic limits of the local node (price and size).
func (pool *TxsPool) validateTx(tx *transaction.Transaction, local bool) error {
	// Accept only legacy transactions until EIP-2718/2930 activates.
	if !pool.eip2718 && tx.Type() != transaction.LegacyTxType {
		return internal.ErrTxTypeNotSupported
	}
	// Reject dynamic fee transactions until EIP-1559 activates.
	if !pool.eip1559 && tx.Type() == transaction.DynamicFeeTxType {
		return internal.ErrTxTypeNotSupported
	}
	gasPrice := tx.GasPrice()

	fromPtr := tx.From()
	if fromPtr == nil {
		return ErrInvalidSender
	}
	addr := *fromPtr

	txData, err := tx.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal transaction: %w", err)
	}
	if uint64(len(txData)) > txMaxSize {
		return ErrOversizedData
	}
	if tx.Value().Sign() < 0 {
		return ErrNegativeValue
	}
	if pool.currentMaxGas < tx.Gas() {
		return ErrGasLimit
	}
	if gasPrice.BitLen() > 256 {
		return ErrFeeCapVeryHigh
	}
	if tx.GasTipCap().BitLen() > 256 {
		return internal.ErrTipVeryHigh
	}
	if tx.GasFeeCapIntCmp(tx.GasTipCap()) < 0 {
		return ErrTipAboveFeeCap
	}

	if !local && gasPrice.Cmp(pool.gasPrice) < 0 {
		return ErrUnderpriced
	}
	if pool.currentState.GetNonce(addr) > tx.Nonce() {
		return ErrNonceTooLow
	}
	const MaxNonce uint64 = 1<<63 - 1
	if tx.Nonce() > MaxNonce {
		return ErrNonceTooHigh
	}
	if pool.currentState.GetBalance(addr).Cmp(tx.Cost()) < 0 {
		return ErrInsufficientFunds
	}

	current := pool.bc.CurrentBlock()
	isPrague := pool.chainconfig != nil && current != nil && pool.chainconfig.IsPrague(current.Time())
	isShanghai := pool.chainconfig != nil && current != nil && current.Number64() != nil && pool.chainconfig.IsShanghai(current.Number64().Uint64()+1)
	isGlamsterdam := pool.chainconfig != nil && current != nil && pool.chainconfig.IsGlamsterdam(current.Time())
	intrGas, err := internal.IntrinsicGas(tx.Data(), tx.AccessList(), tx.AuthList(), tx.To() == nil, true, pool.istanbul, isShanghai, isPrague, isGlamsterdam)
	if err != nil {
		return err
	}
	if tx.Gas() < intrGas {
		return internal.ErrIntrinsicGas
	}

	// SetCode (EIP-7702) transactions are only valid from Prague, must target an
	// address (no contract creation) and must carry at least one authorization.
	// The block/engine validator already enforces this; rejecting here too keeps
	// invalid SetCode txs out of the pool entirely (mirrors go-ethereum's txpool
	// validation, geth #35094 / legacypool).
	if err := validateSetCodeStructure(tx, isPrague); err != nil {
		return err
	}

	// EIP-7702 authorization restrictions (Prague+): at most one in-flight tx
	// for a delegated account / pending authority, and an authority cannot be
	// reserved by more than one in-flight transaction. Runs for every tx once
	// Prague is active (a plain tx from a delegated account is also restricted);
	// the authority-side check is a no-op for non-SetCode txs.
	if isPrague {
		if err := pool.validateAuth(tx); err != nil {
			return err
		}
	}

	if pool.deposit != nil {
		var depositInfo *deposit.Info
		if err := pool.bc.DB().View(pool.ctx, func(tx kv.Tx) error {
			depositInfo = deposit.GetDepositInfo(tx, addr)
			return nil
		}); err != nil {
			log.Warn("Failed to query deposit info from database", "addr", addr, "err", err)
		}
		if depositInfo != nil && pool.deposit.IsDepositAction(tx) {
			return internal.ErrAlreadyDeposited
		}
	}
	return nil
}

// validateSender verifies that the transaction signature is valid and, when the
// transaction declares a sender, that it is the one the signature recovers to.
//
// The signature is the authority on who sent a transaction; a declared From is
// a claim. It used to be REQUIRED here, which quietly coupled the pool to a
// wire format that carried one: transactions gossiped in the standard Ethereum
// encoding have no From field at all, so every one of them was rejected as
// having a "nil From address" -- at Debug level, on a fleet running at Info, so
// the whole mempool gossip pipeline was silently dead again. A missing From is
// now filled in from the recovery, and a present one still has to match.
func (pool *TxsPool) validateSender(tx *transaction.Transaction) bool {
	declaredFrom := tx.From()

	v, r, s := tx.RawSignatureValues()
	if v == nil || r == nil || s == nil {
		log.Debug("Transaction has nil signature values")
		return false
	}
	if r.IsZero() || s.IsZero() {
		log.Debug("Transaction has zero R or S signature value")
		return false
	}

	// Derive recovery ID from V. For EIP-155 protected transactions,
	// V = chainId*2 + 35 + parity, so we need to extract parity (0 or 1).
	vVal := v.Uint64()
	var vByte byte
	if vVal >= 35 {
		// EIP-155: parity = (V - 35) % 2
		vByte = byte((vVal - 35) % 2)
	} else if vVal >= 27 {
		// Unprotected: V is 27 or 28
		vByte = byte(vVal - 27)
	} else {
		vByte = byte(vVal)
	}
	if !crypto.ValidateSignatureValues(vByte, r, s, true) {
		log.Debug("Transaction signature values are invalid", "v", vVal, "vByte", vByte)
		return false
	}

	signer := transaction.LatestSignerForChainID(pool.chainconfig.ChainID)
	recoveredAddr, err := transaction.Sender(signer, tx)
	if err != nil {
		log.Debug("Failed to recover sender from signature", "err", err)
		return false
	}
	if declaredFrom == nil {
		// No claim to check: adopt what the signature recovered, so the rest of
		// the pool and the miner can read tx.From().
		tx.SetFrom(recoveredAddr)
	} else if recoveredAddr != *declaredFrom {
		log.Debug("Recovered sender does not match declared From",
			"recovered", recoveredAddr, "declared", *declaredFrom)
		return false
	}
	return true
}

// --- Scheduling and lifecycle ---

// requestReset requests a pool reset to the new head block.
func (pool *TxsPool) requestReset(oldBlock block.IBlock, newBlock block.IBlock) <-chan struct{} {
	select {
	case pool.reqResetCh <- &txspoolResetRequest{oldBlock, newBlock}:
		select {
		case done := <-pool.reorgDoneCh:
			return done
		case <-pool.ctx.Done():
			return pool.ctx.Done()
		}
	case <-pool.ctx.Done():
		return pool.ctx.Done()
	}
}

// requestPromoteExecutables requests transaction promotion checks for the given addresses.
func (pool *TxsPool) requestPromoteExecutables(set *accountSet) <-chan struct{} {
	select {
	case pool.reqPromoteCh <- set:
		select {
		case done := <-pool.reorgDoneCh:
			return done
		case <-pool.ctx.Done():
			return pool.ctx.Done()
		}
	case <-pool.ctx.Done():
		return pool.ctx.Done()
	}
}

// queueTxEvent enqueues a transaction event to be sent in the next reorg run.
func (pool *TxsPool) queueTxEvent(tx *transaction.Transaction) {
	select {
	case pool.queueTxEventCh <- tx:
	case <-pool.ctx.Done():
	}
}

// reset reinitializes the pool to match the new blockchain head.
func (pool *TxsPool) reset(oldBlock, newBlock block.IBlock) {
	var reinject []*transaction.Transaction

	if newBlock == nil {
		newBlock = currentBlock(pool.bc)
	}
	if newBlock == nil {
		log.Warn("Skipping txpool reset with unavailable current block")
		return
	}

	if oldBlock != nil {
		oldHeader := oldBlock.Header()
		if oldHeader == nil {
			log.Warn("Skipping transaction reset with missing old header", "new", newBlock.Hash())
		} else if oldHeader.Hash().String() != newBlock.ParentHash().String() {
			oldNum, oldNumErr := requireBlockNumber(oldBlock, "old block number unavailable")
			newNum, newNumErr := requireBlockNumber(newBlock, "new block number unavailable")
			if oldNumErr != nil || newNumErr != nil {
				log.Warn("Skipping transaction reset with unavailable head number", "oldErr", oldNumErr, "newErr", newNumErr)
			} else if depth := uint64(math.Abs(float64(oldNum.Uint64()) - float64(newNum.Uint64()))); depth > 64 {
				log.Debug("Skipping deep transaction reorg", "depth", depth)
			} else {
				var discarded, included []*transaction.Transaction
				rem := oldBlock
				add := newBlock
				reorgComplete := true

				for reorgComplete {
					remNum, remErr := requireBlockNumber(rem, "old chain block number unavailable")
					addNum, addErr := requireBlockNumber(add, "new chain block number unavailable")
					if remErr != nil || addErr != nil {
						log.Warn("Skipping transaction reset with unavailable reorg block number", "oldErr", remErr, "newErr", addErr)
						reorgComplete = false
						break
					}
					if remNum.Cmp(addNum) != 1 {
						break
					}
					discarded = append(discarded, rem.Body().Transactions()...)
					if rem, _ = pool.bc.GetBlockByHash(rem.ParentHash()); rem == nil {
						log.Error("Unrooted old chain seen by tx pool", "block", oldNum, "hash", oldBlock.Hash())
						return
					}
				}
				for reorgComplete {
					remNum, remErr := requireBlockNumber(rem, "old chain block number unavailable")
					addNum, addErr := requireBlockNumber(add, "new chain block number unavailable")
					if remErr != nil || addErr != nil {
						log.Warn("Skipping transaction reset with unavailable reorg block number", "oldErr", remErr, "newErr", addErr)
						reorgComplete = false
						break
					}
					if addNum.Cmp(remNum) != 1 {
						break
					}
					included = append(included, add.Body().Transactions()...)
					if add, _ = pool.bc.GetBlockByHash(add.ParentHash()); add == nil {
						log.Error("Unrooted new chain seen by tx pool", "block", newNum, "hash", newBlock.Hash())
						return
					}
				}
				if reorgComplete {
					for rem.Hash() != add.Hash() {
						discarded = append(discarded, rem.Body().Transactions()...)
						if rem, _ = pool.bc.GetBlockByHash(rem.ParentHash()); rem == nil {
							log.Error("Unrooted old chain seen by tx pool", "block", oldNum, "hash", oldBlock.Header().Hash())
							return
						}
						included = append(included, add.Body().Transactions()...)
						if add, _ = pool.bc.GetBlockByHash(add.ParentHash()); add == nil {
							log.Error("Unrooted new chain seen by tx pool", "block", newNum, "hash", newBlock.Hash())
							return
						}
					}
					log.Debugf("start to reset txs pool4 discarded %d , included %d, old number :%d ,new number:%d",
						len(discarded), len(included), oldNum.Uint64(), newNum.Uint64())
					reinject = pool.txDifference(discarded, included)
				}
			}
		}
	}

	pool.currentMaxGas = newBlock.GasLimit()

	if len(reinject) > 0 {
		log.Debugf("Reinjecting stale transactions count %d", len(reinject))
	}
	pool.addTxsLocked(reinject, false)

	// Update all fork indicators by next pending block number.
	next := big.NewInt(1)
	if blockNumber := newBlock.Number64(); blockNumber != nil {
		next = new(big.Int).Add(blockNumber.ToBig(), big.NewInt(1))
	}
	pool.istanbul = pool.chainconfig.IsIstanbul(next.Uint64())
	pool.eip2718 = pool.chainconfig.IsBerlin(next.Uint64())
	pool.eip1559 = pool.chainconfig.IsLondon(next.Uint64())
}

// slowReorgThreshold is the pool-reorg cost above which the phase breakdown is
// logged at Info. A reorg runs on every new head, so anything lower would put a
// line in the log per block for a path that is normally a few milliseconds.
const slowReorgThreshold = 200 * time.Millisecond

// runReorg runs reset and promoteExecutables on behalf of scheduleLoop.
func (pool *TxsPool) runReorg(done chan struct{}, reset *txspoolResetRequest, dirtyAccounts *accountSet, events map[types.Address]*txsSortedMap) {
	defer close(done)

	var promoteAddrs []types.Address
	if dirtyAccounts != nil && reset == nil {
		promoteAddrs = dirtyAccounts.flatten()
	}

	// Phase timings for the reorg the pool runs on every new head. Under load
	// this is the path that decides how many transactions become eligible
	// before the next block is built: at a 2s interval every other block came
	// out empty, and at 4s none did, with identical throughput either way --
	// meaning the pool, not the block schedule, sets the ceiling. Without a
	// breakdown there is no way to tell which phase owns it.
	tStart := time.Now()
	var dReset, dPromote, dDemote, dNonces, dTruncate time.Duration
	nQueue, nPending := 0, 0

	pool.mu.Lock()
	tLocked := time.Now()
	if reset != nil {
		tR := time.Now()
		pool.reset(reset.oldBlock, reset.newBlock)
		dReset = time.Since(tR)

		// Nonces were reset, discard any events that became stale
		for addr := range events {
			events[addr].Forward(pool.pendingNonces.get(addr))
			if events[addr].Len() == 0 {
				delete(events, addr)
			}
		}
		promoteAddrs = make([]types.Address, 0, len(pool.queue))
		for addr := range pool.queue {
			promoteAddrs = append(promoteAddrs, addr)
		}
	}

	tP := time.Now()
	promoted := pool.promoteExecutables(promoteAddrs)
	dPromote = time.Since(tP)

	if reset != nil {
		tD := time.Now()
		pool.demoteUnexecutables()
		dDemote = time.Since(tD)

		if reset.newBlock != nil {
			if blockNumber := reset.newBlock.Number64(); blockNumber != nil && pool.chainconfig.IsLondon(blockNumber.Uint64()+1) {
				if header, ok := reset.newBlock.Header().(*block.Header); ok && header != nil {
					pendingBaseFee, _ := uint256.FromBig(misc.CalcBaseFee(pool.chainconfig, header))
					pool.priced.SetBaseFee(pendingBaseFee)
				}
			}
		}
		tN := time.Now()
		nonces := make(map[types.Address]uint64, len(pool.pending))
		for addr, list := range pool.pending {
			if highestPending := list.LastElement(); highestPending != nil {
				nonces[addr] = highestPending.Nonce() + 1
			}
		}
		pool.pendingNonces.setAll(nonces)
		dNonces = time.Since(tN)
	}
	tT := time.Now()
	pool.truncatePending()
	pool.truncateQueue()
	dTruncate = time.Since(tT)
	nQueue, nPending = len(pool.queue), len(pool.pending)
	pool.changesSinceReorg = 0
	pool.mu.Unlock()

	// Info only when the reorg is slow enough to explain a missed block, Debug
	// otherwise: it runs on every new head and the interesting case is the
	// outlier. Measured on a saturated fleet this is 4-38 ms, comfortably
	// inside a 2s interval -- the pool is not what limits block occupancy.
	if total := time.Since(tStart); reset != nil {
		emit := log.Debug
		if total >= slowReorgThreshold {
			emit = log.Info
		}
		emit("txpool reorg phases",
			"total", total, "lockWait", tLocked.Sub(tStart),
			"reset", dReset, "promote", dPromote, "demote", dDemote,
			"nonces", dNonces, "truncate", dTruncate,
			"promoted", len(promoted), "queueAccts", nQueue, "pendingAccts", nPending)
	}

	// Notify subsystems for newly added transactions
	for _, tx := range promoted {
		sender := tx.From()
		if sender == nil {
			continue
		}
		addr := *sender
		if _, ok := events[addr]; !ok {
			events[addr] = newTxSortedMap()
		}
		events[addr].Put(tx)
	}
	if len(events) > 0 {
		var txs []*transaction.Transaction
		for _, set := range events {
			txs = append(txs, set.Flatten()...)
		}
		event.GlobalEvent.Send(common.NewTxsEvent{Txs: txs})
	}
}

// scheduleLoop is the main scheduling loop for reorg and promote requests.
func (pool *TxsPool) scheduleLoop() {
	defer pool.wg.Done()

	var (
		curDone       chan struct{}
		nextDone      = make(chan struct{})
		launchNextRun bool
		reset         *txspoolResetRequest
		dirtyAccounts *accountSet
		queuedEvents  = make(map[types.Address]*txsSortedMap)
	)

	for {
		if curDone == nil && launchNextRun {
			// Capture variables before clearing them, to avoid race with the goroutine.
			rReset := reset
			rDirty := dirtyAccounts
			rEvents := queuedEvents
			rDone := nextDone

			go func() {
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("panic in txpool runReorg: %v", r)
						close(rDone)
					}
				}()
				pool.runReorg(rDone, rReset, rDirty, rEvents)
			}()

			curDone, nextDone = nextDone, make(chan struct{})
			launchNextRun = false
			reset, dirtyAccounts = nil, nil
			queuedEvents = make(map[types.Address]*txsSortedMap)
		}

		select {
		case req := <-pool.reqResetCh:
			if reset == nil {
				reset = req
			} else {
				reset.newBlock = req.newBlock
			}
			launchNextRun = true
			pool.reorgDoneCh <- nextDone

		case req := <-pool.reqPromoteCh:
			if dirtyAccounts == nil {
				dirtyAccounts = req
			} else {
				dirtyAccounts.merge(req)
			}
			launchNextRun = true
			pool.reorgDoneCh <- nextDone

		case tx := <-pool.queueTxEventCh:
			sender := tx.From()
			if sender == nil {
				continue
			}
			addr := *sender
			if _, ok := queuedEvents[addr]; !ok {
				queuedEvents[addr] = newTxSortedMap()
			}
			queuedEvents[addr].Put(tx)

		case <-curDone:
			curDone = nil

		case <-pool.ctx.Done():
			if curDone != nil {
				<-curDone
			}
			close(nextDone)
			return
		}
	}
}

// blockChangeLoop listens for new block events and triggers pool resets.
func (pool *TxsPool) blockChangeLoop() {
	defer pool.wg.Done()

	highestBlockCh := make(chan common.ChainHighestBlock)
	highestSub, _ := event.GlobalEvent.Subscribe(highestBlockCh)

	oldBlock := pool.bc.CurrentBlock()

	for {
		select {
		case err := <-highestSub.Err():
			// Check if we are shutting down
			select {
			case <-pool.ctx.Done():
				highestSub.Unsubscribe()
				return
			default:
			}
			log.Error("txpool block subscription error, resubscribing", "err", err)
			highestSub.Unsubscribe()
			// Backoff before resubscribing to avoid tight loop
			select {
			case <-time.After(time.Second):
			case <-pool.ctx.Done():
				return
			}
			highestBlockCh = make(chan common.ChainHighestBlock)
			highestSub, _ = event.GlobalEvent.Subscribe(highestBlockCh)
		case <-pool.ctx.Done():
			highestSub.Unsubscribe()
			return
		case highestBlock, ok := <-highestBlockCh:
			if ok && highestBlock.Inserted {
				pool.requestReset(oldBlock, pool.bc.CurrentBlock())
				oldBlock = pool.bc.CurrentBlock()
			}
		}
	}
}

// txDifference returns the transactions in a that are not in b.
func (pool *TxsPool) txDifference(a, b []*transaction.Transaction) []*transaction.Transaction {
	remove := make(map[types.Hash]struct{}, len(b))
	for _, tx := range b {
		remove[tx.Hash()] = struct{}{}
	}

	keep := make([]*transaction.Transaction, 0, len(a))
	for _, tx := range a {
		if _, ok := remove[tx.Hash()]; !ok {
			keep = append(keep, tx)
		}
	}
	return keep
}
