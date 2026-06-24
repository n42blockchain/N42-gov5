// changeset_fallback: splice missing per-block changesets into the build from a
// SECONDARY erigon-style MDBX (AccountChangeSet/StorageChangeSet, e.g.
// D:/N42-hashed/chaindata) when the primary acctcs/storcs freezer recorded an
// EMPTY blob for a block that actually mutated state (a resume-gap left by the
// freezer's staged generation). For each such gap block we derive the FORWARD
// (post-block) delta: newVal of a key = the OLD value recorded at the key's NEXT
// change (forward pending chain), or — if the key is never changed again up to
// the fallback head — the fallback's current HashedAccounts/HashedStorage value.
// The derived dirtyA/dirtyS are injected by decodeOne so the fold consumes a
// COMPLETE, correct changeset. The 304 GB primary freezer is never modified.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

type fbBlock struct {
	dirtyA map[types.Address]*account.StateAccount
	dirtyS map[types.Address]map[types.Hash]*uint256.Int
}

// loadChangesetFallback finds blocks in [start,end) whose primary freezer blob is
// empty but which the fallback chain has a changeset for, and derives their
// forward delta into b.csFallback. Returns the number of gap blocks filled.
func (b *builder) loadChangesetFallback(dir string, start, end uint64) (int, error) {
	t0 := time.Now()
	fdb := openN42Chain(dir, 8192)
	defer fdb.Close()
	ftx, err := fdb.BeginRo(context.Background())
	if err != nil {
		return 0, err
	}
	defer ftx.Rollback()

	// Pass 1: locate gap blocks (primary empty, fallback has data) and seed the
	// per-block result + the set of keys we must track through the sweep.
	b.csFallback = map[uint64]*fbBlock{}
	gapAddr := map[types.Address]bool{}
	gapStor := map[string]bool{} // addr(20)||slot(32)
	var gapLo, gapHi uint64 = ^uint64(0), 0

	accC, err := ftx.Cursor(modules.AccountChangeSet)
	if err != nil {
		return 0, err
	}
	defer accC.Close()
	stoC, err := ftx.Cursor(modules.StorageChangeSet)
	if err != nil {
		return 0, err
	}
	defer stoC.Close()

	blockHasFallback := func(c kv.Cursor, n uint64) bool {
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], n)
		k, _, _ := c.Seek(bk[:])
		return k != nil && binary.BigEndian.Uint64(k[:8]) == n
	}

	// A primary blob is "effectively empty" when its 2-byte LittleEndian count
	// header is 0 (a resume-gap wrote either a 0-length blob OR a count=0 stub —
	// e.g. block 25101866). Both mean the fold sees no changes, so both need the
	// fallback when the block actually mutated state.
	primaryCount := func(blob []byte) int {
		if len(blob) < 2 {
			return 0
		}
		return int(binary.LittleEndian.Uint16(blob[:2]))
	}
	for n := start; n < end; n++ {
		ab, _ := b.acctTbl.Retrieve(n)
		sb, _ := b.storTbl.Retrieve(n)
		if primaryCount(ab) != 0 || primaryCount(sb) != 0 {
			continue // primary has real changes for this block
		}
		hasA := blockHasFallback(accC, n)
		hasS := blockHasFallback(stoC, n)
		if !hasA && !hasS {
			continue // genuinely empty (no state change) — nothing to splice
		}
		fb := &fbBlock{
			dirtyA: map[types.Address]*account.StateAccount{},
			dirtyS: map[types.Address]map[types.Hash]*uint256.Int{},
		}
		b.csFallback[n] = fb
		if n < gapLo {
			gapLo = n
		}
		if n > gapHi {
			gapHi = n
		}
		// collect this block's touched keys (account)
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], n)
		for k, v, e := accC.Seek(bk[:]); k != nil && e == nil; k, v, e = accC.Next() {
			bn, addrB, _, derr := changeset.DecodeAccounts(k, v)
			if derr != nil {
				return 0, fmt.Errorf("decode acc cs %d: %w", n, derr)
			}
			if bn != n {
				break
			}
			var addr types.Address
			copy(addr[:], addrB)
			gapAddr[addr] = true
			fb.dirtyA[addr] = nil // placeholder; filled by the sweep
		}
		for k, v, e := stoC.Seek(bk[:]); k != nil && e == nil; k, v, e = stoC.Next() {
			bn, comp, _, derr := changeset.DecodeStorage(k, v)
			if derr != nil {
				return 0, fmt.Errorf("decode sto cs %d: %w", n, derr)
			}
			if bn != n {
				break
			}
			gapStor[string(comp)] = true // comp = addr(20)||slot(32)
		}
	}
	if len(b.csFallback) == 0 {
		fmt.Printf("[fallback] no gap blocks in [%d,%d) — nothing to splice\n", start, end)
		return 0, nil
	}
	fmt.Printf("[fallback] %d gap block(s) [%d,%d]: %d account keys, %d storage keys to derive\n",
		len(b.csFallback), gapLo, gapHi, len(gapAddr), len(gapStor))

	// latest = current head value (key never changed again after the gap).
	latestA := func(addr types.Address) []byte {
		ah := crypto.Keccak256(addr[:])
		v, _ := ftx.GetOne(modules.HashedAccounts, ah)
		return v
	}
	latestS := func(addr types.Address, slot types.Hash) []byte {
		var hk [64]byte
		copy(hk[:32], crypto.Keccak256(addr[:]))
		copy(hk[32:], crypto.Keccak256(slot[:]))
		v, _ := ftx.GetOne(modules.HashedStorage, hk[:])
		return v
	}

	// Pass 2 (account): forward sweep from gapLo. newVal of a pending gap entry =
	// the OLD value at the key's next change. Early-break once every gap key is
	// resolved and we are past the gap. Unresolved tail -> latest().
	lastGapA := map[types.Address]uint64{}
	{
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], gapLo)
		for k, v, e := accC.Seek(bk[:]); k != nil && e == nil; k, v, e = accC.Next() {
			bn, addrB, oldVal, derr := changeset.DecodeAccounts(k, v)
			if derr != nil {
				return 0, derr
			}
			if bn > gapHi && len(lastGapA) == 0 {
				break
			}
			var addr types.Address
			copy(addr[:], addrB)
			if !gapAddr[addr] {
				continue
			}
			if p, ok := lastGapA[addr]; ok {
				b.csFallback[p].dirtyA[addr] = decodeFbAccount(oldVal)
				delete(lastGapA, addr)
			}
			if bn >= gapLo && bn <= gapHi {
				lastGapA[addr] = bn
			}
		}
		for addr, p := range lastGapA { // never changed again -> head value
			b.csFallback[p].dirtyA[addr] = decodeFbAccount(latestA(addr))
		}
	}

	// Pass 2 (storage): same, keyed by addr||slot.
	lastGapS := map[string]uint64{}
	{
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], gapLo)
		for k, v, e := stoC.Seek(bk[:]); k != nil && e == nil; k, v, e = stoC.Next() {
			bn, comp, oldVal, derr := changeset.DecodeStorage(k, v)
			if derr != nil {
				return 0, derr
			}
			if bn > gapHi && len(lastGapS) == 0 {
				break
			}
			ck := string(comp)
			if !gapStor[ck] {
				continue
			}
			if p, ok := lastGapS[ck]; ok {
				setFbStorage(b.csFallback[p], comp, oldVal)
				delete(lastGapS, ck)
			}
			if bn >= gapLo && bn <= gapHi {
				lastGapS[ck] = bn
			}
		}
		for ck, p := range lastGapS {
			comp := []byte(ck)
			var addr types.Address
			var slot types.Hash
			copy(addr[:], comp[:20])
			copy(slot[:], comp[20:52])
			setFbStorage(b.csFallback[p], comp, latestS(addr, slot))
		}
	}

	fmt.Printf("[fallback] derived %d gap block changesets from %s in %s\n",
		len(b.csFallback), dir, time.Since(t0).Round(time.Millisecond))
	return len(b.csFallback), nil
}

// decodeFbAccount turns a forward NEW account value into *StateAccount; empty =
// nil (account deleted / absent at the end of the block).
func decodeFbAccount(v []byte) *account.StateAccount {
	if len(v) == 0 {
		return nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(v); err != nil {
		die("fallback decode account: %v", err)
	}
	return &a
}

// setFbStorage records a forward storage slot value (empty = cleared to 0 = nil).
func setFbStorage(fb *fbBlock, comp, v []byte) {
	var addr types.Address
	var slot types.Hash
	copy(addr[:], comp[:20])
	copy(slot[:], comp[20:52])
	inner, ok := fb.dirtyS[addr]
	if !ok {
		inner = map[types.Hash]*uint256.Int{}
		fb.dirtyS[addr] = inner
	}
	if len(v) == 0 {
		inner[slot] = nil
		return
	}
	inner[slot] = new(uint256.Int).SetBytes(v)
}
