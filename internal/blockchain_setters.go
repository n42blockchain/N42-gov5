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
// BlockChain setters and simple accessors for optional subsystems.

package internal

import (
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/internal/zkverifier"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/modules/state/snapshot"
	"github.com/n42blockchain/N42/modules/state/witness"
)

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

// TxIndexer receives committed blocks' transaction hashes for a lookup index
// kept outside the consensus write path.
//
// Writing one MDBX row per transaction inside the commit transaction cost
// 2,700 ms of a 3.53 s block cycle at a 480M gas ceiling -- 94.6% of it in
// mdbx_txn_commit, because 22,857 hash-keyed rows touch nearly that many
// separate B-tree leaves. When an indexer is attached, nothing is written here
// and it owns the newest blocks instead. Add takes ownership of hashes.
type TxIndexer interface {
	Add(number uint64, hashes []types.Hash)
}

// SetTxIndexer attaches an index for the newest blocks. Must be set before the
// first commit, or the blocks in between are written to neither tier.
func (bc *BlockChain) SetTxIndexer(ix TxIndexer) {
	bc.txIndexer = ix
	log.Info("Transaction lookup index attached; commit path will not write the table")
}

// SenderHintSource resolves a transaction hash to the pool's copy of that
// transaction, or nil. Declared as an interface for the same reason TxIndexer
// is: the pool package imports this one.
//
// Import-path sender recovery uses it as a cache: a transaction that reached
// the pool already paid its secp256k1 recovery at admission, and the wire copy
// in an imported block is the same signature bytes (same hash), so the pool
// copy's cached sender — re-derived through the block's own signer, which
// verifies the cache actually belongs to that signer — is byte-identical to
// what recovering the wire copy would produce. Measured on a saturated fleet,
// recovery was 260 ms of a 516 ms import (22,857 transactions, 8 workers), all
// of it re-deriving senders the pool had already derived.
type SenderHintSource interface {
	GetTx(hash types.Hash) *transaction.Transaction
}

// SetSenderHintSource attaches the transaction pool as a sender cache for
// block import. Purely an accelerator: a nil source, a miss, or a signer
// mismatch all fall back to plain recovery.
func (bc *BlockChain) SetSenderHintSource(s SenderHintSource) {
	bc.senderHints = s
	log.Info("Sender hint source attached; block import will reuse pool-recovered senders")
}

// SetExecutedHook attaches the early-vote notification: fired the moment a
// block's execution and state validation succeed during import, before the
// block is persisted, so consensus voting can overlap the MDBX commit.
func (bc *BlockChain) SetExecutedHook(h func(hash, txHash, parentHash types.Hash, number uint64, extra []byte)) {
	bc.executedHook = h
	log.Info("Executed hook attached; consensus votes will not wait for block persistence")
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

func (bc *BlockChain) SetEngine(engine interface{}) {
	if e, ok := engine.(consensus.Engine); ok {
		bc.engine = e
	}
}
