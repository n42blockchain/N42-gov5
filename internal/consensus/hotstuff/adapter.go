// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package hotstuff provides a HotStuff-2 BFT consensus engine adapter for N42.
//
// This file adapts the core HotStuff-2 state machine to the N42 consensus.Engine
// interface, bridging the BFT protocol with the block production pipeline.
//
// The HotStuff-2 engine operates as a backup consensus mechanism. It can be activated
// by setting consensus type to "hotstuff" in chain configuration.

package hotstuff

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto/bls/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Header extra-data layout:
// [0..3]   = magic bytes "N42H" (4 bytes)
// [4..11]  = view number (8 bytes LE)
// [12..end] = encoded QC (variable length)
const (
	extraMagicLen   = 4
	extraViewLen    = 8
	extraMinLen     = extraMagicLen + extraViewLen
	inmemorySignatures = 4096
	inmemorySnapshots  = 128
)

var extraMagic = [extraMagicLen]byte{'N', '4', '2', 'H'}

// HotStuff implements the consensus.Engine interface for HotStuff-2 BFT consensus.
type HotStuff struct {
	config      *params.HotStuffConfig
	chainConfig *params.ChainConfig

	ctx    context.Context
	cancel context.CancelFunc

	// BLS key management
	secretKey common.SecretKey
	signer    types.Address
	lock      sync.RWMutex

	// Caches
	signatures *lru.ARCCache

	// Core consensus engine (nil until Start() is called with validator set)
	engine       *ConsensusEngine
	epochManager *EpochManager

	// Output channel for consensus actions
	outputCh chan EngineOutput
}

// New creates a new HotStuff consensus engine.
func New(config *params.HotStuffConfig, chainConfig *params.ChainConfig) *HotStuff {
	ctx, cancel := context.WithCancel(context.Background())
	signatures, err := lru.NewARC(inmemorySignatures)
	if err != nil {
		// This should never happen with a positive capacity constant.
		panic(fmt.Sprintf("hotstuff: failed to create LRU cache: %v", err))
	}

	conf := config
	if conf == nil {
		conf = &params.HotStuffConfig{
			BaseTimeout: 60000,
			MaxTimeout:  120000,
		}
	}

	return &HotStuff{
		config:      conf,
		chainConfig: chainConfig,
		ctx:         ctx,
		cancel:      cancel,
		signatures:  signatures,
		outputCh:    make(chan EngineOutput, 256),
	}
}

// Authorize injects a BLS private key and the corresponding address into the engine.
func (h *HotStuff) Authorize(signer types.Address, secretKey common.SecretKey) {
	h.lock.Lock()
	defer h.lock.Unlock()
	h.signer = signer
	h.secretKey = secretKey
}

// InitEngine initializes the core consensus engine with the given validator set.
// Must be called after Authorize and before participating in consensus.
func (h *HotStuff) InitEngine(validators []ValidatorInfo, faultTolerance uint32) error {
	h.lock.Lock()
	defer h.lock.Unlock()

	if h.secretKey == nil {
		return errors.New("hotstuff: secret key not set, call Authorize first")
	}

	vs := NewValidatorSet(validators, faultTolerance)
	myIndex := vs.FindByAddress(h.signer)
	if myIndex < 0 {
		return fmt.Errorf("hotstuff: signer %s not found in validator set", h.signer.Hex())
	}

	h.epochManager = NewEpochManager(vs)
	h.engine = NewConsensusEngineWithEpochManager(
		ValidatorIndex(myIndex),
		h.secretKey,
		h.epochManager,
		h.config.BaseTimeout,
		h.config.MaxTimeout,
		h.outputCh,
	)

	log.Info("HotStuff consensus engine initialized",
		"signer", h.signer, "index", myIndex,
		"validators", vs.Len(), "quorum", vs.QuorumSize())

	return nil
}

// Engine returns the inner consensus state machine (may be nil if not initialized).
func (h *HotStuff) Engine() *ConsensusEngine {
	h.lock.RLock()
	defer h.lock.RUnlock()
	return h.engine
}

// OutputCh returns the channel for receiving consensus output actions.
func (h *HotStuff) OutputCh() <-chan EngineOutput {
	return h.outputCh
}

// ============================================================================
// consensus.EngineReader implementation
// ============================================================================

// Author extracts the signer address from the block header's extra data.
func (h *HotStuff) Author(iHeader block.IHeader) (types.Address, error) {
	header, ok := iHeader.(*block.Header)
	if !ok {
		return types.Address{}, errors.New("invalid header type: expected *block.Header")
	}

	// For HotStuff, the coinbase IS the block author (set during Prepare).
	return header.Coinbase, nil
}

// IsServiceTransaction returns false — HotStuff has no service transactions.
func (h *HotStuff) IsServiceTransaction(sender types.Address, syscall consensus.SystemCall) bool {
	return false
}

// Type returns the consensus engine type.
func (h *HotStuff) Type() params.ConsensusType {
	return params.HotStuffConsensus
}

// ============================================================================
// consensus.Engine implementation
// ============================================================================

// VerifyHeader checks whether a header conforms to the HotStuff consensus rules.
func (h *HotStuff) VerifyHeader(chain consensus.ChainHeaderReader, iHeader block.IHeader, seal bool) error {
	header, ok := iHeader.(*block.Header)
	if !ok {
		return errors.New("invalid header type: expected *block.Header")
	}

	if header.Number == nil {
		return errors.New("header number is nil")
	}

	// Genesis block has no consensus fields to verify.
	if header.Number.IsZero() {
		return nil
	}

	// Verify extra-data contains valid magic bytes.
	if len(header.Extra) < extraMinLen {
		return fmt.Errorf("extra-data too short: %d < %d", len(header.Extra), extraMinLen)
	}

	var magic [extraMagicLen]byte
	copy(magic[:], header.Extra[:extraMagicLen])
	if magic != extraMagic {
		return fmt.Errorf("invalid extra-data magic: expected N42H, got %s", string(magic[:]))
	}

	// Verify timestamp is after parent.
	parent := chain.GetHeaderByNumber(new(uint256.Int).Sub(header.Number, uint256.NewInt(1)))
	if parent == nil {
		return errors.New("unknown parent")
	}
	parentHeader, ok := parent.(*block.Header)
	if !ok {
		return errors.New("invalid parent header type")
	}

	if header.Time <= parentHeader.Time {
		return errors.New("timestamp must be after parent")
	}

	return nil
}

// VerifyHeaders verifies a batch of headers concurrently.
func (h *HotStuff) VerifyHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))

	go func() {
		defer close(results)
		for _, header := range headers {
			select {
			case <-abort:
				return
			default:
			}
			err := h.VerifyHeader(chain, header, true)
			select {
			case results <- err:
			case <-abort:
				return
			}
		}
	}()

	return abort, results
}

// VerifyUncles always returns nil — HotStuff has no uncle blocks.
func (h *HotStuff) VerifyUncles(chain consensus.ConsensusChainReader, b block.IBlock) error {
	return nil
}

// Prepare initializes the consensus fields of a block header.
func (h *HotStuff) Prepare(chain consensus.ChainHeaderReader, iHeader block.IHeader) error {
	header, ok := iHeader.(*block.Header)
	if !ok {
		return errors.New("invalid header type: expected *block.Header")
	}

	h.lock.RLock()
	signer := h.signer
	h.lock.RUnlock()

	// Set the coinbase to the signer.
	header.Coinbase = signer

	// Set difficulty to 1 (HotStuff uses view numbers, not difficulty).
	header.Difficulty = uint256.NewInt(1)

	// Encode view number in extra-data.
	var view ViewNumber
	if h.engine != nil {
		view = h.engine.CurrentView()
	}

	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])
	binary.LittleEndian.PutUint64(extra[extraMagicLen:], view)
	header.Extra = extra

	// Set timestamp.
	parent := chain.GetHeaderByNumber(new(uint256.Int).Sub(header.Number, uint256.NewInt(1)))
	if parent != nil {
		parentHeader, ok := parent.(*block.Header)
		if ok {
			period := h.config.Period
			if period == 0 {
				period = 3 // default 3 second blocks
			}
			header.Time = parentHeader.Time + period
			now := uint64(time.Now().Unix())
			if header.Time < now {
				header.Time = now
			}
		}
	}

	return nil
}

// Finalize runs any post-transaction state modifications (e.g. block rewards).
func (h *HotStuff) Finalize(chain consensus.ChainHeaderReader, iHeader block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	// HotStuff currently does not implement custom rewards.
	// This can be extended later with validator reward distribution.
	return nil, nil, nil
}

// FinalizeAndAssemble runs finalization and assembles the final block.
func (h *HotStuff) FinalizeAndAssemble(chain consensus.ChainHeaderReader, iHeader block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	header, ok := iHeader.(*block.Header)
	if !ok {
		return nil, nil, nil, errors.New("invalid header type: expected *block.Header")
	}

	rewards, balanceChanges, err := h.Finalize(chain, iHeader, ibs, txs, uncles)
	if err != nil {
		return nil, nil, nil, err
	}

	// Assemble the block.
	b := block.NewBlock(header, txs)
	return b, rewards, balanceChanges, nil
}

// Seal generates a new sealing request for the given block.
// In HotStuff, "sealing" means the leader has produced a block ready for consensus.
func (h *HotStuff) Seal(chain consensus.ChainHeaderReader, b block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	header, ok := b.Header().(*block.Header)
	if !ok {
		return errors.New("invalid header type: expected *block.Header")
	}

	h.lock.RLock()
	sk := h.secretKey
	h.lock.RUnlock()

	if sk == nil {
		return errors.New("hotstuff: secret key not set")
	}

	// Sign the seal hash with BLS.
	sealHash := h.SealHash(b.Header())
	sig := sk.Sign(sealHash[:])

	// Append signature to extra data.
	header.Extra = append(header.Extra, sig.Marshal()...)

	go func() {
		select {
		case results <- b.WithSeal(header):
		case <-stop:
		}
	}()

	return nil
}

// SealHash returns the hash of a block prior to it being sealed.
func (h *HotStuff) SealHash(iHeader block.IHeader) types.Hash {
	header, ok := iHeader.(*block.Header)
	if !ok {
		return types.Hash{}
	}
	return sealHash(header)
}

// CalcDifficulty always returns 1 for HotStuff.
func (h *HotStuff) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent block.IHeader) *uint256.Int {
	return uint256.NewInt(1)
}

// APIs returns the RPC APIs provided by the HotStuff consensus engine.
func (h *HotStuff) APIs(chain consensus.ConsensusChainReader) []jsonrpc.API {
	return []jsonrpc.API{
		{
			Namespace: "hotstuff",
			Service:   &API{hotstuff: h},
		},
	}
}

// Close terminates any background threads.
func (h *HotStuff) Close() error {
	h.cancel()
	return nil
}

// ============================================================================
// Helpers
// ============================================================================

// sealHash computes the hash used for sealing (excludes signature from extra).
func sealHash(header *block.Header) types.Hash {
	// Use the header hash excluding the seal (signature portion of extra data).
	return header.Hash()
}

// ExtractViewFromExtra extracts the view number from header extra-data.
func ExtractViewFromExtra(extra []byte) (ViewNumber, error) {
	if len(extra) < extraMinLen {
		return 0, fmt.Errorf("extra-data too short: %d", len(extra))
	}
	var magic [extraMagicLen]byte
	copy(magic[:], extra[:extraMagicLen])
	if magic != extraMagic {
		return 0, fmt.Errorf("invalid magic: expected N42H")
	}
	return binary.LittleEndian.Uint64(extra[extraMagicLen:extraMinLen]), nil
}

// API provides the hotstuff RPC API.
type API struct {
	hotstuff *HotStuff
}

// GetCurrentView returns the current consensus view number.
func (api *API) GetCurrentView() ViewNumber {
	if api.hotstuff.engine != nil {
		return api.hotstuff.engine.CurrentView()
	}
	return 0
}

// GetCurrentPhase returns the current consensus phase.
func (api *API) GetCurrentPhase() string {
	if api.hotstuff.engine != nil {
		return api.hotstuff.engine.CurrentPhase().String()
	}
	return "uninitialized"
}

// GetValidatorCount returns the number of validators.
func (api *API) GetValidatorCount() uint32 {
	if api.hotstuff.engine != nil {
		return api.hotstuff.engine.ValidatorCount()
	}
	return 0
}

// GetConsecutiveTimeouts returns the current consecutive timeout count.
func (api *API) GetConsecutiveTimeouts() uint32 {
	if api.hotstuff.engine != nil {
		return api.hotstuff.engine.ConsecutiveTimeouts()
	}
	return 0
}

// IsCurrentLeader returns whether this node is the current view leader.
func (api *API) IsCurrentLeader() bool {
	if api.hotstuff.engine != nil {
		return api.hotstuff.engine.IsCurrentLeader()
	}
	return false
}

// Compile-time interface checks.
var (
	_ consensus.Engine       = (*HotStuff)(nil)
	_ consensus.EngineReader = (*HotStuff)(nil)
)
