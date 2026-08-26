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
// EIP-2929/2930 warm access list for transactions.
// accessList stores a map of Address -> slotSetIndex (-1 means address
// warmed without slots) alongside a slice of slot hash sets. ContainsAddress
// and Contains answer address/slot warmness queries used by gas accounting.
// newAccessList and Copy build and clone instances for per-tx lifecycle.

package state

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/types"
)

type accessList struct {
	addresses map[types.Address]int
	slots     []map[types.Hash]struct{}
	// staticWarm marks the precompile addresses EIP-2929 declares warm from
	// the start of every transaction. They used to be inserted into
	// addresses one by one in PrepareAccessList — a map write plus a journal
	// entry for each of 10-17 addresses, every transaction, 0.62% of all
	// replay CPU and the single largest allocation site by object count. A
	// precompile is never removed within a transaction and no snapshot
	// predates its insertion, so a static membership test is equivalent.
	//
	// Precompiles live at 0x00..0000XXXX, so the set is a 65536-bit bitmap
	// indexed by the last two bytes, guarded by an all-zero check on the
	// first eighteen. Any precompile address outside that shape falls back to
	// the map (SetPrecompiles returns it).
	staticWarm     [1024]uint64
	staticWarmBits []uint16 // which bits are set, so Reset clears exactly those
	// slotPool holds previously-used slotmaps that have been cleared
	// by Reset and are ready to be re-bound to a new (addr, slot) pair
	// in AddSlot. EVM hot path empirically allocates ~1.3K slotmap per
	// 100 txs; pooling them across txs eliminates the per-tx
	// map[Hash]struct{} alloc storm visible in alloc profiles
	// (9.3B / 12h = ~13K AddSlot calls per block).
	slotPool []map[types.Hash]struct{}
}

// isStaticWarm reports whether address is a precompile registered through
// SetPrecompiles. Two 8-byte loads and one 2-byte load replace a 20-byte
// key hash for every warm/cold decision on a precompile.
func (al *accessList) isStaticWarm(address *types.Address) bool {
	if len(al.staticWarmBits) == 0 {
		return false
	}
	if binary.LittleEndian.Uint64(address[0:8]) != 0 ||
		binary.LittleEndian.Uint64(address[8:16]) != 0 ||
		address[16] != 0 || address[17] != 0 {
		return false
	}
	bit := uint(binary.BigEndian.Uint16(address[18:20]))
	return al.staticWarm[bit>>6]&(1<<(bit&63)) != 0
}

// SetPrecompiles declares the addresses warm for the transaction being
// prepared. It returns the addresses it could not represent statically; the
// caller must add those through the ordinary journaled path.
func (al *accessList) SetPrecompiles(addrs []types.Address) (fallback []types.Address) {
	for i := range addrs {
		a := &addrs[i]
		if binary.LittleEndian.Uint64(a[0:8]) != 0 ||
			binary.LittleEndian.Uint64(a[8:16]) != 0 ||
			a[16] != 0 || a[17] != 0 {
			fallback = append(fallback, *a)
			continue
		}
		bit := uint16(binary.BigEndian.Uint16(a[18:20]))
		if al.staticWarm[bit>>6]&(1<<(bit&63)) == 0 {
			al.staticWarm[bit>>6] |= 1 << (bit & 63)
			al.staticWarmBits = append(al.staticWarmBits, bit)
		}
	}
	return fallback
}

func (al *accessList) clearStatic() {
	for _, bit := range al.staticWarmBits {
		al.staticWarm[bit>>6] &^= 1 << (bit & 63)
	}
	al.staticWarmBits = al.staticWarmBits[:0]
}

// ContainsAddress returns true if the address is in the access list.
func (al *accessList) ContainsAddress(address types.Address) bool {
	if al.isStaticWarm(&address) {
		return true
	}
	_, ok := al.addresses[address]
	return ok
}

// Contains checks if a slot within an account is present in the access list, returning
// separate flags for the presence of the account and the slot respectively.
func (al *accessList) Contains(address types.Address, slot types.Hash) (addressPresent bool, slotPresent bool) {
	idx, ok := al.addresses[address]
	if !ok {
		// no such address in the map; a precompile is still warm, and a
		// precompile that has never had a slot added has no slots.
		return al.isStaticWarm(&address), false
	}
	if idx == -1 {
		// address yes, but no slots
		return true, false
	}
	_, slotPresent = al.slots[idx][slot]
	return true, slotPresent
}

// newAccessList creates a new accessList.
func newAccessList() *accessList {
	return &accessList{
		addresses: make(map[types.Address]int),
	}
}

// Copy creates an independent copy of an accessList.
func (al *accessList) Copy() *accessList {
	cp := newAccessList()
	cp.staticWarm = al.staticWarm
	cp.staticWarmBits = append([]uint16(nil), al.staticWarmBits...)
	for k, v := range al.addresses {
		cp.addresses[k] = v
	}
	cp.slots = make([]map[types.Hash]struct{}, len(al.slots))
	for i, slotMap := range al.slots {
		newSlotmap := make(map[types.Hash]struct{}, len(slotMap))
		for k := range slotMap {
			newSlotmap[k] = struct{}{}
		}
		cp.slots[i] = newSlotmap
	}
	return cp
}

// AddAddress adds an address to the access list, and returns 'true' if the operation
// caused a change (addr was not previously in the list).
func (al *accessList) AddAddress(address types.Address) bool {
	if al.isStaticWarm(&address) {
		return false
	}
	if _, present := al.addresses[address]; present {
		return false
	}
	al.addresses[address] = -1
	return true
}

// AddSlot adds the specified (addr, slot) combo to the access list.
// Return values are:
// - address added
// - slot added
// For any 'true' value returned, a corresponding journal entry must be made.
func (al *accessList) AddSlot(address types.Address, slot types.Hash) (addrChange bool, slotChange bool) {
	idx, addrPresent := al.addresses[address]
	if !addrPresent || idx == -1 {
		// Address not present, or addr present but no slots there.
		// Reuse a cleared slotmap from the pool when one is available;
		// otherwise allocate. EVM heavy DEX/AMM blocks call AddSlot
		// thousands of times per tx, and a fresh map per call was
		// the second-biggest alloc-rate driver in CPU profiles.
		al.addresses[address] = len(al.slots)
		var slotmap map[types.Hash]struct{}
		if n := len(al.slotPool); n > 0 {
			slotmap = al.slotPool[n-1]
			al.slotPool[n-1] = nil
			al.slotPool = al.slotPool[:n-1]
		} else {
			slotmap = make(map[types.Hash]struct{}, 4)
		}
		slotmap[slot] = struct{}{}
		al.slots = append(al.slots, slotmap)
		return !addrPresent, true
	}
	// There is already an (address,slot) mapping
	slotmap := al.slots[idx]
	if _, ok := slotmap[slot]; !ok {
		slotmap[slot] = struct{}{}
		// Journal add slot change
		return false, true
	}
	// No changes required
	return false, false
}

// Reset clears the access list for reuse on the next transaction.
// Live slotmaps are returned to slotPool so AddSlot can re-bind them
// without going through the allocator. Caller (IntraBlockState.Prepare)
// holds the only reference, so concurrent access isn't a concern.
//
// Pathologically large slotmaps (> slotmapPoolMax) are dropped instead
// of pooled — Go map buckets never shrink on clear(), so retaining a
// 10K-entry bucket array forever would defeat the pool's memory goal.
func (al *accessList) Reset() {
	al.clearStatic()
	clear(al.addresses)
	for _, slotmap := range al.slots {
		if len(slotmap) > slotmapPoolMax {
			continue
		}
		clear(slotmap)
		al.slotPool = append(al.slotPool, slotmap)
	}
	al.slots = al.slots[:0]
}

// slotmapPoolMax bounds the size of slotmaps returned to slotPool.
// Picked from the long tail: typical EVM tx warms < 50 slots; > 256
// is a heavy DEX/AMM tx and we'd rather GC the oversized bucket array
// than retain it across the whole replay.
const slotmapPoolMax = 256

// DeleteSlot removes an (address, slot)-tuple from the access list.
// This operation needs to be performed in the same order as the addition happened.
// This method is meant to be used by the journal, which maintains ordering of
// operations.
func (al *accessList) DeleteSlot(address types.Address, slot types.Hash) {
	idx, addrOk := al.addresses[address]
	if !addrOk {
		panic("reverting slot change, address not present in list")
	}
	slotmap := al.slots[idx]
	delete(slotmap, slot)
	// If that was the last (first) slot, remove it
	// Since additions and rollbacks are always performed in order,
	// we can delete the item without worrying about screwing up later indices
	if len(slotmap) == 0 {
		al.slots = al.slots[:idx]
		al.addresses[address] = -1
	}
}

// DeleteAddress removes an address from the access list. This operation
// needs to be performed in the same order as the addition happened.
// This method is meant to be used by the journal, which maintains ordering of
// operations.
func (al *accessList) DeleteAddress(address types.Address) {
	delete(al.addresses, address)
}
