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
// Canonical commit (CommitToCanonicalWith) and sibling lookup.

package internal

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// runNewBlockMessage, persistBlock and NewBlockHandler were the legacy
// proto (msg_proto.NewBlockMessageData) direct-push receive path. They have been
// removed: block delivery now travels as ETH-standard RLP end to end — SealedBlock
// RLP-encodes once and reaches peers via directPushBlock (RPCBlockPushTopicV1,
// decoded by ReadChunkedBlock) and the RLP gossip fallback (BroadcastBlock).

// knownBlockNeedsAuthorizedReplay reports whether an already-stored block's
// STATE is not on the applied lineage — i.e. the QMDB applied head sits below
// it or has followed a sibling branch — so an authorized import must re-process
// it (unwind the sibling branch + re-execute) instead of skipping it as known.
// Canonical membership is NOT the test: the canonical row can point at this
// block while the applied state is still on a losing branch (they are maintained
// by different writers and can diverge by more than one block after repeated
// view changes or restarts).
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
			return nil
		}
		// Walk the actual applied lineage rather than consulting the canonical
		// table. CommitToCanonical can already have rewritten canonical rows to
		// the winning branch while QMDB still sits on a multi-block losing
		// branch. Checking only n == appliedNum skipped the winning ancestor in
		// that state, leaving its child future-queued with unknown-ancestor on
		// every catch-up attempt.
		curNum, curHash := appliedNum, appliedHash
		for curNum > n {
			hdr := rawdb.ReadHeader(tx, curHash, curNum)
			if hdr == nil {
				// Do not silently skip a block when the applied marker's lineage is
				// incomplete. Authorized replay will return the actionable lineage
				// error without treating the incoming block as invalid.
				need = true
				return nil
			}
			curHash = hdr.ParentHash
			curNum--
		}
		need = curHash != blk.Hash()
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
	return bc.CommitToCanonicalWith(hash, nil)
}

// CommitToCanonicalWith is CommitToCanonical with an optional hook run INSIDE
// the canonicalization transaction, after the canonical rows and head pointers
// are written.
//
// It exists because the consensus layer used to persist its state in a write
// transaction of its own, immediately after this one. Two transactions per
// committed block, and a profile at the 480M tier showed mdbx_txn_begin for
// WRITE transactions costing 20.4% of all node CPU -- more than the page
// writes themselves -- because MDBX has a single writer and charges txn_begin
// in proportion to freelist pressure. The consensus write carried 415 bytes
// and dirtied 16 KB; the head-pointer write carried 176 bytes and dirtied
// 36 KB. Folding one into the other removes a whole transaction per block.
//
// It also closes a crash window: canonical head and consensus state now move
// together. A crash between the two transactions left a node whose persisted
// view/lock did not match its applied chain -- exactly the restart failure the
// persistence code documents.
//
// The hook MUST NOT call back into anything that takes a lock the caller could
// already hold: it runs while this goroutine owns the MDBX writer, so blocking
// on a mutex whose holder is waiting for the writer deadlocks the node. Resolve
// every value the hook needs BEFORE calling. A hook error is logged and
// swallowed, never propagated: canonicalization must not fail because a
// bookkeeping write did, which is exactly what the separate transaction
// guaranteed before.
func (bc *BlockChain) CommitToCanonicalWith(hash types.Hash, inTx func(kv.RwTx) error) error {
	var committedNumber uint64
	var notify bool
	var committedBlk *block.Block
	// Phase timings: this runs inline on HotStuff's serial output loop, and its
	// two transaction-count-proportional steps (reading the block's bodies and
	// writing one TxLookup row per transaction) are what set the block cycle at
	// a high gas ceiling -- measured at 2.7 s mean of a 3.53 s cycle.
	var dRead, dWalk, dLookup, dLock, dBody, dPreRead time.Duration
	var indexed *block.Block
	tCommit := time.Now()
	// The committed block is two views old and almost always the instance
	// this node imported, still in the block cache with every transaction
	// hash memoised. Reading it from MDBX instead decoded 163,000
	// transactions (80 ms cold) and then hashed each of them again for the
	// transaction index (~200 ms, serial) -- inside the HotStuff loop, before
	// the next view could start (round 35zg's commit-to-canonical phases).
	var cached *block.Block
	if bc.blockCache != nil {
		if b, ok := bc.blockCache.Get(hash); ok && b != nil {
			cached = b
		}
	}
	if cached == nil && commitDupFast {
		// The block is not cached: settle a duplicate commit, or read the
		// block, on a read transaction instead of under the MDBX writer.
		dup, dupNumber, blk, perr := bc.commitPreRead(hash)
		if perr != nil {
			return perr
		}
		if dup {
			log.Info("commit-to-canonical phases",
				"number", dupNumber, "dup", true, "total", time.Since(tCommit))
			return nil
		}
		cached = blk
		if fn := commitAfterPreReadForTest; fn != nil {
			fn()
		}
		dPreRead = time.Since(tCommit)
		tCommit = time.Now()
	}
	err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		tEnter := time.Now()
		dLock = tEnter.Sub(tCommit)
		defer func() { dBody = time.Since(tEnter) }()
		tRead := time.Now()
		blk := cached
		if blk == nil && commitDupFast && bc.blockCache != nil {
			// The pre-read ran before the block was stored: this commit then
			// queued for the writer behind the block's own write, which has
			// cached the imported instance by now (E1N26 run 48 B1: 233 such
			// commits decoded 326,000 transactions here, 55-516 ms each).
			if b, ok := bc.blockCache.Get(hash); ok && b != nil {
				blk = b
			}
		}
		if blk == nil {
			var err error
			blk, err = rawdb.ReadBlockByHash(tx, hash)
			if err != nil {
				return err
			}
			// S64 (docs/QS_QUEUE.md): this decode is on the hot HotStuff commit
			// loop; prime the cache with the instance already in hand so the
			// next reader (sync body-range serving, api getBlockByNumber, miner
			// ancestor walk) does not pay for the same decode again. Gated by
			// N42_BLOCK_CACHE_PRIME; unset leaves this a read-only lookup as
			// before.
			//
			// Why `cached` (above) was nil despite writeBlockWithState's own
			// unconditional Add two views ago: bc.blockCache is an in-process
			// LRU, not persisted, so it is empty after every restart, and a
			// hash reached via the embedded-QC catch-up / fork-recovery path
			// (CatchUp, headIsConsensusBlock below) can name a block this
			// process never ran through writeBlockWithState at all -- it was
			// written to MDBX by a prior process lifetime or by range-import
			// tooling. The steady-state case (this process imported the block
			// itself) is already caught by the `cached` lookup above and never
			// reaches this branch.
			if blk != nil && bc.blockCache != nil && BlockCachePrimeEnabled() {
				bc.blockCache.Add(hash, blk)
			}
		}
		dRead = time.Since(tRead)
		if blk == nil {
			return fmt.Errorf("committed block %s not in db", hash.Hex())
		}
		committedNumber = blk.Number64().Uint64()
		// The fleet committed it: an own build is a convergence candidate again.
		if err := rawdb.ClearOwnUnverifiedMark(tx, hash); err != nil {
			return err
		}
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
		// An E=1 light node applies nothing; the executor's result stands
		// for the execution (e1.go).
		if bc.qmdbEnabled && !bc.E1Light() {
			if err := requireAppliedCommitLineage(bc.ctx, tx, hash, committedNumber); err != nil {
				return err
			}
		}
		// Monotonicity guard: a commit for a height at or below the current
		// committed head whose block is ALREADY canonical is a late or duplicate
		// commit (the embedded-QC catch-up path racing the live Decide). The
		// higher commit's walk already canonicalized this lineage; re-running
		// would truncate the newer rows above it.
		if hn := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx)); hn != nil && *hn >= blk.Number64().Uint64() {
			if existing, rerr := rawdb.ReadCanonicalHash(tx, blk.Number64().Uint64()); rerr == nil && existing == hash {
				// HotStuff imports every candidate as a side block, so the normal
				// writeHeadBlock path never creates TxLookup entries. Without the
				// tail indexer, repair them even for an idempotent/duplicate commit
				// (this also self-heals databases produced before commit-time
				// indexing was added). With the indexer on, the tail/segment tiers
				// own the lookups and the normal path writes none: rewriting one row
				// per transaction here held the MDBX writer for 5-27 s on a
				// 163,000-tx block while a lagging node re-committed it. The write
				// is timed as txlookup, not folded into the body phase.
				if bc.txIndexer == nil {
					tLookup := time.Now()
					rawdb.WriteTxLookupEntries(tx, blk)
					dLookup = time.Since(tLookup)
				}
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
		tWalk := time.Now()
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
		dWalk = time.Since(tWalk)
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
		// Leader-driven imports return SideStatTy before writeHeadBlock, which is
		// the ordinary TxLookup writer. Persist the canonical block's lookup
		// entries here so eth_getTransactionByHash/Receipt can resolve live
		// HotStuff transactions as soon as the commit becomes durable.
		// With the tail tier on, the newest blocks are indexed in memory and
		// sealed into RecSplit segments, and nothing is written here: one row
		// per transaction keyed by transaction hash dirtied thousands of
		// scattered pages and made mdbx_txn_commit 94.6% of this phase.
		tLookup := time.Now()
		if bc.txIndexer == nil {
			rawdb.WriteTxLookupEntries(tx, blk)
		} else {
			indexed = blk
		}
		dLookup = time.Since(tLookup)
		// currentBlock is published AFTER the MDBX commit below succeeds —
		// publishing inside the closure advertised a head whose canonical
		// rows never became durable when commit failed (RPC then served a
		// phantom head and the miner's height gate keyed off it).
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
		// Once per committed block, duplicating what "hotstuff: block committed"
		// already reports; a rewrite of the canonical chain is the part worth
		// surfacing and it is logged where it happens.
		log.Debug("commit-to-canonical applied", "number", blk.Number64().Uint64(), "hash", hash.Hex())
		committedNumber = blk.Number64().Uint64()
		committedBlk = blk
		notify = true
		if inTx != nil {
			if herr := inTx(tx); herr != nil {
				log.Warn("commit-to-canonical: in-transaction hook failed",
					"number", committedNumber, "err", herr)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if committedBlk != nil {
		bc.currentBlock.Store(committedBlk)
		bc.framesCommitted(committedBlk)
	}
	// lock: waiting for MDBX's single writer. body: the closure. The remainder
	// (total-lock-body) is mdbx_txn_commit -- writing and fsyncing the pages the
	// closure dirtied, which for one TxLookup row per transaction is thousands
	// of random-key B-tree inserts.
	total := time.Since(tCommit)
	if indexed != nil {
		txs := indexed.Transactions()
		hashes := make([]types.Hash, len(txs))
		// Memoised on the cached instance; a fresh decode still hashes, but
		// across the cores rather than one.
		workers := runtime.GOMAXPROCS(0)
		if workers > 32 {
			workers = 32
		}
		if len(txs) < 4096 || workers < 2 {
			for i, tx := range txs {
				hashes[i] = tx.Hash()
			}
		} else {
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(start int) {
					defer wg.Done()
					for i := start; i < len(txs); i += workers {
						hashes[i] = txs[i].Hash()
					}
				}(w)
			}
			wg.Wait()
		}
		bc.txIndexer.Add(indexed.Number64().Uint64(), hashes)
	}
	log.Info("commit-to-canonical phases",
		"number", committedNumber, "lock", dLock, "read", dRead, "walk", dWalk,
		"txlookup", dLookup, "body", dBody, "commit", total-dLock-dBody, "total", total, "preRead", dPreRead)
	// HotStuff imports candidates as side blocks and advances the canonical
	// head only here. Notify after the database transaction commits: the
	// import-time canonical callback is intentionally bypassed on this path.
	if notify {
		bc.notifyBlockCommitted(committedNumber)
		// Every validator must retire executed pool transactions and refresh
		// head subscribers. A local seal is only a speculative candidate on
		// HotStuff; followers never emit that miner event at all.
		event.GlobalEvent.Send(common.ChainHighestBlock{Block: *committedBlk, Inserted: true})
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
			// A sibling this node failed to validate is never the one to
			// converge on: round 26 locked a fleet on exactly such a block.
			if rawdb.IsBadHeaderMarked(tx, hh) {
				continue
			}
			// A block this node built and wrote itself is not a candidate
			// until the fleet vouched for it (the mark clears on commit):
			// the builder never verifies its own block, and rounds 35zq /
			// 35zw re-proposed a rejected own build after a restart.
			if rawdb.IsOwnUnverifiedMarked(tx, hh) {
				continue
			}
			bc.badSiblingsMu.RLock()
			_, bad := bc.badSiblings[hh]
			bc.badSiblingsMu.RUnlock()
			if bad {
				continue
			}
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
