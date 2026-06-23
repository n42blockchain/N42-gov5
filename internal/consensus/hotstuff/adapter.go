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
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru"
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Header extra-data layout:
// [0..3]    = magic bytes "N42H" (4 bytes)
// [4..11]   = view number (8 bytes LE)
// [12..N)   = optional last committed QC (SSZ, variable length)
// [N..end]  = optional BLS seal signature (96 bytes)
//
// Backward compatibility:
//   - legacy headers may contain only magic+view
//   - legacy sealed headers may contain magic+view+seal
//   - older experiments may contain magic+view+QC
const (
	extraMagicLen      = 4
	extraViewLen       = 8
	extraMinLen        = extraMagicLen + extraViewLen
	inmemorySignatures = 4096
)

var extraMagic = [extraMagicLen]byte{'N', '4', '2', 'H'}

// RewardFunc computes and applies block rewards. Injected by node.go to avoid
// circular dependency between hotstuff and apos packages.
type RewardFunc func(chainConfig *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, chain consensus.N42ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error)

// CommitteePool produces and verifies the per-block BLS committee evidence (the
// simulated 200K-voter / 512-committee multi-sig that carries over from the
// replay reseal). Implemented by *blspool.Pool; an interface here keeps the
// hotstuff package decoupled from blspool.
type CommitteePool interface {
	// VerifyCE re-derives the committee and checks the aggregate signature,
	// returning the number of covered signers.
	VerifyCE(ce *rawdb.ConsensusEvidence) (covered int, ok bool, err error)
}

// CEReader reads stored consensus evidence by block number (parent-CE lookup
// for the ParentBeaconRoot link). Backed by the chain DB; injected by node.go.
type CEReader interface {
	ReadConsensusEvidence(blockNum uint64) (*rawdb.ConsensusEvidence, error)
}

// HotStuff implements the consensus.Engine interface for HotStuff-2 BFT consensus.
type HotStuff struct {
	config      *params.HotStuffConfig
	chainConfig *params.ChainConfig

	ctx    context.Context
	cancel context.CancelFunc

	// BLS key management
	secretKey blscommon.SecretKey
	signer    types.Address
	lock      sync.RWMutex

	// Caches
	signatures *lru.ARCCache

	// Core consensus engine (nil until Start() is called with validator set)
	engine       *ConsensusEngine
	epochManager *EpochManager

	// Output channel for consensus actions
	outputCh chan EngineOutput

	// Block reward function, injected to avoid import cycles.
	rewardFn RewardFunc

	// Live BLS committee evidence (optional). When both are set, Prepare stamps
	// each header's ParentBeaconRoot = parent CE's BeaconRoot and VerifyHeader
	// checks that link, continuing the resealed chain's BLS multi-sig.
	committeePool CommitteePool
	ceReader      CEReader
}

// SetCommitteeEvidence injects the live BLS committee pool and the consensus-
// evidence reader. With both set, the engine maintains the ParentBeaconRoot
// chain (EIP-4788) that commits to per-block committee evidence. Safe to leave
// unset — the engine then behaves exactly as before.
func (h *HotStuff) SetCommitteeEvidence(pool CommitteePool, ce CEReader) {
	h.lock.Lock()
	defer h.lock.Unlock()
	h.committeePool = pool
	h.ceReader = ce
}

// SetRewardFunc injects the block reward function (typically apos.doReward).
func (h *HotStuff) SetRewardFunc(fn RewardFunc) {
	h.lock.Lock()
	defer h.lock.Unlock()
	h.rewardFn = fn
}

// New creates a new HotStuff consensus engine.
func New(config *params.HotStuffConfig, chainConfig *params.ChainConfig) *HotStuff {
	ctx, cancel := context.WithCancel(context.Background())
	// NewARC only fails for invalid capacities. This adapter uses a fixed
	// positive constant, so there is no meaningful runtime recovery path here.
	signatures, _ := lru.NewARC(inmemorySignatures)

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
		outputCh:    make(chan EngineOutput, 1024),
	}
}

// Authorize injects a BLS private key and the corresponding address into the engine.
func (h *HotStuff) Authorize(signer types.Address, secretKey blscommon.SecretKey) {
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

// InitEngineFromConfig parses the genesis validator set from chain config and
// initializes the consensus engine. This is the production entry point called
// from node.go after Authorize.
func (h *HotStuff) InitEngineFromConfig() error {
	cfg := h.config
	if cfg == nil || len(cfg.Validators) == 0 {
		return fmt.Errorf("hotstuff: no validators configured in chain config")
	}

	validators := make([]ValidatorInfo, 0, len(cfg.Validators))
	for i, vc := range cfg.Validators {
		// Validate address format: must be 42-char 0x-prefixed hex.
		addrStr := strings.TrimSpace(vc.Address)
		if len(addrStr) != 42 || !strings.HasPrefix(addrStr, "0x") {
			return fmt.Errorf("hotstuff: validator %d has malformed address: %q", i, vc.Address)
		}
		addr := types.HexToAddress(addrStr)
		if addr == (types.Address{}) {
			return fmt.Errorf("hotstuff: validator %d has zero address", i)
		}

		blsHex := strings.TrimPrefix(vc.BLSKey, "0x")
		pubKeyBytes, err := hex.DecodeString(blsHex)
		if err != nil {
			return fmt.Errorf("hotstuff: validator %d has invalid BLS key hex: %w", i, err)
		}
		if len(pubKeyBytes) != 48 {
			return fmt.Errorf("hotstuff: validator %d BLS key wrong length: got %d, want 48", i, len(pubKeyBytes))
		}
		pubKey, err := bls.PublicKeyFromBytes(pubKeyBytes)
		if err != nil {
			return fmt.Errorf("hotstuff: validator %d has invalid BLS public key: %w", i, err)
		}

		validators = append(validators, ValidatorInfo{
			Address:   addr,
			PublicKey: pubKey,
		})
	}

	n := uint32(len(validators))
	if n < 4 {
		log.Warn("hotstuff: fewer than 4 validators — BFT safety guarantees are weakened", "count", n)
	}
	faultTolerance := (n - 1) / 3

	return h.InitEngine(validators, faultTolerance)
}

// Engine returns the inner consensus state machine (may be nil if not initialized).
func (h *HotStuff) Engine() *ConsensusEngine {
	h.lock.RLock()
	defer h.lock.RUnlock()
	return h.engine
}

// Config returns the HotStuff configuration.
func (h *HotStuff) Config() *params.HotStuffConfig {
	return h.config
}

// IsCurrentLeader reports whether this node is the leader for the current view.
// The miner gates block production on this: only the current view's leader
// builds and broadcasts a block; followers import it via gossip. Without the
// gate every node produces and locally inserts its own block and the chain
// forks.
func (h *HotStuff) IsCurrentLeader() bool {
	if ce := h.Engine(); ce != nil {
		return ce.IsCurrentLeader()
	}
	return false
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

	headerView, qc, _, err := decodeHeaderExtra(header.Extra)
	if err != nil {
		return err
	}

	// Verify timestamp is after parent.
	var parentNum uint256.Int
	parentNum.Sub(header.Number, uint256.NewInt(1))
	parent := chain.GetHeaderByNumber(&parentNum)
	if parent == nil {
		// Return ErrUnknownAncestor (not a generic error) so InsertChain
		// future-queues this block instead of rejecting it as bad. Direct-pushed
		// HotStuff blocks can arrive before their parent; they import once the
		// parent lands.
		return consensus.ErrUnknownAncestor
	}
	parentHeader, ok := parent.(*block.Header)
	if !ok {
		return errors.New("invalid parent header type")
	}

	if header.Time <= parentHeader.Time {
		return errors.New("timestamp must be after parent")
	}

	if qc != nil {
		if qc.View > headerView {
			return fmt.Errorf("QC view %d exceeds header view %d", qc.View, headerView)
		}

		if ce := h.Engine(); ce != nil {
			vs := ce.ValidatorSetForView(qc.View)
			if vs != nil && !vs.IsEmpty() {
				if vErr := VerifyQCAnyDomain(qc, vs); vErr != nil {
					return fmt.Errorf("QC verification failed: %w", vErr)
				}
			}
		}
	}

	// EIP-4788 / committee-evidence link: when the engine maintains BLS committee
	// evidence, every header must commit to the parent's evidence through
	// ParentBeaconRoot = Blake3(parent CE). This transitively validates that the
	// producer used the same deterministic committee evidence we hold locally.
	h.lock.RLock()
	ceReader := h.ceReader
	h.lock.RUnlock()
	if ceReader != nil {
		expected := parentBeaconRoot(ceReader, header.Number.Uint64())
		switch {
		case expected == nil && header.ParentBeaconRoot != nil && *header.ParentBeaconRoot != (types.Hash{}):
			return fmt.Errorf("unexpected ParentBeaconRoot at block %s (no parent evidence)", header.Number)
		case expected != nil && (header.ParentBeaconRoot == nil || *header.ParentBeaconRoot != *expected):
			return fmt.Errorf("ParentBeaconRoot mismatch at block %s: committee-evidence link broken", header.Number)
		}
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
	var committedQC *QuorumCertificate
	if ce := h.Engine(); ce != nil {
		view = ce.CurrentView()
		lastCommittedQC := ce.LastCommittedQC()
		if !isEmptyHeaderQC(&lastCommittedQC) {
			committedQC = &lastCommittedQC
		}
	}

	extra, err := buildHeaderExtra(view, committedQC)
	if err != nil {
		return err
	}
	header.Extra = extra

	// EIP-4788 link: commit this block to the parent's consensus evidence via
	// ParentBeaconRoot = Blake3(parent CE). Mirrors the replay resealer so a node
	// producing block N+1 on a resealed chain continues the BLS multi-sig chain
	// byte-identically. No-op until SetCommitteeEvidence is wired.
	h.lock.RLock()
	ceReader := h.ceReader
	h.lock.RUnlock()
	if ceReader != nil && !header.Number.IsZero() {
		if pbr := parentBeaconRoot(ceReader, header.Number.Uint64()); pbr != nil {
			header.ParentBeaconRoot = pbr
		}
	}

	// Set timestamp (skip for genesis).
	if header.Number.IsZero() {
		return nil
	}
	var pNum uint256.Int
	pNum.Sub(header.Number, uint256.NewInt(1))
	parent := chain.GetHeaderByNumber(&pNum)
	if parent != nil {
		parentHeader, ok := parent.(*block.Header)
		if ok {
			period := h.config.Period
			if period == 0 {
				period = 3 // default 3 second blocks
			}
			// Deterministic block time: parent time + period, with NO now-floor.
			// A now-floor makes the timestamp (and thus the block hash) change on
			// every build attempt; a leader triggered several times for the same
			// parent/view would then produce DIFFERENT blocks (multi-produce) and
			// followers could never agree which one to import. Determinism makes the
			// leader produce exactly one block per height — taskLoop dedups repeat
			// builds by sealHash, and propose/push/import all reference one block.
			header.Time = parentHeader.Time + period
		}
	}

	return nil
}

// Finalize runs any post-transaction state modifications (e.g. block rewards).
// HotStuff delegates reward logic to the APoS reward module so that validators
// receive the same epoch-based rewards regardless of which consensus engine is active.
func (h *HotStuff) Finalize(chain consensus.ChainHeaderReader, iHeader block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	header, ok := iHeader.(*block.Header)
	if !ok {
		return nil, nil, errors.New("invalid header type: expected *block.Header")
	}

	var rewards []*block.Reward
	var unpayMap map[types.Address]*uint256.Int

	if h.rewardFn == nil {
		return nil, nil, errors.New("hotstuff: reward function not set; call SetRewardFunc before block production")
	}
	n42Chain, ok := chain.(consensus.N42ChainHeaderReader)
	if !ok {
		return nil, nil, errors.New("hotstuff reward path requires n42 chain reward reader")
	}
	var err error
	rewards, unpayMap, err = h.rewardFn(h.chainConfig, ibs, header, n42Chain)
	if err != nil {
		return nil, nil, err
	}

	header.Root = ibs.IntermediateRoot()
	return rewards, unpayMap, nil
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

	// Assemble the block with computed TxHash and ReceiptHash.
	b := block.NewBlockFromReceipt(header, txs, nil, receipts, rewards)
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
	sealDigest := h.SealHash(b.Header())
	sig := sk.Sign(sealDigest[:])

	// Copy header before mutating Extra to avoid corrupting the original block's header.
	sealedHeader := block.CopyHeader(header)
	// Fill the reserved trailing seal area in place (buildHeaderExtra reserved
	// extraSealLen bytes) rather than appending, so SealHash strips the SAME
	// region before and after sealing and resultLoop's pendingTasks lookup
	// (keyed by SealHash) matches. Appending would grow Extra past the reserve
	// and desync the unsealed vs sealed SealHash for QC-bearing blocks.
	sigBytes := sig.Marshal()
	if len(sealedHeader.Extra) >= extraSealLen {
		copy(sealedHeader.Extra[len(sealedHeader.Extra)-extraSealLen:], sigBytes)
	} else {
		sealedHeader.Extra = append(sealedHeader.Extra, sigBytes...)
	}
	sealedHeader.ResetHashCache()

	sealed := b.WithSeal(sealedHeader)

	// Deliver the sealed block to the miner's resultLoop. The Proposal is NOT
	// started here — it is started from resultLoop via NotifyBlockSealed, AFTER
	// resultLoop has written and direct-pushed THIS exact sealed block. That makes
	// the proposed (hence committed) block byte-for-byte the one followers receive
	// and import, so import-gated voting and CommitToCanonical can find it.
	// Proposing here (potentially from a seal that resultLoop's HasBlock check
	// skips) would propose a different block than the one actually pushed.
	go func() {
		select {
		case results <- sealed:
		case <-stop:
		}
	}()

	return nil
}

// NotifyBlockSealed feeds a just-sealed-and-pushed block into the consensus
// engine to start a Proposal. The miner's resultLoop calls this immediately
// after it persists and direct-pushes the sealed block, so the proposed block is
// exactly the one followers receive (see Seal).
func (h *HotStuff) NotifyBlockSealed(hash types.Hash, txHash types.Hash) {
	if ce := h.Engine(); ce != nil {
		if err := ce.ProcessEvent(ConsensusEvent{
			Type:       EventBlockReady,
			Hash:       hash,
			TxRootHash: txHash, // Baby Raptr: DA commitment
		}); err != nil {
			log.Debug("hotstuff: seal block event ignored", "err", err)
		}
	}
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

// sealHash computes the hash used for sealing by stripping the BLS seal
// (trailing 96 bytes) from Extra before hashing. This ensures the seal hash
// is stable regardless of whether the signature has been appended.
func sealHash(header *block.Header) types.Hash {
	cpy := *header
	if len(cpy.Extra) > extraSealLen {
		cpy.Extra = cpy.Extra[:len(cpy.Extra)-extraSealLen]
	}
	// The struct copy also copied the cached hash. After stripping the seal from
	// Extra we MUST clear it so Hash() recomputes over the stripped header —
	// otherwise a sealed header (whose hash is already cached) returns its full
	// hash here, while an unsealed header recomputes the stripped hash. The miner
	// keys pendingTasks by SealHash(unsealed) but resultLoop looks it up by
	// SealHash(sealed); the mismatch yields "Block found but no relative pending
	// task" and the produced block is dropped, stalling the chain height.
	cpy.ResetHashCache()
	return cpy.Hash()
}

// parentBeaconRoot returns the ParentBeaconRoot a block at blockNum must carry:
// the BeaconRoot of block blockNum-1's consensus evidence, or nil for
// genesis/block 1 or when no parent evidence exists.
func parentBeaconRoot(r CEReader, blockNum uint64) *types.Hash {
	if blockNum <= 1 {
		return nil
	}
	parentCE, err := r.ReadConsensusEvidence(blockNum - 1)
	if err != nil || parentCE == nil {
		return nil
	}
	root := parentCE.BeaconRoot()
	return &root
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
	if ce := api.hotstuff.Engine(); ce != nil {
		return ce.CurrentView()
	}
	return 0
}

// GetCurrentPhase returns the current consensus phase.
func (api *API) GetCurrentPhase() string {
	if ce := api.hotstuff.Engine(); ce != nil {
		return ce.CurrentPhase().String()
	}
	return "uninitialized"
}

// GetValidatorCount returns the number of validators.
func (api *API) GetValidatorCount() uint32 {
	if ce := api.hotstuff.Engine(); ce != nil {
		return ce.ValidatorCount()
	}
	return 0
}

// GetConsecutiveTimeouts returns the current consecutive timeout count.
func (api *API) GetConsecutiveTimeouts() uint32 {
	if ce := api.hotstuff.Engine(); ce != nil {
		return ce.ConsecutiveTimeouts()
	}
	return 0
}

// IsCurrentLeader returns whether this node is the current view leader.
func (api *API) IsCurrentLeader() bool {
	if ce := api.hotstuff.Engine(); ce != nil {
		return ce.IsCurrentLeader()
	}
	return false
}

// Compile-time interface checks.
var (
	_ consensus.Engine       = (*HotStuff)(nil)
	_ consensus.EngineReader = (*HotStuff)(nil)
)
