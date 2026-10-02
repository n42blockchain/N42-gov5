// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers LtHashAwareRootComputer, which was left at 0%: construction, RootScheme
// delegation to the wrapped JMT computer, the backward-compatible ComputeRoot
// path (JMT only, no LtHash update), and ComputeRootWithOriginals which updates
// both the JMT root and the LtHash digest together. LtHash is a homomorphic
// digest, so it must be order-independent: applying the same set of account
// changes through different per-block groupings must converge on the same
// digest once the same originals are known.

package commitment

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state"
)

func ltAddr(b byte) types.Address {
	var a types.Address
	a[19] = b
	return a
}
func ltAcct(nonce, bal uint64) *account.StateAccount {
	a := &account.StateAccount{Initialised: true, Nonce: nonce}
	a.Balance.SetUint64(bal)
	return a
}
func newLtHashAwareComputer() *LtHashAwareRootComputer {
	jmtRC := NewJMTRootComputer(NewJMTCommitment(jmt.New(jmt.NewMemStore())))
	lt := NewLtHashCommitment(nil)
	return NewLtHashAwareRootComputer(jmtRC, lt)
}

func TestLtHashAwareRootComputerRootScheme(t *testing.T) {
	r := newLtHashAwareComputer()
	if got := r.RootScheme(); got != state.RootSchemeJMTBlake3 {
		t.Fatalf("RootScheme() = %v, want %v", got, state.RootSchemeJMTBlake3)
	}
	// nil-safety: a nil receiver or a nil-wrapped jmt must report Unknown
	// rather than panicking.
	var nilR *LtHashAwareRootComputer
	if got := nilR.RootScheme(); got != state.RootSchemeUnknown {
		t.Fatalf("nil receiver RootScheme() = %v, want RootSchemeUnknown", got)
	}
	empty := &LtHashAwareRootComputer{}
	if got := empty.RootScheme(); got != state.RootSchemeUnknown {
		t.Fatalf("nil-jmt RootScheme() = %v, want RootSchemeUnknown", got)
	}
}

func TestLtHashAwareRootComputerComputeRootBackwardCompat(t *testing.T) {
	r := newLtHashAwareComputer()
	emptyLtRoot := r.LtHashCommitment().Root()

	accts := map[types.Address]*account.StateAccount{ltAddr(1): ltAcct(1, 100)}
	root, err := r.ComputeRoot(accts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if root == (types.Hash{}) {
		t.Fatal("expected a non-empty JMT root")
	}
	// LtHash must NOT be updated through this path: the digest of the empty
	// lattice is unchanged even though the JMT root moved.
	if r.LtHashCommitment().Root() != emptyLtRoot {
		t.Fatal("ComputeRoot must not touch the LtHash digest")
	}
}

func TestLtHashAwareRootComputerWithOriginals(t *testing.T) {
	r := newLtHashAwareComputer()
	slot := types.Hash{31: 9}

	accts := map[types.Address]*account.StateAccount{ltAddr(1): ltAcct(1, 100)}
	stor := map[types.Address]map[types.Hash]*uint256.Int{ltAddr(1): {slot: uint256.NewInt(42)}}

	jmtRoot, ltRoot, err := r.ComputeRootWithOriginals(accts, nil, stor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if jmtRoot == (types.Hash{}) {
		t.Fatal("expected non-empty JMT root")
	}
	if ltRoot == (types.Hash{}) {
		t.Fatal("expected non-empty LtHash root after an update")
	}
	if ltRoot != r.LtHashCommitment().Root() {
		t.Fatal("returned LtHash root must match LtHashCommitment().Root()")
	}

	// Order independence: applying account+storage updates for two addresses
	// as one combined block must equal applying them as two sequential blocks
	// (with correct originals threaded through), since LtHash is an additive
	// homomorphic digest over the *live* key set.
	build := func(grouped bool) types.Hash {
		jmtRC := NewJMTRootComputer(NewJMTCommitment(jmt.New(jmt.NewMemStore())))
		lt := NewLtHashCommitment(nil)
		rc := NewLtHashAwareRootComputer(jmtRC, lt)

		a1, a2 := ltAddr(10), ltAddr(20)
		acct1, acct2 := ltAcct(1, 500), ltAcct(2, 700)

		if grouped {
			_, _, err := rc.ComputeRootWithOriginals(
				map[types.Address]*account.StateAccount{a1: acct1, a2: acct2},
				nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			_, _, err := rc.ComputeRootWithOriginals(
				map[types.Address]*account.StateAccount{a1: acct1}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = rc.ComputeRootWithOriginals(
				map[types.Address]*account.StateAccount{a2: acct2}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		return rc.LtHashCommitment().Root()
	}
	if build(true) != build(false) {
		t.Fatal("LtHash digest must be order-independent over the same live set")
	}
}
