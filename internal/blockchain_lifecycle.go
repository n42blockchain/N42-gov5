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
// BlockChain start-up repair, Start/Close and the run loop.

package internal

import (
	"bytes"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// logStartupStateFingerprint prints, on every node, what the startup repair
// left behind: the live QMDB tree's root and append cursor, the applied
// marker, and the canonical head. Round 35z5: one node came up holding an
// uncommitted speculative block (slot 489598909 against the fleet's
// 489543974), reverted it, led the next view and sealed a root the other six
// did not compute -- and there was no way to tell from the logs whether its
// tree was already wrong here or only diverged when it executed. Seven of
// these lines, one per node, answer that in one grep.
func (bc *BlockChain) logStartupStateFingerprint() {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return
	}
	root := bc.qmdbRootComputer.Root()
	nextSlot := bc.qmdbRootComputer.Tree().NextSlot()
	var appliedNum uint64
	var appliedHash types.Hash
	var haveApplied bool
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		n, h, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		appliedNum, appliedHash, haveApplied = n, types.Hash(h), ok
		return nil
	}); err != nil {
		log.Warn("startup state fingerprint: applied marker unreadable", "err", err)
	}
	head := bc.CurrentBlock()
	headNum := uint64(0)
	headHash := types.Hash{}
	if head != nil {
		headNum = head.Number64().Uint64()
		headHash = head.Hash()
	}
	log.Info("startup state fingerprint",
		"treeRoot", fmt.Sprintf("%x", root[:8]), "nextSlot", nextSlot,
		"appliedNum", appliedNum, "appliedHash", fmt.Sprintf("%x", appliedHash[:8]),
		"haveApplied", haveApplied,
		"headNum", headNum, "headHash", fmt.Sprintf("%x", headHash[:8]))
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
	bc.logStartupStateFingerprint()

	// Pre-warm the speculative build computer off the leader's critical path:
	// the FIRST build after a restart otherwise pays the full ~5s index
	// rebuild inside its 6s view window (caught by the build-phase breakdown -
	// every other build rides the ~0.4s trusted reload). This must run AFTER all
	// startup state repair/revert operations: an earlier snapshot can leave the
	// trusted miner index pointing at slots that a startup unwind removed.
	//
	// SYNCHRONOUS since round 35z2 (2026-09-08): run in the background it
	// held minerRCMu for the whole ~3.5 min load while HotStuff was already
	// running -- every leader's build of those minutes blocked on the lock,
	// eleven consecutive views timed out, and when the lock freed, the
	// stale candidates from all of them surfaced at one height: a sibling
	// storm, branch switches on every node, and the root divergence that
	// aborted rounds 35z and 35z2. Consensus starts after this returns, so
	// the first view finds a warm builder.
	if bc.qmdbEnabled {
		t0 := time.Now()
		err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
			if bc.PrewarmMinerRootComputer(tx) {
				log.Info("miner speculative computer pre-warmed", "elapsed", time.Since(t0))
			}
			return nil
		})
		if err != nil && bc.ctx.Err() == nil {
			log.Warn("miner speculative computer pre-warm failed", "err", err)
		}
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
		// The executed roots: a header's own Root before the deferred
		// execution fork, the stored result after it.
		hdrRoot, hok := bc.executedRootIn(tx, hdr)
		if hok && treeRoot == hdrRoot {
			return nil // marker and executed state agree
		}
		parent := rawdb.ReadHeader(tx, hdr.ParentHash, appliedNum-1)
		parentRoot, pok := types.Hash{}, false
		if parent != nil {
			parentRoot, pok = bc.executedRootIn(tx, parent)
		}
		if parent != nil && pok && treeRoot == parentRoot {
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
//
// Only the applied state is reverted: the blocks' headers and bodies stay
// stored. Consensus may still extend one of them (it can carry the highest
// QC, round S132), and the lag executor then executes it again from storage
// before its child (lag_replay.go).
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
	// A write handed to the ordered writer lands before the store closes, and
	// no import hands off another one after it: imports stop, and an import
	// in progress (it holds bc.lock) finishes first -- bounded, so a stuck
	// import cannot hang the shutdown.
	bc.StopInsert()
	locked := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if locked = bc.lock.TryLock(); locked {
			break
		}
	}
	bc.waitLagWrite()
	if locked {
		bc.lock.Unlock()
	}
	// Outside bc.lock: the hook commits to canonical, which needs the MDBX
	// writer and the live chain context, not the import lock.
	if fn := bc.lagExec.drainedHook.Load(); fn != nil {
		(*fn)()
	}
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

func (bc *BlockChain) syncChain(remoteBlock uint64, peerID peer.ID) {
	log.Debugf("syncChain.......")
}
