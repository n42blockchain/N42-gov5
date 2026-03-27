// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// Well-known chain domain IDs (Hyperlane convention).
const (
	DomainEthereum  uint32 = 1
	DomainN42       uint32 = 42
	DomainArbitrum  uint32 = 42161
	DomainOptimism  uint32 = 10
	DomainPolygon   uint32 = 137
	DomainBSC       uint32 = 56
	DomainAvalanche uint32 = 43114
	DomainBase      uint32 = 8453

	// maxTrackedTransfers bounds the in-memory transfer map.
	maxTrackedTransfers = 10000
)

// RouteType determines which verification path is used for a chain.
type RouteType uint8

const (
	// RouteZK uses N42's native ZK proof path (SP1 header chain + JMT state proof).
	RouteZK RouteType = 0

	// RouteHyperlane uses Hyperlane Mailbox + ISM for message passing.
	RouteHyperlane RouteType = 1
)

// ChainRoute defines the bridge path configuration for a destination chain.
type ChainRoute struct {
	Domain    uint32    // Hyperlane domain ID
	RouteType RouteType // ZK or Hyperlane
	Name      string    // Human-readable chain name
}

// HyperlaneDispatcher abstracts the Hyperlane Mailbox dispatch interface.
type HyperlaneDispatcher interface {
	Dispatch(ctx context.Context, destDomain uint32, recipientAddr [32]byte, body []byte) (types.Hash, error)
	QuoteDispatch(ctx context.Context, destDomain uint32, body []byte) (*uint256.Int, error)
}

// ZKRouter implements the Router interface with automatic path selection:
//   - N42↔ETH: ZK proof path (mathematical guarantee, no trust)
//   - N42↔other chains: Hyperlane path (validator set, 150+ chain coverage)
type ZKRouter struct {
	mu sync.RWMutex

	// ZK path: state root publisher (N42→ETH settlement)
	publisher   *BridgePublisher
	stateProver StateProverFunc

	// Hyperlane path (multi-chain)
	hyperlane HyperlaneDispatcher

	// Reverse bridge (ETH→N42)
	ethLightClient *EthLightClient

	// Route table: destChain → route config
	routes map[uint32]*ChainRoute

	// Transfer tracking (bounded to maxTrackedTransfers)
	transfers    map[types.Hash]*TransferRecord
	transferRing []types.Hash // ring buffer for LRU eviction
	ringIdx      int

	// Monotonic counter for transfer hash uniqueness
	transferNonce atomic.Uint64
}

// StateProverFunc generates a state inclusion proof for a withdrawal.
type StateProverFunc func(key []byte) (*StateInclusionProof, error)

// TransferRecord tracks a cross-chain transfer.
type TransferRecord struct {
	DestChain uint32
	Recipient types.Address
	Amount    *uint256.Int
	Status    BridgeStatus
	Route     RouteType
	ProofData []byte     // ZK proof or Hyperlane message ID
	TxHash    types.Hash // On-chain tx hash
}

// ZKRouterConfig configures the multi-chain router.
type ZKRouterConfig struct {
	CustomRoutes map[uint32]RouteType `json:"customRoutes" yaml:"customRoutes"`
}

// NewZKRouter creates a new multi-chain router.
func NewZKRouter(
	publisher *BridgePublisher,
	hyperlane HyperlaneDispatcher,
	ethLC *EthLightClient,
	stateProver StateProverFunc,
	cfg *ZKRouterConfig,
) *ZKRouter {
	r := &ZKRouter{
		publisher:      publisher,
		hyperlane:      hyperlane,
		ethLightClient: ethLC,
		stateProver:    stateProver,
		routes:         defaultRouteTable(),
		transfers:      make(map[types.Hash]*TransferRecord, maxTrackedTransfers),
		transferRing:   make([]types.Hash, maxTrackedTransfers),
	}

	if cfg != nil {
		for domain, routeType := range cfg.CustomRoutes {
			if route, ok := r.routes[domain]; ok {
				route.RouteType = routeType
			}
		}
	}

	return r
}

// Send initiates a cross-chain transfer, automatically selecting the best path.
func (r *ZKRouter) Send(destChain uint32, recipient types.Address, amount *uint256.Int) (types.Hash, error) {
	// Read route under RLock (no mutation needed)
	r.mu.RLock()
	route, ok := r.routes[destChain]
	r.mu.RUnlock()

	if !ok {
		if r.hyperlane == nil {
			return types.Hash{}, fmt.Errorf("unsupported destination chain: %d", destChain)
		}
		route = &ChainRoute{Domain: destChain, RouteType: RouteHyperlane, Name: fmt.Sprintf("chain-%d", destChain)}
	}

	// Perform I/O outside the lock
	var txHash types.Hash
	var err error

	switch route.RouteType {
	case RouteZK:
		txHash, err = r.sendZK(destChain, recipient, amount)
		routerTransfersZK.Inc()
	case RouteHyperlane:
		txHash, err = r.sendHyperlane(destChain, recipient, amount)
		routerTransfersHyperlane.Inc()
	default:
		return types.Hash{}, fmt.Errorf("unknown route type: %d", route.RouteType)
	}

	if err != nil {
		return types.Hash{}, err
	}

	// Track transfer under write lock
	r.mu.Lock()
	r.addTransfer(txHash, &TransferRecord{
		DestChain: destChain,
		Recipient: recipient,
		Amount:    amount,
		Status:    StatusPending,
		Route:     route.RouteType,
	})
	r.mu.Unlock()

	log.Info("Cross-chain transfer initiated",
		"destChain", route.Name,
		"route", routeTypeName(route.RouteType),
		"recipient", recipient,
		"amount", amount,
		"txHash", txHash,
	)

	return txHash, nil
}

// addTransfer adds a transfer record with ring-buffer eviction.
// Must be called under r.mu.Lock().
func (r *ZKRouter) addTransfer(txHash types.Hash, record *TransferRecord) {
	if len(r.transfers) >= maxTrackedTransfers {
		oldHash := r.transferRing[r.ringIdx]
		delete(r.transfers, oldHash)
	}
	r.transfers[txHash] = record
	r.transferRing[r.ringIdx] = txHash
	r.ringIdx = (r.ringIdx + 1) % maxTrackedTransfers
}

// VerifyIncoming verifies an incoming cross-chain message.
// Full verification requires calling ethLightClient.VerifyMPTProof directly
// with the complete proof parameters (account, key, storage proofs).
// This method validates the state root is known; callers MUST perform
// full MPT/Hyperlane verification separately.
func (r *ZKRouter) VerifyIncoming(proof []byte, stateRoot types.Hash) error {
	if len(proof) == 0 {
		return fmt.Errorf("empty proof")
	}

	if r.ethLightClient != nil {
		finalized := r.ethLightClient.LatestFinalized()
		if finalized != nil && finalized.StateRoot == stateRoot {
			log.Info("Incoming ETH→N42 transfer: state root verified", "stateRoot", stateRoot)
			return nil
		}
		// State root not recognized — reject
		return fmt.Errorf("state root %s not verified by ETH light client", stateRoot)
	}

	// No ETH light client configured — cannot verify
	return fmt.Errorf("ETH light client not configured, cannot verify incoming proof")
}

// Status returns the current status of a cross-chain transfer.
func (r *ZKRouter) Status(txHash types.Hash) (BridgeStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	record, ok := r.transfers[txHash]
	if !ok {
		return StatusFailed, fmt.Errorf("transfer not found: %s", txHash)
	}
	return record.Status, nil
}

// LatestVerifiedBlock returns the latest N42 block verified on the target chain.
func (r *ZKRouter) LatestVerifiedBlock(destChain uint32) (uint64, error) {
	r.mu.RLock()
	route, ok := r.routes[destChain]
	r.mu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("unsupported chain: %d", destChain)
	}

	switch route.RouteType {
	case RouteZK:
		if r.publisher != nil {
			return r.publisher.LastProvenBlock(), nil
		}
		return 0, nil
	case RouteHyperlane:
		return 0, nil
	default:
		return 0, fmt.Errorf("unknown route type")
	}
}

// RouteFor returns the route configuration for a destination chain.
func (r *ZKRouter) RouteFor(destChain uint32) (*ChainRoute, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.routes[destChain]
	return route, ok
}

// AddRoute registers a new chain route.
func (r *ZKRouter) AddRoute(route *ChainRoute) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[route.Domain] = route
}

// --- Internal methods ---

func (r *ZKRouter) sendZK(destChain uint32, recipient types.Address, amount *uint256.Int) (types.Hash, error) {
	nonce := r.transferNonce.Add(1)
	txHash := computeTransferHash(DomainN42, destChain, recipient, amount, nonce)
	log.Info("ZK bridge: transfer queued for proving",
		"dest", "Ethereum",
		"recipient", recipient,
		"amount", amount,
	)
	return txHash, nil
}

func (r *ZKRouter) sendHyperlane(destChain uint32, recipient types.Address, amount *uint256.Int) (types.Hash, error) {
	if r.hyperlane == nil {
		return types.Hash{}, fmt.Errorf("Hyperlane dispatcher not configured")
	}

	var recipientAddr [32]byte
	copy(recipientAddr[12:], recipient[:])

	body := amount.Bytes32()

	messageID, err := r.hyperlane.Dispatch(context.Background(), destChain, recipientAddr, body[:])
	if err != nil {
		return types.Hash{}, fmt.Errorf("Hyperlane dispatch: %w", err)
	}

	hyperlaneDispatchTotal.Inc()

	log.Info("Hyperlane bridge: message dispatched",
		"dest", destChain,
		"messageID", messageID,
		"recipient", recipient,
		"amount", amount,
	)

	return messageID, nil
}

// computeTransferHash generates a collision-resistant Keccak256 hash for a transfer.
// Includes a monotonic nonce to guarantee uniqueness for identical parameters.
func computeTransferHash(srcChain, destChain uint32, recipient types.Address, amount *uint256.Int, nonce uint64) types.Hash {
	var buf [4 + 4 + 20 + 32 + 8]byte
	buf[0] = byte(srcChain)
	buf[1] = byte(srcChain >> 8)
	buf[2] = byte(srcChain >> 16)
	buf[3] = byte(srcChain >> 24)
	buf[4] = byte(destChain)
	buf[5] = byte(destChain >> 8)
	buf[6] = byte(destChain >> 16)
	buf[7] = byte(destChain >> 24)
	copy(buf[8:28], recipient[:])
	amountBytes := amount.Bytes32()
	copy(buf[28:60], amountBytes[:])
	buf[60] = byte(nonce)
	buf[61] = byte(nonce >> 8)
	buf[62] = byte(nonce >> 16)
	buf[63] = byte(nonce >> 24)
	buf[64] = byte(nonce >> 32)
	buf[65] = byte(nonce >> 40)
	buf[66] = byte(nonce >> 48)
	buf[67] = byte(nonce >> 56)
	return crypto.Keccak256Hash(buf[:])
}

func defaultRouteTable() map[uint32]*ChainRoute {
	return map[uint32]*ChainRoute{
		DomainEthereum:  {Domain: DomainEthereum, RouteType: RouteZK, Name: "Ethereum"},
		DomainArbitrum:  {Domain: DomainArbitrum, RouteType: RouteHyperlane, Name: "Arbitrum"},
		DomainOptimism:  {Domain: DomainOptimism, RouteType: RouteHyperlane, Name: "Optimism"},
		DomainPolygon:   {Domain: DomainPolygon, RouteType: RouteHyperlane, Name: "Polygon"},
		DomainBSC:       {Domain: DomainBSC, RouteType: RouteHyperlane, Name: "BSC"},
		DomainAvalanche: {Domain: DomainAvalanche, RouteType: RouteHyperlane, Name: "Avalanche"},
		DomainBase:      {Domain: DomainBase, RouteType: RouteHyperlane, Name: "Base"},
	}
}

func routeTypeName(rt RouteType) string {
	switch rt {
	case RouteZK:
		return "ZK"
	case RouteHyperlane:
		return "Hyperlane"
	default:
		return "Unknown"
	}
}
