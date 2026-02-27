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
	"sync"
	"sync/atomic"

	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/chain"
	"github.com/n42blockchain/N42/lib/common"
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

// Pool is interface for the transaction pool.
// This interface exists for the convenience of testing, and not yet because
// there are multiple implementations.
//
//go:generate mockgen -typed=true -destination=./pool_mock.go -package=txpool . Pool
type Pool interface {
	ValidateSerializedTxn(serializedTxn []byte) error

	// Handle 3 main events - new remote txs from p2p, new local txs from RPC, new blocks from execution layer
	AddRemoteTxs(ctx context.Context, newTxs types.TxSlots)
	AddLocalTxs(ctx context.Context, newTxs types.TxSlots, tx kv.Tx) ([]txpoolcfg.DiscardReason, error)
	OnNewBlock(ctx context.Context, stateChanges *remote.StateChangeBatch, unwindTxs, unwindBlobTxs, minedTxs types.TxSlots, tx kv.Tx) error
	IdHashKnown(tx kv.Tx, hash []byte) (bool, error)
	FilterKnownIdHashes(tx kv.Tx, hashes types.Hashes) (unknownHashes types.Hashes, err error)
	Started() bool
	GetRlp(tx kv.Tx, hash []byte) ([]byte, error)

	AddNewGoodPeer(peerID types.PeerID)
}

var _ Pool = (*TxPool)(nil) // compile-time interface check

// SubPoolMarker is an ordered bitset of five bits that's used to sort transactions into sub-pools.
// Bits meaning:
// 1. Absence of nonce gaps.
// 2. Sufficient balance for gas.
// 3. Not too much gas.
// 4. Dynamic fee requirement (feeCap >= baseFee).
// 5. Local transaction.
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

// TxPool - holds all pool-related data structures and lock-based tiny methods.
// Most of logic implemented by pure tests-friendly functions.
//
// txpool doesn't start any goroutines - "leave concurrency to user" design.
// txpool has no DB-TX fields - "leave db transactions management to user" design.
// txpool has _chainDB field - but it must maximize local state cache hit-rate.
//
// It preserves TxSlot objects immutable.
type TxPool struct {
	_chainDB               kv.RoDB
	_stateCache            kvcache.Cache
	lock                   *sync.Mutex
	recentlyConnectedPeers *recentlyConnectedPeers
	senders                *sendersBatch

	// batch processing of remote transactions
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
	newPendingTxs           chan types.Announcements          // notifications about new txs in Pending sub-pool
	all                     *BySenderAndNonce                 // senderID => (sorted map of tx nonce => *metaTx)
	deletedTxs              []*metaTx                         // list of discarded txs since last db commit
	promoted                types.Announcements
	cfg                     txpoolcfg.Config
	chainID                 uint256.Int
	lastSeenBlock           atomic.Uint64
	lastSeenCond            *sync.Cond
	lastFinalizedBlock      atomic.Uint64
	started                 atomic.Bool
	pendingBaseFee          atomic.Uint64
	pendingBlobFee          atomic.Uint64
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

// recentlyConnectedPeers buffers IDs of recently connected good peers,
// then sync of pooled transactions can happen to all of them at once.
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

// DB key constants for pool persistence
var PoolChainConfigKey = []byte("chain_config")
var PoolLastSeenBlockKey = []byte("last_seen_block")
var PoolPendingBaseFeeKey = []byte("pending_base_fee")
var PoolPendingBlobFeeKey = []byte("pending_blob_fee")
var PoolStateVersion = []byte("state_version")
