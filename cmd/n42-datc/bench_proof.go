// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// bench-proof — the comprehensive arbitrary-height gate: correctness AND
// latency of account (+ optional storage) proofs across the offset classes
// that determine reconstruction cost. With sched e=[64,1024,16384,262144,…],
// an offset off from a depth-3 boundary N₀=k·262144−1 leaves window-replay
// active at exactly the depths whose e[d] does not divide off:
//
//	off=0      all shallow depths clean (record-only)     — the fast path
//	off=1      windows at depths 0..3                     — the worst case
//	off=64     depth-0 clean, windows at 1..3
//	off=1024   depths 0-1 clean, windows at 2..3
//	off=16384  depths 0-2 clean, window at 3 only
//	random     whatever falls out — the "arbitrary N" reality check
//
// Every proof is verified end-to-end: hash-chain walk from header[N].Root
// (headerc oracle) down to the account leaf, and the leaf bytes must equal the
// leaf-history floor value re-encoded with the reconstructed storage root.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func runBenchProof(args []string) {
	fs := flag.NewFlagSet("bench-proof", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir (root oracle)")
	addrsHex := fs.String("addrs", "0x1111111111111111111111111111111111111111", "comma-separated addresses")
	slotsHex := fs.String("slots", "", "comma-separated storage slots (proved for every contract account)")
	bases := fs.Int("bases", 3, "number of depth-3 boundary bases N0=k*262144-1")
	offsets := fs.String("offsets", "0,1,64,1024,16384", "comma-separated offsets from each base")
	nRand := fs.Int("rand", 2, "additional fully random heights")
	maxH := fs.Uint64("max-height", 24990000, "height ceiling (headerc oracle limit)")
	foldDepth := fs.Int("fold-depth", 4, "account-trie fold depth")
	seed := fs.Int64("seed", 1, "base/random-height selection seed")
	mapGB := fs.Int("map.gb", 1024, "MDBX map GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	metaV, err := tx.GetOne(tDatcMeta, []byte("head"))
	if err != nil || len(metaV) < 8 {
		die("DATC meta missing: %v", err)
	}
	head := binary.BigEndian.Uint64(metaV)
	schedV, _ := tx.GetOne(tDatcMeta, []byte("sched"))
	var sched epochSchedule
	for d := 0; d <= maxChgDepth && (d+1)*8 <= len(schedV); d++ {
		sched.e[d] = binary.BigEndian.Uint64(schedV[d*8:])
	}
	q := &querier{tx: tx, sched: sched, foldDepth: *foldDepth}
	cache := newFrameLRU()
	openSeg := func(tab int) *leafSegSet {
		s, ok, e := openLeafSegSet(*out, tab, cache)
		if e != nil || !ok {
			return nil
		}
		return s
	}
	q.segA, q.segS = openSeg(segTabLeafA), openSeg(segTabLeafS)
	q.segCA, q.segCS = openSeg(segTabChgA), openSeg(segTabChgS)

	hdrs, err := ethel.OpenHeaderCompact(*hdrDir)
	if err != nil {
		die("headerc: %v", err)
	}
	defer hdrs.Close()

	limit := *maxH
	if head-1 < limit {
		limit = head - 1
	}
	var addrs []types.Address
	for _, a := range strings.Split(*addrsHex, ",") {
		addrs = append(addrs, types.HexToAddress(strings.TrimSpace(a)))
	}
	var slots []types.Hash
	if *slotsHex != "" {
		for _, s := range strings.Split(*slotsHex, ",") {
			slots = append(slots, types.HexToHash(strings.TrimSpace(s)))
		}
	}
	var offs []uint64
	for _, o := range strings.Split(*offsets, ",") {
		var v uint64
		fmt.Sscanf(strings.TrimSpace(o), "%d", &v)
		offs = append(offs, v)
	}

	rng := rand.New(rand.NewSource(*seed))
	const stride = 262144
	maxK := (limit + 1) / stride // N0 = k*stride-1 ≤ limit
	type sample struct {
		n     uint64
		class string
	}
	var samples []sample
	for b := 0; b < *bases && maxK > 2; b++ {
		k := uint64(2 + rng.Intn(int(maxK-1))) // skip genesis-adjacent
		n0 := k*stride - 1
		for _, off := range offs {
			n := n0 + off
			if n > limit {
				continue
			}
			samples = append(samples, sample{n, fmt.Sprintf("off=%d", off)})
		}
	}
	for r := 0; r < *nRand; r++ {
		samples = append(samples, sample{1 + uint64(rng.Int63n(int64(limit))), "random"})
	}

	fmt.Printf("bench-proof: head=%d limit=%d sched.e=%v foldDepth=%d addrs=%d slots=%d samples=%d\n",
		head, limit, sched.e, *foldDepth, len(addrs), len(slots), len(samples))

	type res struct {
		n            uint64
		class        string
		addr         types.Address
		live         bool
		ms           float64
		folds, reads int
		ok           bool
		reason       string
		nStorProofs  int
	}
	var results []res
	for _, s := range samples {
		hdr, herr := hdrs.ReadHeader(s.n)
		if herr != nil {
			results = append(results, res{n: s.n, class: s.class, ok: false, reason: fmt.Sprintf("headerc: %v", herr)})
			continue
		}
		wantRoot := hdr.Root
		for _, addr := range addrs {
			r := res{n: s.n, class: s.class, addr: addr}
			f0, l0 := q.folds, q.leafReads
			t0 := time.Now()
			ok, reason, live, nsp := proveAndVerify(q, addr, slots, s.n, wantRoot)
			r.ms = float64(time.Since(t0).Microseconds()) / 1000
			r.folds, r.reads = q.folds-f0, q.leafReads-l0
			r.ok, r.reason, r.live, r.nStorProofs = ok, reason, live, nsp
			results = append(results, r)
			status := "PASS"
			if !ok {
				status = "FAIL " + reason
			}
			fmt.Printf("  N=%-9d %-10s addr=%x… live=%-5v %9.1fms folds=%-6d leafReads=%-9d storProofs=%d  %s\n",
				r.n, r.class, addr[:4], live, r.ms, r.folds, r.reads, nsp, status)
		}
	}

	// Summary: latency percentiles per class, pass/fail counts.
	fmt.Println("--- summary (per offset class) ---")
	byClass := map[string][]res{}
	for _, r := range results {
		byClass[r.class] = append(byClass[r.class], r)
	}
	var classes []string
	for c := range byClass {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	allPass := true
	for _, c := range classes {
		rs := byClass[c]
		var ms []float64
		pass := 0
		for _, r := range rs {
			ms = append(ms, r.ms)
			if r.ok {
				pass++
			}
		}
		sort.Float64s(ms)
		med := ms[len(ms)/2]
		fmt.Printf("  %-10s n=%-3d pass=%d/%d  median=%.0fms  min=%.0fms  max=%.0fms\n",
			c, len(rs), pass, len(rs), med, ms[0], ms[len(ms)-1])
		if pass != len(rs) {
			allPass = false
		}
	}
	if allPass {
		fmt.Println("ALL PASS")
	} else {
		fmt.Println("FAILURES PRESENT — see rows above")
		os.Exit(1)
	}
}

// proveAndVerify builds and fully verifies one account proof (+ storage proofs
// for contract accounts) at height n — the same steps as runProof, minus JSON.
func proveAndVerify(q *querier, addr types.Address, slots []types.Hash, n uint64, wantRoot types.Hash) (ok bool, reason string, live bool, nStorProofs int) {
	ah := keccak(addr[:])
	accNib := nibblesOfBytes(ah[:])
	accNodes, err := q.proofPath(nil, accNib, n)
	if err != nil {
		return false, fmt.Sprintf("proofPath: %v", err), false, 0
	}
	accRaw, accLive, err := q.leafFloor(false, ah[:], n)
	if err != nil {
		return false, fmt.Sprintf("leafFloor: %v", err), false, 0
	}
	live = accLive
	storageHash := emptyTrieRoot
	var acct account.StateAccount
	if accLive {
		if err := acct.DecodeForStorage(accRaw); err != nil {
			return false, fmt.Sprintf("decode: %v", err), live, 0
		}
		sroot, hasStorage, found := q.storageRootAt(ah[:], n)
		if !found {
			domain := make([]byte, 40)
			copy(domain, ah[:])
			var err error
			sroot, hasStorage, err = q.nodeHashAt(domain, nil, n)
			if err != nil {
				return false, fmt.Sprintf("storage root: %v", err), live, 0
			}
		}
		if hasStorage {
			storageHash = sroot
		}
	}
	gotVal, err := walkProof(wantRoot, accNodes, accNib)
	if err != nil {
		return false, fmt.Sprintf("VERIFY: %v", err), live, 0
	}
	if accLive {
		ebuf := make([]byte, acct.EncodingLengthForHashing())
		acct.Root = storageHash
		acct.EncodeForHashing(ebuf)
		if !bytes.Equal(gotVal, ebuf) {
			return false, "leaf value mismatch vs proof walk", live, 0
		}
	} else if gotVal != nil {
		return false, "expected absence, walk found value", live, 0
	}
	// Storage proofs for contract accounts with storage.
	if accLive && storageHash != emptyTrieRoot {
		domain := make([]byte, 40)
		copy(domain, ah[:])
		for _, slot := range slots {
			sh := keccak(slot[:])
			sNib := nibblesOfBytes(sh[:])
			sNodes, err := q.proofPath(domain, sNib, n)
			if err != nil {
				return false, fmt.Sprintf("storage proofPath %x: %v", slot[:4], err), live, nStorProofs
			}
			composite := make([]byte, 72)
			copy(composite, domain)
			copy(composite[40:], sh[:])
			sval, slive, err := q.leafFloor(true, composite, n)
			if err != nil {
				return false, fmt.Sprintf("slot floor %x: %v", slot[:4], err), live, nStorProofs
			}
			got, err := walkProof(storageHash, sNodes, sNib)
			if err != nil {
				return false, fmt.Sprintf("STORAGE VERIFY %x: %v", slot[:4], err), live, nStorProofs
			}
			if slive {
				if !bytes.Equal(got, rlpStr(sval)) {
					return false, fmt.Sprintf("slot value mismatch %x", slot[:4]), live, nStorProofs
				}
			} else if got != nil {
				return false, fmt.Sprintf("expected slot absence %x", slot[:4]), live, nStorProofs
			}
			nStorProofs++
		}
	}
	return true, "", live, nStorProofs
}
