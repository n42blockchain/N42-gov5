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
	"sync/atomic"
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
	"github.com/n42blockchain/N42/lib/metrics"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

const DefaultBlockGasLimit = uint64(30000000)

var (
	processBatchTxsTimer    = metrics.NewSummary(`pool_process_remote_txs`)
	addRemoteTxsTimer       = metrics.NewSummary(`pool_add_remote_txs`)
	newBlockTimer           = metrics.NewSummary(`pool_new_block`)
	writeToDBTimer          = metrics.NewSummary(`pool_write_to_db`)
	propagateToNewPeerTimer = metrics.NewSummary(`pool_propagate_to_new_peer`)
	propagateNewTxsTimer    = metrics.NewSummary(`pool_propagate_new_txs`)
	writeToDBBytesCounter   = metrics.GetOrCreateGauge(`pool_write_to_db_bytes`)
	pendingSubCounter       = metrics.GetOrCreateGauge(`txpool_pending`)
	queuedSubCounter        = metrics.GetOrCreateGauge(`txpool_queued`)
	basefeeSubCounter       = metrics.GetOrCreateGauge(`txpool_basefee`)
)

var TraceAll = false

// Pool is interface for the transaction pool
// This interface exists for the convenience of testing, and not yet because
// there are multiple implementations
//
//go:generate mockgen -typed=true -destination=./pool_mock.go -package=txpool . Pool
type Pool interface {
	ValidateSerializedTxn(serializedTxn []byte) error

	// Handle 3 main events - new remote txs from p2p, new local txs from RPC, new blocks from execution layer
	AddRemoteTxs(ctx context.Context, newTxs types.TxSlots)
	AddLocalTxs(ctx context.Context, newTxs types.TxSlots, tx kv.Tx) ([]txpoolcfg.DiscardReason, error)
	OnNewBlock(ctx context.Context, stateChanges *remote.StateChangeBatch, unwindTxs, unwindBlobTxs, minedTxs types.TxSlots, tx kv.Tx) error
	// IdHashKnown check whether transaction with given Id hash is known to the pool
	IdHashKnown(tx kv.Tx, hash []byte) (bool, error)
	FilterKnownIdHashes(tx kv.Tx, hashes types.Hashes) (unknownHashes types.Hashes, err error)
	Started() bool
	GetRlp(tx kv.Tx, hash []byte) ([]byte, error)

	AddNewGoodPeer(peerID types.PeerID)
}

var _ Pool = (*TxPool)(nil) // compile-time interface check

// SubPoolMarker is an ordered bitset of five bits that's used to sort transactions into sub-pools. Bits meaning:
// 1. Absence of nonce gaps. Set to 1 for transactions whose nonce is N, state nonce for the sender is M, and there are transactions for all nonces between M and N from the same sender. Set to 0 is the transaction's nonce is divided from the state nonce by one or more nonce gaps.
// 2. Sufficient balance for gas. Set to 1 if the balance of sender's account in the state is B, nonce of the sender in the state is M, nonce of the transaction is N, and the sum of feeCap x gasLimit + transferred_value of all transactions from this sender with nonces N+1 ... M is no more than B. Set to 0 otherwise. In other words, this bit is set if there is currently a guarantee that the transaction and all its required prior transactions will be able to pay for gas.
// 3. Not too much gas: Set to 1 if the transaction doesn't use too much gas
// 4. Dynamic fee requirement. Set to 1 if feeCap of the transaction is no less than baseFee of the currently pending block. Set to 0 otherwise.
// 5. Local transaction. Set to 1 if transaction is local.
type SubPoolMarker uint8

const (
	NoNonceGaps       = 0b010000
	EnoughBalance     = 0b001000
	NotTooMuchGas     = 0b000100
	EnoughFeeCapBlock = 0b000010
	IsLocal           = 0b000001

	BaseFeePoolBits = NoNonceGaps + EnoughBalance + NotTooMuchGas
)

// metaTx holds transaction and some metadata
type metaTx struct {
	Tx                        *types.TxSlot
	minFeeCap                 uint256.Int
	nonceDistance             uint64 // how far their nonces are from the state's nonce for the sender
	cumulativeBalanceDistance uint64 // how far their cumulativeRequiredBalance are from the state's balance for the sender
	minTip                    uint64
	bestIndex                 int
	worstIndex                int
	timestamp                 uint64 // when it was added to pool
	subPool                   SubPoolMarker
	currentSubPool            SubPoolType
	minedBlockNum             uint64
}

func newMetaTx(slot *types.TxSlot, isLocal bool, timestamp uint64) *metaTx {
	mt := &metaTx{Tx: slot, worstIndex: -1, bestIndex: -1, timestamp: timestamp}
	if isLocal {
		mt.subPool = IsLocal
	}
	return mt
}

type SubPoolType uint8

const PendingSubPool SubPoolType = 1
const BaseFeeSubPool SubPoolType = 2
const QueuedSubPool SubPoolType = 3

func (sp SubPoolType) String() string {
	switch sp {
	case PendingSubPool:
		return "Pending"
	case BaseFeeSubPool:
		return "BaseFee"
	case QueuedSubPool:
		return "Queued"
	}
	return fmt.Sprintf("Unknown:%d", sp)
}

// TxPool - holds all pool-related data structures and lock-based tiny methods
// most of logic implemented by pure tests-friendly functions
//
// txpool doesn't start any goroutines - "leave concurrency to user" design
// txpool has no DB-TX fields - "leave db transactions management to user" design
// txpool has _chainDB field - but it must maximize local state cache hit-rate - and perform minimum _chainDB transactions
//
// It preserve TxSlot objects immutable
type TxPool struct {
	_chainDB               kv.RoDB // remote db - use it wisely
	_stateCache            kvcache.Cache
	lock                   *sync.Mutex
	recentlyConnectedPeers *recentlyConnectedPeers // all txs will be propagated to this peers eventually, and clear list
	senders                *sendersBatch
	// batch processing of remote transactions
	// handling is fast enough without batching, but batching allows:
	//   - fewer _chainDB transactions
	//   - batch notifications about new txs (reduced P2P spam to other nodes about txs propagation)
	//   - and as a result reducing lock contention
	unprocessedRemoteTxs    *types.TxSlots
	unprocessedRemoteByHash map[string]int                                  // to reject duplicates
	byHash                  map[string]*metaTx                              // tx_hash => tx : only those records not committed to db yet
	discardReasonsLRU       *simplelru.LRU[string, txpoolcfg.DiscardReason] // tx_hash => discard_reason : non-persisted
	pending                 *PendingPool
	baseFee                 *SubPool
	queued                  *SubPool
	minedBlobTxsByBlock     map[uint64][]*metaTx             // (blockNum => slice): cache of recently mined blobs
	minedBlobTxsByHash      map[string]*metaTx               // (hash => mt): map of recently mined blobs
	isLocalLRU              *simplelru.LRU[string, struct{}] // tx_hash => is_local : to restore isLocal flag of unwinded transactions
	newPendingTxs           chan types.Announcements         // notifications about new txs in Pending sub-pool
	all                     *BySenderAndNonce                // senderID => (sorted map of tx nonce => *metaTx)
	deletedTxs              []*metaTx                        // list of discarded txs since last db commit
	promoted                types.Announcements
	cfg                     txpoolcfg.Config
	chainID                 uint256.Int
	lastSeenBlock           atomic.Uint64
	lastSeenCond            *sync.Cond
	lastFinalizedBlock      atomic.Uint64
	started                 atomic.Bool
	pendingBaseFee          atomic.Uint64
	pendingBlobFee          atomic.Uint64 // For gas accounting for blobs, which has its own dimension
	blockGasLimit           atomic.Uint64
	totalBlobsInPool        atomic.Uint64
	shanghaiTime            *uint64
	isPostShanghai          atomic.Bool
	agraBlock               *uint64
	isPostAgra              atomic.Bool
	cancunTime              *uint64
	isPostCancun            atomic.Bool
	pragueTime              *uint64
	isPostPrague            atomic.Bool
	osakaTime               *uint64
	isPostOsaka             atomic.Bool
	blobSchedule            *chain.BlobSchedule
	feeCalculator           FeeCalculator
	logger                  log.Logger
	auths                   map[common.Address]*metaTx // All accounts with a pooled authorization
}

type FeeCalculator interface {
	CurrentFees(chainConfig *chain.Config, db kv.Getter) (baseFee uint64, blobFee uint64, minBlobGasPrice, blockGasLimit uint64, err error)
}

func bigIntToUint64Ptr(v *big.Int, name string) (*uint64, error) {
	if v == nil {
		return nil, nil
	}
	if !v.IsUint64() {
		return nil, fmt.Errorf("%s overflow", name)
	}
	u := v.Uint64()
	return &u, nil
}

// recentlyConnectedPeers does buffer IDs of recently connected good peers
// then sync of pooled Transaction can happen to all of then at once
// DoS protection and performance saving
// it doesn't track if peer disconnected, it's fine
type recentlyConnectedPeers struct {
	peers []types.PeerID
	lock  sync.Mutex
}

func (l *recentlyConnectedPeers) AddPeer(p types.PeerID) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.peers = append(l.peers, p)
}

func (l *recentlyConnectedPeers) GetAndClean() []types.PeerID {
	l.lock.Lock()
	defer l.lock.Unlock()
	peers := l.peers
	l.peers = nil
	return peers
}

var PoolChainConfigKey = []byte("chain_config")
var PoolLastSeenBlockKey = []byte("last_seen_block")
var PoolPendingBaseFeeKey = []byte("pending_base_fee")
var PoolPendingBlobFeeKey = []byte("pending_blob_fee")
var PoolStateVersion = []byte("state_version")

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
	// Update pendingBase for all pool queues and slices
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

	// remove auths from pool map
	for _, mt := range minedTxs.Txs {
		if mt.Type == types.SetCodeTxType {
			numAuths := len(mt.AuthRaw)
			for i := range numAuths {
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

	announcements, err = p.addTxsOnNewBlock(block, cacheView, stateChanges, p.senders, unwindTxs, /* newTxs */
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
	if err == nil {
		for i, reason := range addReasons {
			if reason != txpoolcfg.NotSet {
				reasons[i] = reason
			}
		}
	} else {
		return nil, err
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
	hashS := string(idHash)
	p.lock.Lock()
	defer p.lock.Unlock()
	return p.isLocalLRU.Contains(hashS)
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
