// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// hashed-vs-plain-bench quantifies the read-performance cost of the
// hashed-canonical state layout (HashedAccounts/HashedStorage, keys =
// keccak256) against the plain layout (Account/Storage, keys = raw
// address/slot) on REAL databases, using the SAME logical key set.
//
// Sampling: uniform random Seeks over the plain tables collect real
// addresses/slots; each is converted to its hashed key and kept only if
// present in BOTH databases, so the two layouts answer the identical logical
// queries. The benchmark then replays the same shuffled order against each
// layout twice (pass 1 ≈ cold-ish page cache, pass 2 = warm) and reports
// ns/read. For the hashed layout both key-hashing-inclusive and -exclusive
// timings are reported, separating the keccak cost from the locality cost.
//
// Usage:
//
//	hashed-vs-plain-bench --plain D:/N42-eth1177 --hashed D:/N42-hashed-25311/chaindata \
//	    [--acc 50000] [--sto 50000]
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	plainDir := flag.String("plain", "", "plain-layout chaindata dir (Account/Storage tables)")
	hashedDir := flag.String("hashed", "", "hashed-layout chaindata dir (HashedAccount/HashedStorage)")
	nAcc := flag.Int("acc", 50000, "account keys to sample")
	nSto := flag.Int("sto", 50000, "storage keys to sample")
	flag.Parse()
	if *plainDir == "" || *hashedDir == "" {
		flag.Usage()
		os.Exit(2)
	}

	logger := log2.New()
	openRO := func(dir string) kv.RoDB {
		db, err := mdbx.NewMDBX(logger).Path(dir).Label(kv.ChainDB).Readonly().Accede().Open(context.Background())
		if err != nil {
			fatal("open %s: %v", dir, err)
		}
		return db
	}
	pdb := openRO(*plainDir)
	defer pdb.Close()
	hdb := openRO(*hashedDir)
	defer hdb.Close()

	ptx, err := pdb.BeginRo(context.Background())
	if err != nil {
		fatal("plain tx: %v", err)
	}
	defer ptx.Rollback()
	htx, err := hdb.BeginRo(context.Background())
	if err != nil {
		fatal("hashed tx: %v", err)
	}
	defer htx.Rollback()

	rng := rand.New(rand.NewSource(42)) // deterministic

	// ---- Sample account addresses present in BOTH layouts. ----
	log.Info("sampling accounts", "target", *nAcc)
	pc, err := ptx.Cursor(modules.Account)
	if err != nil {
		fatal("plain acc cursor: %v", err)
	}
	var accAddrs [][20]byte
	var accHashed [][32]byte
	seen := map[[20]byte]struct{}{}
	for tries := 0; len(accAddrs) < *nAcc && tries < *nAcc*20; tries++ {
		var probe [20]byte
		rng.Read(probe[:])
		k, _, err := pc.Seek(probe[:])
		if err != nil || len(k) != 20 {
			continue
		}
		var a [20]byte
		copy(a[:], k)
		if _, dup := seen[a]; dup {
			continue
		}
		var ah [32]byte
		copy(ah[:], crypto.Keccak256(a[:]))
		if v, _ := htx.GetOne(modules.HashedAccounts, ah[:]); len(v) == 0 {
			continue // not in hashed head (created later / deleted) — keep sets identical
		}
		seen[a] = struct{}{}
		accAddrs = append(accAddrs, a)
		accHashed = append(accHashed, ah)
	}
	pc.Close()
	log.Info("accounts sampled", "n", len(accAddrs))

	// ---- Sample storage (addr,slot) present in BOTH layouts. ----
	log.Info("sampling storage", "target", *nSto)
	sc, err := ptx.Cursor(modules.Storage)
	if err != nil {
		fatal("plain sto cursor: %v", err)
	}
	type stoKey struct {
		plain  [52]byte // addr(20) + slot(32)
		hashed [64]byte // keccak(addr) + keccak(slot)
	}
	var stoKeys []stoKey
	seenS := map[[52]byte]struct{}{}
	for tries := 0; len(stoKeys) < *nSto && tries < *nSto*20; tries++ {
		var probe [52]byte
		rng.Read(probe[:])
		k, _, err := sc.Seek(probe[:])
		if err != nil || len(k) < 52 {
			continue
		}
		var sk stoKey
		copy(sk.plain[:], k[:52])
		if _, dup := seenS[sk.plain]; dup {
			continue
		}
		copy(sk.hashed[:32], crypto.Keccak256(sk.plain[:20]))
		copy(sk.hashed[32:], crypto.Keccak256(sk.plain[20:52]))
		if v, _ := htx.GetOne(modules.HashedStorage, sk.hashed[:]); len(v) == 0 {
			continue
		}
		seenS[sk.plain] = struct{}{}
		stoKeys = append(stoKeys, sk)
	}
	sc.Close()
	log.Info("storage sampled", "n", len(stoKeys))

	// Shuffle once; the SAME order replays against both layouts.
	rng.Shuffle(len(accAddrs), func(i, j int) {
		accAddrs[i], accAddrs[j] = accAddrs[j], accAddrs[i]
		accHashed[i], accHashed[j] = accHashed[j], accHashed[i]
	})
	rng.Shuffle(len(stoKeys), func(i, j int) { stoKeys[i], stoKeys[j] = stoKeys[j], stoKeys[i] })

	benchGet := func(tx kv.Tx, table string, keys func(i int) []byte, n int) time.Duration {
		t0 := time.Now()
		for i := 0; i < n; i++ {
			if _, err := tx.GetOne(table, keys(i)); err != nil {
				fatal("get %s: %v", table, err)
			}
		}
		return time.Since(t0)
	}

	report := func(label string, d time.Duration, n int) {
		fmt.Printf("%-46s %10.2f ms   %8.0f ns/read\n", label, float64(d.Microseconds())/1000, float64(d.Nanoseconds())/float64(n))
	}

	fmt.Printf("\n=== same-key-set read benchmark: %d accounts, %d slots ===\n", len(accAddrs), len(stoKeys))
	for pass := 1; pass <= 2; pass++ {
		fmt.Printf("\n-- pass %d (%s) --\n", pass, map[int]string{1: "cold-ish", 2: "warm"}[pass])

		d := benchGet(ptx, modules.Account, func(i int) []byte { return accAddrs[i][:] }, len(accAddrs))
		report("plain  account GetOne(addr20)", d, len(accAddrs))

		d = benchGet(htx, modules.HashedAccounts, func(i int) []byte { return accHashed[i][:] }, len(accHashed))
		report("hashed account GetOne(precomputed hash32)", d, len(accHashed))

		t0 := time.Now()
		for i := range accAddrs {
			h := crypto.Keccak256(accAddrs[i][:])
			if _, err := htx.GetOne(modules.HashedAccounts, h); err != nil {
				fatal("get: %v", err)
			}
		}
		report("hashed account keccak+GetOne (end-to-end)", time.Since(t0), len(accAddrs))

		d = benchGet(ptx, modules.Storage, func(i int) []byte { return stoKeys[i].plain[:] }, len(stoKeys))
		report("plain  storage GetOne(addr20+slot32)", d, len(stoKeys))

		d = benchGet(htx, modules.HashedStorage, func(i int) []byte { return stoKeys[i].hashed[:] }, len(stoKeys))
		report("hashed storage GetOne(precomputed hash64)", d, len(stoKeys))

		t0 = time.Now()
		for i := range stoKeys {
			var comp [64]byte
			copy(comp[:32], crypto.Keccak256(stoKeys[i].plain[:20]))
			copy(comp[32:], crypto.Keccak256(stoKeys[i].plain[20:52]))
			if _, err := htx.GetOne(modules.HashedStorage, comp[:]); err != nil {
				fatal("get: %v", err)
			}
		}
		report("hashed storage 2xkeccak+GetOne (end-to-end)", time.Since(t0), len(stoKeys))
	}

	// ---- Locality benchmark: per-contract GROUPED slot reads. ----
	// The random bench above cannot show the plain layout's clustering
	// advantage: EVM workloads read many slots of ONE contract together, and
	// plain keys share the 20B address prefix (same B-tree pages) while
	// hashed keys scatter uniformly. Sample contracts with >= 32 slots and
	// read each contract's slots back-to-back.
	log.Info("sampling contracts for locality bench")
	sc2, err := ptx.Cursor(modules.Storage)
	if err != nil {
		fatal("cursor: %v", err)
	}
	type group struct {
		plain  [][52]byte
		hashed [][64]byte
	}
	var groups []group
	seenC := map[[20]byte]struct{}{}
	for tries := 0; len(groups) < 300 && tries < 30000; tries++ {
		var probe [52]byte
		rng.Read(probe[:])
		k, _, err := sc2.Seek(probe[:])
		if err != nil || len(k) < 52 {
			continue
		}
		var addr [20]byte
		copy(addr[:], k[:20])
		if _, dup := seenC[addr]; dup {
			continue
		}
		var g group
		ah := crypto.Keccak256(addr[:])
		for ; k != nil && len(k) >= 52; k, _, err = sc2.Next() {
			if err != nil || string(k[:20]) != string(addr[:]) || len(g.plain) >= 100 {
				break
			}
			var pk [52]byte
			copy(pk[:], k[:52])
			var hk [64]byte
			copy(hk[:32], ah)
			copy(hk[32:], crypto.Keccak256(k[20:52]))
			g.plain = append(g.plain, pk)
			g.hashed = append(g.hashed, hk)
		}
		if len(g.plain) < 32 {
			continue // want contracts with real slot counts
		}
		// Keep only groups fully present in the hashed head.
		ok := true
		for _, hk := range g.hashed {
			if v, _ := htx.GetOne(modules.HashedStorage, hk[:]); len(v) == 0 {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		seenC[addr] = struct{}{}
		groups = append(groups, g)
	}
	sc2.Close()
	total := 0
	for _, g := range groups {
		total += len(g.plain)
	}
	fmt.Printf("\n=== locality benchmark: %d contracts, %d grouped slot reads ===\n", len(groups), total)
	for pass := 1; pass <= 2; pass++ {
		fmt.Printf("\n-- pass %d --\n", pass)
		t0 := time.Now()
		for _, g := range groups {
			for i := range g.plain {
				if _, err := ptx.GetOne(modules.Storage, g.plain[i][:]); err != nil {
					fatal("get: %v", err)
				}
			}
		}
		report("plain  grouped slots (clustered prefix)", time.Since(t0), total)

		t0 = time.Now()
		for _, g := range groups {
			for i := range g.hashed {
				if _, err := htx.GetOne(modules.HashedStorage, g.hashed[i][:]); err != nil {
					fatal("get: %v", err)
				}
			}
		}
		report("hashed grouped slots (scattered)", time.Since(t0), total)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
