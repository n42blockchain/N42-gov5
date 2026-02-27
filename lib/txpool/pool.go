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
	"context"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"time"

	"github.com/go-stack/stack"
	"github.com/google/btree"
	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/chain"
	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/assert"
	"github.com/n42blockchain/N42/lib/common/dbg"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/kvcache"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

func New(newTxs chan types.Announcements, coreDB kv.RoDB, cfg txpoolcfg.Config, cache kvcache.Cache,
	chainID uint256.Int, shanghaiTime, agraBlock, cancunTime, pragueTime, osakaTime *big.Int, blobSchedule *chain.BlobSchedule,
	feeCalculator FeeCalculator, logger log.Logger,
) (*TxPool, error) {
	localsHistory, err := simplelru.NewLRU[string, struct{}](10_000, nil)
	if err != nil {
		return nil, err
	}
	discardHistory, err := simplelru.NewLRU[string, txpoolcfg.DiscardReason](10_000, nil)
	if err != nil {
		return nil, err
	}

	byNonce := &BySenderAndNonce{
		tree:              btree.NewG[*metaTx](32, SortByNonceLess),
		search:            &metaTx{Tx: &types.TxSlot{}},
		senderIDTxnCount:  map[uint64]int{},
		senderIDBlobCount: map[uint64]uint64{},
	}
	tracedSenders := make(map[common.Address]struct{})
	for _, sender := range cfg.TracedSenders {
		tracedSenders[common.BytesToAddress([]byte(sender))] = struct{}{}
	}

	lock := &sync.Mutex{}

	res := &TxPool{
		lock:                    lock,
		lastSeenCond:            sync.NewCond(lock),
		byHash:                  map[string]*metaTx{},
		isLocalLRU:              localsHistory,
		discardReasonsLRU:       discardHistory,
		all:                     byNonce,
		recentlyConnectedPeers:  &recentlyConnectedPeers{},
		pending:                 NewPendingSubPool(PendingSubPool, cfg.PendingSubPoolLimit),
		baseFee:                 NewSubPool(BaseFeeSubPool, cfg.BaseFeeSubPoolLimit),
		queued:                  NewSubPool(QueuedSubPool, cfg.QueuedSubPoolLimit),
		newPendingTxs:           newTxs,
		_stateCache:             cache,
		senders:                 newSendersCache(tracedSenders),
		_chainDB:                coreDB,
		cfg:                     cfg,
		chainID:                 chainID,
		unprocessedRemoteTxs:    &types.TxSlots{},
		unprocessedRemoteByHash: map[string]int{},
		minedBlobTxsByBlock:     map[uint64][]*metaTx{},
		minedBlobTxsByHash:      map[string]*metaTx{},
		blobSchedule:            blobSchedule,
		feeCalculator:           feeCalculator,
		logger:                  logger,
		auths:                   map[common.Address]*metaTx{},
	}

	// Convert fork activation parameters from *big.Int to *uint64
	forkParams := []struct {
		value *big.Int
		name  string
		dest  **uint64
	}{
		{shanghaiTime, "shanghaiTime", &res.shanghaiTime},
		{agraBlock, "agraBlock", &res.agraBlock},
		{cancunTime, "cancunTime", &res.cancunTime},
		{pragueTime, "pragueTime", &res.pragueTime},
		{osakaTime, "osakaTime", &res.osakaTime},
	}
	for _, fp := range forkParams {
		ptr, err := bigIntToUint64Ptr(fp.value, fp.name)
		if err != nil {
			return nil, err
		}
		*fp.dest = ptr
	}

	return res, nil
}

func (p *TxPool) Start(ctx context.Context, db kv.RwDB) error {
	if p.started.Load() {
		return nil
	}
	return db.View(ctx, func(tx kv.Tx) error {
		coreDb, _ := p.coreDBWithCache()
		coreTx, err := coreDb.BeginRo(ctx)
		if err != nil {
			return err
		}
		defer coreTx.Rollback()

		if err := p.fromDB(ctx, tx, coreTx); err != nil {
			return fmt.Errorf("loading pool from DB: %w", err)
		}
		if p.started.CompareAndSwap(false, true) {
			p.logger.Info("[txpool] Started")
		}
		return nil
	})
}

func (p *TxPool) OnNewBlock(ctx context.Context, stateChanges *remote.StateChangeBatch, unwindTxs, unwindBlobTxs, minedTxs types.TxSlots, tx kv.Tx) error {
	defer newBlockTimer.ObserveDuration(time.Now())
	coreDB, cache := p.coreDBWithCache()
	cache.OnNewBlock(stateChanges)
	coreTx, err := coreDB.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer coreTx.Rollback()

	block := stateChanges.ChangeBatch[len(stateChanges.ChangeBatch)-1].BlockHeight
	baseFee := stateChanges.PendingBlockBaseFee
	available := len(p.pending.best.ms)

	defer func() {
		p.logger.Debug("[txpool] New block", "block", block, "unwound", len(unwindTxs.Txs), "mined", len(minedTxs.Txs), "baseFee", baseFee, "pending-pre", available, "pending", p.pending.Len(), "baseFee", p.baseFee.Len(), "queued", p.queued.Len(), "err", err)
	}()

	if err = minedTxs.Valid(); err != nil {
		return err
	}

	cacheView, err := cache.View(ctx, coreTx)
	if err != nil {
		return err
	}

	p.lock.Lock()
	defer func() {
		if err == nil {
			p.lastSeenBlock.Store(block)
			p.lastSeenCond.Broadcast()
		}
		p.lock.Unlock()
	}()

	if assert.Enable {
		if _, err := kvcache.AssertCheckValues(ctx, coreTx, cache); err != nil {
			p.logger.Error("AssertCheckValues", "err", err, "stack", stack.Trace().String())
		}
	}

	pendingBaseFee, baseFeeChanged := p.setBaseFee(baseFee)
	if baseFeeChanged {
		p.pending.best.pendingBaseFee = pendingBaseFee
		p.pending.worst.pendingBaseFee = pendingBaseFee
		p.baseFee.best.pendingBastFee = pendingBaseFee
		p.baseFee.worst.pendingBaseFee = pendingBaseFee
		p.queued.best.pendingBastFee = pendingBaseFee
		p.queued.worst.pendingBaseFee = pendingBaseFee
	}

	pendingBlobFee := stateChanges.PendingBlobFeePerGas
	p.setBlobFee(pendingBlobFee)

	oldGasLimit := p.blockGasLimit.Swap(stateChanges.BlockGasLimit)
	if oldGasLimit != stateChanges.BlockGasLimit {
		p.all.ascendAll(func(mt *metaTx) bool {
			var updated bool
			if mt.Tx.Gas < stateChanges.BlockGasLimit {
				updated = (mt.subPool & NotTooMuchGas) > 0
				mt.subPool |= NotTooMuchGas
			} else {
				updated = (mt.subPool & NotTooMuchGas) == 0
				mt.subPool &^= NotTooMuchGas
			}

			if mt.Tx.Traced {
				p.logger.Info("TX TRACING: on block gas limit update", "idHash", fmt.Sprintf("%x", mt.Tx.IDHash), "senderId", mt.Tx.SenderID, "nonce", mt.Tx.Nonce, "subPool", mt.currentSubPool, "updated", updated)
			}

			if !updated {
				return true
			}

			switch mt.currentSubPool {
			case PendingSubPool:
				p.pending.Updated(mt)
			case BaseFeeSubPool:
				p.baseFee.Updated(mt)
			case QueuedSubPool:
				p.queued.Updated(mt)
			}
			return true
		})
	}

	for i, txn := range unwindBlobTxs.Txs {
		if txn.Type == types.BlobTxType {
			knownBlobTxn, err := p.getCachedBlobTxnLocked(coreTx, txn.IDHash[:])
			if err != nil {
				return err
			}
			if knownBlobTxn != nil {
				unwindTxs.Append(knownBlobTxn.Tx, unwindBlobTxs.Senders.At(i), false)
			}
		}
	}
	if err = p.senders.onNewBlock(stateChanges, unwindTxs, minedTxs, p.logger); err != nil {
		return err
	}

	_, unwindTxs, err = p.validateTxs(&unwindTxs, cacheView)
	if err != nil {
		return err
	}

	if assert.Enable {
		for _, txn := range unwindTxs.Txs {
			if txn.SenderID == 0 {
				panic(fmt.Errorf("onNewBlock.unwindTxs: senderID can't be zero"))
			}
		}
		for _, txn := range minedTxs.Txs {
			if txn.SenderID == 0 {
				panic(fmt.Errorf("onNewBlock.minedTxs: senderID can't be zero"))
			}
		}
	}

	if err = p.processMinedFinalizedBlobs(coreTx, minedTxs.Txs, stateChanges.FinalizedBlock); err != nil {
		return err
	}

	if err = p.removeMined(p.all, minedTxs.Txs); err != nil {
		return err
	}

	// Remove auths from pool map for mined SetCode transactions
	for _, mt := range minedTxs.Txs {
		if mt.Type == types.SetCodeTxType {
			for i := range len(mt.AuthRaw) {
				signature := mt.Authorizations[i]
				signer, err := RecoverSignerFromRLP(mt.AuthRaw[i], uint8(signature.V.Uint64()), signature.R, signature.S)
				if err != nil {
					continue
				}
				delete(p.auths, *signer)
			}
		}
	}

	var announcements types.Announcements
	announcements, err = p.addTxsOnNewBlock(block, cacheView, stateChanges, p.senders, unwindTxs,
		pendingBaseFee, stateChanges.BlockGasLimit, p.logger)
	if err != nil {
		return err
	}

	p.pending.EnforceWorstInvariants()
	p.baseFee.EnforceInvariants()
	p.queued.EnforceInvariants()
	p.promote(pendingBaseFee, pendingBlobFee, &announcements, p.logger)
	p.pending.EnforceBestInvariants()
	p.promoted.Reset()
	p.promoted.AppendOther(announcements)

	if p.promoted.Len() > 0 {
		select {
		case p.newPendingTxs <- p.promoted.Copy():
		default:
		}
	}

	return nil
}

func (p *TxPool) AddLocalTxs(ctx context.Context, newTransactions types.TxSlots, tx kv.Tx) ([]txpoolcfg.DiscardReason, error) {
	coreDb, cache := p.coreDBWithCache()
	coreTx, err := coreDb.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer coreTx.Rollback()

	cacheView, err := cache.View(ctx, coreTx)
	if err != nil {
		return nil, err
	}

	p.lock.Lock()
	defer p.lock.Unlock()

	if err = p.senders.registerNewSenders(&newTransactions, p.logger); err != nil {
		return nil, err
	}

	reasons, newTxs, err := p.validateTxs(&newTransactions, cacheView)
	if err != nil {
		return nil, err
	}

	announcements, addReasons, err := p.addTxs(p.lastSeenBlock.Load(), cacheView, p.senders, newTxs,
		p.pendingBaseFee.Load(), p.pendingBlobFee.Load(), p.blockGasLimit.Load(), true, p.logger)
	if err != nil {
		return nil, err
	}
	for i, reason := range addReasons {
		if reason != txpoolcfg.NotSet {
			reasons[i] = reason
		}
	}

	p.promoted.Reset()
	p.promoted.AppendOther(announcements)

	reasons = fillDiscardReasons(reasons, newTxs, p.discardReasonsLRU)
	for i, reason := range reasons {
		if reason == txpoolcfg.Success {
			txn := newTxs.Txs[i]
			if txn.Traced {
				p.logger.Info(fmt.Sprintf("TX TRACING: AddLocalTxs promotes idHash=%x, senderId=%d", txn.IDHash, txn.SenderID))
			}
			p.promoted.Append(txn.Type, txn.Size, txn.IDHash[:])
		}
	}
	if p.promoted.Len() > 0 {
		select {
		case p.newPendingTxs <- p.promoted.Copy():
		default:
		}
	}
	return reasons, nil
}

func (p *TxPool) coreDBWithCache() (kv.RoDB, kvcache.Cache) {
	p.lock.Lock()
	defer p.lock.Unlock()
	return p._chainDB, p._stateCache
}

func (p *TxPool) NonceFromAddress(addr [20]byte) (nonce uint64, inPool bool) {
	p.lock.Lock()
	defer p.lock.Unlock()
	senderID, found := p.senders.getID(addr)
	if !found {
		return 0, false
	}
	return p.all.nonce(senderID)
}

func (p *TxPool) IsLocal(idHash []byte) bool {
	p.lock.Lock()
	defer p.lock.Unlock()
	return p.isLocalLRU.Contains(string(idHash))
}

func (p *TxPool) AddNewGoodPeer(peerID types.PeerID) { p.recentlyConnectedPeers.AddPeer(peerID) }
func (p *TxPool) Started() bool                      { return p.started.Load() }

func (p *TxPool) logStats() {
	if !p.Started() {
		return
	}

	p.lock.Lock()
	defer p.lock.Unlock()

	var m runtime.MemStats
	dbg.ReadMemStats(&m)
	ctx := []interface{}{
		"pending", p.pending.Len(),
		"baseFee", p.baseFee.Len(),
		"queued", p.queued.Len(),
	}
	cacheKeys := p._stateCache.Len()
	if cacheKeys > 0 {
		ctx = append(ctx, "cache_keys", cacheKeys)
	}
	ctx = append(ctx, "alloc", common.ByteCount(m.Alloc), "sys", common.ByteCount(m.Sys))
	p.logger.Info("[txpool] stat", ctx...)
	pendingSubCounter.SetInt(p.pending.Len())
	basefeeSubCounter.SetInt(p.baseFee.Len())
	queuedSubCounter.SetInt(p.queued.Len())
}

// nolint
func (p *TxPool) printDebug(prefix string) {
	fmt.Printf("%s.pool.byHash\n", prefix)
	for _, j := range p.byHash {
		fmt.Printf("\tsenderID=%d, nonce=%d, tip=%d\n", j.Tx.SenderID, j.Tx.Nonce, j.Tx.Tip)
	}
	fmt.Printf("%s.pool.queues.len: %d,%d,%d\n", prefix, p.pending.Len(), p.baseFee.Len(), p.queued.Len())
	for _, mt := range p.pending.best.ms {
		mt.Tx.PrintDebug(fmt.Sprintf("%s.pending: %b,%d,%d,%d", prefix, mt.subPool, mt.Tx.SenderID, mt.Tx.Nonce, mt.Tx.Tip))
	}
	for _, mt := range p.baseFee.best.ms {
		mt.Tx.PrintDebug(fmt.Sprintf("%s.baseFee : %b,%d,%d,%d", prefix, mt.subPool, mt.Tx.SenderID, mt.Tx.Nonce, mt.Tx.Tip))
	}
	for _, mt := range p.queued.best.ms {
		mt.Tx.PrintDebug(fmt.Sprintf("%s.queued : %b,%d,%d,%d", prefix, mt.subPool, mt.Tx.SenderID, mt.Tx.Nonce, mt.Tx.Tip))
	}
}

// Deprecated need switch to streaming-like
func (p *TxPool) deprecatedForEach(_ context.Context, f func(rlp []byte, sender common.Address, t SubPoolType), tx kv.Tx) {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.all.ascendAll(func(mt *metaTx) bool {
		slot := mt.Tx
		slotRlp := slot.Rlp
		if slot.Rlp == nil {
			v, err := tx.GetOne(kv.PoolTransaction, slot.IDHash[:])
			if err != nil {
				p.logger.Warn("[txpool] foreach: get tx from db", "err", err)
				return true
			}
			if v == nil {
				p.logger.Warn("[txpool] foreach: tx not found in db")
				return true
			}
			slotRlp = v[20:]
		}
		if sender, found := p.senders.senderID2Addr[slot.SenderID]; found {
			f(slotRlp, sender, mt.currentSubPool)
		}
		return true
	})
}
