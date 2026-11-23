// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Anchor book of the S1 anchored catch-up (sync v2 design §2, N42 sync
// standard §3.3-§3.4): the hashes authenticated by a verified commit-domain
// QC, the parent links of fetched blocks that let authentication propagate
// downwards, and the bounded unanchored tail the sync importer holds
// unexecuted (enforce mode).

package sync

import (
	gosync "sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// anchorTailMaxBlocks and anchorTailMaxBytes bound the unanchored tail
// (standard §3.4 step 6). On overflow the highest blocks are evicted; their
// links stay in the book so they can be re-fetched and recognised later.
// Variables only so tests can lower them.
var (
	anchorTailMaxBlocks        = 64
	anchorTailMaxBytes  uint64 = 512 << 20
)

const (

	// maxAnchorAuth bounds the authenticated-hash set and maxAnchorLinks the
	// parent-link skeleton. Both evict oldest first. 64 Ki links cover 64
	// full ranges of skeleton at about 100 bytes each.
	maxAnchorAuth  = 1 << 16
	maxAnchorLinks = 1 << 16
)

type anchorLink struct {
	parent types.Hash
	number uint64
}

// anchorBook is safe for concurrent use. The catch-up goroutine (single
// owner through catchUpInProgress) screens ranges and drains the tail; the
// consensus layer adds anchors from Decides and commit targets.
type anchorBook struct {
	mu gosync.Mutex

	// auth holds every hash known to lie on the parent chain at or below a
	// verified CommitQC anchor.
	auth      map[types.Hash]struct{}
	authOrder []types.Hash

	// links is the parent-hash skeleton of fetched blocks (bodies may be gone).
	links     map[types.Hash]anchorLink
	linkOrder []types.Hash

	// skelTop is the highest block number fetched for the current anchoring
	// attempt: the top of the last range that superseded the tail, raised by
	// look-ahead ranges above it.
	skelTop uint64

	// tail is the held, contiguous, unanchored chain above the executed
	// prefix, ascending. tailBytes is its estimated decoded size.
	tail      []block.IBlock
	tailBytes uint64
}

func (b *anchorBook) initLocked() {
	if b.auth == nil {
		b.auth = make(map[types.Hash]struct{})
		b.links = make(map[types.Hash]anchorLink)
	}
}

func (b *anchorBook) addAuthLocked(h types.Hash) bool {
	if _, ok := b.auth[h]; ok {
		return false
	}
	if len(b.authOrder) >= maxAnchorAuth {
		delete(b.auth, b.authOrder[0])
		b.authOrder = b.authOrder[1:]
	}
	b.auth[h] = struct{}{}
	b.authOrder = append(b.authOrder, h)
	return true
}

// markAuthLocked authenticates h and walks the link skeleton downwards,
// authenticating every ancestor it knows, until a missing link or an
// already-authenticated hash.
func (b *anchorBook) markAuthLocked(h types.Hash) {
	b.initLocked()
	for cur, steps := h, 0; steps <= maxAnchorLinks; steps++ {
		if !b.addAuthLocked(cur) && steps > 0 {
			return
		}
		l, ok := b.links[cur]
		if !ok {
			return
		}
		cur = l.parent
	}
}

// noteAnchor records a hash named by a verified CommitQC.
func (b *anchorBook) noteAnchor(h types.Hash) {
	if h == (types.Hash{}) {
		return
	}
	b.mu.Lock()
	b.markAuthLocked(h)
	b.mu.Unlock()
}

func (b *anchorBook) isAuth(h types.Hash) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.auth[h]
	return ok
}

// recordLinksLocked adds the parent links of blocks (ascending, linked) and
// propagates authentication through them: a block whose hash is already
// authenticated authenticates its ancestors.
func (b *anchorBook) recordLinksLocked(blocks []block.IBlock) {
	b.initLocked()
	for _, blk := range blocks {
		h := blk.Hash()
		if _, ok := b.links[h]; ok {
			continue
		}
		if len(b.linkOrder) >= maxAnchorLinks {
			delete(b.links, b.linkOrder[0])
			b.linkOrder = b.linkOrder[1:]
		}
		b.links[h] = anchorLink{parent: blk.ParentHash(), number: blk.Number64().Uint64()}
		b.linkOrder = append(b.linkOrder, h)
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if _, ok := b.auth[blocks[i].Hash()]; ok {
			// Walk down from the highest authenticated block of the run.
			b.markAuthLocked(blocks[i].Hash())
			break
		}
	}
}

// hasUnauthLink reports whether h is a recorded link that is not yet
// authenticated (a QC naming it can extend authentication).
func (b *anchorBook) hasUnauthLink(h types.Hash) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.links[h]; !ok {
		return false
	}
	_, authed := b.auth[h]
	return !authed
}

// tailReleasable reports whether the held tail has an authenticated block,
// and the number of the tail's top block.
func (b *anchorBook) tailReleasable() (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.tail) == 0 || b.highestAuthLocked(b.tail) < 0 {
		return 0, false
	}
	return b.tail[len(b.tail)-1].Number64().Uint64(), true
}

// authedLinkAt reports whether the skeleton holds an authenticated hash at
// number (a block whose body must be re-fetched and is then executable).
func (b *anchorBook) authedLinkAt(number uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for h, l := range b.links {
		if l.number != number {
			continue
		}
		if _, ok := b.auth[h]; ok {
			return true
		}
	}
	return false
}

// highestAuthLocked returns the index of the highest authenticated block of
// chain (ascending, linked), or -1. Authentication is downward-closed along
// a linked chain, so everything at or below the index is authenticated.
func (b *anchorBook) highestAuthLocked(chain []block.IBlock) int {
	for i := len(chain) - 1; i >= 0; i-- {
		if _, ok := b.auth[chain[i].Hash()]; ok {
			return i
		}
	}
	return -1
}

// setTailLocked stores held as the new tail, evicting the highest blocks
// beyond the block and byte bounds. It returns how many were evicted.
func (b *anchorBook) setTailLocked(held []block.IBlock) int {
	var bytes uint64
	keep := 0
	for keep < len(held) && keep < anchorTailMaxBlocks {
		sz := estimateBlockBytes(held[keep])
		if keep > 0 && bytes+sz > anchorTailMaxBytes {
			break
		}
		bytes += sz
		keep++
	}
	b.tail = append([]block.IBlock(nil), held[:keep]...)
	b.tailBytes = bytes
	syncAnchorTailBlocks.Set(float64(len(b.tail)))
	return len(held) - keep
}

// tailHolds reports whether h is a block of the held, unanchored tail.
func (b *anchorBook) tailHolds(h types.Hash) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, blk := range b.tail {
		if blk.Hash() == h {
			return true
		}
	}
	return false
}

func (b *anchorBook) tailLen() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.tail)
}

// releaseAuthenticatedTail splits off and returns the authenticated prefix of
// the held tail (nil if none), keeping the rest held.
func (b *anchorBook) releaseAuthenticatedTail() []block.IBlock {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.tail) == 0 {
		return nil
	}
	top := b.highestAuthLocked(b.tail)
	if top < 0 {
		return nil
	}
	released := append([]block.IBlock(nil), b.tail[:top+1]...)
	rest := b.tail[top+1:]
	b.tail = nil
	b.setTailLocked(rest)
	return released
}

// estimateBlockBytes is a cheap upper-ish estimate of a block's decoded size
// for the tail byte bound: header overhead plus calldata and a per-transaction
// envelope allowance. It avoids re-encoding a block of up to 64 MiB.
func estimateBlockBytes(blk block.IBlock) uint64 {
	n := uint64(1024)
	for _, tx := range blk.Transactions() {
		if tx == nil {
			continue
		}
		n += uint64(len(tx.Data())) + 160
	}
	return n
}
