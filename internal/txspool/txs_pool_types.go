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
// Shared types, configuration and sentinel errors for the N42
// transaction pool: TxPoolConfig defaults, TxStatus enum, journal
// entry record, and ErrAlreadyKnown / ErrUnderpriced / ErrTxPoolFull
// style errors consumed by both the local pool and the encrypted
// variant.

package txspool

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/contracts/deposit"
	"github.com/n42blockchain/N42/params"
)

const (
	// txSlotSize is used to calculate how many data slots a single transaction
	// takes up based on its size. The slots are used as DoS protection, ensuring
	// that validating a new transaction remains a constant operation (in reality
	// O(maxslots), where max slots are 4 currently).
	txSlotSize = 32 * 1024

	// txMaxSize is the maximum size a single transaction can have. This field has
	// non-trivial consequences: larger transactions are significantly harder and
	// more expensive to propagate; larger transactions also take more resources
	// to validate whether they fit into the pool or not.
	txMaxSize = 4 * txSlotSize // 128KB
)

var (
	ErrAlreadyKnown       = errors.New("already known")
	ErrInvalidSender      = errors.New("invalid sender")
	ErrOversizedData      = errors.New("oversized data")
	ErrNegativeValue      = errors.New("negative value")
	ErrGasLimit           = errors.New("exceeds block gas limit")
	ErrUnderpriced        = errors.New("transaction underpriced")
	ErrTxPoolOverflow     = errors.New("txpool is full")
	ErrReplaceUnderpriced = errors.New("replacement transaction underpriced")
	ErrFeeCapVeryHigh     = errors.New("max fee per gas higher than 2^256-1")
	ErrNonceTooLow        = errors.New("nonce too low")
	ErrNonceTooHigh       = errors.New("nonce too high")
	ErrInsufficientFunds  = errors.New("insufficient funds for gas * price + value")
	ErrTipAboveFeeCap     = errors.New("max priority fee per gas higher than max fee per gas")

	// EIP-7702 (SetCode) authorization pool restrictions — mirror go-ethereum's
	// legacypool. Prevent an attacker from stacking transactions on an account
	// that has an in-flight delegation, or reserving an authority with multiple
	// in-flight transactions.
	ErrInflightTxLimitReached    = errors.New("in-flight transaction limit reached for delegated accounts")
	ErrAuthorityReserved         = errors.New("authority already reserved")
	ErrOutOfOrderTxFromDelegated = errors.New("origin account that has gapped nonce cannot be delegated")
	ErrSetCodeTxCreate           = errors.New("setcode transaction cannot be used to create contract")
	ErrSetCodeTxNoAuth           = errors.New("setcode transaction with no authorization list")

	pendingGauge = prometheus.GetOrCreateCounter("txpool_pending", true)
	queuedGauge  = prometheus.GetOrCreateCounter("txpool_queued", true)
	localGauge   = prometheus.GetOrCreateCounter("txpool_local", true)

	txpoolAddedTotal       = prometheus.GetOrCreateCounter("txpool_added_total")
	txpoolDroppedTotal     = prometheus.GetOrCreateCounter("txpool_dropped_total")
	txpoolRejectedTotal    = prometheus.GetOrCreateCounter("txpool_rejected_total")
	txpoolUnderpricedTotal = prometheus.GetOrCreateCounter("txpool_underpriced_total")
	txpoolOverflowTotal    = prometheus.GetOrCreateCounter("txpool_overflow_total")
)

type txspoolResetRequest struct {
	oldBlock, newBlock block.IBlock
}

// TxsPoolConfig contains the configuration for the transaction pool.
type TxsPoolConfig struct {
	Locals   []types.Address
	NoLocals bool

	PriceLimit uint64
	PriceBump  uint64

	AccountSlots uint64
	GlobalSlots  uint64
	AccountQueue uint64
	GlobalQueue  uint64

	Lifetime time.Duration

	// DynamicSizing enables memory-aware pool capacity adjustment.
	// When enabled, the pool periodically checks heap usage and scales
	// GlobalSlots between MinGlobalSlots and GlobalSlots.
	DynamicSizing  bool   `json:"dynamic_sizing" yaml:"dynamic_sizing"`
	MinGlobalSlots uint64 `json:"min_global_slots" yaml:"min_global_slots"`
	MemoryLimitMB  uint64 `json:"memory_limit_mb" yaml:"memory_limit_mb"`
}

// DefaultTxPoolConfig contains the default configuration for the transaction pool.
//
// The capacity fields can be raised from the environment. Nothing plumbs this
// config from the node configuration -- NewTxsPool uses the defaults verbatim
// -- so without these there is no way to size the pool at all. The stock
// capacity is about 6,000 transactions in total, which is smaller than a single
// block once the gas ceiling is raised: at 480M gas a block holds 22,857, so a
// throughput test spends its time being rejected rather than measuring
// anything. Same idiom as N42_STRESS_GASLIMIT and N42_MAX_GOSSIP_MB, and the
// same caveat: sizing the pool up costs memory, roughly proportional to the
// number of transactions held.
var DefaultTxPoolConfig = applyTxPoolEnvOverrides(TxsPoolConfig{
	PriceLimit: 1,
	PriceBump:  10,

	AccountSlots: 16,
	GlobalSlots:  4096 + 1024,
	AccountQueue: 64,
	GlobalQueue:  1024,

	Lifetime: 3 * time.Hour,

	DynamicSizing:  false,
	MinGlobalSlots: 1024,
	MemoryLimitMB:  4096, // 4GB default memory limit
})

// applyTxPoolEnvOverrides raises pool capacity from the environment. Values
// below the built-in default are ignored: these knobs exist to make room for a
// deep backlog, not to shrink the pool by accident.
func applyTxPoolEnvOverrides(c TxsPoolConfig) TxsPoolConfig {
	set := func(dst *uint64, name string) {
		v := os.Getenv(name)
		if v == "" {
			return
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n <= *dst {
			return
		}
		*dst = n
	}
	set(&c.GlobalSlots, "N42_TXPOOL_GLOBAL_SLOTS")
	set(&c.GlobalQueue, "N42_TXPOOL_GLOBAL_QUEUE")
	set(&c.AccountSlots, "N42_TXPOOL_ACCOUNT_SLOTS")
	set(&c.AccountQueue, "N42_TXPOOL_ACCOUNT_QUEUE")
	if c.MinGlobalSlots > c.GlobalSlots {
		c.MinGlobalSlots = c.GlobalSlots
	}
	return c
}

// TxsPool contains all currently known transactions.
type TxsPool struct {
	config      TxsPoolConfig
	chainconfig *params.ChainConfig

	bc common.IBlockChain

	currentState  ReadState
	pendingNonces *txNoncer
	currentMaxGas uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex

	// Fork indicators.
	istanbul bool
	eip2718  bool
	eip1559  bool

	locals   *accountSet
	pending  map[types.Address]*txsList
	queue    map[types.Address]*txsList
	beats    map[types.Address]time.Time
	all      *txLookup
	priced   *txPricedList
	gasPrice *uint256.Int

	// Channels for scheduling.
	reqResetCh     chan *txspoolResetRequest
	reqPromoteCh   chan *accountSet
	queueTxEventCh chan *transaction.Transaction
	reorgDoneCh    chan chan struct{}

	changesSinceReorg int

	deposit *deposit.Deposit

	// Dynamic sizing: effective slots may be lower than config.GlobalSlots
	// when memory pressure is detected.
	effectiveGlobalSlots atomic.Uint64

	// schedulerLive is set once scheduleLoop is running. Every ingestion path
	// ends in requestPromoteExecutables, an unbuffered send on reqPromoteCh
	// that only scheduleLoop drains — so ingesting before it exists blocks the
	// caller forever. NewTxsPool orders the two correctly; this flag makes a
	// re-introduction a logged, survivable skip instead of a node that never
	// finishes starting.
	schedulerLive atomic.Bool
}

// addressByHeartbeat is an account address tagged with its last activity timestamp.
type addressByHeartbeat struct {
	address   types.Address
	heartbeat time.Time
}

type addressesByHeartbeat []addressByHeartbeat

func (a addressesByHeartbeat) Len() int           { return len(a) }
func (a addressesByHeartbeat) Less(i, j int) bool { return a[i].heartbeat.Before(a[j].heartbeat) }
func (a addressesByHeartbeat) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
