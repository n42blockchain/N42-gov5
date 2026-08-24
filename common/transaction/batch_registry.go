// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Account-index registry for batch transactions (Feature B item 2). Addresses
// registered on-chain get a compact monotonic index; a batch can then reference a
// recipient/sender by its index (a 1–4 byte varint) instead of the 20-byte
// address, resolved from registry state at decode/expand time. This is the lever
// that takes the all-distinct-recipient case toward the ~12 B/tx target (the
// intra-batch dictionary in encodeToColumn only helps when addresses REPEAT
// within one batch; the registry helps even when they are all distinct, as long
// as they are registered).
//
// AddrRegistry is the resolver interface the codec needs (both directions). The
// production implementation is an on-chain registry contract (same shape as
// contracts/pqregistry); MapAddrRegistry is the in-memory PoC/test impl.

package transaction

import "github.com/n42blockchain/N42/common/types"

// AddrRegistry resolves between addresses and their compact on-chain indices.
type AddrRegistry interface {
	// IndexOf returns the registry index of addr, or ok=false if unregistered.
	IndexOf(addr types.Address) (uint64, bool)
	// AddrAt returns the address at a registry index, or ok=false if unknown.
	AddrAt(idx uint64) (types.Address, bool)
}

// MapAddrRegistry is an in-memory AddrRegistry (PoC/tests). Indices are 1-based;
// 0 is reserved by the codec for "nil / contract creation".
type MapAddrRegistry struct {
	byAddr map[types.Address]uint64
	byIdx  []types.Address // byIdx[i] is index i+1
}

// NewMapAddrRegistry creates an empty registry.
func NewMapAddrRegistry() *MapAddrRegistry {
	return &MapAddrRegistry{byAddr: make(map[types.Address]uint64)}
}

// Register adds addr if absent and returns its (1-based) index.
func (r *MapAddrRegistry) Register(addr types.Address) uint64 {
	if i, ok := r.byAddr[addr]; ok {
		return i
	}
	r.byIdx = append(r.byIdx, addr)
	idx := uint64(len(r.byIdx)) // 1-based
	r.byAddr[addr] = idx
	return idx
}

func (r *MapAddrRegistry) IndexOf(addr types.Address) (uint64, bool) {
	i, ok := r.byAddr[addr]
	return i, ok
}

func (r *MapAddrRegistry) AddrAt(idx uint64) (types.Address, bool) {
	if idx == 0 || idx > uint64(len(r.byIdx)) {
		return types.Address{}, false
	}
	return r.byIdx[idx-1], true
}

// allRegistered reports whether every non-nil address in addrs is registered —
// the precondition for using registry-index mode for the column.
func allRegistered(addrs []*types.Address, reg AddrRegistry) bool {
	if reg == nil {
		return false
	}
	for _, a := range addrs {
		if a == nil {
			continue
		}
		if _, ok := reg.IndexOf(*a); !ok {
			return false
		}
	}
	return true
}
