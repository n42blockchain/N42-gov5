// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// referenceAccessList is the pre-static semantics: every address, precompile
// or not, lives in the map. The static path must be observationally identical
// through ContainsAddress and Contains.
type referenceAccessList struct {
	addrs map[types.Address]map[types.Hash]struct{}
}

func newReferenceAccessList() *referenceAccessList {
	return &referenceAccessList{addrs: map[types.Address]map[types.Hash]struct{}{}}
}
func (r *referenceAccessList) addAddress(a types.Address) {
	if _, ok := r.addrs[a]; !ok {
		r.addrs[a] = map[types.Hash]struct{}{}
	}
}
func (r *referenceAccessList) addSlot(a types.Address, s types.Hash) {
	r.addAddress(a)
	r.addrs[a][s] = struct{}{}
}
func (r *referenceAccessList) contains(a types.Address, s types.Hash) (bool, bool) {
	m, ok := r.addrs[a]
	if !ok {
		return false, false
	}
	_, sp := m[s]
	return true, sp
}

func precompileAddr(n uint16) types.Address {
	var a types.Address
	a[18] = byte(n >> 8)
	a[19] = byte(n)
	return a
}

func mainnetPrecompiles() []types.Address {
	out := make([]types.Address, 0, 20)
	for n := uint16(1); n <= 0x11; n++ {
		out = append(out, precompileAddr(n))
	}
	out = append(out, precompileAddr(0x100), precompileAddr(0x101))
	return out
}

// TestAccessListStaticWarmMatchesReference drives both implementations with
// the same random sequence of address/slot additions — over precompiles, over
// addresses that share a precompile's low bytes but are not precompiles, and
// over random addresses — and checks every query agrees.
func TestAccessListStaticWarmMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	pre := mainnetPrecompiles()
	nearMiss := precompileAddr(0x05)
	nearMiss[0] = 1 // same tail as ecrecover's neighbour, not a precompile

	for round := 0; round < 200; round++ {
		al := newAccessList()
		ref := newReferenceAccessList()
		if rest := al.SetPrecompiles(pre); len(rest) != 0 {
			t.Fatalf("mainnet precompiles should all be representable statically, got %d fallbacks", len(rest))
		}
		for _, p := range pre {
			ref.addAddress(p)
		}
		pool := append([]types.Address{nearMiss}, pre[:5]...)
		for i := 0; i < 8; i++ {
			var a types.Address
			rng.Read(a[:])
			pool = append(pool, a)
		}
		for step := 0; step < 60; step++ {
			a := pool[rng.Intn(len(pool))]
			var s types.Hash
			s[31] = byte(rng.Intn(4))
			switch rng.Intn(3) {
			case 0:
				al.AddAddress(a)
				ref.addAddress(a)
			case 1:
				al.AddSlot(a, s)
				ref.addSlot(a, s)
			}
			for _, q := range pool {
				if got, want := al.ContainsAddress(q), func() bool { ok, _ := ref.contains(q, s); return ok }(); got != want {
					t.Fatalf("round %d step %d: ContainsAddress(%x) = %v want %v", round, step, q, got, want)
				}
				ga, gs := al.Contains(q, s)
				wa, ws := ref.contains(q, s)
				if ga != wa || gs != ws {
					t.Fatalf("round %d step %d: Contains(%x, %x) = (%v,%v) want (%v,%v)", round, step, q, s, ga, gs, wa, ws)
				}
			}
		}
	}
}

// TestAccessListStaticWarmAddAddressReportsNoChange: adding a precompile must
// not report a change, because a change would produce a journal entry whose
// revert would delete a map entry that does not exist.
func TestAccessListStaticWarmAddAddressReportsNoChange(t *testing.T) {
	al := newAccessList()
	al.SetPrecompiles(mainnetPrecompiles())
	if al.AddAddress(precompileAddr(1)) {
		t.Fatal("AddAddress on a static-warm precompile reported a change")
	}
	if !al.ContainsAddress(precompileAddr(1)) {
		t.Fatal("precompile not warm")
	}
	if al.ContainsAddress(precompileAddr(0x12)) {
		t.Fatal("0x12 is not a mainnet precompile and must be cold")
	}
}

// TestAccessListStaticWarmSlotRevert: EIP-2930 lists may attach storage keys
// to a precompile address. That takes the map path; reverting it must leave
// the precompile warm.
func TestAccessListStaticWarmSlotRevert(t *testing.T) {
	al := newAccessList()
	al.SetPrecompiles(mainnetPrecompiles())
	p := precompileAddr(2)
	slot := types.Hash{7}
	addrChange, slotChange := al.AddSlot(p, slot)
	if !addrChange || !slotChange {
		t.Fatalf("AddSlot on precompile: got (%v,%v), want (true,true) — map path", addrChange, slotChange)
	}
	if a, s := al.Contains(p, slot); !a || !s {
		t.Fatal("slot not present after AddSlot")
	}
	// Journal order: slot first, then address.
	al.DeleteSlot(p, slot)
	al.DeleteAddress(p)
	if a, s := al.Contains(p, slot); !a || s {
		t.Fatalf("after revert: Contains = (%v,%v), want (true,false)", a, s)
	}
	if !al.ContainsAddress(p) {
		t.Fatal("precompile went cold after reverting its slot")
	}
}

func TestAccessListStaticWarmResetAndCopy(t *testing.T) {
	al := newAccessList()
	al.SetPrecompiles(mainnetPrecompiles())
	cp := al.Copy()
	if !cp.ContainsAddress(precompileAddr(9)) {
		t.Fatal("Copy lost the static set")
	}
	al.Reset()
	if al.ContainsAddress(precompileAddr(9)) {
		t.Fatal("Reset did not clear the static set")
	}
	if !cp.ContainsAddress(precompileAddr(9)) {
		t.Fatal("Reset on the original affected the copy")
	}
}

func TestAccessListStaticWarmFallback(t *testing.T) {
	al := newAccessList()
	var odd types.Address
	odd[0] = 0xff
	rest := al.SetPrecompiles([]types.Address{precompileAddr(1), odd})
	if len(rest) != 1 || rest[0] != odd {
		t.Fatalf("expected the non-canonical address to fall back, got %v", rest)
	}
	if al.ContainsAddress(odd) {
		t.Fatal("fallback address must not be warm until the caller adds it")
	}
}

func BenchmarkAccessListContainsPrecompile(b *testing.B) {
	al := newAccessList()
	al.SetPrecompiles(mainnetPrecompiles())
	p := precompileAddr(1)
	for i := 0; i < b.N; i++ {
		if !al.ContainsAddress(p) {
			b.Fatal("cold")
		}
	}
}
