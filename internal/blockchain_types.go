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
// Core BlockChain value types, constants and configuration knobs
// (BodyCacheLimit, ReceiptsCacheLimit, MaxFutureBlocks, snapshot
// settings). Kept in a separate file so blockchain.go stays focused
// on lifecycle while tests can reference the constants directly.

package internal

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	prometheus "github.com/n42blockchain/N42/common/metrics"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/zkverifier"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/modules/state/snapshot"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Blockchain Errors
// =============================================================================

var (
	ErrKnownBlock           = errors.New("block already known")
	ErrUnknownAncestor      = errors.New("unknown ancestor")
	ErrPrunedAncestor       = errors.New("pruned ancestor")
	ErrFutureBlock          = errors.New("block in the future")
	ErrInvalidNumber        = errors.New("invalid block number")
	ErrInvalidTerminalBlock = errors.New("insertion is interrupted")
	errChainStopped         = errors.New("blockchain is stopped")
	errInsertionInterrupted = errors.New("insertion is interrupted")
	errBlockDoesNotExist    = errors.New("block does not exist in blockchain")
	errZKProofRequired      = errors.New("block rejected: valid ZK proof required but not provided")
	// errRevertUnavailable marks a branch-switch revert that could not complete
	// due to a recoverable local state condition (e.g. a QMDB twig whose leaf
	// blob is stale/missing) rather than the block being invalid. Treated like an
	// unknown ancestor: future-queue and retry instead of marking the block BAD.
	errRevertUnavailable = errors.New("branch-switch revert unavailable (recoverable)")
	// errQMDBBehindParent: the live tree simply has not reached the incoming
	// block's parent yet (out-of-order arrival) — future-queue silently, this
	// is not a tree/marker discontinuity.
	errQMDBBehindParent = errors.New("qmdb applied state behind the incoming parent")
	// ErrStaleSeal: a leader's sealed block reached the write path after the
	// applied head had already moved past its parent (a competing same-height
	// candidate imported first). The seal lost the race - the miner drops it
	// quietly; nothing is wrong with the node.
	ErrStaleSeal = errors.New("sealed block is stale (applied head moved past its parent)")
)

// =============================================================================
// Metrics
// =============================================================================

var (
	headBlockGauge        = prometheus.GetOrCreateCounter("chain_head_block", true)
	headGasUsedGauge      = prometheus.GetOrCreateCounter("chain_head_gas_used", true)
	headGasLimitGauge     = prometheus.GetOrCreateCounter("chain_head_gas_limit", true)
	headTransactionsGauge = prometheus.GetOrCreateCounter("chain_head_transactions", true)
	blockInsertTimer      = prometheus.GetOrCreateHistogram("chain_inserts")
	blockValidationTimer  = prometheus.GetOrCreateHistogram("chain_validation")
	blockExecutionTimer   = prometheus.GetOrCreateHistogram("chain_execution")
	blockWriteTimer       = prometheus.GetOrCreateHistogram("chain_write")

	blockCacheHits    = prometheus.GetOrCreateCounter("cache_block_hits", true)
	blockCacheMisses  = prometheus.GetOrCreateCounter("cache_block_misses", true)
	headerCacheHits   = prometheus.GetOrCreateCounter("cache_header_hits", true)
	headerCacheMisses = prometheus.GetOrCreateCounter("cache_header_misses", true)
	tdCacheHits       = prometheus.GetOrCreateCounter("cache_td_hits", true)
	tdCacheMisses     = prometheus.GetOrCreateCounter("cache_td_misses", true)
)

// =============================================================================
// WriteStatus and Cache Constants
// =============================================================================

type WriteStatus byte

const (
	NonStatTy WriteStatus = iota
	CanonStatTy
	SideStatTy
)

// blockCacheLimit is a count of FULLY DECODED blocks, so what it retains scales
// with block size: a decoded transaction is several times its wire encoding
// (uint256 fields, not bytes), and at the bench fleet's 22,857-transaction
// blocks 512 of them is gigabytes. On a chain of small blocks the constant is
// nothing, which is why it reads as harmless.
//
// A peer client hit the identical defect from the other side -- an RPC block
// cache keeping the last 256 blocks with their decoded transactions, ~33 GB a
// node of blocks nobody queried -- and cutting it to 4 took their windows from
// ~119k to ~191k TPS, because the retained anonymous memory was evicting the
// page cache their importer's mmap needed. See docs/QS_TPS_BENCHMARK.md.
//
// N42_BLOCK_CACHE_BLOCKS exists to bookend the same question here without
// rebuilding between legs, which would make the binary a second variable. It is
// a benchmark instrument, not a tuning knob: the default is unchanged and
// nothing in production should set it until a round says what the right number
// is. Whether this cache is even full on the import path is UNMEASURED -- its
// largest filler (HasBlockAndState) stopped decoding blocks earlier today.
var blockCacheLimit = envInt("N42_BLOCK_CACHE_BLOCKS", 512)

const (
	receiptsCacheLimit  = 256
	maxFutureBlocks     = 256
	maxTimeFutureBlocks = 5 * 60 // 5 minutes

	headerCacheLimit  = 1024
	tdCacheLimit      = 512
	numberCacheLimit  = 2048
	witnessCacheLimit = 256
)

// =============================================================================
// BlockChain Struct
// =============================================================================

type BlockChain struct {
	chainConfig  *params.ChainConfig
	ctx          context.Context
	cancel       context.CancelFunc
	genesisBlock block.IBlock
	blocks       []block.IBlock
	headers      []block.IHeader
	currentBlock atomic.Pointer[block.Block]
	ChainDB      kv.RwDB
	engine       consensus.Engine

	// Transaction-lookup indexing, when a tier outside this package owns it.
	// Declared as an interface rather than imported: internal/txlookup reaches
	// internal/ethel, which imports this package, so depending on the
	// implementation directly would close an import cycle -- and committing a
	// block has no business knowing what an index is.
	txIndexer TxIndexer

	// Pool-backed sender cache for block import; see SenderHintSource.
	senderHints SenderHintSource

	// executedHook, when set, fires the moment a block's execution and state
	// validation succeed during import — BEFORE writeBlockWithState. Wired to
	// the consensus early-vote path so the vote round overlaps persistence.
	executedHook func(hash, txHash, parentHash types.Hash, number uint64, extra []byte)

	insertLock    chan struct{}
	latestBlockCh chan block.IBlock
	lock          sync.Mutex

	peers map[peer.ID]bool

	p2p p2p.P2P

	errorCh chan error

	process           Processor
	parallelEVM       bool
	prefetchEnabled   bool
	prefetchPredictor *PrefetchPredictor

	freezer       freezer.FreezerAPI
	ancientReader *freezer.AncientReader

	exexManager           *exex.Manager
	onBlockCommittedMu    sync.RWMutex
	onBlockCommitted      func(number uint64) // optional, package-agnostic canonical-commit hook (any node, not just the leader)
	snapshotTree          *snapshot.Tree
	jmtCommitment         *commitment.JMTCommitment
	jmtEnabled            bool
	jmtForBlockProcessing bool   // true for fresh chains (private/dev) where JMT is used from genesis
	jmtStoreRefresh       func() // called after block commit to refresh JMT backing store tx

	bmtCommitment *commitment.BMTCommitment
	bmtEnabled    bool

	verkleCommitment *commitment.VerkleCommitment
	verkleEnabled    bool

	mptRootComputer *commitment.MPTRootComputer
	mptEnabled      bool

	qmdbRootComputer *commitment.QMDBRootComputer
	minerRC          *commitment.QMDBRootComputer // persistent speculative-build computer (guarded by minerRCMu; see NewMinerRootComputer)
	minerRCMu        sync.Mutex                   // serializes the startup pre-warm against the first leader build
	qmdbEnabled      bool

	ltHashCommitment   *commitment.LtHashCommitment
	ltHashEnabled      bool
	rootComputer       state.RootComputer
	stateProofProvider StateProofProvider

	witnessCache *lru.Cache[types.Hash, *witness.BlockWitness]

	zkProving      bool
	zkRequireProof bool
	zkVerifier     *zkverifier.Verifier

	wg sync.WaitGroup

	procInterrupt int32
	futureBlocks  *lru.Cache[types.Hash, *block.Block]
	receiptCache  *lru.Cache[types.Hash, []*block.Receipt]
	blockCache    *lru.Cache[types.Hash, *block.Block]

	headerCache *lru.Cache[types.Hash, *block.Header]
	numberCache *lru.Cache[types.Hash, uint64]
	tdCache     *lru.Cache[types.Hash, *uint256.Int]

	forker    *ForkChoice
	validator Validator

	// committeePool, when set, builds the per-block BLS committee evidence (the
	// simulated 200K-voter / 512-committee multi-sig carried over from the replay
	// reseal) and persists it alongside each block. Optional — nil leaves the
	// chain's write path unchanged.
	committeePool CommitteeEvidenceBuilder
}

// CommitteeEvidenceBuilder builds the consensus evidence for a freshly persisted
// block, folding in any real validator signatures collected for it (identical to
// the all-simulated evidence when none). Implemented by *blspool.Pool (via
// BuildBlockEvidence); an interface keeps the blockchain decoupled from the
// blspool package and its BLS types.
type CommitteeEvidenceBuilder interface {
	BuildBlockEvidence(blockNum uint64, blockHash, receiptRoot types.Hash) (*rawdb.ConsensusEvidence, error)
}

type insertStats struct {
	queued, processed, ignored int
	usedGas                    uint64
	lastIndex                  int
	startTime                  time.Time
}

// envInt reads a positive integer from the environment, falling back to def on
// anything unset, unparseable or non-positive. Used only for benchmark
// instruments; a production value belongs in conf, not here.
func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
