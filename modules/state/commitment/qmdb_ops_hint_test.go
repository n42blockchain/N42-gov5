// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// hintCase is one block for the hint tests: the dirty set the root is
// handed and the finals the run's hint carries.
type hintCase struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash]*uint256.Int
	finals   []HintAccount
}

func randAddr(rnd *rand.Rand) types.Address {
	var a types.Address
	rnd.Read(a[:])
	return a
}

// laneShapedCase builds a lane-shaped block: senders (nonce > 0, some
// emptied to a zero balance) and recipients (some credited zero), all in
// the hint, plus the block's extras the hint lacks (fee recipient, system
// accounts, a deleted account, an empty account, storage), entries whose
// dirty-set value differs from the hint's, hint entries the dirty set
// omits (the unchanged-account rule) and an empty account in the hint.
func laneShapedCase(seed int64, senders, recips int) hintCase {
	rnd := rand.New(rand.NewSource(seed))
	c := hintCase{accounts: make(map[types.Address]*account.StateAccount, senders+recips+16),
		storage: make(map[types.Address]map[types.Hash]*uint256.Int)}
	add := func(a types.Address, acc account.StateAccount, inHint, inSet bool) {
		if inHint {
			c.finals = append(c.finals, HintAccount{Addr: a, Acct: acc})
		}
		if inSet {
			cp := acc
			c.accounts[a] = &cp
		}
	}
	for i := 0; i < senders; i++ {
		acc := account.StateAccount{Initialised: true, Nonce: uint64(1 + rnd.Intn(60)), CodeHash: emptyCodeHashForTest}
		if rnd.Intn(10) != 0 {
			acc.Balance.SetUint64(rnd.Uint64())
		}
		add(randAddr(rnd), acc, true, true)
	}
	for i := 0; i < recips; i++ {
		acc := account.StateAccount{Initialised: true, CodeHash: emptyCodeHashForTest}
		if rnd.Intn(8) != 0 {
			acc.Balance.SetUint64(rnd.Uint64() >> 4)
		}
		switch rnd.Intn(40) {
		case 0: // the dirty set omits it (unchanged-account rule)
			add(randAddr(rnd), acc, true, false)
		case 1: // the dirty set holds another value
			a := randAddr(rnd)
			add(a, acc, true, false)
			other := acc
			other.Balance.AddUint64(&other.Balance, 1)
			add(a, other, false, true)
		case 2: // empty in both (a delete op)
			add(randAddr(rnd), account.StateAccount{}, true, true)
		default:
			add(randAddr(rnd), acc, true, true)
		}
	}
	// Extras the hint does not know.
	for i := 0; i < 6; i++ {
		acc := account.StateAccount{Initialised: true, Nonce: uint64(i)}
		acc.Balance.SetUint64(uint64(rnd.Int63()))
		add(randAddr(rnd), acc, false, true)
	}
	c.accounts[randAddr(rnd)] = nil                     // deleted
	c.accounts[randAddr(rnd)] = &account.StateAccount{} // empty: delete op
	sa := randAddr(rnd)
	c.storage[sa] = map[types.Hash]*uint256.Int{}
	for i := 0; i < 5; i++ {
		var k types.Hash
		rnd.Read(k[:])
		if i == 0 {
			c.storage[sa][k] = new(uint256.Int)
		} else {
			c.storage[sa][k] = uint256.NewInt(rnd.Uint64())
		}
	}
	rnd.Shuffle(len(c.finals), func(i, j int) { c.finals[i], c.finals[j] = c.finals[j], c.finals[i] })
	return c
}

var emptyCodeHashForTest = types.Hash{0xc5, 0xd2, 0x46, 0x01}

func (c *hintCase) hint() *AccountOpsHint {
	h := NewAccountOpsHint()
	if h.Caches() {
		h.Build(c.finals)
	} else {
		h.MarkBuilt()
	}
	return h
}

// TestRootOpsHintOpsEqual: the hinted op build is today's, element by
// element, on many lane-shaped blocks (big enough for the parallel paths
// and small).
func TestRootOpsHintOpsEqual(t *testing.T) {
	defer SetRootOpsFromRunForTest("on")()
	for seed := int64(1); seed <= 24; seed++ {
		senders, recips := 50+int(seed)*7, 300+int(seed)*13
		if seed%4 == 0 {
			senders, recips = 2000, 6000
		}
		c := laneShapedCase(seed, senders, recips)
		h := c.hint()
		got, st := buildRootOps(c.accounts, c.storage, h)
		want := buildRootOpsToday(c.accounts, c.storage)
		if d := diffOps(want, got); d != "" {
			t.Fatalf("seed %d: %s", seed, d)
		}
		if !st.used || st.hits == 0 || st.misses < 8 || st.unused == 0 {
			t.Fatalf("seed %d: stats %+v", seed, st)
		}
	}
}

// TestRootOpsHintNoExtras: a dirty set that is exactly the hint takes the
// presorted copy.
func TestRootOpsHintNoExtras(t *testing.T) {
	defer SetRootOpsFromRunForTest("on")()
	rnd := rand.New(rand.NewSource(7))
	var finals []HintAccount
	accounts := map[types.Address]*account.StateAccount{}
	for i := 0; i < 5000; i++ {
		a := randAddr(rnd)
		acc := account.StateAccount{Initialised: true, Nonce: uint64(i + 1)}
		acc.Balance.SetUint64(rnd.Uint64())
		finals = append(finals, HintAccount{Addr: a, Acct: acc})
		cp := acc
		accounts[a] = &cp
	}
	h := NewAccountOpsHint()
	h.Build(finals)
	got, st := buildRootOps(accounts, nil, h)
	if d := diffOps(buildRootOpsToday(accounts, nil), got); d != "" {
		t.Fatal(d)
	}
	if st.hits != 5000 || st.misses != 0 || st.unused != 0 {
		t.Fatalf("stats %+v", st)
	}
}

// TestRootOpsHintFallbacks: a hint that is not ready, repeats an address,
// or was made with the switch off is ignored; the ops are today's.
func TestRootOpsHintFallbacks(t *testing.T) {
	c := laneShapedCase(3, 100, 400)
	want := buildRootOpsToday(c.accounts, c.storage)
	func() {
		defer SetRootOpsFromRunForTest("on")()
		h := NewAccountOpsHint() // never built
		got, st := buildRootOps(c.accounts, c.storage, h)
		if st.used || st.why != "late" || diffOps(want, got) != "" {
			t.Fatalf("late: %+v", st)
		}
		h2 := NewAccountOpsHint()
		h2.Build(append(append([]HintAccount(nil), c.finals...), c.finals[0]))
		got, st = buildRootOps(c.accounts, c.storage, h2)
		if st.used || st.why == "" || diffOps(want, got) != "" {
			t.Fatalf("dup: %+v", st)
		}
	}()
	h := NewAccountOpsHint() // switch off: no cache
	h.MarkBuilt()
	defer SetRootOpsFromRunForTest("on")()
	got, st := buildRootOps(c.accounts, c.storage, h)
	if st.used || st.why != "off" || diffOps(want, got) != "" {
		t.Fatalf("off: %+v", st)
	}
}

// TestRootOpsHintCheckCatches: check mode reports a hint whose op does not
// encode its account (a panic in test binaries) and returns today's ops.
func TestRootOpsHintCheckCatches(t *testing.T) {
	defer SetRootOpsFromRunForTest("check")()
	c := laneShapedCase(5, 100, 400)
	h := c.hint()
	for i := range h.ops {
		if len(h.ops[i].Value) > 0 {
			h.ops[i].Value = append([]byte(nil), h.ops[i].Value...)
			h.ops[i].Value[len(h.ops[i].Value)-1] ^= 1
			break
		}
	}
	_, _, m0, _, _ := RootOpsStats()
	defer func() {
		if recover() == nil {
			t.Fatal("check mode did not report the corrupted hint")
		}
		if _, _, m1, _, _ := RootOpsStats(); m1 != m0+1 {
			t.Fatalf("mismatches %d -> %d", m0, m1)
		}
	}()
	buildRootOps(c.accounts, c.storage, h)
}

// TestRootOpsHintedRootsEqual: over a chain of lane-shaped blocks on a tree
// with undo recording, every switch combination gives today's root and
// undo record block by block.
func TestRootOpsHintedRootsEqual(t *testing.T) {
	for _, workers := range []int{0, 16} {
		for _, fr := range []string{"off", "on", "check"} {
			for _, ul := range []string{"off", "on", "check"} {
				for _, hr := range []string{"off", "on", "check"} {
					t.Run(fmt.Sprintf("shard%d/fromrun-%s/unlocked-%s/headroom-%s", workers, fr, ul, hr), func(t *testing.T) {
						defer SetQMDBApplyHeadroomForTest(hr)()
						prevW := qmdb.ShardApplyWorkers
						qmdb.ShardApplyWorkers = workers
						defer func() { qmdb.ShardApplyWorkers = prevW }()
						restore := SetRootOpsFromRunForTest(fr)
						defer restore()
						defer SetRootOpsUnlockedForTest(ul)()
						ref, hinted := NewQMDBRootComputer(), NewQMDBRootComputer()
						ref.EnableUndoRecording()
						hinted.EnableUndoRecording()
						for blk := int64(0); blk < 4; blk++ {
							c := laneShapedCase(100+blk, 700, 2100)
							// Later blocks rewrite earlier keys (updates, not
							// only inserts).
							if blk > 0 {
								prev := laneShapedCase(100+blk-1, 700, 2100)
								for i, f := range prev.finals {
									if i%3 != 0 {
										continue
									}
									acc := f.Acct
									acc.Nonce += 100
									c.finals = append(c.finals, HintAccount{Addr: f.Addr, Acct: acc})
									cp := acc
									c.accounts[f.Addr] = &cp
								}
							}
							want, err := ref.ComputeRoot(c.accounts, c.storage)
							if err != nil {
								t.Fatal(err)
							}
							var h *AccountOpsHint
							if RootOpsHintWanted() {
								h = c.hint()
							}
							var got types.Hash
							if h != nil {
								got, err = hinted.ComputeRootHinted(c.accounts, c.storage, h)
							} else {
								got, err = hinted.ComputeRoot(c.accounts, c.storage)
							}
							if err != nil {
								t.Fatal(err)
							}
							if got != want {
								t.Fatalf("block %d: root %x, today %x", blk, got, want)
							}
							if !reflect.DeepEqual(ref.LastUndo(), hinted.LastUndo()) {
								t.Fatalf("block %d: undo records differ", blk)
							}
							p := LastComputeRootPhases()
							if p.Hinted != (h != nil) || (fr != "off" && !p.HintUsed) {
								t.Fatalf("block %d: phases %+v", blk, *p)
							}
						}
					})
				}
			}
		}
	}
}
