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

package internal

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	prometheus "github.com/n42blockchain/N42/common/metrics"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/lib/kv"
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
)

// =============================================================================
// Metrics
// =============================================================================

var (
	headBlockGauge       = prometheus.GetOrCreateCounter("chain_head_block", true)
	blockInsertTimer     = prometheus.GetOrCreateHistogram("chain_inserts")
	blockValidationTimer = prometheus.GetOrCreateHistogram("chain_validation")
	blockExecutionTimer  = prometheus.GetOrCreateHistogram("chain_execution")
	blockWriteTimer      = prometheus.GetOrCreateHistogram("chain_write")

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
	NonStatTy   WriteStatus = iota
	CanonStatTy
	SideStatTy
)

const (
	blockCacheLimit     = 512
	receiptsCacheLimit  = 256
	maxFutureBlocks     = 256
	maxTimeFutureBlocks = 5 * 60 // 5 minutes

	headerCacheLimit = 1024
	tdCacheLimit     = 512
	numberCacheLimit = 2048
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

	insertLock    chan struct{}
	latestBlockCh chan block.IBlock
	lock          sync.Mutex

	peers map[peer.ID]bool

	chBlocks chan block.IBlock

	p2p p2p.P2P

	errorCh chan error

	process Processor

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
}

type insertStats struct {
	queued, processed, ignored int
	usedGas                    uint64
	lastIndex                  int
	startTime                  time.Time
}
