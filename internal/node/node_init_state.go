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

// NewNode phases: state commitment, ZK proving and snapshot acceleration.

package node

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	gverkle "github.com/ethereum/go-verkle"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/internal/zkverifier"
	"github.com/n42blockchain/N42/lib/bmt"
	bmtstore "github.com/n42blockchain/N42/lib/bmt/store"
	"github.com/n42blockchain/N42/lib/jmt"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	libverkle "github.com/n42blockchain/N42/lib/verkle"
	verklestore "github.com/n42blockchain/N42/lib/verkle/store"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	statesnapshot "github.com/n42blockchain/N42/modules/state/snapshot"
)

// initStateCommitment installs the state commitment tree selected by
// ChainConfig.StateScheme.
func (n *Node) initStateCommitment(b *nodeBuild) error {
	cfg := b.cfg
	// Initialize state commitment tree based on ChainConfig.StateScheme.
	// Falls back to NodeConfig.JMTCommitment for backward compatibility.
	stateScheme := state.DefaultScheme(b.chainConfig.StateScheme)
	if stateScheme == state.RootSchemeLegacyKeccak && cfg.NodeCfg.JMTCommitment {
		stateScheme = state.RootSchemeJMTBlake3 // backward compat
	}

	if realBC, ok := b.bc.(*internal.BlockChain); ok {
		switch stateScheme {
		case state.RootSchemeJMTBlake3:
			n.initJMTCommitment(b, realBC)

		case state.RootSchemeBMTBlake3:
			n.initBMTCommitment(b, realBC)

		case state.RootSchemeVerkle:
			n.initVerkleCommitment(b, realBC)

		case state.RootSchemeEthereumMPT:
			n.initMPTCommitment(b, realBC)

		case state.RootSchemeQMDB:
			if err := n.initQMDBCommitment(b, realBC); err != nil {
				return err
			}

		case state.RootSchemeLegacyKeccak:
			log.Info("State commitment: Legacy-Keccak (no tree)")

		default:
			return fmt.Errorf("unsupported state scheme: %s", stateScheme)
		}
	}
	if commitment.QMDBOnlyAccountWrites() && !modules.PlainAccountWriteSkipped() {
		return fmt.Errorf("N42_STATE_WRITE_QMDB_ONLY=1 needs a chain committing with QMDB (state scheme %s does not)", stateScheme)
	}
	return nil
}

// initJMTCommitment installs the JMT-Blake3 commitment (with LtHash alongside).
func (n *Node) initJMTCommitment(b *nodeBuild, realBC *internal.BlockChain) {
	cfg, ctx := b.cfg, b.ctx
	// --- JMT initialization (existing production code) ---
	var jmtRoot jmt.Hash
	var jmtVersion uint64
	if rtx, err := b.chainKv.BeginRo(ctx); err == nil {
		jmtRoot, _ = jmtstore.ReadJMTRoot(rtx)
		jmtVersion, _ = jmtstore.ReadJMTVersion(rtx)
		rtx.Rollback()
	}
	pooledStore := jmtstore.NewPooledDBStore(ctx, b.chainKv, modules.JMTNode)
	var tree *jmt.Tree
	if jmtRoot == jmt.EmptyHash {
		tree = jmt.New(pooledStore)
	} else {
		tree = jmt.NewFromRoot(pooledStore, jmtRoot)
	}
	tree.SetVersion(jmtVersion)
	jmtCommit := commitment.NewJMTCommitment(tree)
	realBC.SetJMTCommitment(jmtCommit)
	realBC.SetJMTStoreRefresh(pooledStore.RefreshTx)

	// LtHash runs alongside JMT.
	ltDigest := e_initLtHash(ctx, b.chainKv)
	ltCommit := commitment.NewLtHashCommitment(ltDigest)
	realBC.SetLtHashCommitment(ltCommit)

	jmtRC := commitment.NewJMTRootComputer(jmtCommit)
	ltAwareRC := commitment.NewLtHashAwareRootComputer(jmtRC, ltCommit)
	realBC.SetRootComputer(ltAwareRC)

	if cfg.NodeCfg.Chain == "private" {
		realBC.EnableJMTForBlockProcessing()
	}
	log.Info("State commitment: JMT-Blake3",
		"root", fmt.Sprintf("%x", jmtRoot[:8]),
		"version", jmtVersion,
	)
}

// initBMTCommitment installs the BMT-Blake3 commitment.
func (n *Node) initBMTCommitment(b *nodeBuild, realBC *internal.BlockChain) {
	ctx := b.ctx
	// --- BMT initialization (content-addressed Binary Merkle Tree) ---
	var bmtRoot bmt.Hash
	if rtx, err := b.chainKv.BeginRo(ctx); err == nil {
		bmtRoot, _ = bmtstore.ReadBMTRoot(rtx)
		rtx.Rollback()
	}
	// BMT uses content-addressed storage — MDBXStore tx is set per-block in writeBlockWithState.
	bmtNodeStore := bmtstore.NewMDBXStore(nil, bmtstore.BMTNodeTable)
	_ = bmtNodeStore // store reference kept via commitment
	var tree *bmt.Tree
	if bmtRoot == bmt.EmptyHash {
		tree = bmt.New(bmt.NewMemStore()) // in-memory until tx available
	} else {
		tree = bmt.NewFromRoot(bmt.NewMemStore(), bmtRoot)
	}
	bmtCommit := commitment.NewBMTCommitment(tree)
	realBC.SetBMTCommitment(bmtCommit)
	bmtRC := commitment.NewBMTRootComputer(bmtCommit)
	realBC.SetRootComputer(bmtRC)

	log.Info("State commitment: BMT-Blake3",
		"root", fmt.Sprintf("%x", bmtRoot[:8]),
	)
}

// initVerkleCommitment installs the experimental Verkle commitment.
func (n *Node) initVerkleCommitment(b *nodeBuild, realBC *internal.BlockChain) {
	ctx := b.ctx
	// --- Verkle initialization (experimental, IPA/Banderwagon) ---
	// Read persisted version for crash recovery logging.
	var verkleVersion uint64
	if rtx, err := b.chainKv.BeginRo(ctx); err == nil {
		verkleVersion, _ = verklestore.ReadVerkleVersion(rtx)
		rtx.Rollback()
	}
	// In-memory tree; per-block FlushTo writes to MDBX in writeBlockWithState.
	verkleRoot := gverkle.New()
	verkleStore := libverkle.NewMemStore()
	verkleCommit := commitment.NewVerkleCommitment(verkleRoot, verkleStore)
	realBC.SetVerkleCommitment(verkleCommit)
	verkleRC := commitment.NewVerkleRootComputer(verkleCommit)
	realBC.SetRootComputer(verkleRC)

	log.Info("State commitment: Verkle (IPA/Banderwagon, experimental)",
		"persisted_block", verkleVersion,
	)
}

// initMPTCommitment installs the Ethereum MPT (HexPatriciaHashed) commitment.
func (n *Node) initMPTCommitment(b *nodeBuild, realBC *internal.BlockChain) {
	ctx := b.ctx
	// --- MPT initialization (standard Ethereum HexPatriciaHashed) ---
	mptRC := commitment.NewPersistentMPTRootComputer()
	var mptRoot types.Hash
	var mptBlock uint64
	if rtx, err := b.chainKv.BeginRo(ctx); err == nil {
		mptRoot, err = commitment.ReadMPTRoot(rtx)
		if err != nil {
			log.Warn("Failed to read MPT root", "err", err)
		}
		// Restore HPH trie state from checkpoint (Erigon SeekCommitment pattern).
		if blockNum, trieState, err := commitment.ReadMPTTrieState(rtx); err == nil && len(trieState) > 0 {
			if err := mptRC.RestoreTrieState(trieState); err != nil {
				log.Warn("Failed to restore MPT trie state, will rebuild", "block", blockNum, "err", err)
			} else {
				mptBlock = blockNum
			}
		}
		rtx.Rollback()
	}
	realBC.SetMPTRootComputer(mptRC)
	realBC.SetRootComputer(mptRC)
	log.Info("State commitment: Ethereum-MPT (HexPatriciaHashed)",
		"root", fmt.Sprintf("%x", mptRoot[:8]),
		"checkpoint", mptBlock,
	)
}

// initQMDBCommitment reloads the QMDB twig forest and installs it for live
// block production.
func (n *Node) initQMDBCommitment(b *nodeBuild, realBC *internal.BlockChain) error {
	ctx := b.ctx
	// QMDB live block production: reload the twig forest from the
	// persisted positional log (history-dependent root, so we replay the
	// exact slot layout, not a key-set rebuild), then drive it as the
	// block-processing root computer — writeBlockWithState flushes the
	// entry log per block. The in-RAM key->slot index is used because the
	// per-block evmRecord tx is read-only (cannot back the index with
	// MDBX on the live path). Also serve QMDB-native eth_getProof.
	// Wire the cold/leaf getters BEFORE LoadFrom: the rebuild faults
	// frozen leaves and cold entries through them. A failed reload can
	// leave a partially populated twig slice, so it is not an empty-tree
	// fallback: using it can panic in Root(), and an actually empty tree
	// would not reproduce this history-dependent root. Fail closed and
	// require repair/reseed instead -- after retrying on a fresh
	// computer and read view: rounds 35g-35j lost a node at every
	// fleet start to "twig metadata inconsistent" part way through the
	// scan, and the same store loaded whole on the next start ten
	// minutes later, so the first failure is a transient read, not
	// the disk.
	var qmdbRC *commitment.QMDBRootComputer
	var loadErr error
	// Round 35z2 (2026-09-08): all three attempts failed on three
	// different twigs (172644, 179292, 185698 of 219511) and the fourth
	// start, ten minutes later, loaded whole -- six attempts with a
	// longer pause ride out a longer burst of whatever the transient is.
	for attempt := 1; attempt <= 6; attempt++ {
		qmdbRC = commitment.NewQMDBRootComputer()
		rtx, err := b.chainKv.BeginRo(ctx)
		if err != nil {
			return fmt.Errorf("open QMDB forest reload view: %w", err)
		}
		qmdbRC.SetCold(rtx)
		loadErr = qmdbRC.LoadFrom(rtx)
		rtx.Rollback()
		// rtx is dead from here; detach so nothing faults through it (the
		// first block's execution re-points at a live tx).
		qmdbRC.SetCold(nil)
		if loadErr == nil {
			break
		}
		log.Error("QMDB forest reload failed at startup", "attempt", attempt, "err", loadErr)
		time.Sleep(5 * time.Second)
	}
	if loadErr != nil {
		return fmt.Errorf("reload QMDB forest: %w", loadErr)
	}
	qmdbRC.EnableUndoRecording()
	realBC.SetQMDBRootComputer(qmdbRC)
	realBC.SetStateProofProvider(internal.NewQMDBStateProofProvider(realBC.Config()))
	log.Info("State commitment: QMDB (twig forest, live block production)",
		"root", fmt.Sprintf("%x", qmdbRC.Root()))
	if commitment.QMDBStateReadMode() == commitment.QMDBReadOn {
		// N42_STATE_READ_QMDB=1: EVERY head-state account read -- the
		// miner's build, the txpool, RPC, the history fallback -- goes
		// to the tree, not only block execution. Round 26's first
		// attempt switched execution alone and the miner built on a
		// table the previous leg had frozen: state-root mismatch on
		// the first block, seven nodes wedged.
		modules.SetLatestAccountSource(commitment.NewQMDBLatestAccountSource(qmdbRC, b.chainKv))
		log.Info("head-state account reads served from the QMDB tree (N42_STATE_READ_QMDB=1): miner, txpool and RPC included")
	}
	if commitment.QMDBOnlyAccountWrites() {
		// N42_STATE_WRITE_QMDB_ONLY=1: stop maintaining the plain
		// `Account` table. Only sound once reads no longer touch it.
		if !modules.LatestAccountSourceInstalled() {
			return fmt.Errorf("N42_STATE_WRITE_QMDB_ONLY=1 requires N42_STATE_READ_QMDB=1: the plain Account table stops being written, so reads must not depend on it")
		}
		modules.SetPlainAccountWriteSkipped(true)
		// Record the head at the FIRST enablement so a repair can later
		// rebuild exactly the rows the table missed: every address in an
		// AccountChangeSet from this block on, re-read from the tree.
		var frozenAt uint64
		if err := b.chainKv.Update(ctx, func(tx kv.RwTx) error {
			if v, err := tx.GetOne(modules.QMDBMeta, commitment.QMDBAccountFrozenAtKey); err == nil && len(v) == 8 {
				frozenAt = binary.BigEndian.Uint64(v)
				return nil
			}
			frozenAt = b.bc.CurrentBlock().Number64().Uint64() + 1
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], frozenAt)
			return tx.Put(modules.QMDBMeta, commitment.QMDBAccountFrozenAtKey, buf[:])
		}); err != nil {
			return fmt.Errorf("record the Account table freeze: %w", err)
		}
		log.Warn("QMDB-only account persistence: the plain Account table is frozen; snap-sync serving and state dumps read a stale table (measurement lever, docs/QS_BLOCK_TIME_BUDGET.md round 26)",
			"frozenAt", frozenAt)
	}
	return nil
}

// initZKProving configures ZK proving, the verifier and the prover service.
func (n *Node) initZKProving(b *nodeBuild) error {
	cfg := b.cfg
	// Initialize ZK proving if configured.
	if cfg.NodeCfg.ZKProving || cfg.ZKProverCfg.Enabled {
		if realBC, ok := b.bc.(*internal.BlockChain); ok {
			realBC.SetZKProving(true, cfg.ZKProverCfg.RequireProof)
		}
		b.zkVerify = zkverifier.NewVerifier()
		if cfg.ZKProverCfg.RequireProof && !b.zkVerify.CryptographicReady() {
			return fmt.Errorf("zkprover: require_proof cannot be enabled before cryptographic verification is ready")
		}
		if cfg.ZKProverCfg.Enabled && !cfg.ZKProverCfg.VerifyOnly {
			var err error
			b.zkProverSvc, err = zkprover.NewService(&cfg.ZKProverCfg)
			if err != nil {
				log.Warn("Failed to create ZK prover service", "err", err)
			} else {
				// Inject SP1 prover client.
				sp1Client := zkprover.NewSP1ProverClient(
					cfg.ZKProverCfg.ProverAddr,
					cfg.ZKProverCfg.GuestBinary,
					cfg.ZKProverCfg.SP1CLIPath,
				)
				b.zkProverSvc.SetProverClient(sp1Client)
				log.Info("ZK prover service configured",
					"addr", cfg.ZKProverCfg.ProverAddr,
					"guest", cfg.ZKProverCfg.GuestBinary,
					"simulate", cfg.ZKProverCfg.ProverAddr == "",
				)
			}
		}
	}
	return nil
}

// initSnapshotAccel builds the snapshot acceleration tree when configured.
func (n *Node) initSnapshotAccel(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	// Initialize snapshot acceleration tree if configured.
	if cfg.SnapshotAccelCfg.Enable {
		if realBC, ok := b.bc.(*internal.BlockChain); ok {
			cache := layered.ExtractCache(b.chainKv)
			if cache != nil {
				cur := realBC.CurrentBlock()
				if cur == nil {
					return fmt.Errorf("snapshot acceleration requires current block")
				}
				curBlockNumber := blockNumberOrZero(cur)
				tree := statesnapshot.NewTree(cache, curBlockNumber, cur.Hash(), cfg.SnapshotAccelCfg.MaxDiffLayers)

				// Enable persistence if configured.
				if cfg.SnapshotAccelCfg.Persist {
					tree.SetDB(b.chainKv)

					// Check if flat snapshot generation is complete.
					if tx, err := b.chainKv.BeginRo(ctx); err == nil {
						complete, _ := rawdb.IsSnapshotGenComplete(tx)
						tx.Rollback()
						if complete {
							tree.SetGenReady(true)
							log.Info("Snapshot flat tables ready for reads")
						} else {
							// Start background generation.
							gen := statesnapshot.NewGenerator(b.chainKv, cur.Hash(), curBlockNumber)
							go func() {
								gen.Run(ctx)
								// Only mark ready if generation actually completed
								// (not cancelled or failed mid-way).
								if ctx.Err() == nil {
									if rtx, err := b.chainKv.BeginRo(context.Background()); err == nil {
										complete, _ := rawdb.IsSnapshotGenComplete(rtx)
										rtx.Rollback()
										if complete {
											tree.SetGenReady(true)
										}
									}
								}
							}()
						}
					}

					// Load journal for crash recovery.
					if tx, err := b.chainKv.BeginRo(ctx); err == nil {
						if err := statesnapshot.LoadJournal(tx, tree); err != nil {
							log.Warn("Failed to load snapshot journal", "err", err)
						}
						tx.Rollback()
					}
				}

				realBC.SetSnapshotTree(tree)
			}
		}
	}
	return nil
}
