// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Committed-block vote floor. A validator must never vote for, or locally
// commit, a block that does not extend the block its own chain has already
// committed (the QC-backed HotStuffCommittedHead marker). The HotStuff
// round state alone cannot guarantee that: a reset tool, a lost journal or a
// fresh engine restarts with an empty lock, and the extends rule then fails
// open on the genesis JustifyQC. Round S131 lost a 7-node fleet exactly that
// way: three nodes that had committed f343 (h=14131247) header-voted, after
// a reset, for a sibling of f343 proposed at the same height in view 2.

package hotstuff

import (
	"context"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// CommittedChainReader exposes this node's own committed chain to the vote
// rule. Implementations must be cheap and must not call back into the
// engine: they run with the engine mutex held.
type CommittedChainReader interface {
	// CommittedHead returns the highest block this node has committed. ok is
	// false when nothing beyond genesis is committed yet; err reports a read
	// failure (the caller then abstains rather than failing open).
	CommittedHead() (number uint64, hash types.Hash, ok bool, err error)
	// HeaderInfo returns a locally stored header's number and parent.
	HeaderInfo(hash types.Hash) (number uint64, parent types.Hash, ok bool)
	// CanonicalHash returns the committed (canonical) hash at number.
	CanonicalHash(number uint64) (types.Hash, bool)
}

// floorVerdict is the outcome of checking a block against the committed floor.
type floorVerdict uint8

const (
	floorAllow  floorVerdict = iota // the block extends (or is on) the committed chain
	floorRefuse                     // the block provably conflicts with the committed chain
	floorWait                       // ancestry not known yet: do not vote now, retry on the next block event
)

func (v floorVerdict) String() string {
	switch v {
	case floorAllow:
		return "allow"
	case floorRefuse:
		return "refuse"
	default:
		return "wait"
	}
}

// maxFloorWalk bounds the ancestry walk. The engine's parent maps hold at
// most three FIFOs of MaxImportedBlocks entries, and a proposal more than
// this far above the committed head belongs to a node that must catch up
// before it can attest anything.
const maxFloorWalk = 4 * MaxImportedBlocks

// SetCommittedChain installs the committed-chain reader used by the vote
// floor. Nil disables the floor (unit tests, embedded harnesses). Call
// before Start; thread-safe.
func (e *ConsensusEngine) SetCommittedChain(r CommittedChainReader) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.committedChain = r
}

// parentOf returns blockHash's parent from the engine's own maps, falling
// back to the stored header.
func (e *ConsensusEngine) parentOf(blockHash types.Hash) (types.Hash, bool) {
	if p, ok := e.importedParents[blockHash]; ok && p != (types.Hash{}) {
		return p, true
	}
	if _, p, ok := e.committedChain.HeaderInfo(blockHash); ok && p != (types.Hash{}) {
		return p, true
	}
	return types.Hash{}, false
}

// checkCommittedFloor decides whether blockHash is consistent with this
// node's committed chain: it is the committed block, a committed ancestor of
// it, or a descendant whose ancestor at the committed height IS the
// committed block. Anything else is refused; an ancestry this node cannot
// resolve yet is "wait", never a pass. Caller must hold e.mu.
//
// Cost on the healthy path: one committed-head read and a map lookup -- the
// proposal's parent is the committed block, or one or two still-uncommitted
// blocks whose parents the header-known/checked events already recorded.
func (e *ConsensusEngine) checkCommittedFloor(blockHash types.Hash) floorVerdict {
	if e.committedChain == nil {
		return floorAllow
	}
	cNum, cHash, ok, err := e.committedChain.CommittedHead()
	if err != nil {
		log.Warn("hotstuff: committed floor unreadable, abstaining", "blockHash", blockHash, "err", err)
		return floorWait
	}
	if !ok || cHash == (types.Hash{}) || blockHash == cHash {
		return floorAllow
	}

	// Phase 1: walk the engine's parent maps until the committed block shows
	// up as a parent (the common case) or a stored header gives a height.
	cur, steps := blockHash, uint64(0)
	for {
		if p, known := e.importedParents[cur]; known && p != (types.Hash{}) {
			if p == cHash {
				return floorAllow
			}
			cur, steps = p, steps+1
			if steps > maxFloorWalk {
				return floorWait
			}
			continue
		}
		break
	}
	num, parent, found := e.committedChain.HeaderInfo(cur)
	if !found {
		return floorWait
	}
	blockHeight := num + steps
	if blockHeight <= cNum {
		// At or below the committed height the block must BE committed (a
		// late Decide or a stale re-proposal of a block already on this
		// chain is harmless).
		if ch, ok := e.committedChain.CanonicalHash(blockHeight); ok && ch == blockHash {
			return floorAllow
		}
		return floorRefuse
	}
	if num <= cNum {
		// The walk passed the committed height without meeting the committed
		// block: the path crosses that height on another branch.
		return floorRefuse
	}

	// Phase 2: heights are known from here down; reach the committed height.
	for num > cNum+1 {
		if steps++; steps > maxFloorWalk {
			return floorWait
		}
		cur, num = parent, num-1
		p, ok := e.parentOf(cur)
		if !ok {
			return floorWait
		}
		parent = p
	}
	if parent == cHash {
		return floorAllow
	}
	return floorRefuse
}

// extendsCommitted is checkCommittedFloor for the vote sites: true only on
// floorAllow, logging the refusals. Caller must hold e.mu.
func (e *ConsensusEngine) extendsCommitted(view ViewNumber, blockHash types.Hash) bool {
	v := e.checkCommittedFloor(blockHash)
	switch v {
	case floorAllow:
		return true
	case floorRefuse:
		cNum, cHash, _, _ := e.committedChain.CommittedHead()
		log.Warn("hotstuff vote REFUSED: block does not extend this node's committed block",
			"view", view, "blockHash", blockHash, "committedNumber", cNum, "committedHash", cHash)
		metricVoteCommittedFloor.Inc()
	default:
		log.Info("hotstuff vote deferred: ancestry to the committed block not known yet",
			"view", view, "blockHash", blockHash)
	}
	return false
}

// ConflictsWithCommitted reports whether blockHash provably conflicts with
// this node's committed chain (see checkCommittedFloor). For the leader's
// build gate, which must not stall on the ordinary unresolved case: the
// leader's own previous block is in neither the parent maps nor, until its
// write lands, the header table.
func (e *ConsensusEngine) ConflictsWithCommitted(blockHash types.Hash) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.checkCommittedFloor(blockHash) == floorRefuse
}

// DBCommittedChain reads the committed chain from the chain database: the
// QC-backed HotStuffCommittedHead marker (written only by
// CommitToCanonical), the header table and the canonical table.
type DBCommittedChain struct {
	ctx context.Context
	db  kv.RoDB
}

// NewDBCommittedChain returns a CommittedChainReader over db.
func NewDBCommittedChain(ctx context.Context, db kv.RoDB) *DBCommittedChain {
	return &DBCommittedChain{ctx: ctx, db: db}
}

// CommittedHead implements CommittedChainReader.
func (c *DBCommittedChain) CommittedHead() (number uint64, hash types.Hash, ok bool, err error) {
	err = c.db.View(c.ctx, func(tx kv.Tx) error {
		h := rawdb.ReadHotStuffCommittedHead(tx)
		if h == (types.Hash{}) {
			return nil
		}
		n := rawdb.ReadHeaderNumber(tx, h)
		if n == nil {
			return nil
		}
		number, hash, ok = *n, h, true
		return nil
	})
	return number, hash, ok, err
}

// HeaderInfo implements CommittedChainReader.
func (c *DBCommittedChain) HeaderInfo(hash types.Hash) (number uint64, parent types.Hash, ok bool) {
	_ = c.db.View(c.ctx, func(tx kv.Tx) error {
		n := rawdb.ReadHeaderNumber(tx, hash)
		if n == nil {
			return nil
		}
		hdr := rawdb.ReadHeader(tx, hash, *n)
		if hdr == nil {
			return nil
		}
		number, parent, ok = *n, hdr.ParentHash, true
		return nil
	})
	return number, parent, ok
}

// CanonicalHash implements CommittedChainReader.
func (c *DBCommittedChain) CanonicalHash(number uint64) (types.Hash, bool) {
	var h types.Hash
	_ = c.db.View(c.ctx, func(tx kv.Tx) error {
		ch, err := rawdb.ReadCanonicalHash(tx, number)
		if err == nil {
			h = ch
		}
		return nil
	})
	return h, h != (types.Hash{})
}
