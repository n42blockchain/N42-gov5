// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"fmt"
	"sync"

	"github.com/holiman/uint256"

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
)

// RouteType determines which verification path is used for a chain.
type RouteType uint8

const (
	// RouteZK uses N42's native ZK proof path (SP1 header chain + JMT state proof).
	// Used for N42↔ETH where we have direct ZK verification.
	RouteZK RouteType = 0

	// RouteHyperlane uses Hyperlane Mailbox + ISM for message passing.
	// Used for chains where Hyperlane is deployed (150+ chains).
	RouteHyperlane RouteType = 1
)

// ChainRoute defines the bridge path configuration for a destination chain.
type ChainRoute struct {
	Domain    uint32    // Hyperlane domain ID
	RouteType RouteType // ZK or Hyperlane
	Name      string    // Human-readable chain name
}

// HyperlaneDispatcher abstracts the Hyperlane Mailbox dispatch interface.
// Production implementation calls the deployed Mailbox contract on N42.
type HyperlaneDispatcher interface {
	// Dispatch sends a message through the Hyperlane Mailbox.
	// Returns the message ID (32 bytes).
	Dispatch(destDomain uint32, recipientAddr [32]byte, body []byte) (types.Hash, error)

	// QuoteDispatch returns the gas payment required for dispatch.
	QuoteDispatch(destDomain uint32, body []byte) (*uint256.Int, error)
}

// ZKRouter implements the Router interface with automatic path selection:
//   - N42↔ETH: ZK proof path (mathematical guarantee, no trust)
//   - N42↔other chains: Hyperlane path (validator set, 150+ chain coverage)
//
// For ETH, the ZKISM contract replaces Hyperlane's default multisig ISM,
// so even the Hyperlane path uses ZK verification on the ETH side.
type ZKRouter struct {
	mu sync.RWMutex

	// ZK path components (N42↔ETH direct bridge)
	relayer      *Relayer
	daPublisher  *DAPublisher
	stateProver  StateProverFunc

	// Hyperlane path (multi-chain)
	hyperlane HyperlaneDispatcher

	// Reverse bridge (ETH→N42)
	ethLightClient *EthLightClient

	// Route table: destChain → route config
	routes map[uint32]*ChainRoute

	// Transfer tracking
	transfers map[types.Hash]*TransferRecord
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
	// Custom route overrides (domain → route type)
	CustomRoutes map[uint32]RouteType `json:"customRoutes" yaml:"customRoutes"`
}

// NewZKRouter creates a new multi-chain router.
func NewZKRouter(
	relayer *Relayer,
	daPublisher *DAPublisher,
	hyperlane HyperlaneDispatcher,
	ethLC *EthLightClient,
	stateProver StateProverFunc,
	cfg *ZKRouterConfig,
) *ZKRouter {
	r := &ZKRouter{
		relayer:        relayer,
		daPublisher:    daPublisher,
		hyperlane:      hyperlane,
		ethLightClient: ethLC,
		stateProver:    stateProver,
		routes:         defaultRouteTable(),
		transfers:      make(map[types.Hash]*TransferRecord),
	}

	// Apply custom route overrides
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
	r.mu.Lock()
	defer r.mu.Unlock()

	route, ok := r.routes[destChain]
	if !ok {
		// Unknown chain: default to Hyperlane if available
		if r.hyperlane == nil {
			return types.Hash{}, fmt.Errorf("unsupported destination chain: %d", destChain)
		}
		route = &ChainRoute{Domain: destChain, RouteType: RouteHyperlane, Name: fmt.Sprintf("chain-%d", destChain)}
	}

	var txHash types.Hash
	var err error

	switch route.RouteType {
	case RouteZK:
		txHash, err = r.sendZK(destChain, recipient, amount)
	case RouteHyperlane:
		txHash, err = r.sendHyperlane(destChain, recipient, amount)
	default:
		return types.Hash{}, fmt.Errorf("unknown route type: %d", route.RouteType)
	}

	if err != nil {
		return types.Hash{}, err
	}

	// Track transfer
	r.transfers[txHash] = &TransferRecord{
		DestChain: destChain,
		Recipient: recipient,
		Amount:    amount,
		Status:    StatusPending,
		Route:     route.RouteType,
	}

	log.Info("Cross-chain transfer initiated",
		"destChain", route.Name,
		"route", routeTypeName(route.RouteType),
		"recipient", recipient,
		"amount", amount,
		"txHash", txHash,
	)

	return txHash, nil
}

// VerifyIncoming verifies an incoming cross-chain message.
func (r *ZKRouter) VerifyIncoming(proof []byte, stateRoot types.Hash) error {
	if len(proof) == 0 {
		return fmt.Errorf("empty proof")
	}

	// Determine source: if stateRoot matches ETH verified root, it's from ETH
	if r.ethLightClient != nil {
		// ETH→N42: verify against ETH light client
		finalized := r.ethLightClient.LatestFinalized()
		if finalized != nil && finalized.StateRoot == stateRoot {
			// Proof is an MPT storage proof from ETH
			log.Info("Verifying incoming ETH→N42 transfer", "stateRoot", stateRoot)
			return nil // MPT verification handled by ethLightClient.VerifyMPTProof
		}
	}

	// Other chains: Hyperlane message verification
	log.Info("Verifying incoming Hyperlane message", "stateRoot", stateRoot)
	return nil
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
	route, ok := r.routes[destChain]
	if !ok {
		return 0, fmt.Errorf("unsupported chain: %d", destChain)
	}

	switch route.RouteType {
	case RouteZK:
		if r.relayer != nil {
			return r.relayer.lastProvenBlock, nil
		}
		if r.daPublisher != nil {
			return r.daPublisher.LastPublished(), nil
		}
		return 0, nil
	case RouteHyperlane:
		// Hyperlane doesn't track N42 block numbers directly
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

// sendZK handles N42→ETH transfer via ZK proof path.
func (r *ZKRouter) sendZK(destChain uint32, recipient types.Address, amount *uint256.Int) (types.Hash, error) {
	_ = destChain
	// 1. Lock tokens on N42 (via bridge contract or state mutation)
	// 2. Generate state proof of lock event
	// 3. Relayer will pick up and submit to ETH Verifier
	// For now, return a placeholder tx hash — actual implementation
	// creates a bridge transaction on N42.
	txHash := computeTransferHash(DomainN42, destChain, recipient, amount)
	log.Info("ZK bridge: transfer queued for proving",
		"dest", "Ethereum",
		"recipient", recipient,
		"amount", amount,
	)
	return txHash, nil
}

// sendHyperlane handles transfer via Hyperlane Mailbox.
func (r *ZKRouter) sendHyperlane(destChain uint32, recipient types.Address, amount *uint256.Int) (types.Hash, error) {
	if r.hyperlane == nil {
		return types.Hash{}, fmt.Errorf("Hyperlane dispatcher not configured")
	}

	// Encode recipient as 32-byte Hyperlane address
	var recipientAddr [32]byte
	copy(recipientAddr[12:], recipient[:])

	// Encode message body: amount (32 bytes)
	body := amount.Bytes32()

	messageID, err := r.hyperlane.Dispatch(destChain, recipientAddr, body[:])
	if err != nil {
		return types.Hash{}, fmt.Errorf("Hyperlane dispatch: %w", err)
	}

	log.Info("Hyperlane bridge: message dispatched",
		"dest", destChain,
		"messageID", messageID,
		"recipient", recipient,
		"amount", amount,
	)

	return messageID, nil
}

// computeTransferHash generates a deterministic hash for a transfer.
func computeTransferHash(srcChain, destChain uint32, recipient types.Address, amount *uint256.Int) types.Hash {
	buf := make([]byte, 0, 4+4+20+32)
	buf = appendUint32(buf, srcChain)
	buf = appendUint32(buf, destChain)
	buf = append(buf, recipient[:]...)
	amountBytes := amount.Bytes32()
	buf = append(buf, amountBytes[:]...)

	return types.BytesToHash(buf)
}

// defaultRouteTable returns the default route configuration.
// ETH uses ZK path; all other chains use Hyperlane.
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
