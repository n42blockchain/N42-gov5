// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// buildTestArchive builds the e2e scenario into a finished leaf-seg archive
// with the exact ladder and birth partitions, the shape a node serves.
func buildTestArchive(t *testing.T) (*scenario, string) {
	t.Helper()
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	db, err := openDatcDB(log.New(), out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, db, sc)
	o := e2eOpts{sched: schedE0is1, leafSeg: true, batch: 50, stoCache: 64}
	if err := newTestBuilder(t, db, out, sc, o, 0).run(0, uint64(len(sc.blocks)), o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}
	db.Close()
	stageBin = 8
	defer func() { stageBin = 1024 }()
	var contracts []deriveContract
	for a, d := range map[types.Address]int{sc.big[0]: 2, sc.big[1]: 3, sc.small[2]: 1} {
		h := keccak(a[:])
		contracts = append(contracts, deriveContract{h[:], d})
	}
	if err := deriveNS(out, out, contracts, 4, 16, deriveOpts{}); err != nil {
		t.Fatalf("derive-ns: %v", err)
	}
	if err := deriveAccParts(out, out, []uint64{40, 120, 250}, 4); err != nil {
		t.Fatalf("derive-acc-parts: %v", err)
	}
	return sc, out
}

// TestArchiveProve: the library reader answers eth_getProof at every height
// — values from the archive, proofs verified against the reference root —
// for existing and absent accounts and slots, concurrently; refuses heights
// it does not cover; and fails closed on a root it cannot prove against.
func TestArchiveProve(t *testing.T) {
	sc, dir := buildTestArchive(t)
	a, err := OpenArchive(dir, ArchiveOptions{MapGB: 4, Readers: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	start, head := a.Range()
	if start != 0 || head != uint64(len(sc.blocks)) {
		t.Fatalf("range [%d,%d), want [0,%d)", start, head, len(sc.blocks))
	}

	addrs := []types.Address{sc.eoas[0], sc.eoas[7], sc.big[0], sc.big[1], sc.small[2], sc.small[5], sc.absent}
	check := func(n uint64, addr types.Address, rng *rand.Rand) error {
		want := sc.accountAt(addr, n)
		var slots []types.Hash
		for s := range sc.storageAt(addr, n) {
			if slots = append(slots, s); len(slots) == 2 {
				break
			}
		}
		var absent types.Hash
		rng.Read(absent[:])
		slots = append(slots, absent)
		root := sc.roots[n]
		p, err := a.Prove(context.Background(), addr, slots, n, &root)
		if err != nil {
			return err
		}
		if (want != nil) != p.Exists {
			t.Errorf("%x at %d: exists=%v", addr[:4], n, p.Exists)
			return nil
		}
		if want != nil {
			if p.Nonce != want.Nonce || !p.Balance.Eq(&want.Balance) {
				t.Errorf("%x at %d: nonce/balance %d/%s, want %d/%s", addr[:4], n, p.Nonce, p.Balance, want.Nonce, &want.Balance)
			}
			if p.StorageHash != sc.storageRootAt(addr, n) {
				t.Errorf("%x at %d: storage hash differs", addr[:4], n)
			}
		}
		sm := sc.storageAt(addr, n)
		for _, sp := range p.Storage {
			v, live := sm[sp.Key]
			exp := new(uint256.Int)
			if live {
				exp.SetBytes(v)
			}
			if !sp.Value.Eq(exp) {
				t.Errorf("%x/%x at %d: value %s, want %s", addr[:4], sp.Key[:4], n, sp.Value, exp)
			}
		}
		return nil
	}

	// Every height for a few accounts, and random (height, account) pairs from
	// several goroutines against the shared pool.
	rng := rand.New(rand.NewSource(1))
	for n := uint64(0); n < head; n++ {
		for _, addr := range []types.Address{sc.big[1], sc.eoas[0]} {
			if err := check(n, addr, rng); err != nil {
				t.Fatalf("height %d: %v", n, err)
			}
		}
	}
	errs := make(chan error, 8)
	for w := 0; w < 8; w++ {
		go func(seed int64) {
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 60; i++ {
				if err := check(uint64(rng.Intn(int(head))), addrs[rng.Intn(len(addrs))], rng); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}(int64(w))
	}
	for w := 0; w < 8; w++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if _, err := a.Prove(context.Background(), sc.big[0], nil, head, nil); !errors.Is(err, ErrNotCovered) {
		t.Fatalf("height %d (head): err=%v, want ErrNotCovered", head, err)
	}
	wrong := sc.roots[10]
	if _, err := a.Prove(context.Background(), sc.big[0], nil, 200, &wrong); !errors.Is(err, ErrProofMismatch) {
		t.Fatalf("proof against another height's root: err=%v, want ErrProofMismatch", err)
	}
	// The pool still serves after a mismatch.
	root := sc.roots[200]
	if _, err := a.Prove(context.Background(), sc.big[0], nil, 200, &root); err != nil {
		t.Fatal(err)
	}
}

// TestArchiveNoticesUpdate: a weekly update rewrites the sidecars; readers
// built before it are retired and later proofs use the new generation.
func TestArchiveNoticesUpdate(t *testing.T) {
	sc, dir := buildTestArchive(t)
	a, err := OpenArchive(dir, ArchiveOptions{MapGB: 4, RefreshEvery: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	root := sc.roots[300]
	if _, err := a.Prove(context.Background(), sc.big[1], nil, 300, &root); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	gen := a.gen
	a.mu.Unlock()
	lad := filepath.Join(dir, leafSegDir, exactLaddersFile)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(lad, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Prove(context.Background(), sc.big[1], nil, 300, &root); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen == gen {
		t.Fatal("a rewritten ns.ladders did not start a new reader generation")
	}
	for len(a.pool) > 0 {
		r := <-a.pool
		stale := r.gen != a.gen
		r.close() // a reader left open would keep Close waiting for its transaction
		if stale {
			t.Fatal("a reader of the old generation went back into the pool")
		}
	}
}

// TestProofOrderIsChecked: an EIP-1186 proof is the path, root first. The
// folded part of a proof used to come out deepest-first while walkProof only
// checked that the nodes hash-linked as a set, so every "verified" proof of
// the time would have been refused by a client. A proof with its tail in the
// old order, or with a stray node appended, must not verify.
func TestProofOrderIsChecked(t *testing.T) {
	sc, dir := buildTestArchive(t)
	a, err := OpenArchive(dir, ArchiveOptions{MapGB: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	n := uint64(len(sc.blocks) - 1)
	p, err := a.Prove(context.Background(), sc.eoas[3], nil, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AccountProof) < 3 {
		t.Fatalf("proof of %d nodes is too short to reorder", len(p.AccountProof))
	}
	ah := keccak(sc.eoas[3][:])
	nib := nibblesOfBytes(ah[:])
	if _, err := walkProof(sc.roots[n], p.AccountProof, nib); err != nil {
		t.Fatalf("the proof as served: %v", err)
	}
	swapped := append([][]byte{}, p.AccountProof...)
	last := len(swapped) - 1
	swapped[last], swapped[last-1] = swapped[last-1], swapped[last]
	if _, err := walkProof(sc.roots[n], swapped, nib); err == nil {
		t.Fatal("a proof with its last two nodes swapped verified")
	}
	extra := append(append([][]byte{}, p.AccountProof...), p.AccountProof[1])
	if _, err := walkProof(sc.roots[n], extra, nib); err == nil {
		t.Fatal("a proof with a stray node appended verified")
	}
}
