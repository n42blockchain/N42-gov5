// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42-datc proof-bench — the first-hand "is this archive usable" measurement.
// Samples real hashed account/storage keys, then generates N historical
// EIP-1186 proofs at random block heights ≤ head, spanning five cases:
//   acct        — account proof for a real account (EOA/contract mix)
//   acct-absent — account proof of absence (random hash)
//   sto-small   — storage proof, small contract (few slots)
//   sto-large   — storage proof, large contract (USDT/USDC-scale)
//   sto-absent  — storage proof of absence in a large contract
// Each proof is timed AND independently verified with walkProof against the
// real Ethereum state root from headerc (the trust anchor) — so the report is
// both a latency distribution and a correctness rate. Read-only; never writes.

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"time"

	"flag"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// proofStat captures per-proof diagnostic counters for the profile report.
type proofStat struct {
	at           uint64
	kase         int
	dur          time.Duration
	folds        int
	recs         int
	leafReads    int
	distinctKeys int
	frameDecodes int64
	frameRawMB   float64
}

// storSample is one contract with a handful of real slot hashes.
type storSample struct {
	ah    []byte   // 32-byte hashed address
	slots [][]byte // sampled 32-byte hashed slots
	large bool
}

func runProofBench(args []string) {
	fs := flag.NewFlagSet("proof-bench", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (from build)")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir (root oracle)")
	nIter := fs.Int("n", 10000, "number of proofs to run")
	seed := fs.Int64("seed", 42, "rng seed")
	foldDepth := fs.Int("fold-depth", 4, "account-trie fold depth (must match data density)")
	poolAcc := fs.Int("pool-acc", 4000, "sampled account-key pool size")
	poolSto := fs.Int("pool-sto", 400, "sampled contract pool size")
	largeThresh := fs.Uint64("large-thresh", 5000, "slot count ≥ this ⇒ 'large' contract")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	doVerify := fs.Bool("verify", true, "verify each proof against the headerc root")
	schedOverride := fs.String("sched", "", "explicit account schedule e[0..5] (comma-separated); overrides meta. Needed when the build committed 'progress' but not 'sched' (e.g. concurrent-root stops)")
	stoSchedOverride := fs.String("sto-sched", "", "explicit storage schedule e[0..5]; overrides meta")
	largeOnly := fs.Bool("large-only", false, "run ALL trials against the largest contracts (sto-large/sto-absent) — worst-case storage-proof measurement")
	skipLargeScan := fs.Bool("skip-large-scan", false, "skip the full-table scan for large contracts (fast run: storage cases use small contracts only)")
	trustStoRoot := fs.Bool("trust-storoot", false, "force stoRootTrust=true (treat DatcStoRoot misses as authoritative 'no storage', no fold fallback) — tests fix-1 without writing the DB. verify catches any coverage gap.")
	trustDense := fs.Bool("trust-dense", false, "force denseTrust=true: a node-record floorRecord MISS is authoritative 'empty at N' (skip the leaf fold). Kills the early-block 100M-leafRead folds. verify catches any coverage gap.")
	prunedFold := fs.Bool("pruned-fold", false, "route the subtree fold through the change-index traversal (visit only the subtree existing at N) instead of scanning the whole prefix. Kills the early-block shallow-tree folds. verify catches any error.")
	ckptFold := fs.Bool("ckpt-fold", false, "route the subtree fold through the live-key checkpoints (ckpt/ dir): candidate keys from the checkpoints bracketing N, bounding the fold to the state existing at N. Falls back to scan when N is past the last checkpoint. verify catches any error.")
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

	// Head: prefer meta "head", fall back to "progress" (last committed block).
	// A concurrent-root stop may commit "progress" without "head".
	var head uint64
	if hv, _ := tx.GetOne(tDatcMeta, []byte("head")); len(hv) >= 8 {
		head = binary.BigEndian.Uint64(hv[:8])
	} else if pv, _ := tx.GetOne(tDatcMeta, []byte("progress")); len(pv) >= 8 {
		head = binary.BigEndian.Uint64(pv[:8])
		fmt.Printf("note: meta 'head' absent — using 'progress'=%d as the queryable head\n", head)
	} else {
		die("DATC meta has neither 'head' nor 'progress' — is this a DATC build dir?")
	}

	// Schedule: explicit flags override meta (needed when 'sched' wasn't committed).
	var sched, stoSched epochSchedule
	parseSched := func(s string, dst *epochSchedule) bool {
		if s == "" {
			return false
		}
		parts := splitComma(s)
		if len(parts) != maxChgDepth+1 {
			die("--sched/--sto-sched needs exactly %d comma-separated values", maxChgDepth+1)
		}
		for d, p := range parts {
			var v uint64
			if _, e := fmt.Sscanf(p, "%d", &v); e != nil || v == 0 {
				die("schedule entry %d (%q) must be a positive integer", d, p)
			}
			dst.e[d] = v
		}
		return true
	}
	if !parseSched(*schedOverride, &sched) {
		schedV, _ := tx.GetOne(tDatcMeta, []byte("sched"))
		got := 0
		for d := 0; d <= maxChgDepth && (d+1)*8 <= len(schedV); d++ {
			sched.e[d] = binary.BigEndian.Uint64(schedV[d*8:])
			got++
		}
		if got != maxChgDepth+1 {
			die("meta 'sched' absent/incomplete and no --sched given: pass --sched (this build used 64,1024,16384,1,4194304,4194304)")
		}
	}
	if !parseSched(*stoSchedOverride, &stoSched) {
		stoSched = sched
		if ssV, _ := tx.GetOne(tDatcMeta, []byte("stoSched")); len(ssV) >= (maxChgDepth+1)*8 {
			for d := 0; d <= maxChgDepth; d++ {
				stoSched.e[d] = binary.BigEndian.Uint64(ssV[d*8:])
			}
		}
	}

	q := &querier{tx: tx, sched: sched, stoSched: stoSched, foldDepth: *foldDepth}
	if *trustStoRoot {
		// Force the completeness stamp the concurrent-root build failed to write:
		// misses become authoritative "no storage" instead of a full-history fold.
		q.stoRootChecked = true
		q.stoRootTrust = true
		fmt.Printf("stoRootTrust=FORCED (testing fix-1; verify will catch any coverage gap)\n")
	}
	if *trustDense {
		q.denseTrust = true
		fmt.Printf("denseTrust=FORCED (record-miss ⇒ empty at N, no fold; verify catches gaps)\n")
	}
	if *prunedFold {
		q.prunedFold = true
		fmt.Printf("prunedFold=ON (change-index traversal fold; verify catches errors)\n")
	}
	if *ckptFold {
		q.ckpt = openCkptStore(*out)
		q.ckptFold = true
		fmt.Printf("ckptFold=ON (live-key checkpoint fold; acct ckpts=%v storage ckpts=%v)\n",
			q.ckpt.blocks[0], q.ckpt.blocks[1])
	}
	{
		cache := newFrameLRU()
		open := func(tab int) *leafSegSet {
			s, ok, e := openLeafSegSet(*out, tab, cache)
			if e != nil || !ok {
				return nil
			}
			return s
		}
		q.segA, q.segS = open(segTabLeafA), open(segTabLeafS)
		q.segCA, q.segCS = open(segTabChgA), open(segTabChgS)
	}

	var hdrs *ethel.HeaderCompactReader
	if *doVerify {
		hdrs, err = ethel.OpenHeaderCompact(*hdrDir)
		if err != nil {
			die("open headerc (needed for --verify): %v", err)
		}
		defer hdrs.Close()
	}

	rng := rand.New(rand.NewSource(*seed))
	fmt.Printf("proof-bench: head=%d n=%d verify=%v sched.e=%v stoSched.e=%v\n",
		head, *nIter, *doVerify, sched.e, stoSched.e)

	// Diagnostic: isolate root reconstruction from proof generation. Compare
	// nodeHashAt(root) to the header root for a few blocks (== what verify does).
	if *doVerify && os.Getenv("DATC_ROOTCHECK") != "" {
		for _, n := range []uint64{100, 1000, 100000, 1000000, head / 2, head - 100} {
			root, exists, e := q.nodeHashAt(nil, nil, n)
			if !exists {
				root = emptyTrieRoot
			}
			hdr, he := hdrs.ReadHeader(n)
			var hnum uint64
			var hroot types.Hash
			if he == nil {
				hnum = hdr.Number.Uint64()
				hroot = hdr.Root
			}
			match := "MISMATCH"
			if he == nil && root == hroot {
				match = "MATCH"
			}
			fmt.Printf("  N=%-9d hdr.Number=%-9d datc=%x header=%x %s (e=%v %v)\n",
				n, hnum, root[:8], hroot[:8], match, e, he)
		}
		return
	}

	// ---- sample real key pools ------------------------------------------------
	accPool := sampleAccounts(tx, rng, *poolAcc)
	if len(accPool) == 0 {
		die("no accounts sampled from HashedAccounts — is the DB populated?")
	}
	small, large := sampleContracts(tx, rng, *poolSto, *largeThresh, *skipLargeScan)
	fmt.Printf("sampled: %d accounts, %d small contracts, %d large contracts\n",
		len(accPool), len(small), len(large))
	if len(large) == 0 {
		fmt.Printf("  (no 'large' contract ≥ %d slots found in the sample; sto-large falls back to small)\n", *largeThresh)
		large = small
	}
	if len(small) == 0 {
		small = large
	}

	// ---- case definitions -----------------------------------------------------
	const (
		cAcct = iota
		cAcctAbsent
		cStoSmall
		cStoLarge
		cStoAbsent
		nCases
	)
	names := [nCases]string{"acct", "acct-absent", "sto-small", "sto-large", "sto-absent"}
	var slowStats []proofStat
	lat := make([][]time.Duration, nCases)
	okc := make([]int, nCases)
	failc := make([]int, nCases)
	// Split latency by flushed vs current-epoch range to expose the unflushed
	// tail's fold-fallback cost. Last full deep flush was at 12,582,912.
	const lastFlush = 12582912
	var latFlushed, latUnflushed []time.Duration

	if *largeOnly && len(large) == 0 {
		die("--large-only requested but no large contract (>= %d slots) was found", *largeThresh)
	}

	t0 := time.Now()
	for i := 0; i < *nIter; i++ {
		at := uint64(1) + rng.Uint64()%(head-1)
		kase := i % nCases
		if *largeOnly {
			// Alternate large-hit and large-absent slots only.
			if i%2 == 0 {
				kase = cStoLarge
			} else {
				kase = cStoAbsent
			}
		}

		// Reset per-proof counters (diagnostic).
		q.folds, q.recs, q.leafReads, q.distinctKeys = 0, 0, 0, 0
		fd0, fr0 := dbgFrameDecodes, dbgFrameRawBytes

		start := time.Now()
		ok, verified := runOneProof(q, hdrs, *doVerify, kase, cAcct, cAcctAbsent, cStoSmall, cStoLarge, cStoAbsent,
			accPool, small, large, rng, at)
		d := time.Since(start)

		slowStats = append(slowStats, proofStat{
			at: at, kase: kase, dur: d,
			folds: q.folds, recs: q.recs, leafReads: q.leafReads, distinctKeys: q.distinctKeys,
			frameDecodes: dbgFrameDecodes - fd0,
			frameRawMB:   float64(dbgFrameRawBytes-fr0) / (1 << 20),
		})

		lat[kase] = append(lat[kase], d)
		if ok {
			if at < lastFlush {
				latFlushed = append(latFlushed, d)
			} else {
				latUnflushed = append(latUnflushed, d)
			}
		}
		if *doVerify {
			if verified {
				okc[kase]++
			} else {
				failc[kase]++
			}
		}
		if (i+1)%1000 == 0 {
			fmt.Printf("  ... %d/%d  (%.1f proofs/s)\n", i+1, *nIter, float64(i+1)/time.Since(t0).Seconds())
		}
	}
	total := time.Since(t0)

	// ---- report ---------------------------------------------------------------
	fmt.Printf("\n=== proof-bench results (%d proofs in %v) ===\n", *nIter, total.Round(time.Millisecond))
	fmt.Printf("%-12s %8s %10s %10s %10s %10s %10s", "case", "count", "min", "p50", "p90", "p99", "max")
	if *doVerify {
		fmt.Printf("  %10s", "verified")
	}
	fmt.Printf("\n")
	for c := 0; c < nCases; c++ {
		ds := lat[c]
		if len(ds) == 0 {
			continue
		}
		mn, p50, p90, p99, mx := pctl(ds)
		fmt.Printf("%-12s %8d %10v %10v %10v %10v %10v", names[c], len(ds),
			mn.Round(time.Microsecond), p50.Round(time.Microsecond), p90.Round(time.Microsecond),
			p99.Round(time.Microsecond), mx.Round(time.Microsecond))
		if *doVerify {
			fmt.Printf("  %6d/%-3d", okc[c], okc[c]+failc[c])
		}
		fmt.Printf("\n")
	}
	if len(latFlushed) > 0 {
		mn, p50, p90, p99, mx := pctl(latFlushed)
		fmt.Printf("\nby range (verified-ok):\n")
		fmt.Printf("  flushed  <%-9d %6d proofs  min=%v p50=%v p90=%v p99=%v max=%v\n",
			lastFlush, len(latFlushed), mn.Round(time.Microsecond), p50.Round(time.Microsecond),
			p90.Round(time.Microsecond), p99.Round(time.Microsecond), mx.Round(time.Microsecond))
	}
	if len(latUnflushed) > 0 {
		mn, p50, p90, p99, mx := pctl(latUnflushed)
		fmt.Printf("  current  ≥%-9d %6d proofs  min=%v p50=%v p90=%v p99=%v max=%v\n",
			lastFlush, len(latUnflushed), mn.Round(time.Microsecond), p50.Round(time.Microsecond),
			p90.Round(time.Microsecond), p99.Round(time.Microsecond), mx.Round(time.Microsecond))
	}
	// ---- slowest-proof profile (confirms the fold read/decompress volume) ----
	sort.Slice(slowStats, func(i, j int) bool { return slowStats[i].dur > slowStats[j].dur })
	fmt.Printf("\nslowest 12 proofs (folds/recs/leafReads/frameDecodes/decompMB — where the time goes):\n")
	fmt.Printf("%-8s %-11s %10s %8s %8s %12s %12s %10s\n", "case", "at", "dur", "folds", "recs", "leafReads", "distinctK", "decompMB")
	for i := 0; i < len(slowStats) && i < 12; i++ {
		s := slowStats[i]
		fmt.Printf("%-8s %-11d %10v %8d %8d %12d %12d %10.1f\n",
			names[s.kase], s.at, s.dur.Round(time.Millisecond), s.folds, s.recs, s.leafReads, s.distinctKeys, s.frameRawMB)
	}
	// Correlate: median vs slow-tail leafReads/decompMB.
	if len(slowStats) > 0 {
		byLR := append([]proofStat(nil), slowStats...)
		sort.Slice(byLR, func(i, j int) bool { return byLR[i].leafReads < byLR[j].leafReads })
		med := byLR[len(byLR)/2]
		fmt.Printf("median-leafReads proof: leafReads=%d frameDec=%d decompMB=%.1f dur=%v\n",
			med.leafReads, med.frameDecodes, med.frameRawMB, med.dur.Round(time.Millisecond))
	}

	if *doVerify {
		totOK, totN := 0, 0
		for c := 0; c < nCases; c++ {
			totOK += okc[c]
			totN += okc[c] + failc[c]
		}
		fmt.Printf("\ncorrectness: %d/%d proofs verified byte-exact against the real headerc state root\n", totOK, totN)
		if totOK != totN {
			fmt.Printf("  ⚠️  %d proofs FAILED verification — data NOT fully usable\n", totN-totOK)
		} else {
			fmt.Printf("  ✅ all proofs verify — the %d-block partial archive is usable\n", head)
		}
	}
}

// runOneProof generates (and optionally verifies) one proof for the given case.
// Returns (generatedOK, verifiedOK).
func runOneProof(q *querier, hdrs *ethel.HeaderCompactReader, doVerify bool,
	kase, cAcct, cAcctAbsent, cStoSmall, cStoLarge, cStoAbsent int,
	accPool [][]byte, small, large []storSample, rng *rand.Rand, at uint64) (bool, bool) {

	// Pick the account-hash + optional slot for this case.
	var ah []byte
	var sh []byte // non-nil for storage cases
	switch kase {
	case cAcct:
		ah = accPool[rng.Intn(len(accPool))]
	case cAcctAbsent:
		ah = make([]byte, 32)
		rng.Read(ah)
	case cStoSmall:
		cs := small[rng.Intn(len(small))]
		ah = cs.ah
		sh = cs.slots[rng.Intn(len(cs.slots))]
	case cStoLarge:
		cs := large[rng.Intn(len(large))]
		ah = cs.ah
		sh = cs.slots[rng.Intn(len(cs.slots))]
	case cStoAbsent:
		cs := large[rng.Intn(len(large))]
		ah = cs.ah
		sh = make([]byte, 32)
		rng.Read(sh)
	}

	accNib := nibblesOfBytes(ah)
	accNodes, err := q.proofPath(nil, accNib, at)
	if err != nil {
		return false, false
	}

	// Account value + storage root.
	accRaw, accLive, err := q.leafFloor(false, ah, at)
	if err != nil {
		return false, false
	}
	storageHash := emptyTrieRoot
	var acct account.StateAccount
	if accLive {
		if err := acct.DecodeForStorage(accRaw); err != nil {
			return false, false
		}
		domain := make([]byte, 40)
		copy(domain, ah)
		sroot, hasStorage, found := q.storageRootAt(ah, at)
		if !found {
			sroot, hasStorage, err = q.nodeHashAt(domain, nil, at)
			if err != nil {
				return false, false
			}
		}
		if hasStorage {
			storageHash = sroot
		}
	}

	// Storage proof (if this is a storage case and the account has storage).
	var stoNodes [][]byte
	var sNib []byte
	if sh != nil && accLive && storageHash != emptyTrieRoot {
		domain := make([]byte, 40)
		copy(domain, ah)
		sNib = nibblesOfBytes(sh)
		stoNodes, err = q.proofPath(domain, sNib, at)
		if err != nil {
			return false, false
		}
	}

	if !doVerify {
		return true, false
	}

	// Verify against the real Ethereum state root.
	hdr, err := hdrs.ReadHeader(at)
	if err != nil {
		return true, false // generated ok, oracle unavailable
	}
	wantRoot := hdr.Root
	gotVal, err := walkProof(wantRoot, accNodes, accNib)
	if err != nil {
		return true, false
	}
	if accLive {
		acct.Root = storageHash
		ebuf := make([]byte, acct.EncodingLengthForHashing())
		acct.EncodeForHashing(ebuf)
		if !bytes.Equal(gotVal, ebuf) {
			return true, false
		}
	} else if gotVal != nil {
		return true, false
	}
	// Storage leg.
	if stoNodes != nil {
		if _, err := walkProof(storageHash, stoNodes, sNib); err != nil {
			return true, false
		}
	}
	return true, true
}

// sampleAccounts pulls `n` real hashed-account keys by seeking random prefixes.
func sampleAccounts(tx kv.Tx, rng *rand.Rand, n int) [][]byte {
	c, err := tx.Cursor(modules.HashedAccounts)
	if err != nil {
		return nil
	}
	defer c.Close()
	out := make([][]byte, 0, n)
	seek := make([]byte, 32)
	for len(out) < n {
		rng.Read(seek)
		k, _, e := c.Seek(seek)
		if e != nil {
			break
		}
		if k == nil {
			k, _, e = c.First() // wrap
			if e != nil || k == nil {
				break
			}
		}
		out = append(out, append([]byte(nil), k...))
	}
	return out
}

// sampleContracts pulls contracts from HashedStorage, classifying by slot count.
// Random seeks rarely land on a big contract's addrHash, so `small` comes from
// random seeks but `large` comes from a full NextNoDup scan that keeps the
// top-N contracts by dup count (guaranteeing USDT/USDC-scale coverage).
func sampleContracts(tx kv.Tx, rng *rand.Rand, n int, largeThresh uint64, skipLargeScan bool) (small, large []storSample) {
	// --- small: random seeks (cheap, representative of the long tail) ---
	{
		c, err := tx.CursorDupSort(modules.HashedStorage)
		if err == nil {
			defer c.Close()
			seek := make([]byte, 32)
			for tries := 0; len(small) < n && tries < n*4; tries++ {
				rng.Read(seek)
				k, v, e := c.Seek(seek)
				if e != nil || k == nil {
					k, v, e = c.First()
					if e != nil || k == nil {
						break
					}
				}
				cnt, _ := c.CountDuplicates()
				if cnt >= largeThresh {
					continue // large ones handled by the scan below
				}
				slots := collectSlots(c, v, 8)
				if len(slots) > 0 {
					small = append(small, storSample{ah: append([]byte(nil), k[:32]...), slots: slots})
				}
			}
		}
	}
	// --- large: full NextNoDup scan, keep contracts with count >= largeThresh ---
	if !skipLargeScan {
		large = scanLargeContracts(tx, n, largeThresh)
	}
	return small, large
}

// scanLargeContracts walks every distinct contract (NextNoDup) and keeps those
// with >= largeThresh storage slots, returning up to n of them sorted by count
// descending (the largest first). CountDuplicates is O(1) per contract, so the
// cost is one linear pass over distinct addrHashes.
func scanLargeContracts(tx kv.Tx, n int, largeThresh uint64) []storSample {
	c, err := tx.CursorDupSort(modules.HashedStorage)
	if err != nil {
		return nil
	}
	defer c.Close()
	type cc struct {
		ah    []byte
		cnt   uint64
		slots [][]byte
	}
	var found []cc
	scanned := 0
	k, v, e := c.First()
	for k != nil && e == nil {
		scanned++
		cnt, _ := c.CountDuplicates()
		if cnt >= largeThresh {
			found = append(found, cc{ah: append([]byte(nil), k[:32]...), cnt: cnt, slots: collectSlots(c, v, 16)})
		}
		k, v, e = c.NextNoDup()
	}
	fmt.Printf("  scanned %d distinct contracts, %d are large (>=%d slots)\n", scanned, len(found), largeThresh)
	sort.Slice(found, func(i, j int) bool { return found[i].cnt > found[j].cnt })
	out := make([]storSample, 0, n)
	for i := 0; i < len(found) && i < n; i++ {
		out = append(out, storSample{ah: found[i].ah, slots: found[i].slots, large: true})
	}
	return out
}

// collectSlots reads up to max slot hashes for the current contract, starting
// from the cursor's current value v (value = slotHash(32) || encodedValue).
func collectSlots(c kv.CursorDupSort, v []byte, max int) [][]byte {
	var slots [][]byte
	for j := 0; j < max && v != nil; j++ {
		if len(v) >= 32 {
			slots = append(slots, append([]byte(nil), v[:32]...))
		}
		_, v, _ = c.NextDup()
	}
	return slots
}

// pctl returns min, p50, p90, p99, max of a duration slice.
func pctl(ds []time.Duration) (mn, p50, p90, p99, mx time.Duration) {
	cp := append([]time.Duration(nil), ds...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	at := func(p float64) time.Duration {
		if len(cp) == 0 {
			return 0
		}
		idx := int(p * float64(len(cp)-1))
		return cp[idx]
	}
	return cp[0], at(0.50), at(0.90), at(0.99), cp[len(cp)-1]
}

// splitComma splits a comma-separated list, trimming spaces.
func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, trimSpace(cur))
			cur = ""
		} else {
			cur += string(r)
		}
	}
	out = append(out, trimSpace(cur))
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

var _ = os.Stderr
