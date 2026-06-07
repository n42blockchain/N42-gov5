// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// mobile_overlay.go — the mobile node's rolling in-memory state overlay. As
// MobileFollowTick witness-verifies (layer ②) each new block, it folds that
// block's post-state changeset (new account/storage values) into this overlay.
// The overlay keeps only the most recent `retention` blocks (default 900):
// when block H falls out of the window, the values it set are dropped UNLESS a
// later block overwrote them (tracked via per-key last-writer block number).
//
// Reads (MobileBalanceOf) consult the overlay first — an overlay hit is a value
// that was witness-verified on-device, so it needs no network round-trip. A
// miss (key not touched in the window, or evicted) falls back to a verified
// EIP-1186 proof from the IDC (layer ③). Nothing is written to disk; the whole
// overlay lives in RAM, sized by how many distinct keys changed in ~900 blocks.

package evmsdk

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
)

// ovalue is one overlay cell: the latest value plus the block that wrote it
// (so pruning can tell whether an aged-out block is still the last writer).
type ovalue struct {
	val       []byte // account V2 bytes or storage value bytes; nil = deleted/zero
	lastBlock uint64
}

type slotKey struct {
	addr types.Address
	slot types.Hash
}

// touchedKeys records which keys a single block wrote, for window pruning.
type touchedKeys struct {
	accs  []types.Address
	slots []slotKey
}

// mobileOverlay is the rolling post-state cache (RAM only).
type mobileOverlay struct {
	retention uint64
	accounts  map[types.Address]ovalue
	storage   map[types.Address]map[types.Hash]ovalue
	touched   map[uint64]*touchedKeys
	minBlock  uint64 // oldest block currently retained (0 = none yet)
	head      uint64 // highest block applied (0 = none yet)
}

func newMobileOverlay(retention uint64) *mobileOverlay {
	if retention == 0 {
		retention = 900
	}
	return &mobileOverlay{
		retention: retention,
		accounts:  make(map[types.Address]ovalue),
		storage:   make(map[types.Address]map[types.Hash]ovalue),
		touched:   make(map[uint64]*touchedKeys),
	}
}

// apply folds one verified block's post-state into the overlay, then prunes any
// blocks that fell outside the [head-retention+1, head] window.
//
// The overlay's last-writer / prune invariants assume blocks are applied
// monotonically and gap-free. Callers must feed it only forward-contiguous
// blocks; as defense-in-depth a non-forward block (blockNum <= head) is ignored
// so a stray out-of-order apply can never clobber a newer cell with a stale
// value (which would otherwise be served as "verified") or strand entries below
// the prune cursor.
func (o *mobileOverlay) apply(blockNum uint64, ps *ethel.PostState) {
	if o.head != 0 && blockNum <= o.head {
		return // non-forward apply — ignore (see contract above)
	}
	o.head = blockNum
	if ps == nil {
		o.pruneTo(blockNum)
		return
	}
	tk := &touchedKeys{}

	// A wiped (CreateContract) address loses all prior-block storage: drop its
	// overlay slots before applying this block's fresh writes. We can't keep
	// last-writer bookkeeping for the dropped slots, but stale `touched`
	// entries pointing at them are harmless — prune only deletes a cell when it
	// is still present AND last-written by the aged block.
	for addr := range ps.Wiped {
		delete(o.storage, addr)
	}

	for addr, b := range ps.Accounts {
		o.accounts[addr] = ovalue{val: b, lastBlock: blockNum}
		tk.accs = append(tk.accs, addr)
	}
	for addr, slots := range ps.Storage {
		inner := o.storage[addr]
		if inner == nil {
			inner = make(map[types.Hash]ovalue, len(slots))
			o.storage[addr] = inner
		}
		for slot, v := range slots {
			inner[slot] = ovalue{val: v, lastBlock: blockNum}
			tk.slots = append(tk.slots, slotKey{addr: addr, slot: slot})
		}
	}
	o.touched[blockNum] = tk
	if o.minBlock == 0 {
		o.minBlock = blockNum
	}
	o.pruneTo(blockNum)
}

// pruneTo evicts blocks older than head-retention+1 (i.e. keeps the most recent
// `retention` blocks ending at head). Blocks are applied contiguously so the
// oldest cursor advances by one each step; a gap (nil touched) is skipped.
func (o *mobileOverlay) pruneTo(head uint64) {
	if head < o.retention {
		return // window not full yet — nothing to evict
	}
	cutoff := head - o.retention // evict blocks with number <= cutoff
	for o.minBlock != 0 && o.minBlock <= cutoff {
		o.pruneBlock(o.minBlock)
		o.minBlock++
	}
}

func (o *mobileOverlay) pruneBlock(b uint64) {
	tk := o.touched[b]
	if tk == nil {
		return
	}
	for _, addr := range tk.accs {
		if cur, ok := o.accounts[addr]; ok && cur.lastBlock == b {
			delete(o.accounts, addr)
		}
	}
	for _, sk := range tk.slots {
		if m := o.storage[sk.addr]; m != nil {
			if cur, ok := m[sk.slot]; ok && cur.lastBlock == b {
				delete(m, sk.slot)
			}
			if len(m) == 0 {
				delete(o.storage, sk.addr)
			}
		}
	}
	delete(o.touched, b)
}

// account returns (V2-bytes, true) if the address was touched within the
// window. A nil byte slice with ok=true means the account was deleted/absent
// as of its last write. ok=false means "not in overlay — consult a proof".
func (o *mobileOverlay) account(addr types.Address) ([]byte, uint64, bool) {
	if v, ok := o.accounts[addr]; ok {
		return v.val, v.lastBlock, true
	}
	return nil, 0, false
}

// storageAt returns (value-bytes, true) if (addr,slot) was touched within the
// window; nil bytes = zeroed. ok=false → consult a proof.
func (o *mobileOverlay) storageAt(addr types.Address, slot types.Hash) ([]byte, uint64, bool) {
	if m := o.storage[addr]; m != nil {
		if v, ok := m[slot]; ok {
			return v.val, v.lastBlock, true
		}
	}
	return nil, 0, false
}

// decodeAccount unpacks the overlay's V2 account bytes into balance/nonce/etc.
// A nil/empty slice means the account is absent (deleted or never existed).
func decodeAccount(b []byte) (*account.StateAccount, bool) {
	if len(b) == 0 {
		return nil, false
	}
	var a account.StateAccount
	if err := a.DecodeForStorageV2(b); err != nil {
		return nil, false
	}
	return &a, true
}

// stats reports overlay occupancy for diagnostics.
func (o *mobileOverlay) stats() (accounts, storageAddrs, blocks int) {
	return len(o.accounts), len(o.storage), len(o.touched)
}
