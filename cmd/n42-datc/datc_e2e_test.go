// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// datc_e2e_test.go — synthetic end-to-end correctness harness for the DATC
// record layer.
//
// A deterministic generator produces forward per-block changesets (account
// create/modify/delete, storage writes/deletes, SELFDESTRUCT with and without
// explicit wipe rows, same-block destroy+recreate, ghost storage for absent
// accounts). An INDEPENDENT reference MPT (plain recursive RLP hasher, no
// HashBuilder/GenStructStep) produces the expected root at every height. The
// build runs in fwd mode over those changesets (gold-checked block by block
// against the reference), then the querier reconstructs the root at EVERY
// height from the DATC records alone, and EIP-1186 proofs for sampled keys
// are walked against the reference roots.

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// ---------------------------------------------------------------------------
// independent reference MPT (recursive, sorted-leaf, inline-aware)

func rStr(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return []byte{b[0]}
	}
	if len(b) < 56 {
		return append([]byte{0x80 + byte(len(b))}, b...)
	}
	var lb []byte
	for v := len(b); v > 0; v >>= 8 {
		lb = append([]byte{byte(v)}, lb...)
	}
	return append(append([]byte{0xb7 + byte(len(lb))}, lb...), b...)
}

func rList(items ...[]byte) []byte {
	var p []byte
	for _, it := range items {
		p = append(p, it...)
	}
	if len(p) < 56 {
		return append([]byte{0xc0 + byte(len(p))}, p...)
	}
	var lb []byte
	for v := len(p); v > 0; v >>= 8 {
		lb = append([]byte{byte(v)}, lb...)
	}
	return append(append([]byte{0xf7 + byte(len(lb))}, lb...), p...)
}

func rHP(nib []byte, leaf bool) []byte {
	f := byte(0)
	if leaf {
		f = 2
	}
	if len(nib)%2 == 1 {
		out := []byte{(f+1)<<4 | nib[0]}
		for i := 1; i+1 < len(nib); i += 2 {
			out = append(out, nib[i]<<4|nib[i+1])
		}
		return out
	}
	out := []byte{f << 4}
	for i := 0; i+1 < len(nib); i += 2 {
		out = append(out, nib[i]<<4|nib[i+1])
	}
	return out
}

type rkv struct{ nib, item []byte }

func rRef(node []byte) []byte {
	if len(node) < 32 {
		return node
	}
	h := keccak(node)
	return rStr(h[:])
}

// rNode returns the RLP of the trie over kvs (sorted by nib, all sharing
// nib[:depth], non-empty).
func rNode(kvs []rkv, depth int) []byte {
	if len(kvs) == 1 {
		return rList(rStr(rHP(kvs[0].nib[depth:], true)), kvs[0].item)
	}
	lcp := depth
	for lcp < len(kvs[0].nib) {
		c := kvs[0].nib[lcp]
		ok := true
		for _, e := range kvs[1:] {
			if e.nib[lcp] != c {
				ok = false
				break
			}
		}
		if !ok {
			break
		}
		lcp++
	}
	if lcp > depth {
		return rList(rStr(rHP(kvs[0].nib[depth:lcp], false)), rRef(rNode(kvs, lcp)))
	}
	items := make([][]byte, 17)
	for i := range items {
		items[i] = []byte{0x80}
	}
	i := 0
	for i < len(kvs) {
		j := i
		for j < len(kvs) && kvs[j].nib[depth] == kvs[i].nib[depth] {
			j++
		}
		items[kvs[i].nib[depth]] = rRef(rNode(kvs[i:j], depth+1))
		i = j
	}
	return rList(items...)
}

func rRoot(kvs []rkv) types.Hash {
	if len(kvs) == 0 {
		return emptyTrieRoot
	}
	sort.Slice(kvs, func(i, j int) bool { return bytes.Compare(kvs[i].nib, kvs[j].nib) < 0 })
	return keccak(rNode(kvs, 0))
}

// ---------------------------------------------------------------------------
// reference world + generator

type refWorld struct {
	acc map[types.Address]account.StateAccount
	sto map[types.Address]map[types.Hash][]byte // trimmed non-zero slot bytes
}

func newRefWorld() *refWorld {
	return &refWorld{acc: map[types.Address]account.StateAccount{}, sto: map[types.Address]map[types.Hash][]byte{}}
}

func (w *refWorld) storageRoot(addr types.Address) types.Hash {
	m := w.sto[addr]
	if len(m) == 0 {
		return emptyTrieRoot
	}
	kvs := make([]rkv, 0, len(m))
	for slot, v := range m {
		sh := keccak(slot[:])
		kvs = append(kvs, rkv{nib: nibblesOfBytes(sh[:]), item: rStr(rStr(v))})
	}
	return rRoot(kvs)
}

func (w *refWorld) stateRoot() types.Hash {
	kvs := make([]rkv, 0, len(w.acc))
	for addr, a := range w.acc {
		a.Root = w.storageRoot(addr)
		buf := make([]byte, a.EncodingLengthForHashing())
		a.EncodeForHashing(buf)
		ah := keccak(addr[:])
		kvs = append(kvs, rkv{nib: nibblesOfBytes(ah[:]), item: rStr(buf)})
	}
	return rRoot(kvs)
}

// genBlock is one block's forward delta. Order of application: wipes (deleted
// accounts lose all storage), then accs, then slots.
type genBlock struct {
	accs  map[types.Address]*account.StateAccount // nil = delete
	slots map[types.Address]map[types.Hash][]byte // nil = delete
	// explicitWipes: for deleted accounts, whether the storage blob carries
	// the per-slot wipe rows (the n42 convert path does; the mainnet path
	// relies on the builder's own pre-state enumeration).
	explicitWipes map[types.Address]bool
	wipedSlots    map[types.Address][]types.Hash // slots live before the wipe
}

func (w *refWorld) apply(gb *genBlock) {
	for addr, a := range gb.accs {
		if a == nil {
			delete(w.acc, addr)
			delete(w.sto, addr)
			continue
		}
		if _, wasLive := w.acc[addr]; !wasLive {
			delete(w.sto, addr) // (re)creation: storage starts empty
		}
		if _, recreate := gb.wipedSlots[addr]; recreate {
			delete(w.sto, addr) // same-block destroy+recreate
		}
		w.acc[addr] = *a
	}
	for addr, m := range gb.slots {
		if _, live := w.acc[addr]; !live {
			continue // ghost storage of an absent account
		}
		inner := w.sto[addr]
		if inner == nil {
			inner = map[types.Hash][]byte{}
			w.sto[addr] = inner
		}
		for slot, v := range m {
			if v == nil {
				delete(inner, slot)
			} else {
				inner[slot] = v
			}
		}
	}
}

func trimBE(v *uint256.Int) []byte {
	b := v.Bytes32()
	i := 0
	for i < 31 && b[i] == 0 {
		i++
	}
	return append([]byte{}, b[i:]...)
}

type genCfg struct {
	blocks   int
	eoas     int
	bigN     int
	bigSlots int
	smallN   int
	smSlots  int
	seed     int64
}

type scenario struct {
	blocks []*genBlock
	roots  []types.Hash
	eoas   []types.Address
	big    []types.Address
	small  []types.Address
	absent types.Address
}

func generate(cfg genCfg) *scenario {
	rng := rand.New(rand.NewSource(cfg.seed))
	randAddr := func() types.Address {
		var a types.Address
		rng.Read(a[:])
		return a
	}
	randHash := func() types.Hash {
		var h types.Hash
		rng.Read(h[:])
		return h
	}
	sc := &scenario{absent: randAddr()}
	for i := 0; i < cfg.eoas; i++ {
		sc.eoas = append(sc.eoas, randAddr())
	}
	bigUniverse := map[types.Address][]types.Hash{}
	for i := 0; i < cfg.bigN; i++ {
		a := randAddr()
		sc.big = append(sc.big, a)
		for j := 0; j < cfg.bigSlots; j++ {
			bigUniverse[a] = append(bigUniverse[a], randHash())
		}
	}
	smallUniverse := map[types.Address][]types.Hash{}
	for i := 0; i < cfg.smallN; i++ {
		a := randAddr()
		sc.small = append(sc.small, a)
		for j := 0; j < cfg.smSlots; j++ {
			smallUniverse[a] = append(smallUniverse[a], randHash())
		}
	}
	world := newRefWorld()
	eoa := func(nonce uint64) *account.StateAccount {
		a := account.NewAccount()
		a.Nonce = nonce
		a.Balance.SetUint64(uint64(rng.Intn(1<<40)) + 1)
		return &a
	}
	contract := func(nonce uint64) *account.StateAccount {
		a := account.NewAccount()
		a.Nonce = nonce
		a.Balance.SetUint64(uint64(rng.Intn(1 << 30)))
		ch := randHash()
		a.CodeHash = ch
		return &a
	}
	randVal := func() []byte {
		v := uint256.NewInt(0)
		switch rng.Intn(4) {
		case 0:
			v.SetUint64(uint64(rng.Intn(200)) + 1) // 1-byte values (inline-prone)
		case 1:
			v.SetUint64(uint64(rng.Int63()) + 1)
		default:
			var h types.Hash
			rng.Read(h[:])
			v.SetBytes(h[:])
			if v.IsZero() {
				v.SetUint64(7)
			}
		}
		return trimBE(v)
	}
	newBlock := func() *genBlock {
		return &genBlock{accs: map[types.Address]*account.StateAccount{}, slots: map[types.Address]map[types.Hash][]byte{},
			explicitWipes: map[types.Address]bool{}, wipedSlots: map[types.Address][]types.Hash{}}
	}
	setSlot := func(gb *genBlock, a types.Address, s types.Hash, v []byte) {
		m := gb.slots[a]
		if m == nil {
			m = map[types.Hash][]byte{}
			gb.slots[a] = m
		}
		m[s] = v
	}
	liveSlots := func(a types.Address) []types.Hash {
		var out []types.Hash
		for s := range world.sto[a] {
			out = append(out, s)
		}
		sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
		return out
	}

	// Genesis.
	g := newBlock()
	for _, a := range sc.eoas {
		g.accs[a] = eoa(0)
	}
	for _, a := range sc.big {
		g.accs[a] = contract(1)
		for _, s := range bigUniverse[a] {
			setSlot(g, a, s, randVal())
		}
	}
	for i, a := range sc.small {
		if i%3 == 2 {
			continue // a third of the small contracts are created later
		}
		g.accs[a] = contract(1)
		for _, s := range smallUniverse[a] {
			if rng.Intn(2) == 0 {
				setSlot(g, a, s, randVal())
			}
		}
	}
	sc.blocks = append(sc.blocks, g)
	world.apply(g)
	sc.roots = append(sc.roots, world.stateRoot())

	for n := 1; n < cfg.blocks; n++ {
		gb := newBlock()
		// EOA churn.
		for i := 0; i < 40; i++ {
			a := sc.eoas[rng.Intn(len(sc.eoas))]
			cur, live := world.acc[a]
			switch {
			case live && rng.Intn(50) == 0:
				gb.accs[a] = nil
			case live:
				gb.accs[a] = eoa(cur.Nonce + 1)
			default:
				gb.accs[a] = eoa(0)
			}
		}
		// Big contracts: storage-only churn, occasionally account too.
		for _, a := range sc.big {
			for i := 0; i < 25; i++ {
				s := bigUniverse[a][rng.Intn(len(bigUniverse[a]))]
				if rng.Intn(5) == 0 {
					setSlot(gb, a, s, nil)
				} else {
					setSlot(gb, a, s, randVal())
				}
			}
			if rng.Intn(4) == 0 {
				cur := world.acc[a]
				c := contract(cur.Nonce + 1)
				c.CodeHash = cur.CodeHash
				gb.accs[a] = c
			}
		}
		// Small contracts.
		for i := 0; i < 4; i++ {
			a := sc.small[rng.Intn(len(sc.small))]
			if _, live := world.acc[a]; !live {
				continue
			}
			for j := 0; j < 3; j++ {
				s := smallUniverse[a][rng.Intn(len(smallUniverse[a]))]
				if rng.Intn(4) == 0 {
					setSlot(gb, a, s, nil)
				} else {
					setSlot(gb, a, s, randVal())
				}
			}
		}
		// SELFDESTRUCT (with or without explicit wipe rows).
		if rng.Intn(12) == 0 {
			a := sc.small[rng.Intn(len(sc.small))]
			if _, live := world.acc[a]; live {
				if _, touched := gb.slots[a]; !touched {
					gb.accs[a] = nil
					gb.wipedSlots[a] = liveSlots(a)
					gb.explicitWipes[a] = rng.Intn(2) == 0
				}
			}
		}
		// Recreate / create.
		if rng.Intn(10) == 0 {
			a := sc.small[rng.Intn(len(sc.small))]
			if _, live := world.acc[a]; !live {
				if _, del := gb.accs[a]; !del {
					gb.accs[a] = contract(1)
					for j := 0; j < 5; j++ {
						setSlot(gb, a, smallUniverse[a][rng.Intn(len(smallUniverse[a]))], randVal())
					}
				}
			}
		}
		// Same-block destroy + recreate: new account, old slots wiped unless
		// rewritten (explicit wipe rows are mandatory here — the pre-state
		// enumeration cannot see a same-block recreate).
		if rng.Intn(30) == 0 {
			a := sc.small[rng.Intn(len(sc.small))]
			if _, live := world.acc[a]; live {
				if _, touched := gb.accs[a]; !touched {
					if _, st := gb.slots[a]; !st {
						gb.accs[a] = contract(1)
						gb.wipedSlots[a] = liveSlots(a)
						gb.explicitWipes[a] = true
						for j := 0; j < 3; j++ {
							setSlot(gb, a, smallUniverse[a][rng.Intn(len(smallUniverse[a]))], randVal())
						}
					}
				}
			}
		}
		// Ghost storage: writes for an address that never exists.
		if rng.Intn(15) == 0 {
			setSlot(gb, sc.absent, randHash(), randVal())
		}
		sc.blocks = append(sc.blocks, gb)
		world.apply(gb)
		sc.roots = append(sc.roots, world.stateRoot())
	}
	return sc
}

// fwdBlobs encodes one genBlock into the FwdAcctCS / FwdStorCS blob format.
func fwdBlobs(gb *genBlock) (acc, sto []byte) {
	addrs := make([]types.Address, 0, len(gb.accs))
	for a := range gb.accs {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return bytes.Compare(addrs[i][:], addrs[j][:]) < 0 })
	for _, a := range addrs {
		acc = append(acc, a[:]...)
		if v := gb.accs[a]; v == nil {
			acc = binary.AppendUvarint(acc, 0)
		} else {
			enc := v.MarshalV2()
			acc = binary.AppendUvarint(acc, uint64(len(enc)))
			acc = append(acc, enc...)
		}
	}
	type row struct {
		k []byte
		v []byte
	}
	var rows []row
	for a, slots := range gb.wipedSlots {
		if !gb.explicitWipes[a] {
			continue
		}
		for _, s := range slots {
			if m := gb.slots[a]; m != nil {
				if _, rewritten := m[s]; rewritten {
					continue // net value comes from the write below
				}
			}
			rows = append(rows, row{k: append(append([]byte{}, a[:]...), s[:]...)})
		}
	}
	for a, m := range gb.slots {
		for s, v := range m {
			rows = append(rows, row{k: append(append([]byte{}, a[:]...), s[:]...), v: v})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return bytes.Compare(rows[i].k, rows[j].k) < 0 })
	for _, r := range rows {
		sto = append(sto, r.k...)
		sto = binary.AppendUvarint(sto, uint64(len(r.v)))
		sto = append(sto, r.v...)
	}
	return acc, sto
}

// valueAt replays the deltas to give the reference account / storage value at
// height n (nil = absent).
func (sc *scenario) accountAt(addr types.Address, n uint64) *account.StateAccount {
	var cur *account.StateAccount
	for i := uint64(0); i <= n; i++ {
		if a, ok := sc.blocks[i].accs[addr]; ok {
			if a == nil {
				cur = nil
			} else {
				c := *a
				cur = &c
			}
		}
	}
	return cur
}

func (sc *scenario) storageAt(addr types.Address, n uint64) map[types.Hash][]byte {
	live := false
	m := map[types.Hash][]byte{}
	for i := uint64(0); i <= n; i++ {
		gb := sc.blocks[i]
		if a, ok := gb.accs[addr]; ok {
			if a == nil {
				live = false
				m = map[types.Hash][]byte{}
				continue
			}
			if !live {
				m = map[types.Hash][]byte{}
			}
			if _, w := gb.wipedSlots[addr]; w {
				m = map[types.Hash][]byte{}
			}
			live = true
		}
		if !live {
			continue
		}
		for s, v := range gb.slots[addr] {
			if v == nil {
				delete(m, s)
			} else {
				m[s] = v
			}
		}
	}
	return m
}

func (sc *scenario) storageRootAt(addr types.Address, n uint64) types.Hash {
	m := sc.storageAt(addr, n)
	kvs := make([]rkv, 0, len(m))
	for slot, v := range m {
		sh := keccak(slot[:])
		kvs = append(kvs, rkv{nib: nibblesOfBytes(sh[:]), item: rStr(rStr(v))})
	}
	return rRoot(kvs)
}

// ---------------------------------------------------------------------------
// harness

type e2eOpts struct {
	sched       epochSchedule
	window      bool
	leafSeg     bool
	batch       uint64
	stoCache    int
	accDepth    int    // build: account record depth (0 → 2)
	stoDepth    int    // build: storage record depth (0 → 2)
	foldDepth   int    // query: account fold override (0 = record depth)
	splitAt     uint64 // >0: stop the first run here and resume with a fresh builder
	finalizeMid bool   // leafSeg + splitAt: finalize segments before the resume (exercises the merge)
	concurrent  bool   // --concurrent-root
}

func newTestBuilder(t *testing.T, db kv.RwDB, out string, sc *scenario, o e2eOpts, start uint64) *builder {
	t.Helper()
	b := &builder{
		sched: o.sched, db: db,
		addrHashCache: make(map[types.Address][32]byte, 1<<10),
		slotHashCache: make(map[types.Hash][32]byte, 1<<10),
		accLastFull:   make(map[string]nodeRecState, 1<<10),
		stoLastFull:   newLastFullCache(o.stoCache),
		chgStoAgg:     make(map[string]*[]chgEvent, 1<<10),
		outDir:        out,
		accDepth:      o.accDepth,
		stoDepth:      o.stoDepth,
	}
	if b.accDepth == 0 {
		b.accDepth = 2
	}
	if b.stoDepth == 0 {
		b.stoDepth = 2
	}
	b.concurrentRoot = o.concurrent
	for d := 0; d <= maxChgDepth; d++ {
		size := 1
		for i := 0; i < d; i++ {
			size *= 16
		}
		b.accDirty[d] = make([]uint16, size)
		b.chgAccAgg[d] = make([]chgSlot, size)
		b.stoDirty[d] = make(map[string]*uint16, 1<<10)
	}
	b.fwdMode = true
	b.windowing = o.window
	b.resumed = start > 0
	b.stoLastFull.resumed = b.resumed
	b.winA = make(map[types.Address]*account.StateAccount, 64)
	b.winS = make(map[types.Address]map[types.Hash]*uint256.Int, 16)
	b.winWiped = make(map[types.Address]bool)
	b.rootOracle = func(n uint64) (types.Hash, error) {
		if n >= uint64(len(sc.roots)) {
			return types.Hash{}, fmt.Errorf("no reference root for %d", n)
		}
		return sc.roots[n], nil
	}
	if o.leafSeg {
		sw, err := newLeafSpillWriter(out)
		if err != nil {
			t.Fatal(err)
		}
		b.spill = sw
	}
	return b
}

func writeFwd(t *testing.T, db kv.RwDB, sc *scenario) {
	t.Helper()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for n, gb := range sc.blocks {
		var k [8]byte
		binary.BigEndian.PutUint64(k[:], uint64(n))
		a, s := fwdBlobs(gb)
		if err := tx.Put(tFwdAcctCS, k[:], a); err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(tFwdStorCS, k[:], s); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func openTestQuerier(t *testing.T, db kv.RwDB, out string, o e2eOpts) (*querier, func()) {
	t.Helper()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q, _, err := loadQuerier(tx, out, o.foldDepth)
	if err != nil {
		t.Fatal(err)
	}
	if o.leafSeg && (q.segA == nil || q.segS == nil || q.segSR == nil || q.segNA == nil) {
		t.Fatal("leaf-seg build produced no segments")
	}
	return q, func() {
		tx.Rollback()
		for _, s := range []*leafSegSet{q.segA, q.segS, q.segCA, q.segCS, q.segSR, q.segNA} {
			if s != nil {
				s.Close()
			}
		}
	}
}

var testScenario *scenario

func getScenario(t *testing.T) *scenario {
	if testScenario == nil {
		testScenario = generate(genCfg{blocks: 360, eoas: 3000, bigN: 2, bigSlots: 2200, smallN: 30, smSlots: 24, seed: 7})
	}
	return testScenario
}

func runE2E(t *testing.T, o e2eOpts) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	logger := log.New()
	db, err := openDatcDB(logger, out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writeFwd(t, db, sc)

	end := uint64(len(sc.blocks))
	stop := end
	if o.splitAt > 0 {
		stop = o.splitAt
	}
	b := newTestBuilder(t, db, out, sc, o, 0)
	if err := b.run(0, stop, o.batch); err != nil {
		t.Fatalf("build [0,%d): %v", stop, err)
	}
	if stop < end {
		if b.spill != nil {
			if err := b.spill.close(); err != nil {
				t.Fatal(err)
			}
			if o.finalizeMid {
				if err := finalizeLeafSegments(out); err != nil {
					t.Fatal(err)
				}
			}
		}
		b2 := newTestBuilder(t, db, out, sc, o, stop)
		if err := b2.run(stop, end, o.batch); err != nil {
			t.Fatalf("resumed build [%d,%d): %v", stop, end, err)
		}
	}

	q, closeQ := openTestQuerier(t, db, out, o)
	defer closeQ()

	// Every height: root reconstructed from the records == reference root.
	fails := 0
	totalRecs, totalFolds := 0, 0
	for n := uint64(0); n < end; n++ {
		q.recs, q.folds, q.leafReads = 0, 0, 0
		root, exists, err := q.nodeHashAt(nil, nil, n)
		if err != nil {
			t.Fatalf("nodeHashAt(%d): %v", n, err)
		}
		if !exists {
			root = emptyTrieRoot
		}
		totalRecs += q.recs
		totalFolds += q.folds
		if root != sc.roots[n] {
			fails++
			if fails <= 12 {
				t.Errorf("height %d: root %x != reference %x (recs=%d folds=%d)", n, root[:6], sc.roots[n][:6], q.recs, q.folds)
			}
		}
	}
	if fails > 0 {
		t.Errorf("%d/%d heights mismatched", fails, end)
	}
	t.Logf("heights=%d recs=%d folds=%d", end, totalRecs, totalFolds)
	if totalRecs == 0 && o.foldDepth != 1 {
		t.Errorf("record path never exercised (recs=0) — the test data is not dense enough")
	}
	reportSizes(t, db, out, b)

	// Proofs for sampled keys at sampled heights, walked against the
	// reference root; values compared to the replayed reference state.
	rng := rand.New(rand.NewSource(99))
	keys := []types.Address{sc.eoas[0], sc.eoas[7], sc.big[0], sc.small[2], sc.small[5], sc.absent}
	for trial := 0; trial < 40; trial++ {
		n := uint64(rng.Intn(int(end)))
		for _, addr := range keys {
			ah := keccak(addr[:])
			nib := nibblesOfBytes(ah[:])
			nodes, err := q.proofPath(nil, nib, n)
			if err != nil {
				t.Fatalf("proofPath(%x, %d): %v", addr[:4], n, err)
			}
			got, err := walkProof(sc.roots[n], nodes, nib)
			if err != nil {
				t.Fatalf("proof for %x at %d does not verify: %v", addr[:4], n, err)
			}
			want := sc.accountAt(addr, n)
			if want == nil {
				if got != nil {
					t.Fatalf("%x at %d: expected absence, proof yields a value", addr[:4], n)
				}
				continue
			}
			w := *want
			w.Root = sc.storageRootAt(addr, n)
			buf := make([]byte, w.EncodingLengthForHashing())
			w.EncodeForHashing(buf)
			if !bytes.Equal(got, buf) {
				t.Fatalf("%x at %d: account leaf mismatch\n got %x\nwant %x", addr[:4], n, got, buf)
			}
			// Storage proofs for a contract: two live/absent slots.
			if w.Root == emptyTrieRoot {
				continue
			}
			domain := ah[:]
			sm := sc.storageAt(addr, n)
			var slots []types.Hash
			for s := range sm {
				slots = append(slots, s)
				if len(slots) == 2 {
					break
				}
			}
			var absent types.Hash
			rng.Read(absent[:])
			slots = append(slots, absent)
			for _, slot := range slots {
				sh := keccak(slot[:])
				sNib := nibblesOfBytes(sh[:])
				sNodes, err := q.proofPath(domain, sNib, n)
				if err != nil {
					t.Fatalf("storage proofPath(%x/%x, %d): %v", addr[:4], slot[:4], n, err)
				}
				sgot, err := walkProof(w.Root, sNodes, sNib)
				if err != nil {
					t.Fatalf("storage proof %x/%x at %d does not verify: %v", addr[:4], slot[:4], n, err)
				}
				if v, live := sm[slot]; live {
					if !bytes.Equal(sgot, rStr(v)) {
						t.Fatalf("slot %x/%x at %d: value %x != %x", addr[:4], slot[:4], n, sgot, rStr(v))
					}
				} else if sgot != nil {
					t.Fatalf("slot %x/%x at %d: expected absence", addr[:4], slot[:4], n)
				}
			}
		}
	}
	_ = os.Remove
}

// reportSizes prints per-table row/byte counts of the build (MDBX mode) plus
// the builder's record-kind counters, so the storage effect of the format
// changes is visible in the test log.
func reportSizes(t *testing.T, db kv.RwDB, out string, b *builder) {
	t.Helper()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var total uint64
	for _, tab := range []string{tDatcAccNode, tDatcStoNode, tDatcAccChg, tDatcStoChg, tDatcLeafA, tDatcLeafS, tDatcStoRoot} {
		c, err := tx.Cursor(tab)
		if err != nil {
			t.Fatal(err)
		}
		var rows, kb, vb uint64
		var full, diff, mixed, tomb uint64
		for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
			rows++
			kb += uint64(len(k))
			vb += uint64(len(v))
			if tab == tDatcAccNode || tab == tDatcStoNode {
				switch {
				case len(v) == 0:
					tomb++
				case v[0] == nodeRecFull:
					full++
				case v[0] == nodeRecDiff:
					diff++
				case v[0] == nodeRecMixed:
					mixed++
				}
			}
		}
		c.Close()
		total += kb + vb
		if rows == 0 {
			continue
		}
		line := fmt.Sprintf("  %-12s rows=%-7d key+val=%-9d (%.1f B/row)", tab, rows, kb+vb, float64(kb+vb)/float64(rows))
		if tab == tDatcAccNode || tab == tDatcStoNode {
			line += fmt.Sprintf("  full=%d diff=%d mixed=%d tomb=%d", full, diff, mixed, tomb)
		}
		t.Log(line)
	}
	t.Logf("  TOTAL key+val bytes=%d; mixed markers replaced %d record bytes, %d further mixed epochs elided",
		total, b.statMixedBytesSaved, b.statMixedElided)
	if b2 := b.statLegacyExtraBytes(); b2 > 0 {
		t.Logf("  legacy layout (40B storage domain, 8B block suffix) would add %d bytes", b2)
	}
}

var (
	schedE0is1 = epochSchedule{e: [maxChgDepth + 1]uint64{1, 4, 16, 64, 256, 1024}}
	schedE0is4 = epochSchedule{e: [maxChgDepth + 1]uint64{4, 16, 64, 256, 1024, 4096}}
)

func TestE2E_PerBlock_E0is1(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, batch: 50, stoCache: 64})
}

func TestE2E_PerBlock_E0is4(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is4, batch: 50, stoCache: 64})
}

func TestE2E_Window_E0is1(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, window: true, batch: 48, stoCache: 64})
}

func TestE2E_Resume(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, batch: 50, stoCache: 64, splitAt: 150})
}

func TestE2E_Window_Resume(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, window: true, batch: 48, stoCache: 64, splitAt: 144})
}

func TestE2E_LeafSeg(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, leafSeg: true, batch: 50, stoCache: 64})
}

func TestE2E_LeafSeg_ResumeMerge(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, leafSeg: true, batch: 50, stoCache: 64, splitAt: 150, finalizeMid: true})
}

func TestE2E_Depths_Acc3_Sto1(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, batch: 50, stoCache: 64, accDepth: 3, stoDepth: 1})
}

func TestE2E_FoldOverride(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, batch: 50, stoCache: 64, accDepth: 3, stoDepth: 2, foldDepth: 1})
}

// Dense depth-3 with sparse tops (the mainnet shape); accDepth 4 so the
// account trie folds at depth 4 — with 3000 test accounts that is a small
// fold, and depth-1/2 records are sparse and mostly stale.
func TestE2E_DenseD3(t *testing.T) {
	runE2E(t, e2eOpts{sched: epochSchedule{e: [maxChgDepth + 1]uint64{8, 64, 16, 1, 4096, 4096}}, batch: 50, stoCache: 64, accDepth: 4, stoDepth: 2, leafSeg: true})
}

func TestE2E_ConcurrentRoot(t *testing.T) {
	runE2E(t, e2eOpts{sched: schedE0is1, window: true, batch: 48, stoCache: 64, concurrent: true})
}
