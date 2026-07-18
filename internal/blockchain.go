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
// BlockChain core lifecycle and chain-insert orchestration. Owns the
// canonical head pointer, manages import locks, drives header /
// block validation through BlockValidator and StateProcessor, and
// integrates the freezer, pruner and state commitment layers. Types
// and constants live in blockchain_types.go; read and write helper
// methods live in blockchain_reader.go and blockchain_write.go.

package internal

// This file contains the core lifecycle and chain-insert orchestration logic.
// Types and constants: blockchain_types.go
// Write methods: blockchain_write.go
// Read methods: blockchain_reader.go

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/internal/zkverifier"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/modules/state/snapshot"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Constructor
// =============================================================================

func NewBlockChain(ctx context.Context, genesisBlock block.IBlock, engine consensus.Engine, db kv.RwDB, p2p p2p.P2P, config *params.ChainConfig) (common.IBlockChain, error) {
	c, cancel := context.WithCancel(ctx)
	// Select the transaction-root encoding for this chain: mainnet_qmdb uses the
	// Ethereum-standard MPT root over RLP txs (block.TxRoot -> DeriveShaErigon);
	// legacy native chains keep the proto keccak-concat root for historical hash
	// continuity. Must be set before any block is produced or validated.
	block.UseEthereumTxRoot = config != nil && config.StateScheme == string(params.StateCommitmentPresetQMDB)
	concreteGenesis, err := requireConcreteBlock(genesisBlock, "unexpected genesis block type")
	if err != nil {
		cancel()
		return nil, err
	}
	if _, err := requireBlockNumber(concreteGenesis, "genesis block number unavailable"); err != nil {
		cancel()
		return nil, err
	}
	var current *block.Block

	if err := db.View(c, func(tx kv.Tx) error {
		current = rawdb.ReadCurrentBlock(tx)
		if current == nil {
			current = concreteGenesis
		}
		return nil
	}); err != nil {
		log.Warnf("failed to read current block from database, using genesis: %v", err)
		current = concreteGenesis
	}
	currentNumber, err := requireBlockNumber(current, "current block number unavailable")
	if err != nil {
		cancel()
		return nil, err
	}

	blockCache, _ := lru.New[types.Hash, *block.Block](blockCacheLimit)
	futureBlocks, _ := lru.New[types.Hash, *block.Block](maxFutureBlocks)
	receiptsCache, _ := lru.New[types.Hash, []*block.Receipt](receiptsCacheLimit)
	tdCache, _ := lru.New[types.Hash, *uint256.Int](tdCacheLimit)
	numberCache, _ := lru.New[types.Hash, uint64](numberCacheLimit)
	headerCache, _ := lru.New[types.Hash, *block.Header](headerCacheLimit)
	witnessCache, _ := lru.New[types.Hash, *witness.BlockWitness](witnessCacheLimit)

	bc := &BlockChain{
		chainConfig:   config,
		genesisBlock:  genesisBlock,
		blocks:        []block.IBlock{},
		ChainDB:       db,
		ctx:           c,
		cancel:        cancel,
		insertLock:    make(chan struct{}, 1),
		peers:         make(map[peer.ID]bool),
		errorCh:       make(chan error, 1),
		p2p:           p2p,
		latestBlockCh: make(chan block.IBlock, 50),
		engine:        engine,
		blockCache:    blockCache,
		tdCache:       tdCache,
		futureBlocks:  futureBlocks,
		receiptCache:  receiptsCache,
		numberCache:   numberCache,
		headerCache:   headerCache,
		witnessCache:  witnessCache,
	}

	bc.currentBlock.Store(current)
	headBlockGauge.Set(currentNumber.Uint64())
	headGasUsedGauge.Set(current.GasUsed())
	headGasLimitGauge.Set(current.GasLimit())
	headTransactionsGauge.Set(uint64(len(current.Transactions())))
	bc.forker = NewForkChoice(bc, nil)
	bc.process = NewStateProcessor(config, bc, engine)
	bc.validator = NewBlockValidator(config, bc, engine)

	return bc, nil
}

// SetParallelEVM enables or disables Block-STM parallel transaction execution.
func (bc *BlockChain) SetParallelEVM(enabled bool) {
	bc.parallelEVM = enabled
	if enabled {
		log.Info("Block-STM parallel EVM execution enabled")
	}
}

// SetPrefetch enables or disables state prefetching before block execution.
func (bc *BlockChain) SetPrefetch(enabled bool) {
	bc.prefetchEnabled = enabled
	if enabled {
		log.Info("State prefetching enabled")
	}
}

// SetPrefetchPredictor enables predictive slot prefetching based on historical access patterns.
// Also wires the predictor as a SlotAccessRecorder so SLOAD feeds it learning data.
func (bc *BlockChain) SetPrefetchPredictor(p *PrefetchPredictor) {
	bc.prefetchPredictor = p
	if p != nil {
		log.Info("Predictive slot prefetching enabled")
		if sp, ok := bc.process.(*StateProcessor); ok {
			sp.SetSlotRecorder(p)
		}
	}
}

// SetFreezer attaches the ancient data freezer to the blockchain.
// It accepts any implementation of freezer.FreezerAPI. If the concrete type
// is *freezer.Freezer, an AncientReader is also created for high-level access.
func (bc *BlockChain) SetFreezer(f freezer.FreezerAPI) {
	bc.freezer = f
	if concrete, ok := f.(*freezer.Freezer); ok {
		bc.ancientReader = freezer.NewAncientReader(concrete)
	}
	log.Info("Ancient data freezer attached", "frozen", f.Frozen())
}

// SetExExManager attaches an Execution Extensions manager to the blockchain.
// The manager will receive notifications after each block commit or revert.
func (bc *BlockChain) SetExExManager(m *exex.Manager) {
	bc.exexManager = m
	log.Info("ExEx manager attached to blockchain")
}

// ExExManager returns the attached ExEx manager, or nil if not configured.
func (bc *BlockChain) ExExManager() *exex.Manager {
	return bc.exexManager
}

// SetOnBlockCommitted installs a generic canonical-commit hook, called
// alongside the ExEx notification for every newly-canonical block on every
// node (leader or follower) — unlike consensus.BlockImportNotifier, which is
// a single HotStuff-owned slot, this is a plain callback any package-agnostic
// consumer can attach without introducing a blockchain -> consumer import
// (e.g. mobile-attestation cohort merge uses block height as its
// cross-node coordination clock; see internal/mobileverify/coordinator.go).
func (bc *BlockChain) SetOnBlockCommitted(fn func(number uint64)) {
	bc.onBlockCommittedMu.Lock()
	bc.onBlockCommitted = fn
	bc.onBlockCommittedMu.Unlock()
}

func (bc *BlockChain) notifyBlockCommitted(number uint64) {
	bc.onBlockCommittedMu.RLock()
	fn := bc.onBlockCommitted
	bc.onBlockCommittedMu.RUnlock()
	if fn != nil {
		fn(number)
	}
}

// SetSnapshotTree attaches a snapshot acceleration tree to the blockchain.
// The tree caches recent block state diffs for fast reads without DB access.
func (bc *BlockChain) SetSnapshotTree(tree *snapshot.Tree) {
	bc.snapshotTree = tree
	log.Info("Snapshot acceleration tree attached")
}

// SnapshotTree returns the attached snapshot tree, or nil if not configured.
func (bc *BlockChain) SnapshotTree() *snapshot.Tree {
	return bc.snapshotTree
}

// SetJMTCommitment enables the Jellyfish Merkle Tree state commitment.
func (bc *BlockChain) SetJMTCommitment(c *commitment.JMTCommitment) {
	bc.jmtCommitment = c
	bc.jmtEnabled = true
	bc.SetStateProofProvider(NewJMTStateProofProvider(c))
	log.Info("JMT state commitment enabled (Blake3)")
}

// SetJMTStoreRefresh sets the callback to refresh the JMT backing store's
// read transaction after each block commit.
func (bc *BlockChain) SetJMTStoreRefresh(fn func()) {
	bc.jmtStoreRefresh = fn
}

// SetBMTCommitment enables the Binary Merkle Tree state commitment.
func (bc *BlockChain) SetBMTCommitment(c *commitment.BMTCommitment) {
	bc.bmtCommitment = c
	bc.bmtEnabled = true
	log.Info("BMT state commitment enabled (Blake3, content-addressed)")
}

// SetVerkleCommitment enables the Verkle tree state commitment (experimental).
func (bc *BlockChain) SetVerkleCommitment(c *commitment.VerkleCommitment) {
	bc.verkleCommitment = c
	bc.verkleEnabled = true
	log.Info("Verkle state commitment enabled (IPA/Banderwagon, experimental)")
}

// SetMPTRootComputer enables persistent Ethereum MPT state root computation.
func (bc *BlockChain) SetMPTRootComputer(rc *commitment.MPTRootComputer) {
	bc.mptRootComputer = rc
	bc.mptEnabled = true
	log.Info("MPT state commitment enabled (Ethereum-compatible, HexPatriciaHashed)")
}

// SetLtHashCommitment enables the LtHash lattice state digest.
func (bc *BlockChain) SetLtHashCommitment(c *commitment.LtHashCommitment) {
	bc.ltHashCommitment = c
	bc.ltHashEnabled = true
	log.Info("LtHash lattice state digest enabled (BLAKE3 XOF 2048-byte)")
}

// LtHashCommitment returns the LtHash commitment, or nil if disabled.
func (bc *BlockChain) LtHashCommitment() *commitment.LtHashCommitment {
	return bc.ltHashCommitment
}

// SetRootComputer stores the RootComputer for injection into IntraBlockState.
func (bc *BlockChain) SetRootComputer(rc state.RootComputer) {
	bc.rootComputer = rc
}

// RootComputer returns the pluggable root computer (JMT + LtHash), or nil.
func (bc *BlockChain) RootComputer() state.RootComputer {
	return bc.rootComputer
}

// SetQMDBRootComputer installs the QMDB twig-forest root computer for live
// block production and marks QMDB enabled so writeBlockWithState flushes the
// positional entry log per block. Unlike JMT, QMDB injection is gated by its
// own flag (not jmtForBlockProcessing) and uses the default in-RAM key->slot
// index: the per-block evmRecord tx is read-only, so the index cannot be
// MDBX-backed on the live path.
func (bc *BlockChain) SetQMDBRootComputer(rc *commitment.QMDBRootComputer) {
	bc.qmdbRootComputer = rc
	bc.qmdbEnabled = true
}

// NewMinerRootComputer returns an ISOLATED root computer for speculative block
// building. The miner/worker assembles candidate blocks that may never be
// committed (another validator's proposal may win the HotStuff round), so it
// must NOT mutate the live commitment tree. For QMDB this reloads a fresh twig
// forest from the DB at the current head — bc.qmdbRootComputer is untouched and
// there is no concurrent-mutation race. The instance is discarded with the
// block. Returns nil when no tree-based commitment is active (the miner then
// falls back to the default MPT state root). tx must be a read tx at the parent
// head; the QMDB DB state tracks the head because writeBlockWithState flushes
// per block.
func (bc *BlockChain) NewMinerRootComputer(tx kv.Tx) state.RootComputer {
	if !bc.qmdbEnabled {
		return nil
	}
	// PERSISTENT speculative computer: a fresh instance pays a full index
	// rescan (5.9M point reads, ~5s IO-bound — invisible on CPU profiles) on
	// EVERY build, which alone consumed most of the 6s view window and kept
	// the network in its slow, timeout-riddled steady state. Reusing one
	// instance rides the sealed-twig O(meta) fast path: peel the previous
	// candidate's appends (restoring tree AND index to the last loaded
	// layout), then reload trusting the index below that cursor. Every doubt
	// falls back to the full rebuild inside ReloadForBuild.
	//
	// Serialization: only the miner worker's single build goroutine calls
	// this, so the computer needs no lock of its own.
	bc.minerRCMu.Lock()
	defer bc.minerRCMu.Unlock()
	rc := bc.minerRC
	if rc == nil {
		rc = commitment.NewQMDBRootComputer()
		rc.EnableUndoRecording() // the candidate peel below needs per-build undo
		bc.minerRC = rc
	}
	rc.SetCold(tx)
	if undo := rc.TakeUndo(); undo != nil {
		// Previous build's candidate ops are still on the speculative tree;
		// peel them so the index matches the last loaded layout again. A
		// failed peel just voids the trust — ReloadForBuild rebuilds.
		if err := rc.Tree().ApplyUndo(undo); err != nil {
			log.Debug("miner speculative tree candidate peel failed; full reload", "err", err)
			rc.VoidIndexTrust()
		}
	}
	if err := rc.ReloadForBuild(tx); err != nil {
		log.Warn("miner QMDB speculative reload failed; block uses default root", "err", err)
		return nil
	}
	return rc
}

// JMTCommitment returns the JMT commitment layer, or nil if not enabled.
func (bc *BlockChain) JMTCommitment() *commitment.JMTCommitment {
	return bc.jmtCommitment
}

// IsJMTEnabled returns true if JMT state commitment is active.
func (bc *BlockChain) IsJMTEnabled() bool {
	return bc.jmtEnabled
}

// VerkleCommitment returns the Verkle commitment layer, or nil if not enabled.
func (bc *BlockChain) VerkleCommitment() *commitment.VerkleCommitment {
	return bc.verkleCommitment
}

// VerkleEnabled returns true if Verkle state commitment is active.
func (bc *BlockChain) VerkleEnabled() bool {
	return bc.verkleEnabled
}

// EnableJMTForBlockProcessing enables JMT root computation during block
// processing. Only safe for fresh chains (private/dev) where all blocks
// are produced with JMT from genesis. Mainnet requires a state migration first.
func (bc *BlockChain) EnableJMTForBlockProcessing() {
	bc.jmtForBlockProcessing = true
	log.Info("JMT enabled for block processing (fresh chain)")
}

// StoreWitness caches a block witness keyed by block hash.
func (bc *BlockChain) StoreWitness(hash types.Hash, w *witness.BlockWitness) {
	if w != nil {
		bc.witnessCache.Add(hash, w)
	}
}

// GetWitness retrieves a cached block witness by block hash.
func (bc *BlockChain) GetWitness(hash types.Hash) (*witness.BlockWitness, bool) {
	return bc.witnessCache.Get(hash)
}

// SetZKProving enables or disables ZK proving mode.
// When requireProof is true, blocks without valid ZK proofs are rejected.
// Note: requireProof implies zkProving — if requireProof is true, zkProving is
// forced to true regardless of the enabled parameter.
func (bc *BlockChain) SetZKProving(enabled bool, requireProof ...bool) {
	bc.zkProving = enabled
	if len(requireProof) > 0 {
		bc.zkRequireProof = requireProof[0]
	}
	// requireProof implies zkProving — we need the verifier to validate proofs.
	if bc.zkRequireProof {
		bc.zkProving = true
	}
	if bc.zkProving {
		bc.zkVerifier = zkverifier.NewVerifier()
		log.Info("ZK proving mode enabled", "requireProof", bc.zkRequireProof)
	}
}

// IsZKProvingEnabled returns true if ZK proving is active.
func (bc *BlockChain) IsZKProvingEnabled() bool {
	return bc.zkProving
}

// Freezer returns the attached freezer, or nil if not configured.
func (bc *BlockChain) Freezer() freezer.FreezerAPI {
	return bc.freezer
}

// AncientReader returns the ancient reader, or nil if no freezer is attached.
func (bc *BlockChain) AncientReader() *freezer.AncientReader {
	return bc.ancientReader
}

// =============================================================================
// Lifecycle
// =============================================================================

func (bc *BlockChain) Start() error {
	bc.healPlainStateAheadOfMarkerOnStartup()
	bc.verifyAppliedStateOnStartup()
	bc.alignCanonicalToAppliedOnStartup()
	bc.repairCanonicalLinkageOnStartup()
	bc.revertSpeculativeOnStartup()

	// Pre-warm the speculative build computer off the leader's critical path:
	// the FIRST build after a restart otherwise pays the full ~5s index
	// rebuild inside its 6s view window (caught by the build-phase breakdown -
	// every other build rides the ~0.4s trusted reload). This must run AFTER all
	// startup state repair/revert operations: an earlier snapshot can leave the
	// trusted miner index pointing at slots that a startup unwind removed.
	if bc.qmdbEnabled {
		go func() {
			err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
				if rc := bc.NewMinerRootComputer(tx); rc != nil {
					if q, ok := rc.(*commitment.QMDBRootComputer); ok {
						q.SetCold(nil) // detach before this tx dies
					}
					log.Info("miner speculative computer pre-warmed")
				}
				return nil
			})
			if err != nil && bc.ctx.Err() == nil {
				log.Warn("miner speculative computer pre-warm failed", "err", err)
			}
		}()
	}
	// One Add per goroutine started right here — an Add larger than the
	// number of goroutines makes Close()'s wg.Wait() block forever and every
	// shutdown die on the 30s watchdog (observed fleet-wide when the third
	// loop was removed but the Add(3) stayed behind).
	bc.wg.Add(2)
	go bc.runLoop()
	go bc.updateFutureBlocksLoop()
	return nil
}

// healPlainStateAheadOfMarkerOnStartup detects and repairs the half-healed
// shape the old marker-only discontinuity realign left behind (observed live:
// a validator whose marker and tree sat at height T while PlainState still
// held blocks above T — every block it built carried stale nonces and the
// whole network rejected them). The changeset head is the detector:
// writeBlockWithState persists PlainState, changesets and marker in one
// transaction, so a changeset block above the marker proves PlainState is
// ahead. Roll it back to the marker; catch-up re-imports forward from there.
func (bc *BlockChain) healPlainStateAheadOfMarkerOnStartup() {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return
	}
	healed := false
	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		appliedNum, appliedHash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !ok {
			return err
		}
		last, err := highestChangesetBlock(tx)
		if err != nil || last <= appliedNum {
			return err
		}
		log.Warn("PlainState is ahead of the applied marker (half-healed discontinuity); rolling it back",
			"plainState", last, "marker", appliedNum)
		if err := realignAppliedToTree(tx, appliedNum, appliedHash); err != nil {
			return err
		}
		healed = true
		return nil
	}); err != nil {
		log.Error("PlainState/marker startup heal failed — the node may build on phantom state", "err", err)
	}
	if healed {
		bc.clearReadThroughCache()
	}
}

// verifyAppliedStateOnStartup cross-checks the QMDBApplied marker against the
// state the QMDB tree ACTUALLY holds (its root vs the applied block's
// header.Root). Observed live: a node's applied marker sat on a tx-bearing
// block whose transactions were never executed here — the marker said
// 13017704 while the world state was 13017703's, so the known-block replay
// decision skipped the block forever and every later replay failed with
// nonce-too-high. Root cause still under investigation (see the fingerprint
// log in writeBlockWithState); this check makes any such divergence
// self-healing: step the marker back to the parent when the tree root
// matches the parent's, so catch-up re-fetches and re-EXECUTES the block.
func (bc *BlockChain) verifyAppliedStateOnStartup() {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return
	}
	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		appliedNum, appliedHash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !ok || appliedNum == 0 {
			return err
		}
		hdr := rawdb.ReadHeader(tx, appliedHash, appliedNum)
		if hdr == nil {
			return nil
		}
		treeRoot := bc.qmdbRootComputer.Root()
		if treeRoot == hdr.Root {
			return nil // marker and executed state agree
		}
		parent := rawdb.ReadHeader(tx, hdr.ParentHash, appliedNum-1)
		if parent != nil && treeRoot == parent.Root {
			log.Warn("applied marker was one block ahead of the executed state; rolling PlainState and marker back",
				"marker", appliedNum, "markerRoot", fmt.Sprintf("%x", hdr.Root[:8]),
				"treeRoot", fmt.Sprintf("%x", treeRoot[:8]))
			return realignAppliedToTree(tx, appliedNum-1, hdr.ParentHash)
		}
		// Root cause found (qs-canon-probe -qmdbroot/-qmdbdiff): failed-block
		// executions used to leave their appends on the live tree (no undo row
		// ever persisted → unrevertable), and the mid-run recovery reload kept
		// a stale in-RAM index — both fixed (insertChain peel +
		// QMDBRootComputer.LoadFrom index rebuild). Stores that diverged
		// BEFORE those fixes carry the divergence baked into the persisted
		// layout, so this line keeps firing on them after every restart; it is
		// historical damage, not fresh. Chain validity is still enforced by
		// per-block execution (ValidateState). A re-replay/resync of the
		// store is the only way to clear it.
		log.Warn("applied marker does not match the reloaded QMDB tree root (pre-fix divergence baked into this store)",
			"marker", appliedNum, "markerRoot", fmt.Sprintf("%x", hdr.Root[:8]),
			"treeRoot", fmt.Sprintf("%x", treeRoot[:8]))
		return nil
	}); err != nil {
		log.Error("applied-state verification failed", "err", err)
	}
}

// alignCanonicalToAppliedOnStartup pulls the canonical head BACK to the QMDB
// applied head when a pre-guard bug (embedded-QC canonicalization of a stored
// but never-executed block) left canonical/HeadBlockHash ahead of the executed
// chain. Such a database is wedged: catch-up starts above the unexecuted span,
// every replayed block fails with nonce-too-high (the span's transactions are
// missing from the world state), and nothing re-executes the span because the
// canonical head claims it is already done. Truncating canonical back to the
// applied head makes the next catch-up re-fetch and EXECUTE the span. Runs
// before the linkage repair so the repair walks from the corrected head.
func (bc *BlockChain) alignCanonicalToAppliedOnStartup() {
	if !bc.qmdbEnabled {
		return
	}
	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		appliedNum, appliedHash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !ok {
			return err
		}
		headHash := rawdb.ReadHeadBlockHash(tx)
		headNum := rawdb.ReadHeaderNumber(tx, headHash)
		if headNum == nil || *headNum <= appliedNum {
			return nil // canonical at or behind the executed chain — healthy
		}
		blk, berr := rawdb.ReadBlockByHash(tx, appliedHash)
		if berr != nil || blk == nil {
			return fmt.Errorf("applied head block %d/%x unreadable: %w", appliedNum, appliedHash[:8], berr)
		}
		log.Warn("canonical head ran ahead of the executed chain; pulling it back to the applied head",
			"canonicalHead", *headNum, "appliedHead", appliedNum)
		if terr := rawdb.TruncateCanonicalHash(tx, appliedNum+1, false); terr != nil {
			return terr
		}
		rawdb.WriteHeadBlockHash(tx, appliedHash)
		if werr := rawdb.WriteHeadHeaderHash(tx, appliedHash); werr != nil {
			return werr
		}
		if werr := rawdb.WriteHotStuffCommittedHead(tx, appliedHash); werr != nil {
			return werr
		}
		bc.currentBlock.Store(blk)
		return nil
	}); err != nil {
		log.Error("align canonical to applied failed", "err", err)
	}
}

// canonicalRepairMaxScan caps the startup canonical-linkage walk. The walk's
// real terminator is the consensus-era boundary (the first header without the
// hotstuff N42H extra magic — replay-written canonical rows below it were never
// touched by any consensus-era writer); the cap is defense so a pathological
// table cannot turn startup into a 13M-header walk. NOTE: damage is NOT
// necessarily adjacent to the head — observed live: a non-linked span at
// head-2334 buried under 2.3k clean blocks (the network kept producing above
// it), which both a fixed depth (1024) and a quiet-streak heuristic missed.
const canonicalRepairMaxScan = 1_000_000

// hotstuffExtraMagic mirrors hotstuff.extraMagic ("N42H") — checked inline to
// avoid an internal -> consensus/hotstuff import (same pattern as internal/sync).
var hotstuffExtraMagic = []byte("N42H")

// repairCanonicalLinkageOnStartup heals holes in the canonical number→hash
// mapping: heights where canonical[n]'s parent is not canonical[n-1]. The old
// CommitToCanonical walk-back exited silently when an ancestor's BODY was
// missing locally, freezing such a hole permanently (its stop condition assumed
// prefix consistency). A holed chain is served by-number to catch-up peers as a
// non-linked sequence, wedging them (observed live at 13014242/243). The walk
// repairs every inconsistent link within canonicalRepairDepth of the head using
// header parent hashes — the header of a canonical block is always local.
func (bc *BlockChain) repairCanonicalLinkageOnStartup() {
	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		// Head via HeadBlockHash — the key CommitToCanonical writes. Older
		// leader-driven databases did not maintain HeadHeaderHash, so heal that
		// compatibility marker before walking the canonical lineage.
		headHash := rawdb.ReadHeadBlockHash(tx)
		headNum := rawdb.ReadHeaderNumber(tx, headHash)
		if headNum == nil || *headNum == 0 {
			return nil
		}
		if rawdb.ReadHeadHeaderHash(tx) != headHash {
			if werr := rawdb.WriteHeadHeaderHash(tx, headHash); werr != nil {
				return werr
			}
			log.Warn("canonical repair: advanced stale header-head marker to committed block head",
				"number", *headNum, "hash", fmt.Sprintf("%x", headHash[:8]))
		}
		num := *headNum
		curHash := headHash
		repaired, scanned := 0, 0
		for num > 0 && scanned < canonicalRepairMaxScan {
			hdr := rawdb.ReadHeader(tx, curHash, num)
			if hdr == nil {
				log.Error("canonical repair: header missing for canonical block, stopping",
					"number", num, "hash", fmt.Sprintf("%x", curHash[:8]))
				return nil
			}
			// Crossed below the consensus era into the replay prefix: those
			// canonical rows were written once by the replay tool and never
			// touched since — nothing to repair, and the prefix is millions of
			// blocks deep.
			if len(hdr.Extra) < len(hotstuffExtraMagic) ||
				!bytes.Equal(hdr.Extra[:len(hotstuffExtraMagic)], hotstuffExtraMagic) {
				break
			}
			below, berr := rawdb.ReadCanonicalHash(tx, num-1)
			if berr != nil {
				return berr
			}
			if below != hdr.ParentHash {
				if werr := rawdb.WriteCanonicalHash(tx, hdr.ParentHash, num-1); werr != nil {
					return werr
				}
				log.Warn("canonical repair: relinked broken canonical mapping",
					"number", num-1, "was", fmt.Sprintf("%x", below[:8]),
					"now", fmt.Sprintf("%x", hdr.ParentHash[:8]))
				repaired++
			}
			curHash = hdr.ParentHash
			num--
			scanned++
		}
		if repaired > 0 {
			log.Warn("canonical repair: completed", "relinked", repaired, "scanned", scanned)
		}
		return nil
	}); err != nil {
		log.Error("canonical repair failed", "err", err)
	}
}

// revertSpeculativeOnStartup restores the HotStuff-2 restart invariant: a
// recovering validator trusts only COMMITTED blocks. Speculative imports move
// the applied world state ahead of the canonical (= commit-driven) head; after
// a crash those uncommitted blocks — possibly losing sibling candidates — must
// be reverted so the node rejoins the network from its last committed state
// and converges via consensus (proposals + JustifyQC), not by replaying stale
// local candidates against live traffic.
func (bc *BlockChain) revertSpeculativeOnStartup() {
	var (
		appliedNum  uint64
		appliedHash types.Hash
		haveMarker  bool
	)
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		n, h, ok, err := rawdb.ReadQMDBApplied(tx)
		if err == nil && ok {
			appliedNum, appliedHash, haveMarker = n, types.Hash(h), true
		}
		return nil
	})
	if !haveMarker {
		return // fresh / non-speculative data — nothing tracked to revert
	}
	cur := bc.CurrentBlock()
	if cur == nil {
		return
	}
	canonNum := cur.Number64().Uint64()
	canonHash := cur.Hash()
	if appliedHash == canonHash {
		return // applied == committed: clean shutdown
	}
	log.Info("startup: reverting speculative (uncommitted) blocks to last committed head",
		"applied", appliedNum, "appliedHash", appliedHash.Hex()[:12],
		"committed", canonNum, "committedHash", canonHash.Hex()[:12])
	// CommitToCanonical only canonicalizes applied blocks, so the committed
	// head is on the applied lineage and the branch-switch unwind reaches it
	// directly. If the DB was left mid-switch in a state where it does not,
	// don't block startup — consensus catch-up aligns the branch later.
	if err := bc.AlignAppliedBranch(canonNum+1, canonHash); err != nil {
		log.Warn("startup speculative revert incomplete; consensus catch-up will align the branch", "err", err)
	}
}

func (bc *BlockChain) Close() error {
	bc.cancel()
	bc.wg.Wait()
	if bc.freezer != nil {
		if err := bc.freezer.Close(); err != nil {
			log.Warn("Failed to close freezer", "err", err)
		}
	}
	return nil
}

// StopInsert signals that block insertion should be aborted.
func (bc *BlockChain) StopInsert() {
	atomic.StoreInt32(&bc.procInterrupt, 1)
}

func (bc *BlockChain) insertStopped() bool {
	return atomic.LoadInt32(&bc.procInterrupt) == 1
}

// =============================================================================
// Background Loops
// =============================================================================

func (bc *BlockChain) runLoop() {
	defer func() {
		bc.cancel()
		bc.StopInsert()
		bc.wg.Done()
	}()

	for {
		select {
		case <-bc.ctx.Done():
			return
		case err, ok := <-bc.errorCh:
			if ok {
				log.Errorf("receive error from action, err:%v", err)
				return
			}
		}
	}
}

func (bc *BlockChain) updateFutureBlocksLoop() {
	futureTimer := time.NewTicker(2 * time.Second)
	defer futureTimer.Stop()
	defer bc.wg.Done()

	for {
		select {
		case <-futureTimer.C:
			bc.processFutureBlocks()
		case <-bc.ctx.Done():
			return
		}
	}
}

func (bc *BlockChain) processFutureBlocks() {
	if bc.futureBlocks.Len() == 0 {
		return
	}

	currentNumber, err := requireBlockNumber(bc.CurrentBlock(), "current block number unavailable")
	if err != nil {
		log.Warn("Skipping future block processing", "err", err)
		return
	}

	blocks := make([]block.IBlock, 0, bc.futureBlocks.Len())
	for _, key := range bc.futureBlocks.Keys() {
		if value, ok := bc.futureBlocks.Get(key); ok {
			if _, err := requireBlockNumber(value, "future block number unavailable"); err != nil {
				log.Warn("Dropping future block with unavailable number", "hash", key, "err", err)
				bc.futureBlocks.Remove(key)
				continue
			}
			blocks = append(blocks, value)
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Number64().Cmp(blocks[j].Number64()) < 0
	})

	if len(blocks) == 0 {
		return
	}
	firstNumber, err := requireBlockNumber(blocks[0], "future block number unavailable")
	if err != nil {
		log.Warn("Skipping future block processing", "err", err)
		return
	}
	if firstNumber.Uint64() > currentNumber.Uint64()+1 {
		return
	}

	// Insert one block at a time: under HotStuff the queue can hold SEVERAL
	// same-height sibling candidates (competing views), and a batch insert
	// trips InsertChain's contiguity check ("non contiguous insert: item 0 is
	// #N, item 1 is #N") — rejecting the WHOLE batch forever, in a retry loop.
	// Per-block inserts let each candidate succeed or fail on its own; a block
	// that imports (or is already known) leaves the queue, a still-unready one
	// stays for the next pass.
	inserted := 0
	for _, b := range blocks {
		if _, err := bc.InsertChain([]block.IBlock{b}); err != nil {
			log.Debug("insert future block failed", "number", b.Number64().Uint64(),
				"hash", b.Hash().Hex()[:12], "err", err)
			continue
		}
		bc.futureBlocks.Remove(b.Hash())
		inserted++
	}
	if inserted > 0 {
		log.Infof("insert %d future block(s) success from %d queued", inserted, len(blocks))
	}
}

// runNewBlockMessage, persistBlock and NewBlockHandler were the legacy
// proto (msg_proto.NewBlockMessageData) direct-push receive path. They have been
// removed: block delivery now travels as ETH-standard RLP end to end — SealedBlock
// RLP-encodes once and reaches peers via directPushBlock (RPCBlockPushTopicV1,
// decoded by ReadChunkedBlock) and the RLP gossip fallback (BroadcastBlock).

// knownBlockNeedsAuthorizedReplay reports whether an already-stored block's
// STATE is not the applied world state — i.e. the QMDB applied head sits on a
// same-height sibling or below — so an authorized import must re-process it
// (unwind the sibling + re-execute) instead of skipping it as known. Canonical
// membership is NOT the test: the canonical row can point at this block while
// the applied state is still the losing sibling's (they are maintained by
// different writers and can be one block apart after a crash or a wedge).
func (bc *BlockChain) knownBlockNeedsAuthorizedReplay(blk block.IBlock) bool {
	if !bc.qmdbEnabled {
		return false
	}
	need := false
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		appliedNum, appliedHash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !ok {
			return nil // fresh replay data: stored == applied
		}
		n := blk.Number64().Uint64()
		if n > appliedNum {
			need = true // above the applied head: state never applied
		} else if n == appliedNum && blk.Hash() != appliedHash {
			need = true // applied head is a same-height sibling
		}
		return nil
	})
	return need
}

// canonicalByCommitOnly reports whether this chain's canonical mapping, head
// pointers and in-memory head are advanced exclusively by the consensus commit
// (CommitToCanonical) rather than by import-time fork choice — true for
// leader-driven engines (HotStuff). Import paths must not touch
// HeaderCanonical/HeadBlockHash/currentBlock when this is set.
func (bc *BlockChain) canonicalByCommitOnly() bool {
	return bc.engine != nil && !bc.engine.Type().UsesTimerDrivenSealing()
}

// CommitToCanonical forces the HotStuff-committed block to be the canonical
// head. The miner can produce several candidate blocks at a height and the local
// node may have inserted a different one as canonical (via resultLoop) than the
// one consensus actually committed; this reconciles the two so every node's
// canonical chain follows the single committed chain. The committed block must
// already be in the DB (inserted by the leader's seal or received via direct
// push); if it isn't, this returns an error and the caller skips (it imports and
// retries once the block arrives). It walks back from the committed block,
// rewriting the canonical number→hash mapping until it reaches an ancestor that
// is already canonical, then updates the head pointers.
func (bc *BlockChain) CommitToCanonical(hash types.Hash) error {
	var committedNumber uint64
	var notify bool
	err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		blk, err := rawdb.ReadBlockByHash(tx, hash)
		if err != nil {
			return err
		}
		if blk == nil {
			return fmt.Errorf("committed block %s not in db", hash.Hex())
		}
		committedNumber = blk.Number64().Uint64()
		// Applied-state guard: canonicalization must never run ahead of the
		// EXECUTED chain. The embedded-QC catch-up path can name a block that
		// is STORED locally but was never executed here (a known-skip during a
		// range import); advancing canonical/HeadBlockHash past the applied
		// head makes the next catch-up start ABOVE the unexecuted span, whose
		// transactions are then permanently missing from the world state —
		// observed live: three nodes wedged in a BAD BLOCK loop with
		// "nonce too high" (state 23 nonces behind) after replaying past a
		// skipped tx-bearing block. Returning an error keeps the commit
		// retryable (pendingCommit / the next import notification) until the
		// executor catches up.
		if bc.qmdbEnabled {
			if an, _, ok, aerr := rawdb.ReadQMDBApplied(tx); aerr == nil && ok && blk.Number64().Uint64() > an {
				return fmt.Errorf("commit-to-canonical %d ahead of applied head %d (block stored but not executed)",
					blk.Number64().Uint64(), an)
			}
		}
		// Monotonicity guard: a commit for a height at or below the current
		// committed head whose block is ALREADY canonical is a late or duplicate
		// commit (the embedded-QC catch-up path racing the live Decide). The
		// higher commit's walk already canonicalized this lineage; re-running
		// would truncate the newer rows above it.
		if hn := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx)); hn != nil && *hn >= blk.Number64().Uint64() {
			if existing, rerr := rawdb.ReadCanonicalHash(tx, blk.Number64().Uint64()); rerr == nil && existing == hash {
				return nil
			}
		}
		// Rewrite canonical mapping from the committed block back to the fork
		// point. Walk via HEADERS, not bodies: the previous ReadBlockByHash walk
		// exited SILENTLY when an ancestor's body was missing locally, leaving a
		// permanent hole in the canonical chain (observed live: canonical[n]
		// whose parent is not canonical[n-1] — served by-number to catch-up
		// peers as a non-linked chain, wedging them). Headers exist for every
		// block on a committed lineage (they arrive with the proposal/import).
		// The stop condition requires BOTH the current mapping to match AND the
		// link below to be consistent — "existing == hash" alone re-froze holes
		// left by the old walk (prefix-consistency assumption).
		curNum := blk.Number64().Uint64()
		curHash := hash
		curParent := blk.ParentHash()
		rewrote := false
		for {
			existing, rerr := rawdb.ReadCanonicalHash(tx, curNum)
			if rerr != nil {
				return rerr
			}
			if existing != curHash {
				if werr := rawdb.WriteCanonicalHash(tx, curHash, curNum); werr != nil {
					return werr
				}
				rewrote = true
			}
			if curNum == 0 {
				break
			}
			below, berr := rawdb.ReadCanonicalHash(tx, curNum-1)
			if berr != nil {
				return berr
			}
			if existing == curHash && below == curParent {
				break // mapping and link both consistent — reached the canonical chain
			}
			hdr := rawdb.ReadHeader(tx, curParent, curNum-1)
			if hdr == nil {
				// Ancestor header not local (should not happen for a committed
				// lineage). Do NOT leave a silent hole: surface it — the
				// pendingCommit retry re-runs this walk when the block lands.
				return fmt.Errorf("commit-to-canonical: ancestor header %d/%x missing, canonical rewrite incomplete", curNum-1, curParent[:8])
			}
			curNum--
			curHash = curParent
			curParent = hdr.ParentHash
		}
		// Canonical rows ABOVE the committed height are dead-branch leftovers
		// (written by the retired import-time reorg path, or by an interrupted
		// higher rewrite). The canonical table must be exactly the committed-
		// chain prefix, or the by-number range server hands catching-up peers a
		// non-linked tail. TruncateCanonicalHash deletes every row >= from, so
		// one call self-heals a previously poisoned table.
		headNum := blk.Number64().Uint64()
		if above, aerr := rawdb.ReadCanonicalHash(tx, headNum+1); aerr != nil {
			return aerr
		} else if above != (types.Hash{}) {
			if terr := rawdb.TruncateCanonicalHash(tx, headNum+1, false); terr != nil {
				return terr
			}
			rewrote = true
		}
		rawdb.WriteHeadBlockHash(tx, hash)
		if werr := rawdb.WriteHeadHeaderHash(tx, hash); werr != nil {
			return werr
		}
		// QC-backed finality marker: written ONLY here. unwindForReimport's
		// committed floor trusts this key — not HeadBlockHash, which legacy
		// import-time writers polluted (a losing sibling stamped as "head"
		// permanently blocked the authorized branch switch that would have let
		// the node rejoin the committed chain).
		if werr := rawdb.WriteHotStuffCommittedHead(tx, hash); werr != nil {
			return werr
		}
		bc.currentBlock.Store(blk)
		// Canonical rows changed under any read-through layer: invalidate it, or
		// by-number readers (RPC, the catch-up range SERVER) keep returning the
		// pre-rewrite chain indefinitely — observed live: a peer catching up was
		// fed a stale, non-linked canonical sequence and wedged. unwindForReimport
		// already does this; the commit-time rewrite needs it just as much.
		if rewrote {
			if cache := layered.ExtractCache(bc.ChainDB); cache != nil {
				cache.Clear()
			}
		}
		log.Info("commit-to-canonical applied", "number", blk.Number64().Uint64(), "hash", hash.Hex())
		committedNumber = blk.Number64().Uint64()
		notify = true
		return nil
	})
	if err != nil {
		return err
	}
	// HotStuff imports candidates as side blocks and advances the canonical
	// head only here. Notify after the database transaction commits: the
	// import-time canonical callback is intentionally bypassed on this path.
	if notify {
		bc.notifyBlockCommitted(committedNumber)
	}
	return nil
}

// LowestSiblingAtHeight returns the locally-known block at `number` with the
// lowest hash among those whose parent is `parentHash`, and whether one exists.
// The leader uses it (fix A) to converge on a single same-height candidate: when
// a height misses its first commit, later views' leaders each build a divergent
// block, scattering import-gated votes so no candidate reaches quorum. Having
// every leader re-propose the same deterministic (lowest-hash) candidate stacks
// the votes on one block. Header enumeration is a cheap number-prefix cursor
// scan (siblings share the number prefix in the Headers table).
func (bc *BlockChain) LowestSiblingAtHeight(number uint64, parentHash types.Hash) (block.IBlock, bool) {
	var result block.IBlock
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		headers, err := rawdb.ReadHeadersByNumber(tx, number)
		if err != nil {
			return err
		}
		var lowest types.Hash
		found := false
		for _, h := range headers {
			if h.ParentHash != parentHash {
				continue
			}
			hh := h.Hash()
			if !found || bytes.Compare(hh.Bytes(), lowest.Bytes()) < 0 {
				lowest, found = hh, true
			}
		}
		if !found {
			return nil
		}
		blk, err := rawdb.ReadBlockByHash(tx, lowest)
		if err != nil {
			return err
		}
		if blk != nil {
			result = blk
		}
		return nil
	}); err != nil {
		return nil, false
	}
	if result == nil {
		return nil, false
	}
	return result, true
}

// SetCommitteePool injects the BLS committee-evidence builder. With it set, each
// persisted block also writes its consensus evidence (continuing the resealed
// chain's multi-sig). Pass nil to disable.
func (bc *BlockChain) SetCommitteePool(pool CommitteeEvidenceBuilder) {
	bc.committeePool = pool
}

// CommitteePool returns the configured committee-evidence builder, or nil.
func (bc *BlockChain) CommitteePool() CommitteeEvidenceBuilder {
	return bc.committeePool
}

// ReadConsensusEvidence reads the stored consensus evidence for a block number,
// satisfying the hotstuff.CEReader interface (parent-CE lookup for the
// ParentBeaconRoot link).
func (bc *BlockChain) ReadConsensusEvidence(blockNum uint64) (*rawdb.ConsensusEvidence, error) {
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return rawdb.ReadConsensusEvidence(tx, blockNum)
}

// =============================================================================
// Peer and P2P
// =============================================================================

func (bc *BlockChain) AddPeer(hash string, remoteBlock uint64, peerID peer.ID) error {
	if bc.genesisBlock.Hash().String() != hash {
		return errors.New("failed to addPeer, err: genesis block different")
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if _, ok := bc.peers[peerID]; ok {
		return errors.New("failed to addPeer, err: the peer already exists")
	}

	log.Debugf("local height:%d --> remote height: %d", bc.CurrentBlock().Number64(), remoteBlock)
	bc.peers[peerID] = true
	return nil
}

func (bc *BlockChain) SealedBlock(b block.IBlock) error {
	// Reliable direct push to each connected peer. Single-publisher gossip is
	// unreliable for HotStuff block delivery — when only the current leader
	// publishes the block topic, the gossipsub mesh doesn't form and followers
	// never receive the block (so they can't import/vote on the hash-only
	// Proposal). Mirrors the Rust node's send_block_direct_reliable.
	// Encode the block to ETH-standard RLP once and reuse the bytes for both the
	// direct push and the gossip fallback (the hash is keccak(rlp(header)), so the
	// same bytes are semantically identical on every path). Avoids re-encoding the
	// block twice per seal on the producer's hot path.
	data, err := rlp.EncodeToBytes(b)
	if err != nil {
		return err
	}
	bc.directPushBlock(b, data)
	// Also gossip as a best-effort fallback.
	return bc.p2p.BroadcastBlock(bc.ctx, data)
}

// directPushBlock opens a stream to each connected peer and writes the block as
// a single chunked response (status code + fork digest + encoded block) — the
// same wire format ReadChunkedBlock decodes in the sync block-push handler. The
// RLP-encoded block bytes are passed in (encoded once by SealedBlock).
func (bc *BlockChain) directPushBlock(b block.IBlock, data []byte) {
	if bc.p2p == nil {
		return
	}
	digest, err := utils.CreateForkDigest(b.Number64(), bc.genesisBlock.Hash())
	if err != nil {
		return
	}
	protoID := protocol.ID(p2p.RPCBlockPushTopicV1 + bc.p2p.Encoding().ProtocolSuffix())
	peers := bc.p2p.Peers().Connected()
	log.Info("direct-push block", "number", b.Number64().Uint64(), "peers", len(peers))
	for _, pid := range peers {
		go func(pid peer.ID) {
			ctx, cancel := context.WithTimeout(bc.ctx, 5*time.Second)
			defer cancel()
			stream, err := bc.p2p.Host().NewStream(ctx, pid, protoID)
			if err != nil {
				log.Info("direct-push stream failed", "peer", pid.String()[:12], "err", err)
				return
			}
			defer func() { _ = stream.Close() }()
			if _, err := stream.Write([]byte{0x00}); err != nil { // success status code
				return
			}
			if _, err := stream.Write(digest[:]); err != nil {
				return
			}
			if _, err := bc.p2p.Encoding().EncodeWithMaxLength(stream, &rawBlockBytes{data: data}); err != nil {
				return
			}
		}(pid)
	}
}

// rawBlockBytes carries pre-encoded RLP block bytes through the SSZ length/snappy
// framing of EncodeWithMaxLength without imposing an SSZ schema. Mirrors the sync
// package's rawSSZBytes; defined here to avoid an import cycle with internal/sync.
type rawBlockBytes struct{ data []byte }

func (r *rawBlockBytes) MarshalSSZ() ([]byte, error)             { return r.data, nil }
func (r *rawBlockBytes) MarshalSSZTo(buf []byte) ([]byte, error) { return append(buf, r.data...), nil }
func (r *rawBlockBytes) SizeSSZ() int                            { return len(r.data) }
func (r *rawBlockBytes) UnmarshalSSZ(buf []byte) error {
	r.data = append([]byte(nil), buf...)
	return nil
}

func (bc *BlockChain) SetEngine(engine interface{}) {
	if e, ok := engine.(consensus.Engine); ok {
		bc.engine = e
	}
}

// =============================================================================
// Chain Queries (simple)
// =============================================================================

func (bc *BlockChain) LatestBlockCh() (block.IBlock, error) {
	select {
	case <-bc.ctx.Done():
		return nil, errors.New("the main chain is closed")
	case blk, ok := <-bc.latestBlockCh:
		if !ok {
			return nil, errors.New("the main chain is closed")
		}
		return blk, nil
	}
}

// InsertHeader is not implemented (light client mode unsupported).
func (bc *BlockChain) InsertHeader(headers []block.IHeader) (int, error) {
	return 0, errors.New("InsertHeader not implemented: light client mode is not yet supported")
}

// InsertBlock is deprecated; use InsertChain.
func (bc *BlockChain) InsertBlock(blocks []block.IBlock, isSync bool) (int, error) {
	return 0, fmt.Errorf("deprecated: use InsertChain instead, got %d blocks", len(blocks))
}

func (bc *BlockChain) skipBlock(err error) bool {
	return errors.Is(err, ErrKnownBlock)
}

// ReorgNeeded determines if a reorg should occur based on block numbers and difficulty.
func (bc *BlockChain) ReorgNeeded(current block.IBlock, header block.IBlock) bool {
	switch current.Number64().Cmp(header.Number64()) {
	case 1:
		return false
	case 0:
		return current.Difficulty().Cmp(header.Difficulty()) < 0
	}
	return true
}

// SetHead resets the canonical head to a specific block number.
func (bc *BlockChain) SetHead(head uint64) error {
	newHeadBlock, err := bc.GetBlockByNumber(uint256.NewInt(head))
	if err != nil {
		return err
	}
	if newHeadBlock == nil {
		return fmt.Errorf("block #%d not found", head)
	}
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		return rawdb.WriteHeadHeaderHash(tx, newHeadBlock.Hash())
	})
}

// AddFutureBlock queues a block for future processing if it is within the allowed window.
// PoS blocks (difficulty == 0) are never queued.
func (bc *BlockChain) AddFutureBlock(blk block.IBlock) error {
	max := uint64(time.Now().Unix() + maxTimeFutureBlocks)
	if blk.Time() > max {
		return fmt.Errorf("future block timestamp %v > allowed %v", blk.Time(), max)
	}
	// Note: difficulty-0 (PoS/HotStuff) blocks are NOT skipped. HotStuff blocks
	// pushed directly from the leader can arrive before their parent and must be
	// queued so processFutureBlocks imports them once the parent lands; skipping
	// them here would silently drop out-of-order blocks and stall the follower.
	concreteBlock, err := requireConcreteBlock(blk, "unexpected block type")
	if err != nil {
		return err
	}
	blockNumber, err := requireBlockNumber(concreteBlock, "block number unavailable")
	if err != nil {
		return err
	}
	log.Info("add future block", "hash", blk.Hash(), "number", blockNumber.Uint64(), "stateRoot", blk.StateRoot(), "txs", len(blk.Body().Transactions()))
	bc.futureBlocks.Add(blk.Hash(), concreteBlock)
	return nil
}

// =============================================================================
// Chain Insertion
// =============================================================================

// InsertChain validates contiguity and inserts blocks into the canonical chain.
func (bc *BlockChain) InsertChain(chain []block.IBlock) (int, error) {
	if len(chain) == 0 {
		return 0, nil
	}
	for i := 1; i < len(chain); i++ {
		blk, prev := chain[i], chain[i-1]
		if blk.Number64().Cmp(uint256.NewInt(0).Add(prev.Number64(), uint256.NewInt(1))) != 0 || blk.ParentHash() != prev.Hash() {
			log.Error("Non contiguous block insert",
				"number", blk.Number64().String(), "hash", blk.Hash(),
				"parent", blk.ParentHash(),
				"prev number", prev.Number64(), "prev hash", prev.Hash(),
			)
			return 0, fmt.Errorf("non contiguous insert: item %d is #%s [%x..], item %d is #%s [%x..] (parent [%x..])", i-1, prev.Number64().String(),
				prev.Hash().Bytes()[:4], i, blk.Number64().String(), blk.Hash().Bytes()[:4], blk.ParentHash().Bytes()[:4])
		}
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	return bc.insertChain(chain, false)
}

// InsertChainAuthorized imports blocks WITH branch-switch authority: if a
// block's parent is a sibling of the applied head, the applied branch is
// reverted onto the incoming branch. Reserved for consensus-driven imports
// (the proposal chain-align in fetch-on-miss); passive paths (gossip, push,
// future-queue) must use InsertChain, which refuses to revert.
func (bc *BlockChain) InsertChainAuthorized(chain []block.IBlock) (int, error) {
	if len(chain) == 0 {
		return 0, nil
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	return bc.insertChain(chain, true)
}

func (bc *BlockChain) insertChain(chain []block.IBlock, authorizedSwitch bool) (int, error) {
	if bc.insertStopped() {
		return 0, nil
	}

	var (
		stats     = insertStats{startTime: time.Now()}
		lastCanon block.IBlock
	)
	defer func() {
		_ = lastCanon
	}()

	// Parallel header verification
	headers := make([]block.IHeader, len(chain))
	seals := make([]bool, len(chain))
	for i, blk := range chain {
		headers[i] = blk.Header()
		seals[i] = true
	}
	abort, results := bc.engine.VerifyHeaders(bc, headers, seals)
	defer close(abort)

	it := newInsertIterator(chain, results, bc.validator)
	blk, err := it.next()

	// Skip already-known blocks
	if bc.skipBlock(err) {
		// Leader-driven chains: an already-known block never moves the canonical
		// chain at import time — canonicalization is commit-driven
		// (CommitToCanonical is the single authority). writeKnownBlock would
		// re-enter the reorg/head-write path for a non-canonical sibling and
		// fight the commit authority over the canonical table.
		if bc.canonicalByCommitOnly() {
			for blk != nil && bc.skipBlock(err) {
				// A block that is STORED (even canonical) is not necessarily
				// APPLIED: the QMDB world state may sit on a same-height
				// sibling (the node's own dead candidate). On an AUTHORIZED
				// import (consensus/catch-up named this branch) such a block
				// must NOT be skipped — nothing else will ever move the
				// applied state off the sibling and onto it (observed live: a
				// node held the committed block in its DB, canonical even
				// pointed at it, but the applied head was its own candidate;
				// every fetched range was "already known" at that height and
				// the child failed unknown-ancestor forever). The main loop
				// accepts ErrKnownBlock and performs the unwind + re-exec.
				if authorizedSwitch && bc.knownBlockNeedsAuthorizedReplay(blk) {
					break
				}
				log.Debug("Ignoring already known block", "number", blk.Number64(), "hash", blk.Hash())
				stats.ignored++
				blk, err = it.next()
			}
		} else {
			var (
				reorg         bool
				current       = bc.CurrentBlock()
				currentNumber *uint256.Int
			)
			currentNumber, err = requireBlockNumber(current, "current block number unavailable")
			if err != nil {
				return it.index, err
			}
			for blk != nil && bc.skipBlock(err) {
				reorg, err = bc.forker.ReorgNeeded(current.Header(), blk.Header())
				if err != nil {
					return it.index, err
				}
				if reorg {
					blockNumber, numberErr := requireBlockNumber(blk, "block number unavailable")
					if numberErr != nil {
						return it.index, numberErr
					}
					if blockNumber.Uint64() > currentNumber.Uint64() || bc.GetCanonicalHash(blockNumber) != blk.Hash() {
						break
					}
				}
				log.Debug("Ignoring already known block", "number", blk.Number64(), "hash", blk.Hash())
				stats.ignored++
				blk, err = it.next()
			}
			for blk != nil && bc.skipBlock(err) {
				log.Debug("Writing previously known block", "number", blk.Number64(), "hash", blk.Hash())
				if err := bc.writeKnownBlock(nil, blk); err != nil {
					return it.index, err
				}
				lastCanon = blk
				blk, err = it.next()
			}
		}
	}

	switch {
	case errors.Is(err, ErrPrunedAncestor):
		// Authorized branch switch: the parent's STATE is missing because the
		// applied head is a LOSING same-height sibling of this block (the
		// world state sits on the sibling, so the parent has a header but no
		// state → ErrPrunedAncestor). insertSideChain cannot resolve this —
		// it walks down looking for a stateful ancestor it will never find
		// (QMDB keeps state only at the applied head) and the whole range is
		// rejected forever (observed live: a laggard spinning on
		// pruned-ancestor for every fetched range). The remedy is the same
		// authorized unwind the main loop performs: revert the applied
		// sibling branch so the parent's state is restored, then process this
		// block on the normal path.
		if authorizedSwitch {
			if bn, nerr := requireBlockNumber(blk, "block number unavailable"); nerr == nil {
				if uerr := bc.unwindForReimport(bn.Uint64(), blk.ParentHash(), true); uerr == nil {
					log.Info("authorized sibling switch: applied branch unwound for pruned-ancestor import",
						"number", bn.Uint64(), "hash", blk.Hash().Hex()[:12])
					err = nil // parent state restored — fall through to normal processing
				} else {
					log.Warn("authorized sibling switch failed; falling back to sidechain path",
						"number", bn.Uint64(), "err", uerr)
				}
			}
		}
		if err != nil {
			log.Debug("Pruned ancestor, inserting as sidechain", "number", blk.Number64(), "hash", blk.Hash())
			return bc.insertSideChain(blk, it, authorizedSwitch)
		}

	case errors.Is(err, ErrFutureBlock) || isUnknownAncestorErr(err):
		// Queue any block whose parent we don't have yet (not only when the parent
		// is already queued). Direct-pushed HotStuff blocks can arrive out of order
		// (e.g. block N+1 before N); future-queue them so they import once the
		// parent arrives, instead of rejecting them as bad blocks.
		for blk != nil && (it.index == 0 || isUnknownAncestorErr(err)) {
			log.Debug("Future block, postponing import", "number", blk.Number64(), "hash", blk.Hash())
			if err := bc.AddFutureBlock(blk); err != nil {
				return it.index, err
			}
			blk, err = it.next()
		}
		stats.queued += it.processed()
		stats.ignored += it.remaining()
		return it.index, err

	case err != nil && !errors.Is(err, ErrKnownBlock):
		bc.futureBlocks.Remove(blk.Hash())
		stats.ignored += len(it.chain)
		bc.reportBlock(blk, nil, err)
		return it.index, err
	}

	// Process blocks
	evmRecord := func(ctx context.Context, db kv.RwDB, blockNr uint64, f func(tx kv.Tx, ibs *state.IntraBlockState, reader state.StateReader, writer state.WriterWithChangeSets) (map[types.Address]*uint256.Int, error)) (*state.IntraBlockState, map[types.Address]*uint256.Int, error) {
		tx, err := db.BeginRo(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback()

		var stateReader state.StateReader = state.NewPlainStateReader(tx)
		if cache := layered.ExtractCache(db); cache != nil {
			stateReader = state.NewCachedStateReader(stateReader, cache)
		}
		ibs := state.New(stateReader)
		// Inject root computer for tree-based state root computation.
		// JMT: only for fresh chains where all blocks use JMT from genesis.
		// MPT: always inject when enabled (branches persisted in MDBX).
		if bc.rootComputer != nil && bc.jmtForBlockProcessing {
			ibs.SetRootComputer(bc.rootComputer)
		}
		if bc.mptEnabled && bc.mptRootComputer != nil {
			bc.mptRootComputer.SetReadTx(tx)
			bc.mptRootComputer.SetStateReader(commitment.NewPlainStateMPTReader(tx))
			ibs.SetRootComputer(bc.mptRootComputer)
		}
		if bc.qmdbEnabled && bc.qmdbRootComputer != nil {
			// QMDB faults flushed entries back from the committed positional log
			// via this block's read tx; the in-RAM index keeps key->slot resident.
			bc.qmdbRootComputer.SetCold(tx)
			ibs.SetRootComputer(bc.qmdbRootComputer)
		}
		stateWriter := state.NewNoopWriter()

		nopay, err := f(tx, ibs, stateReader, stateWriter)
		if err != nil {
			return nil, nil, err
		}
		return ibs, nopay, nil
	}

	for ; blk != nil && (err == nil || errors.Is(err, ErrKnownBlock)); blk, err = it.next() {
		if bc.insertStopped() {
			log.Debug("Abort during block processing")
			break
		}

		log.Tracef("Current block: number=%v, hash=%v, difficult=%v | Insert block block: number=%v, hash=%v, difficult= %v",
			bc.CurrentBlock().Number64(), bc.CurrentBlock().Hash(), bc.CurrentBlock().Difficulty(), blk.Number64(), blk.Hash(), blk.Difficulty())

		// Out-of-order direct-pushed block: if the parent isn't imported yet,
		// future-queue this block instead of executing it. EVM execution would fail
		// to read the missing parent state and report a bad block; queuing lets it
		// import once the parent arrives and processFutureBlocks retries it. This is
		// the norm for HotStuff blocks pushed directly from the leader, which can
		// arrive before their parent.
		if n := blk.Number64().Uint64(); n > 0 && !bc.HasBlock(blk.ParentHash(), n-1) {
			if aerr := bc.AddFutureBlock(blk); aerr != nil {
				return it.index, aerr
			}
			stats.queued++
			continue
		}

		// ZK fast path: if the block has a valid ZK proof and ZK verification
		// is enabled, skip EVM re-execution and trust the proven state root.
		hasZKProof := bc.zkProving && blk.Body() != nil && len(blk.Body().ZKProof()) > 0
		if hasZKProof {
			if bc.tryZKFastPath(blk) {
				start := time.Now()
				wstart := time.Now()
				status, werr := bc.writeBlockWithState(blk, nil, nil, nil)
				if werr != nil {
					return it.index, werr
				}
				blockWriteTimer.Observe(float64(time.Since(wstart)))
				blockInsertTimer.Observe(float64(time.Since(start)))
				stats.processed++
				if status == CanonStatTy {
					lastCanon = blk
				}
				log.Info("Block accepted via ZK proof (EVM skipped)", "number", blk.Number64(), "hash", blk.Hash())
				continue
			}
			// Proof exists but verification failed.
			if bc.zkRequireProof {
				log.Warn("Block rejected: ZK proof verification failed", "number", blk.Number64(), "hash", blk.Hash())
				return it.index, errZKProofRequired
			}
			// Not required — fall through to EVM execution.
		} else if bc.zkRequireProof {
			// No proof present but required.
			log.Warn("Block rejected: ZK proof required but not provided", "number", blk.Number64(), "hash", blk.Hash())
			return it.index, errZKProofRequired
		}

		start := time.Now()
		var receipts block.Receipts
		var logs []*block.Log
		var usedGas uint64
		concreteBlock, err := requireConcreteBlock(blk, "unexpected block type")
		if err != nil {
			return it.index, err
		}
		concreteHeader, err := requireConcreteHeader(concreteBlock.Header(), "unexpected block header type")
		if err != nil {
			return it.index, err
		}
		blockNumber, err := requireBlockNumber(blk, "block number unavailable")
		if err != nil {
			return it.index, err
		}

		// Applied-evidence known-block gate. The validator's ErrKnownBlock uses
		// HasBlockAndState = IsCanonicalHash, which on a commit-authority-only
		// chain is FALSE for a block that was just written but not yet
		// committed — so a concurrent re-import of the very block this node
		// just applied (fetch-on-miss racing the leader's own write; observed
		// live) sailed past every known-block skip, and unwindForReimport
		// "branch-switched" the block onto itself: a pointless revert+re-execute
		// of an identical block that destabilized the live tree under the
		// next build. A block whose state IS the applied lineage needs nothing
		// from this loop.
		if bc.qmdbEnabled && bc.HasAppliedBlock(blk.Hash(), blockNumber.Uint64()) {
			log.Debug("Ignoring already applied block", "number", blockNumber.Uint64(), "hash", blk.Hash())
			stats.ignored++
			continue
		}

		// HotStuff branch switch: if the applied world state is not the state of
		// this block's parent (same-height sibling re-import / view-change
		// re-proposal), cleanly revert applied blocks until it is — the EVM must
		// read the parent's state and the QMDB tree must re-append at identical
		// slots (its root is append-order-dependent). Committed BEFORE evmRecord
		// so the execution's read snapshot sees the reverted state. No-op on the
		// plain forward path.
		if uerr := bc.unwindForReimport(blockNumber.Uint64(), blk.ParentHash(), authorizedSwitch); uerr != nil {
			if errors.Is(uerr, consensus.ErrUnknownAncestor) || errors.Is(uerr, errRevertUnavailable) {
				// Either the incoming block's parent is a sibling this node never
				// applied, or the revert hit a recoverable local QMDB condition
				// (stale/missing leaf blob). Both are non-fatal: future-queue the
				// block and let it retry — never crash, never mark it BAD.
				if errors.Is(uerr, errRevertUnavailable) {
					log.Warn("branch-switch revert unavailable; future-queuing block",
						"number", blockNumber.Uint64(), "hash", fmt.Sprintf("%x", blk.Hash().Bytes()[:8]), "err", uerr)
				}
				if aerr := bc.AddFutureBlock(blk); aerr != nil {
					return it.index, aerr
				}
				stats.queued++
				continue
			}
			return it.index, uerr
		}

		// Second line of defense against a dangling candidate: a leader whose
		// sealed block lost the view (or never sealed) left that candidate's
		// appends on the live tree; executing the incoming winner on top would
		// compute a shifted root. The miner peels on its own path — this covers
		// the import path racing ahead of it.
		bc.PeelDanglingQMDBAppends()
		// Pre-execution invariant: the live tree must BE the parent's
		// post-state. Any tree/marker discontinuity (observed live: a store
		// rewound by an unwind while the marker claimed blocks the tree never
		// held) would otherwise execute on the wrong base and produce a root
		// the cluster rejects — silently before root verification existed,
		// loudly-but-wastefully after. Realign the marker to the tree and
		// future-queue the block; catch-up then replays forward from the
		// tree's true height.
		if qerr := bc.ensureQMDBTreeAtParent(blk); qerr != nil {
			if errors.Is(qerr, errQMDBBehindParent) {
				log.Debug("block ahead of the applied state; future-queuing",
					"number", blockNumber.Uint64())
			} else {
				log.Warn("QMDB tree/marker discontinuity before execution; realigned and queueing",
					"number", blockNumber.Uint64(), "err", qerr)
			}
			if aerr := bc.AddFutureBlock(blk); aerr != nil {
				return it.index, aerr
			}
			stats.queued++
			continue
		}
		ibs, nopay, err := evmRecord(bc.ctx, bc.ChainDB, blockNumber.Uint64(), func(tx kv.Tx, ibs *state.IntraBlockState, reader state.StateReader, writer state.WriterWithChangeSets) (map[types.Address]*uint256.Int, error) {
			getHeader := func(hash types.Hash, number uint64) *block.Header {
				return rawdb.ReadHeader(tx, hash, number)
			}
			blockHashFunc := GetHashFn(concreteHeader, getHeader)

			// Start state prefetching in background if cache is available.
			if bc.prefetchEnabled {
				prefetcher := NewStatePrefetcher(bc.chainConfig)
				if bc.prefetchPredictor != nil {
					prefetcher.SetPredictor(bc.prefetchPredictor)
				}
				prefetcher.Prefetch(concreteBlock, reader)
				defer prefetcher.Close()
			}

			pstart := time.Now()
			var nopay map[types.Address]*uint256.Int
			if bc.parallelEVM {
				if sp, ok := bc.process.(*StateProcessor); ok {
					receipts, nopay, logs, usedGas, err = sp.ProcessParallel(concreteBlock, ibs, reader, writer, blockHashFunc)
				} else {
					receipts, nopay, logs, usedGas, err = bc.process.Process(concreteBlock, ibs, reader, writer, blockHashFunc)
				}
			} else {
				receipts, nopay, logs, usedGas, err = bc.process.Process(concreteBlock, ibs, reader, writer, blockHashFunc)
			}
			if err != nil {
				// Only a deterministic execution / consensus violation is a bad
				// block. Transient/internal failures — a nonce-too-high ordering
				// gap (block ran ahead of the executed state), context
				// cancellation, an unavailable ancestor, or a DB/IO error wrapped
				// as consensus.ErrInternal — are RETRYABLE: the outer loop queues
				// or retries them. Marking those BAD cached a healthy block as bad
				// until a restart cleared the LRU (the recurring QMDB pain); the
				// is_validation_error split (mirroring reth) prevents it.
				if consensus.IsInternalError(err) || strings.Contains(err.Error(), "nonce too high") {
					return nil, err
				}
				bc.reportBlock(blk, receipts, err)
				return nil, fmt.Errorf("%w: %w", consensus.ErrExecutionInvalid, err)
			}
			ptime := time.Since(pstart)

			vstart := time.Now()
			if err := bc.validator.ValidateState(blk, ibs, receipts, usedGas); err != nil {
				bc.reportBlock(blk, receipts, err)
				return nil, fmt.Errorf("%w: %w", consensus.ErrExecutionInvalid, err)
			}
			vtime := time.Since(vstart)

			blockExecutionTimer.Observe(float64(ptime))
			blockValidationTimer.Observe(float64(vtime))
			return nopay, nil
		})
		if err != nil {
			// The execution tx above rolled back, but the live QMDB tree is
			// NOT transactional: if execution got as far as ComputeRoot (a
			// ValidateState root mismatch always does), the failed block's
			// appends are already on the tree, undo row never persisted —
			// they could never be unwound and every later root would be
			// silently shifted (observed live as disk/tree/cluster
			// divergence). Peel them off with the in-memory undo record.
			bc.revertUncommittedQMDBAppends(blockNumber.Uint64())
			if strings.Contains(err.Error(), "nonce too high") {
				// Execution ran ahead of the applied state (see evmRecord):
				// queue the block for replay once its prefix lands — the same
				// treatment as an unknown ancestor. Under transaction load
				// this fired about once a minute as a transient, self-healing
				// "BAD BLOCK" that was never actually bad.
				log.Debug("nonce gap during execution; future-queuing block",
					"number", blk.Number64().Uint64(), "err", err)
				if aerr := bc.AddFutureBlock(blk); aerr != nil {
					return it.index, aerr
				}
				stats.queued++
				continue
			}
			return it.index, err
		}

		wstart := time.Now()
		status, err := bc.writeBlockWithState(blk, receipts, ibs, nopay)
		if err != nil {
			return it.index, err
		}
		blockWriteTimer.Observe(float64(time.Since(wstart)))
		blockInsertTimer.Observe(float64(time.Since(start)))

		stats.processed++
		stats.usedGas += usedGas

		switch status {
		case CanonStatTy:
			log.Trace("Inserted new block ", "number ", blk.Number64(), "hash", blk.Hash(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(),
				"elapsed", time.Since(start).Seconds(), "root", blk.StateRoot())
			if len(logs) > 0 {
				event.GlobalEvent.Send(common.NewLogsEvent{Logs: logs})
			}
			lastCanon = blk
		case SideStatTy:
			log.Debug("Inserted forked block", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		default:
			log.Warn("Inserted block with unknown status", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		}
	}

	// Queue remaining future blocks
	if blk != nil && errors.Is(err, ErrFutureBlock) {
		if err := bc.AddFutureBlock(blk); err != nil {
			return it.index, err
		}
		blk, err = it.next()
		for ; blk != nil && errors.Is(err, ErrUnknownAncestor); blk, err = it.next() {
			if err := bc.AddFutureBlock(blk); err != nil {
				return it.index, err
			}
			stats.queued++
		}
	}
	stats.ignored += it.remaining()
	return it.index, err
}

// =============================================================================
// Sidechain Insertion
// =============================================================================

func (bc *BlockChain) insertSideChain(blk block.IBlock, it *insertIterator, authorizedSwitch bool) (int, error) {
	var (
		externTd      uint256.Int
		lastBlock     = blk
		current       = bc.CurrentBlock()
		currentNumber *uint256.Int
	)
	err := ErrPrunedAncestor
	currentNumber, err = requireBlockNumber(current, "current block number unavailable")
	if err != nil {
		return 0, err
	}

	for ; blk != nil && errors.Is(err, ErrPrunedAncestor); blk, err = it.next() {
		blockNumber, numberErr := requireBlockNumber(blk, "block number unavailable")
		if numberErr != nil {
			return it.index, numberErr
		}
		if currentNumber.Cmp(blockNumber) >= 0 {
			canonical, err := bc.GetBlockByNumber(blockNumber)
			if err != nil {
				return 0, err
			}
			if canonical != nil && canonical.Hash() == blk.Hash() {
				pt := bc.GetTd(blk.Hash(), blockNumber)
				if pt == nil {
					log.Warn("Missing td for canonical block, skipping sidechain processing", "hash", blk.Hash(), "number", blk.Number64())
					return 0, nil
				}
				externTd = *pt
				continue
			}
			if canonical != nil && canonical.StateRoot() == blk.StateRoot() {
				log.Warn("Sidechain ghost-state mismatch detected", "number", blk.Number64(), "sideroot", blk.StateRoot(), "canonroot", canonical.StateRoot())
				return it.index, errors.New("sidechain ghost-state mismatch")
			}
		}
		if externTd.Cmp(uint256.NewInt(0)) == 0 {
			if blockNumber.IsZero() {
				return it.index, errors.New("sidechain block has no parent")
			}
			parentTd := bc.GetTd(blk.ParentHash(), uint256.NewInt(0).Sub(blockNumber, uint256.NewInt(1)))
			if parentTd == nil {
				log.Warn("Missing td for parent block, skipping sidechain processing", "parentHash", blk.ParentHash(), "number", blk.Number64())
				return 0, nil
			}
			externTd = *parentTd
		}
		externTd = *externTd.Add(&externTd, blk.Difficulty())

		if !bc.HasBlock(blk.Hash(), blockNumber.Uint64()) {
			start := time.Now()
			if err := bc.writeBlockWithTd(blk, externTd.Clone()); err != nil {
				return it.index, err
			}
			log.Debug("Stored sidechain block", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		} else {
			bc.tdCache.Add(blk.Hash(), externTd.Clone())
		}
		lastBlock = blk
	}

	reorg, err := bc.forker.ReorgNeeded(current.Header(), lastBlock.Header())
	if err != nil {
		return it.index, err
	}
	if !reorg {
		localTd := bc.GetTd(current.Hash(), currentNumber)
		log.Info("Sidechain written to disk", "start", it.first().Number64(), "end", it.previous().Number64(), "sidetd", externTd, "localtd", localTd)
		return it.index, err
	}

	var (
		hashes  []types.Hash
		numbers []uint64
	)
	parent := it.previous()
	for parent != nil {
		parentHeader, err := requireConcreteHeader(parent, "unexpected parent header type")
		if err != nil {
			return it.index, err
		}
		parentNumber, err := requireHeaderNumber(parent, "parent header number unavailable")
		if err != nil {
			return it.index, err
		}
		if bc.HasState(parent.StateRoot()) {
			break
		}
		hashes = append(hashes, parent.Hash())
		numbers = append(numbers, parentNumber.Uint64())
		if parentNumber.IsZero() {
			parent = nil
			break
		}
		parent = bc.GetHeader(parentHeader.ParentHash, uint256.NewInt(0).Sub(parentNumber, uint256.NewInt(1)))
	}
	if parent == nil {
		// Semantic sentinel, not a bare error: the side chain's ancestry walks
		// off blocks we don't hold (e.g. a committed same-height sibling whose
		// direct push we missed). The gossip/push handlers future-queue on
		// unknown-ancestor and actively fetch the missing parent by hash;
		// a bare error would mark the block BAD and drop it permanently.
		return it.index, consensus.ErrUnknownAncestor
	}

	var blocks []block.IBlock
	for i := len(hashes) - 1; i >= 0; i-- {
		b := bc.GetBlock(hashes[i], numbers[i])
		blocks = append(blocks, b)

		if len(blocks) >= 2048 {
			log.Info("Importing heavy sidechain segment", "blocks", len(blocks), "start", blocks[0].Number64(), "end", b.Number64())
			if _, err := bc.insertChain(blocks, authorizedSwitch); err != nil {
				return 0, err
			}
			blocks = blocks[:0]
			if bc.insertStopped() {
				log.Debug("Abort during blocks processing")
				return 0, nil
			}
		}
	}
	if len(blocks) > 0 {
		log.Info("Importing sidechain segment", "start", blocks[0].Number64(), "end", blocks[len(blocks)-1].Number64())
		return bc.insertChain(blocks, authorizedSwitch)
	}
	return 0, nil
}

// =============================================================================
// Ancestor Recovery
// =============================================================================

func (bc *BlockChain) recoverAncestors(blk block.IBlock, authorizedSwitch bool) (types.Hash, error) {
	var (
		hashes  []types.Hash
		numbers []uint256.Int
		parent  = blk
	)
	for parent != nil {
		parentNumber, err := requireBlockNumber(parent, "ancestor block number unavailable")
		if err != nil {
			return types.Hash{}, err
		}
		if bc.HasState(parent.Hash()) {
			break
		}
		hashes = append(hashes, parent.Hash())
		numbers = append(numbers, *parentNumber)
		if parentNumber.IsZero() {
			break
		}
		parent = bc.GetBlock(parent.ParentHash(), parentNumber.Uint64()-1)
	}
	if parent == nil {
		// See insertSideChain: unknown-ancestor semantics so callers can
		// future-queue + fetch instead of marking the block bad.
		return types.Hash{}, consensus.ErrUnknownAncestor
	}
	for i := len(hashes) - 1; i >= 0; i-- {
		var b block.IBlock
		if i == 0 {
			b = blk
		} else {
			b = bc.GetBlock(hashes[i], numbers[i].Uint64())
		}
		if _, err := bc.insertChain([]block.IBlock{b}, authorizedSwitch); err != nil {
			return b.ParentHash(), err
		}
	}
	return blk.Hash(), nil
}

// =============================================================================
// Reorg
// =============================================================================

// reorg reconstructs the canonical chain when a fork is detected.
// Instrumented with ReorgAudit for observability.
func (bc *BlockChain) reorg(tx kv.RwTx, oldBlock, newBlock block.IBlock) error {
	audit := GetReorgAudit()
	auditEvent := audit.StartReorg(oldBlock, newBlock)

	var (
		newChain    block.Blocks
		oldChain    block.Blocks
		commonBlock block.IBlock
		deletedTxs  []types.Hash
		addedTxs    []types.Hash
	)
	defer func() {
		audit.EndReorg(auditEvent, commonBlock, oldChain, newChain, len(deletedTxs), len(addedTxs), nil)
	}()

	if oldBlock == nil {
		return errors.New("invalid old chain")
	}
	if newBlock == nil {
		return errors.New("invalid new chain")
	}
	// No-op reorg: old and new head are the same block. The common-ancestor walk
	// would match on its first iteration, leaving both chains empty and tripping
	// the "Impossible reorg" ERROR below — a false alarm that also spins when an
	// applied≠canonical node repeatedly asks to align onto its own canonical head.
	// Nothing to reorg; return quietly (at debug) instead of erroring.
	if oldBlock.Hash() == newBlock.Hash() {
		log.Debug("reorg no-op: old and new head are identical",
			"number", newBlock.Number64(), "hash", newBlock.Hash())
		return nil
	}
	oldNumber, err := requireBlockNumber(oldBlock, "old block number unavailable")
	if err != nil {
		return err
	}
	newNumber, err := requireBlockNumber(newBlock, "new block number unavailable")
	if err != nil {
		return err
	}

	// Reduce the longer chain to match the shorter one
	if oldNumber.Uint64() > newNumber.Uint64() {
		for oldBlock != nil && oldNumber.Uint64() != newNumber.Uint64() {
			oldChain = append(oldChain, oldBlock)
			for _, tx := range oldBlock.Transactions() {
				deletedTxs = append(deletedTxs, tx.Hash())
			}
			if oldNumber.IsZero() {
				break
			}
			oldBlock = bc.GetBlock(oldBlock.ParentHash(), oldNumber.Uint64()-1)
			if oldBlock != nil {
				oldNumber, err = requireBlockNumber(oldBlock, "old block number unavailable")
				if err != nil {
					return err
				}
			}
		}
	} else {
		for newBlock != nil && newNumber.Uint64() != oldNumber.Uint64() {
			newChain = append(newChain, newBlock)
			if newNumber.IsZero() {
				break
			}
			newBlock = bc.GetBlock(newBlock.ParentHash(), newNumber.Uint64()-1)
			if newBlock != nil {
				newNumber, err = requireBlockNumber(newBlock, "new block number unavailable")
				if err != nil {
					return err
				}
			}
		}
	}
	if oldBlock == nil {
		return errors.New("invalid old chain")
	}
	if newBlock == nil {
		return errors.New("invalid new chain")
	}

	var useExternalTx bool
	if tx == nil {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
	} else {
		useExternalTx = true
	}

	// Walk back both sides to find the common ancestor
	for {
		if oldBlock.Hash() == newBlock.Hash() {
			commonBlock = oldBlock
			break
		}
		oldChain = append(oldChain, oldBlock)
		for _, t := range oldBlock.Transactions() {
			deletedTxs = append(deletedTxs, t.Hash())
		}
		newChain = append(newChain, newBlock)

		oldNumber, err = requireBlockNumber(oldBlock, "old block number unavailable")
		if err != nil {
			return err
		}
		newNumber, err = requireBlockNumber(newBlock, "new block number unavailable")
		if err != nil {
			return err
		}
		if oldNumber.IsZero() || newNumber.IsZero() {
			return errors.New("reached genesis without finding common ancestor")
		}
		oldBlock = rawdb.ReadBlock(tx, oldBlock.ParentHash(), oldNumber.Uint64()-1)
		if oldBlock == nil {
			return errors.New("invalid old chain")
		}
		newBlock = rawdb.ReadBlock(tx, newBlock.ParentHash(), newNumber.Uint64()-1)
		if newBlock == nil {
			return errors.New("invalid new chain")
		}
	}

	// Log the reorg
	if len(oldChain) > 0 && len(newChain) > 0 {
		logFn := log.Info
		msg := "Chain reorg detected"
		if len(oldChain) > 63 {
			msg = "Large chain reorg detected"
			logFn = log.Warn
		}
		logFn(msg, "number", commonBlock.Number64(), "hash", commonBlock.Hash(),
			"drop", len(oldChain), "dropfrom", oldChain[0].Hash(), "add", len(newChain), "addfrom", newChain[0].Hash())
	} else if len(newChain) > 0 {
		log.Info("Extend chain", "add", len(newChain), "number", newChain[0].Number64(), "hash", newChain[0].Hash())
	} else {
		log.Error("Impossible reorg, please file an issue", "oldnum", oldBlock.Number64(), "oldhash", oldBlock.Hash(), "oldblocks", len(oldChain), "newnum", newBlock.Number64(), "newhash", newBlock.Hash(), "newblocks", len(newChain))
	}

	// Insert new chain blocks (except head) in proper order
	for i := len(newChain) - 1; i >= 1; i-- {
		blockNumber, numberErr := requireBlockNumber(newChain[i], "new chain block number unavailable")
		if numberErr != nil {
			return numberErr
		}
		if err := bc.writeHeadBlock(tx, newChain[i]); err != nil {
			return fmt.Errorf("failed to write head block during reorg at %d: %w", blockNumber.Uint64(), err)
		}
		for _, t := range newChain[i].Transactions() {
			addedTxs = append(addedTxs, t.Hash())
		}
	}

	// Clean up stale tx indexes
	for _, t := range types.HashDifference(deletedTxs, addedTxs) {
		rawdb.DeleteTxLookupEntry(tx, t)
	}

	// Delete stale canonical hash markers
	commonNumber, err := requireBlockNumber(commonBlock, "common block number unavailable")
	if err != nil {
		return err
	}
	number := commonNumber.Uint64()
	if len(newChain) > 1 {
		newChainNumber, numberErr := requireBlockNumber(newChain[1], "new chain block number unavailable")
		if numberErr != nil {
			return numberErr
		}
		number = newChainNumber.Uint64()
	}
	for i := number + 1; ; i++ {
		hash, _ := rawdb.ReadCanonicalHash(tx, i)
		if hash == (types.Hash{}) {
			break
		}
		rawdb.TruncateCanonicalHash(tx, i, false)
	}

	if !useExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// isUnknownAncestorErr reports whether err signals a missing parent, across the
// several independent "unknown ancestor" errors defined in different packages
// (internal.ErrUnknownAncestor, consensus.ErrUnknownAncestor, consensus/misc,
// plus bare errors.New in apoa/apos). A child that arrives before its parent
// must be future-queued, not reported as a bad block. Matching only
// internal.ErrUnknownAncestor let the consensus-package sentinel returned by
// hotstuff VerifyHeader fall through to reportBlock, spinning a joining node on
// BAD BLOCK while it waited for the gap to fill.
func isUnknownAncestorErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnknownAncestor) || errors.Is(err, consensus.ErrUnknownAncestor) {
		return true
	}
	return strings.Contains(err.Error(), "unknown ancestor")
}

// reportBlock logs a bad block error.
func (bc *BlockChain) reportBlock(blk block.IBlock, receipts []*block.Receipt, err error) {
	var receiptString string
	for i, receipt := range receipts {
		receiptString += fmt.Sprintf("\t %d: cumulative: %v gas: %v contract: %v status: %v tx: %v logs: %v bloom: %x state: %x\n",
			i, receipt.CumulativeGasUsed, receipt.GasUsed, receipt.ContractAddress.String(),
			receipt.Status, receipt.TxHash.String(), "Logs", receipt.Bloom, receipt.PostState)
	}
	log.Error(fmt.Sprintf(`
########## BAD BLOCK #########

Number: %v
Hash: %#x
%v

Error: %v
##############################
`, blk.Number64().String(), blk.Hash(), receiptString, err))
}

// tryZKFastPath attempts to verify the block's ZK proof. Returns true if
// the proof is valid and EVM re-execution can be skipped.
// unwindForReimport reverts the applied world state to just before block n
// when a block at height >= n has already been applied — the HotStuff
// same-height sibling switch (a view-change re-proposal arriving after a
// competing candidate was speculatively imported). Without this, the sibling
// re-executes on DIRTY state: PlainState keys the loser touched keep its
// values, and the append-order-dependent QMDB tree appends at shifted slots,
// forking its root permanently vs nodes that only ever applied the winner.
//
// Detection is the QMDB undo record itself: every applied block writes one
// (blockchain_write.go), so undo rows at heights >= n mean applied state that
// must be cleanly reverted first. Per reverted height h (newest→oldest):
// QMDB tree via RevertBlock (undo), PlainState via the block's changeset
// pre-values, and the by-number derived rows (receipts/logs truncate, CE,
// the undo row itself). History-index rows are left (inflation only, no
// correctness impact — tracked in the status doc). Forward-path cost: one
// missing-key read of undo[n].
//
// NOTE: the in-memory QMDB tree mutates inside the tx; if the tx later fails
// to commit, tree and disk disagree until restart (LoadFrom rebuilds from
// disk). The error is propagated, so the node does not continue on the
// mismatched state.
// AlignAppliedBranch makes the applied world state be the post-state of
// parentHash (at height childNum-1), reverting any locally-applied uncommitted
// sibling branch first — the precondition for building/executing a block on
// the consensus-mandated parent (HotStuff HighQC block) rather than the local
// head. No-op when already aligned; ErrUnknownAncestor when the parent itself
// was never applied and must import first.
func (bc *BlockChain) AlignAppliedBranch(childNum uint64, parentHash types.Hash) error {
	// Miner alignment races ordinary block import. Both paths can peel or
	// unwind the live QMDB tree, whose in-memory mutations are not covered by
	// the MDBX transaction. Serialize the whole operation with InsertChain and
	// WriteBlockWithState so a leader cannot consume an import's in-flight undo.
	bc.lock.Lock()
	defer bc.lock.Unlock()
	return bc.unwindForReimport(childNum, parentHash, true)
}

// revertUncommittedQMDBAppends peels a FAILED block's appends off the live
// QMDB tree. Execution mutates the tree (ComputeRoot appends) inside a tx that
// then rolls back on validation failure — but the tree itself is not
// transactional, and the block's undo row is only persisted by
// writeBlockWithState, so those appends could never be unwound later: every
// subsequent block would append at shifted slots and the node's roots would
// silently diverge from the cluster until a restart reload. The undo record
// captured by that very ComputeRoot is still in RAM — apply it (memory-only:
// nothing of the block ever reached disk). TakeUndo doubles as the "did
// execution reach ComputeRoot" test: it is cleared by every successful
// writeBlockWithState and by this function, so a non-nil record here always
// belongs to the failed block.
// PeelDanglingQMDBAppends implements common.IBlockChain: revert appends a
// discarded miner candidate left on the live tree (see
// revertUncommittedQMDBAppends — same mechanism, caller-facing entry).
func (bc *BlockChain) PeelDanglingQMDBAppends() {
	bc.revertUncommittedQMDBAppends(0)
}

// ensureQMDBTreeAtParent enforces the pre-execution invariant that the live
// QMDB tree's root equals the incoming block's PARENT header root — i.e. the
// world state actually is the state the block builds on. On a mismatch it
// realigns the applied marker to the height whose header root the tree DOES
// match (walking the parent lineage down through the undo window), so the
// regular catch-up/future-queue machinery replays forward from the tree's
// true position instead of executing on a wrong base. Returns nil when
// aligned (or QMDB inactive / parent unknown — later checks own those).
func (bc *BlockChain) ensureQMDBTreeAtParent(blk block.IBlock) error {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return nil
	}
	parent, err := bc.GetHeaderByHash(blk.ParentHash())
	if err != nil || parent == nil {
		return nil // unknown ancestor is handled by the normal import path
	}
	pHdr, ok := parent.(*block.Header)
	if !ok {
		return nil
	}
	treeRoot := bc.qmdbRootComputer.Root()
	if treeRoot == pHdr.Root {
		return nil
	}
	// Out-of-order arrival, not a discontinuity: the applied state simply has
	// not reached the parent yet (block N+1 gossiped before N imported). The
	// regular future-queue/catch-up path owns this — stay silent.
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		if an, _, ok, aerr := rawdb.ReadQMDBApplied(tx); aerr == nil && ok && an < pHdr.Number.Uint64() {
			return errQMDBBehindParent
		}
		return nil
	}); err != nil {
		return err
	}
	// Walk down from the parent through the undo window looking for the
	// header whose root the tree matches, then roll the OTHER two states back
	// to it. A marker-only realign proved insufficient live: PlainState kept
	// the blocks the tree had lost, catch-up re-executed them on that phantom
	// world state, and the node built nonce-stale blocks the whole network
	// rejected. The realign is now three-state atomic — PlainState unwound
	// block-by-block from its changeset pre-values down to the tree's height,
	// per-block bookkeeping consumed, marker moved — in one transaction, so a
	// failure leaves everything exactly as it was.
	cur := pHdr
	for depth := 0; depth < 256 && cur != nil; depth++ {
		if treeRoot == cur.Root {
			target, targetHash := cur.Number.Uint64(), cur.Hash()
			if werr := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
				return realignAppliedToTree(tx, target, targetHash)
			}); werr != nil {
				return fmt.Errorf("tree at %d/%x but three-state realign failed: %w", target, targetHash[:8], werr)
			}
			bc.clearReadThroughCache()
			return fmt.Errorf("tree root %x matches header %d, not the incoming parent %d — PlainState and marker realigned to the tree",
				treeRoot[:8], target, pHdr.Number.Uint64())
		}
		p, perr := bc.GetHeaderByHash(cur.ParentHash)
		if perr != nil || p == nil {
			break
		}
		cur, _ = p.(*block.Header)
	}
	return fmt.Errorf("tree root %x matches no header within the undo window below parent %d/%x",
		treeRoot[:8], pHdr.Number.Uint64(), pHdr.Root[:8])
}

// qmdbRealignMaxDepth caps how many blocks a discontinuity realign may roll
// back. It mirrors (with slack) the 256-block undo/lineage window: a span
// beyond it means the changesets needed for a faithful PlainState rewind may
// already be consumed, and a partial heal is worse than staying wedged behind
// the root/nonce verification walls.
const qmdbRealignMaxDepth = 512

// realignAppliedToTree rolls PlainState and the applied marker back to
// `target` — the height whose header root the live QMDB tree ALREADY holds —
// restoring the tree/PlainState/marker three-state invariant. The tree itself
// is never touched: it is the reference the other two are aligned to.
//
// PlainState's true height is taken from the changeset head, not the marker:
// writeBlockWithState persists PlainState, its changesets and the marker in
// one transaction, so the highest changeset block number IS the last block
// PlainState absorbed — even when an earlier marker-only realign already
// moved the marker away (the half-healed shape observed live on node4).
// Everything above target is unwound via the changeset pre-values and its
// per-block bookkeeping (undo record, consensus evidence, receipts) consumed;
// the re-imported blocks rewrite all of it.
func realignAppliedToTree(tx kv.RwTx, target uint64, targetHash types.Hash) error {
	from, err := highestChangesetBlock(tx)
	if err != nil {
		return err
	}
	if from > target {
		if from-target > qmdbRealignMaxDepth {
			return fmt.Errorf("refusing to realign %d blocks (PlainState at %d, tree at %d): beyond the changeset window", from-target, from, target)
		}
		for h := from; h > target; h-- {
			if err := commitment.UnwindPlainStateBlock(tx, h); err != nil {
				return fmt.Errorf("plain-state unwind of block %d: %w", h, err)
			}
			key := modules.EncodeBlockNumber(h)
			if err := tx.Delete(modules.QMDBUndoWindow, key); err != nil {
				return err
			}
			if err := tx.Delete(modules.ConsensusEvidence, key); err != nil {
				return err
			}
		}
		if err := rawdb.TruncateReceipts(tx, target+1); err != nil {
			return fmt.Errorf("truncate receipts from %d: %w", target+1, err)
		}
	}
	return rawdb.WriteQMDBApplied(tx, target, targetHash)
}

// highestChangesetBlock returns the highest block number present in the
// account/storage changesets — PlainState's true height (see
// realignAppliedToTree). 0 when both tables are empty.
func highestChangesetBlock(tx kv.Tx) (uint64, error) {
	var last uint64
	for _, table := range []string{modules.AccountChangeSet, modules.StorageChangeSet} {
		c, err := tx.Cursor(table)
		if err != nil {
			return 0, err
		}
		k, _, err := c.Last()
		c.Close()
		if err != nil {
			return 0, err
		}
		if len(k) >= 8 {
			if n := binary.BigEndian.Uint64(k[:8]); n > last {
				last = n
			}
		}
	}
	return last, nil
}

// clearReadThroughCache invalidates the layered read-through cache after a
// direct PlainState mutation that bypassed the normal import path.
func (bc *BlockChain) clearReadThroughCache() {
	if cache := layered.ExtractCache(bc.ChainDB); cache != nil {
		cache.Clear()
	}
}

// HasAppliedBlock reports whether the block's state has actually been
// EXECUTED onto the world state — it sits on the applied-marker lineage —
// as opposed to merely having its body stored, future-queued, or named by a
// proposal. This is the evidence level consensus voting needs: an import-
// gated vote certified by body presence let a quorum commit a block whose
// state no honest node could ever reach (observed live: a forked leader's
// block was "already imported" on six nodes that had only stored it, got its
// QC, and wedged the whole network's committed head on an inexecutable
// block). Chains without an applied marker fall back to body presence — the
// pre-gate behavior.
func (bc *BlockChain) HasAppliedBlock(hash types.Hash, number uint64) bool {
	applied := false
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		an, ah, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return nil
		}
		if !ok {
			applied = rawdb.ReadHeader(tx, hash, number) != nil
			return nil
		}
		if number > an {
			return nil // above the applied head — cannot have been executed
		}
		// Canonical fast path: the canonical table is the committed prefix,
		// and a committed block at or below the marker is necessarily applied.
		if ch, cerr := rawdb.ReadCanonicalHash(tx, number); cerr == nil && ch == hash {
			applied = true
			return nil
		}
		// Not (yet) canonical: an applied-but-uncommitted block lives within
		// the recent window — walk the applied lineage down from the marker.
		cur, curN := ah, an
		for curN > number && an-curN < 256 {
			hdr := rawdb.ReadHeader(tx, cur, curN)
			if hdr == nil {
				return nil
			}
			cur = hdr.ParentHash
			curN--
		}
		applied = curN == number && cur == hash
		return nil
	})
	return applied
}

func (bc *BlockChain) revertUncommittedQMDBAppends(blockNum uint64) {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return
	}
	// Idempotent with the writeBlockWithState failure path: make sure the
	// staged flush is discarded before the peel — ApplyUndo prunes revived
	// slots out of deadFlushed and must see the re-queued reclaim list.
	bc.qmdbRootComputer.AbortFlushed()
	undo := bc.qmdbRootComputer.TakeUndo()
	if undo == nil {
		return // failed before ComputeRoot — nothing was appended
	}
	err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		// The tree's cold getter still points at the rolled-back execution
		// tx; the boundary-twig reconstruction inside ApplyUndo may fault
		// leaves through it. Re-point at a live tx for the duration.
		bc.qmdbRootComputer.SetCold(tx)
		defer bc.qmdbRootComputer.SetCold(nil)
		return bc.qmdbRootComputer.Tree().ApplyUndo(undo)
	})
	if err == nil {
		log.Debug("peeled failed block's appends off the QMDB tree", "number", blockNum)
		return
	}
	log.Warn("failed to peel failed block's QMDB appends; reloading tree from disk",
		"number", blockNum, "err", err)
	if rerr := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		bc.qmdbRootComputer.SetCold(tx)
		defer bc.qmdbRootComputer.SetCold(nil)
		return bc.qmdbRootComputer.LoadFrom(tx)
	}); rerr != nil {
		log.Error("qmdb tree reload after failed block FAILED — state may diverge", "err", rerr)
	}
}

func (bc *BlockChain) unwindForReimport(n uint64, parentHash types.Hash, authorizedSwitch bool) error {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil || n == 0 {
		return nil
	}
	// The revert chain must start from the tree the applied marker describes.
	// A dangling candidate's appends (a leader whose sealed block lost the
	// view before this switch arrived) sit between the live tree head and the
	// marker's first undo record; reverting over them either wedges (the undo
	// no longer matches the tree head) or leaves a stale lastUndo behind that
	// the NEXT import's peel replays against the rewound tree — observed live
	// as "undo record is ahead of this tree" followed by a disk reload whose
	// root matched no header in the undo window. Peel before unwinding.
	bc.PeelDanglingQMDBAppends()
	mutated := false
	err := bc.unwindForReimportTx(n, parentHash, authorizedSwitch, &mutated)
	if err != nil && !mutated {
		// Pre-check rejections (finality floor, lineage mismatch → future
		// queue, unauthorized passive switch) fail BEFORE any tree mutation:
		// nothing to restore, and reloading the whole tree for each of them
		// turned routine catch-up traffic into a stream of expensive reloads
		// (observed: 34-54 "failed unwind" reloads per replay, most benign).
		return err
	}
	if err != nil {
		// A multi-block unwind that fails MIDWAY leaves the in-memory QMDB
		// tree holding reverts the rolled-back transaction discarded: block N
		// reverted in memory AND in the tx, block N-1's revert failed, the tx
		// rolled back (disk keeps N applied) — but the memory tree already
		// dropped N. The node then keeps running on the diverged tree and the
		// next FlushTo writes the divergence to disk permanently. Observed
		// live: three nodes whose tree roots matched NO nearby header after a
		// degraded-mesh period of repeated sibling switches, wedged with
		// nonce errors. Reload the tree from disk to restore the invariant —
		// the inline version of the "until restart" the old comment settled
		// for.
		// Use an Update (not a View): the MDBX write lock serializes this
		// reload against any concurrent unwind's tree mutations. And refresh
		// the tree's cold getter FIRST — LoadFrom faults some leaves through
		// the getter the tree already holds, which at this point is a
		// long-dead read transaction from the previous block's execution
		// (observed live: reload panicked on a nil mdbx txn and killed six
		// nodes).
		if rerr := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
			bc.qmdbRootComputer.SetCold(tx)
			lerr := bc.qmdbRootComputer.LoadFrom(tx)
			// The tx dies when this callback returns; detach so nothing
			// faults through it before the next block re-points the getter.
			bc.qmdbRootComputer.SetCold(nil)
			return lerr
		}); rerr != nil {
			log.Error("qmdb tree reload after failed unwind FAILED — state may diverge", "err", rerr)
		} else {
			log.Warn("qmdb tree reloaded from disk after failed unwind", "unwindErr", err)
		}
	}
	return err
}

func (bc *BlockChain) unwindForReimportTx(n uint64, parentHash types.Hash, authorizedSwitch bool, mutated *bool) error {
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		appliedNum, appliedHash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok {
			// Fresh replay data: the applied chain is the stored head.
			hn := rawdb.ReadCurrentBlockNumber(tx)
			if hn == nil {
				return nil
			}
			appliedNum = *hn
			ch, cerr := rawdb.ReadCanonicalHash(tx, appliedNum)
			if cerr != nil {
				return cerr
			}
			appliedHash = ch
		}
		if appliedNum == n-1 && appliedHash == parentHash {
			return nil // plain forward import — the incoming block extends the applied head
		}
		if appliedNum < n-1 {
			return nil // parent's state not applied yet — ValidateBody/future-queue handles it
		}
		// Committed-BLOCK floor (HotStuff-2: only the 2-chain-committed block is
		// final): a block ON the committed chain must never be reverted.
		// Finality evidence is the HotStuffCommittedHead marker — written
		// EXCLUSIVELY by CommitToCanonical, not HeadBlockHash (legacy
		// import-time writers stamped losing siblings into HeadBlockHash and
		// froze such nodes forever). Crucially the floor protects BLOCKS, not
		// HEIGHTS: reverting a losing sibling AT (or below) the committed
		// height, in order to replay the committed block itself, is exactly
		// the recovery path — a height-only check bricked it (observed live:
		// a node whose canonical/marker pointed at the committed 13016802
		// while its applied state sat on a same-height sibling rejected every
		// range with unknown-ancestor; the sibling could never be reverted).
		// The block-identity check runs against the applied lineage AFTER the
		// walk below; here we only resolve the committed height.
		var committedHead uint64
		if ch := rawdb.ReadHotStuffCommittedHead(tx); ch != (types.Hash{}) {
			if hn := rawdb.ReadHeaderNumber(tx, ch); hn != nil {
				committedHead = *hn
			}
		}
		// READ-ONLY pre-check first (no side effects on failure): walk the
		// applied lineage down to height n-1 and require it to BE the incoming
		// block's parent. If it isn't, the parent is a competing sibling this
		// node never applied — it cannot be "unwound into"; it must import
		// first (ErrUnknownAncestor → future-queue), and nothing may be
		// reverted yet (a partial revert with a rolled-back tx would desync the
		// in-memory QMDB tree from disk).
		lineage := make([]types.Hash, 0, appliedNum-(n-1)+1)
		curNum, curHash := appliedNum, appliedHash
		for {
			lineage = append(lineage, curHash)
			if curNum == n-1 {
				break
			}
			hdr := rawdb.ReadHeader(tx, curHash, curNum)
			if hdr == nil {
				return fmt.Errorf("branch switch at %d: applied header %d/%x missing", n, curNum, curHash[:8])
			}
			curNum--
			curHash = hdr.ParentHash
		}
		if curHash != parentHash {
			return fmt.Errorf("branch switch at %d: applied lineage at %d is %x, incoming parent is %x (sibling parent not applied): %w",
				n, n-1, curHash[:8], parentHash[:8], consensus.ErrUnknownAncestor)
		}

		// Committed-BLOCK floor enforcement (see the marker resolution above):
		// none of the applied blocks about to be reverted (heights
		// n..appliedNum, lineage[i] = height appliedNum-i) may sit ON the
		// committed chain — the canonical table is the committed prefix
		// (single writer + truncate), so "at/below the committed height AND
		// canonical" identifies a committed block. Reverting a same-height
		// LOSING sibling is allowed: that is the recovery path.
		for i, h := 0, appliedNum; h >= n; i, h = i+1, h-1 {
			if h > committedHead {
				continue
			}
			ch, cerr := rawdb.ReadCanonicalHash(tx, h)
			if cerr != nil {
				return cerr
			}
			if ch == lineage[i] {
				return fmt.Errorf("branch switch at %d rejected: would revert committed block %d/%x: %w",
					n, h, lineage[i][:8], consensus.ErrUnknownAncestor)
			}
		}

		// Only CONSENSUS-DRIVEN imports may revert applied blocks. A passively
		// received block (gossip, direct push, future-queue retry) whose branch
		// requires a revert is a stale sibling candidate from an old view — it
		// carries no new QC and has no authority over the applied head. Letting
		// it switch tugs the network between sibling branches (observed live:
		// a node applied through the CURRENT proposal got reverted 2 deep by a
		// dead same-height candidate, destroying the vote window every round).
		// Authorized callers: the proposal-driven chain-align (fetch-on-miss),
		// the leader's pre-build AlignAppliedBranch, and startup revert — all
		// funnel through InsertChainAuthorized / AlignAppliedBranch.
		if !authorizedSwitch {
			return fmt.Errorf("branch switch at %d requires consensus authority (passive import of a sibling branch): %w",
				n, consensus.ErrUnknownAncestor)
		}

		depth := appliedNum - (n - 1)
		log.Warn("branch switch: unwinding applied blocks to the incoming block's parent",
			"incoming", n, "incomingParent", fmt.Sprintf("%x", parentHash[:8]),
			"appliedHead", appliedNum, "appliedHash", fmt.Sprintf("%x", appliedHash[:8]), "depth", depth)

		// Revert the applied chain head-first down to the parent. Each step
		// needs the applied block's undo record (the window is 256 blocks — a
		// deeper switch is outside HotStuff's view-change reach and is rejected
		// rather than mis-unwound). lineage[i] is the applied hash at height
		// appliedNum-i.
		for i := 0; appliedNum >= n; i++ {
			key := modules.EncodeBlockNumber(appliedNum)
			raw, gerr := tx.GetOne(modules.QMDBUndoWindow, key)
			if gerr != nil {
				return gerr
			}
			if len(raw) == 0 {
				return fmt.Errorf("branch switch at %d: no undo record for applied block %d (beyond the undo window)", n, appliedNum)
			}
			undo, derr := qmdb.UnmarshalBlockUndo(raw)
			if derr != nil {
				return fmt.Errorf("unwind block %d: decode undo: %w", appliedNum, derr)
			}
			if err := bc.qmdbRootComputer.RevertBlock(tx, undo); err != nil {
				// A revert that cannot reconstruct a twig's frozen leaves (stale/
				// missing leaf blob) is a recoverable LOCAL condition, not proof the
				// incoming block is invalid. Tag it so the caller future-queues the
				// block instead of marking it BAD BLOCK — and so a single rogue block
				// can no longer panic-crash the node (the QMDB layer now returns this
				// error rather than panicking). Step 0 of ApplyUndo runs the only
				// fallible reconstruction before mutating, so this first-block failure
				// leaves the in-memory tree untouched; the tx rolls back on return.
				return fmt.Errorf("unwind block %d: qmdb revert: %w: %w", appliedNum, err, errRevertUnavailable)
			}
			// The tree is mutated from here on: a failure below this point
			// (or in a LATER iteration) leaves the in-memory tree ahead of
			// the rolled-back transaction — the caller must reload it.
			*mutated = true
			if err := commitment.UnwindPlainStateBlock(tx, appliedNum); err != nil {
				return fmt.Errorf("unwind block %d: plain state: %w", appliedNum, err)
			}
			if err := tx.Delete(modules.QMDBUndoWindow, key); err != nil {
				return err
			}
			if err := tx.Delete(modules.ConsensusEvidence, key); err != nil {
				return err
			}
			appliedNum--
			appliedHash = lineage[i+1]
			if err := rawdb.WriteQMDBApplied(tx, appliedNum, appliedHash); err != nil {
				return err
			}
		}
		// Receipts + logs for the reverted range (append-keyed; the re-executed
		// blocks re-append them).
		if err := rawdb.TruncateReceipts(tx, n); err != nil {
			return fmt.Errorf("unwind receipts from %d: %w", n, err)
		}
		// Invalidate any read-through caches that bypassed the unwind.
		if cache := layered.ExtractCache(bc.ChainDB); cache != nil {
			cache.Clear()
		}
		return nil
	})
}

func (bc *BlockChain) tryZKFastPath(blk block.IBlock) bool {
	body := blk.Body()
	if body == nil || len(body.ZKProof()) == 0 {
		return false
	}

	proof, err := zkprover.DecodeProof(body.ZKProof())
	if err != nil {
		log.Debug("Failed to decode ZK proof, falling back to EVM", "block", blk.Number64(), "err", err)
		return false
	}

	if bc.zkVerifier == nil {
		return false
	}
	if !bc.zkVerifier.CryptographicReady() {
		log.Debug("ZK verifier is in side-check mode; skipping ZK fast path", "block", blk.Number64())
		return false
	}
	if err := bc.zkVerifier.Verify(proof, blk.StateRoot(), blk.GasUsed()); err != nil {
		log.Warn("ZK proof verification failed, falling back to EVM", "block", blk.Number64(), "err", err)
		return false
	}

	return true
}

func (bc *BlockChain) syncChain(remoteBlock uint64, peerID peer.ID) {
	log.Debugf("syncChain.......")
}
