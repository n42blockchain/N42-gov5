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
	"github.com/n42blockchain/N42/common/hash"
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
	// BuildSimulatedCE deterministically rebuilds a block's committee evidence
	// from (blockNum, blockHash, receiptRoot). Used to re-derive a parent's CE
	// from the actual parent header for the ParentBeaconRoot link, instead of
	// trusting the by-number CE store (which a speculative same-height candidate
	// may have overwritten). Implemented by *blspool.Pool.
	BuildSimulatedCE(blockNum uint64, blockHash, receiptRoot types.Hash) (*rawdb.ConsensusEvidence, error)
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
	// A signer absent from the genesis set is NOT a fatal error: it starts as a
	// pending observer that syncs and processes consensus messages (so its view
	// advances) but does not propose/vote/time out, until reconfig adds it at an
	// epoch boundary and its engine activates. This is what lets a brand-new
	// validator bootstrap — without it, InitEngine hard-failed and the node could
	// never join. Requires epochs enabled for the add to ever take effect.
	observer := myIndex < 0
	if observer {
		// Observer mode only makes sense with epochs enabled: without an epoch
		// boundary there is no reconfig activation, so an outsider signer could
		// never join. In that case fail fast (almost always a misconfigured
		// etherbase) instead of starting a permanently-silent node.
		if h.config == nil || h.config.EpochLength == 0 {
			return fmt.Errorf("hotstuff: signer %s not found in validator set (epochs disabled — cannot join via reconfig)", h.signer.Hex())
		}
		log.Warn("hotstuff: signer not in genesis validator set — starting as PENDING OBSERVER (joins via reconfig at an epoch boundary)",
			"signer", h.signer.Hex())
	}

	// Wire the chainspec's epochLength into the EpochManager. Previously this
	// always used NewEpochManager(vs) which hardcodes epochLength=0, leaving
	// epoch transitions (and therefore validator reconfiguration, which
	// activates at epoch boundaries) permanently dormant despite the chainspec
	// setting and the fully-built ReconfigurationManager. With epochLength>0,
	// ProposeAdd/RemoveValidator now take effect at the next boundary. A chain
	// starting from genesis has currentEpoch=0 correctly; a mid-chain restart
	// seeds currentEpoch from the recovered head view in RestoreState via
	// EpochManager.SeedCurrentEpoch, so historicalSets stay aligned.
	if h.config != nil && h.config.EpochLength > 0 {
		h.epochManager = NewEpochManagerWithLength(vs, h.config.EpochLength)
	} else {
		h.epochManager = NewEpochManager(vs)
	}
	engineIndex := NonMemberIndex
	if !observer {
		engineIndex = ValidatorIndex(myIndex)
	}
	h.engine = NewConsensusEngineWithEpochManager(
		engineIndex,
		h.secretKey,
		h.epochManager,
		h.config.BaseTimeout,
		h.config.MaxTimeout,
		h.outputCh,
	)
	// Record own address so the engine can find itself in a new set at an epoch
	// boundary even while an observer (myIndex==NonMemberIndex can't index the set).
	h.engine.SetSelfAddress(h.signer)
	if observer {
		h.engine.removed = true // inactive until reconfig adds it; still processes events
	}

	if h.config.TwoPhaseVoteGate {
		h.engine.SetTwoPhaseVote(true)
		log.Info("HotStuff two-phase vote gate ENABLED (order-then-execute: R1 static, R2 import-gated)")
	}

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
	return h.verifyHeaderWithBatch(chain, iHeader, nil)
}

// verifyHeaderWithBatch is VerifyHeader with an optional in-batch parent index.
// A range import verifies all headers BEFORE any block is written, so a
// header's parent that is earlier in the same batch is not in the DB yet — a
// DB-only lookup returns ErrUnknownAncestor for every block past the stored
// prefix, and a 1024-block catch-up range degrades to one accepted block per
// attempt with a full unwind/replay each round (observed live: a laggard
// advancing exactly +1 block per 8s tick while re-executing everything).
func (h *HotStuff) verifyHeaderWithBatch(chain consensus.ChainHeaderReader, iHeader block.IHeader, batch map[types.Hash]*block.Header) error {
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

	// Resolve the parent by the header's OWN ParentHash — not by number. A
	// by-number lookup returns the locally-canonical header, but under HotStuff-2
	// a view-change can leave competing same-height siblings and nodes may not
	// have converged on which one is canonical yet (CommitToCanonical is the
	// authority, and it can lag). Validating a child against the by-number parent
	// mis-rejects any block built on the non-canonical sibling. Headers are
	// stored keyed by (number, hash) for non-canonical blocks too, so the true
	// parent is retrievable regardless of local canonical choice.
	var parentHeader *block.Header
	if batch != nil {
		parentHeader = batch[header.ParentHash] // in-batch parent (range import)
	}
	if parentHeader == nil {
		parent, perr := chain.GetHeaderByHash(header.ParentHash)
		if perr != nil || parent == nil {
			// Return ErrUnknownAncestor (not a generic error) so InsertChain
			// future-queues this block instead of rejecting it as bad. Direct-pushed
			// HotStuff blocks can arrive before their parent; they import once the
			// parent lands.
			return consensus.ErrUnknownAncestor
		}
		var pok bool
		parentHeader, pok = parent.(*block.Header)
		if !pok || parentHeader == nil {
			return consensus.ErrUnknownAncestor
		}
	}
	// Sanity: the referenced parent must sit exactly one height below.
	var wantParentNum uint256.Int
	wantParentNum.Sub(header.Number, uint256.NewInt(1))
	if parentHeader.Number == nil || parentHeader.Number.Cmp(&wantParentNum) != 0 {
		return fmt.Errorf("parent %x has height %v, want %v", header.ParentHash[:8], parentHeader.Number, &wantParentNum)
	}

	if header.Time <= parentHeader.Time {
		return errors.New("timestamp must be after parent")
	}

	// MobileAnchor is a scheduled header fork, not an optional producer hint.
	// Enforce the RLP shape on both sides of the boundary so a validator started
	// without the mobile pipeline cannot silently produce nil-root blocks after
	// activation, and a pre-fork producer cannot change historical wire form.
	if cfg := chain.Config(); cfg != nil {
		active := cfg.IsMobileAnchor(header.Time)
		switch {
		case active && header.MobileRegistryRoot == nil:
			return fmt.Errorf("missing MobileRegistryRoot at block %s after MobileAnchor activation", header.Number)
		case !active && header.MobileRegistryRoot != nil:
			return fmt.Errorf("unexpected MobileRegistryRoot at block %s before MobileAnchor activation", header.Number)
		}
	}

	if qc != nil {
		if qc.View > headerView {
			return fmt.Errorf("QC view %d exceeds header view %d", qc.View, headerView)
		}

		if ce := h.Engine(); ce != nil {
			vs := ce.ResolveQCValidatorSet(qc.View, len(qc.Signers))
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
	pool := h.committeePool
	h.lock.RUnlock()
	if pool != nil {
		// Re-derive the parent's committee evidence from the ACTUAL parent header
		// (fetched above) rather than the by-number CE store, so the link is
		// immune to speculative same-height overwrites (see parentBeaconRootFromHeader).
		expected, cerr := parentBeaconRootFromHeader(pool, parentHeader)
		if cerr != nil {
			return fmt.Errorf("deriving parent committee evidence for block %s: %w", header.Number, cerr)
		}
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

	// Index the batch by hash so a header can resolve a parent that precedes
	// it in the SAME batch (none of the batch is in the DB yet during a range
	// import) — see verifyHeaderWithBatch.
	batch := make(map[types.Hash]*block.Header, len(headers))
	for _, ih := range headers {
		if hd, ok := ih.(*block.Header); ok && hd != nil {
			batch[hd.Hash()] = hd
		}
	}

	go func() {
		defer close(results)
		for _, header := range headers {
			select {
			case <-abort:
				return
			default:
			}
			err := h.verifyHeaderWithBatch(chain, header, batch)
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

	// Difficulty is 0: HotStuff orders blocks by view number and chooses the
	// canonical chain by commit authority (CommitToCanonical), never by total
	// difficulty. Zero matches the post-merge/PoS convention (EIP-3675: a non-PoW
	// block carries difficulty=0 alongside nonce=0 and ommersHash=EmptyUncleHash),
	// lets ForkChoice.ReorgNeeded fall through to its height-based virtual-TD path,
	// and lets the compact header codec omit the field entirely (it is stored only
	// when non-zero). A non-zero placeholder here was a vestige of the Clique-style
	// in-turn/no-turn difficulty that BFT consensus does not use.
	header.Difficulty = uint256.NewInt(0)

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
	// Set timestamp (skip for genesis; genesis carries no parent evidence).
	if header.Number.IsZero() {
		return nil
	}
	// Resolve the parent by the header's ParentHash (set by the miner before
	// Prepare), mirroring VerifyHeader — the leader may be extending a block
	// that is not yet locally canonical (commit lag), and the timestamp + PBR
	// must derive from the block actually extended.
	parent, _ := chain.GetHeaderByHash(header.ParentHash)
	if parent != nil {
		parentHeader, ok := parent.(*block.Header)
		if ok && parentHeader != nil {
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

			// EIP-4788 committee-evidence link: stamp ParentBeaconRoot from the
			// actual parent header via deterministic CE re-derivation, matching
			// exactly what VerifyHeader recomputes. No-op until the committee pool
			// is wired (SetCommitteeEvidence).
			h.lock.RLock()
			pool := h.committeePool
			h.lock.RUnlock()
			if pbr, perr := parentBeaconRootFromHeader(pool, parentHeader); perr != nil {
				return perr
			} else if pbr != nil {
				header.ParentBeaconRoot = pbr
			}
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

	// Dev-chain fixed block reward (chainspec hotstuff.devBlockReward): credit
	// the coinbase so replay-seeded dev meshes — where no staking deposits
	// exist and the APoS module pays nothing — have funded validator accounts
	// (the txgen faucet loop needs a non-zero source). Consensus-relevant:
	// runs identically on the build and the verify path, so the state root
	// carries it and all nodes must share the chainspec. Zero = disabled.
	if h.chainConfig.HotStuff != nil && h.chainConfig.HotStuff.DevBlockReward > 0 {
		amount := uint256.NewInt(h.chainConfig.HotStuff.DevBlockReward)
		ibs.AddBalance(header.Coinbase, amount)
		rewards = append(rewards, &block.Reward{Address: header.Coinbase, Amount: amount})
		// Chain-level dev faucet: validator addresses on BLS-resealed chains
		// have no secp keys, so only a dedicated dev account can sign txs.
		if fa := h.chainConfig.HotStuff.DevFaucetAddress; fa != nil && *fa != (types.Address{}) {
			famount := uint256.NewInt(h.chainConfig.HotStuff.DevBlockReward)
			ibs.AddBalance(*fa, famount)
			rewards = append(rewards, &block.Reward{Address: *fa, Amount: famount})
		}
	}

	// Build-vs-verify split on the state root. On the BUILD path (miner →
	// FinalizeAndAssemble) header.Root is still zero: set it from the locally
	// computed root. On the VERIFY path (importing a proposer's block) it
	// carries the proposer's root: COMPARE and reject on mismatch. The old
	// unconditional overwrite silently discarded the proposer's root on
	// import, so no state-root check existed anywhere in the pipeline
	// (BlockValidator.ValidateState's root check is disabled for legacy
	// replay reasons) — validator QMDB trees could drift apart indefinitely,
	// surfacing only as nonce anomalies under load and as startup
	// marker/tree-root mismatches.
	localRoot := ibs.IntermediateRoot()
	if header.Root != (types.Hash{}) && header.Root != localRoot {
		return nil, nil, fmt.Errorf("state root mismatch at block %d: proposer %x, locally computed %x",
			header.Number.Uint64(), header.Root[:8], localRoot[:8])
	}
	header.Root = localRoot
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

	// Fork-mandated header fields, mirroring replay-v2's resealed shape so a
	// live block N+1 continues the chain with the same wire form. This also
	// keeps the header's RLP optional chain CONTINUOUS: with the committee
	// pool wired, Prepare sets ParentBeaconRoot (optional #20), and a nil
	// *Hash before it (WithdrawalsHash, #17) encodes as an empty string that
	// cannot decode back into types.Hash — block push/fetch transport then
	// rejects every produced block ("input string too short", found by the
	// 7-node smoke after enabling committeePool).
	if cfg := chain.Config(); cfg != nil {
		num := header.Number.Uint64()
		if cfg.IsShanghaiAt(num, header.Time) && header.WithdrawalsHash == nil {
			// N42 repurposes the withdrawals slot as the rewards commitment
			// (same derivation as replay-v2).
			rh := hash.DeriveSha(block.Rewards(rewards))
			header.WithdrawalsHash = &rh
		}
		if cfg.IsCancunAt(num, header.Time) {
			if header.BlobGasUsed == nil {
				z := uint64(0)
				header.BlobGasUsed = &z
			}
			if header.ExcessBlobGas == nil {
				z := uint64(0)
				header.ExcessBlobGas = &z
			}
		}
		if cfg.IsPrague(header.Time) && header.RequestsHash == nil {
			rq := hash.EmptyRootHash // keccak256(RLP([])): no EIP-7685 requests
			header.RequestsHash = &rq
		}
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

// CalcDifficulty always returns 0 for HotStuff: block ordering is by view number
// and canonicalization is by commit authority, not total difficulty. Zero is the
// post-merge/PoS convention and keeps headers consistent with their zero ommers.
func (h *HotStuff) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent block.IHeader) *uint256.Int {
	return uint256.NewInt(0)
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

// parentBeaconRootFromHeader derives the ParentBeaconRoot a block must carry
// given the specific parent header it extends: Blake3 of the parent's committee
// evidence, rebuilt deterministically from (parentNum, parentHash,
// parentReceiptRoot) via the committee pool. Returns nil for a genesis parent
// (block 1 carries no parent evidence).
//
// This binds the ParentBeaconRoot link to the block actually being extended,
// NOT to whatever the by-number ConsensusEvidence store happens to hold. Under
// HotStuff-2 a height can be speculatively imported more than once (view-change
// re-production / competing same-height proposals), each overwriting the
// by-number CE row; reading that row would make the link depend on import order
// rather than on the committed/extended block. Because CE is a pure function of
// (num, hash, receiptRoot), re-deriving from the parent header always matches
// the parent the producer actually built on — and never a losing same-height
// candidate.
func parentBeaconRootFromHeader(pool CommitteePool, parentHeader *block.Header) (*types.Hash, error) {
	if pool == nil || parentHeader == nil || parentHeader.Number == nil || parentHeader.Number.IsZero() {
		return nil, nil
	}
	ce, err := pool.BuildSimulatedCE(parentHeader.Number.Uint64(), parentHeader.Hash(), parentHeader.ReceiptHash)
	if err != nil {
		return nil, err
	}
	if ce == nil {
		return nil, nil
	}
	root := ce.BeaconRoot()
	return &root, nil
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
