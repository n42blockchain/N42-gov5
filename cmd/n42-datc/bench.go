// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// bench — proof-latency benchmark over a finished DATC build.
//
// Samples (address, height) pairs from the changeset freezer (touched
// accounts, with their touched slots when any), generates the EIP-1186 proof
// at the sampled height through the exact same querier the proof command
// uses, verifies it against the real header root, and reports the latency
// distribution plus the slowest cases with their fold/leaf-read counters.
//
//	n42-datc bench --out D:/n42-datc --headers D:/n42-eth1/chain/freezer \
//	  --changesets D:/N42-eth1177/chain/freezer --samples 200 --mode mixed
//
// Modes: touch (height = the block that touched the key), random (a uniformly
// random later height), mixed (half / half). --parallel N runs N independent
// queriers (own RoTx + segment sets) to measure throughput.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// benchSample is one (address, slots, height) query.
type benchSample struct {
	Addr    types.Address `json:"addr"`
	Slots   []types.Hash  `json:"slots,omitempty"`
	Touched uint64        `json:"touched"` // block that touched the address
	Height  uint64        `json:"height"`  // queried height
}

// benchResult is one measured query.
type benchResult struct {
	benchSample
	AccountMs float64 `json:"accountMs"`
	TotalMs   float64 `json:"totalMs"`
	Recs      int     `json:"recs"`
	Folds     int     `json:"folds"`
	LeafReads int     `json:"leafReads"`
	Live      bool    `json:"live"`
	Err       string  `json:"err,omitempty"`
}

// proveAt builds and verifies the account (+ slots) proof at height h with
// the querier's counters reset, returning the measured result.
func proveAt(q *querier, s benchSample, wantRoot types.Hash) benchResult {
	r := benchResult{benchSample: s}
	q.recs, q.folds, q.leafReads = 0, 0, 0
	t0 := time.Now()
	ah := keccak(s.Addr[:])
	accNib := nibblesOfBytes(ah[:])
	accNodes, err := q.proofPath(nil, accNib, s.Height)
	if err != nil {
		r.Err = "account proofPath: " + err.Error()
		return r
	}
	accRaw, accLive, err := q.leafFloor(false, ah[:], s.Height)
	if err != nil {
		r.Err = "account leafFloor: " + err.Error()
		return r
	}
	storageHash := emptyTrieRoot
	var acct account.StateAccount
	if accLive {
		if err := acct.DecodeForStorage(accRaw); err != nil {
			r.Err = "account decode: " + err.Error()
			return r
		}
		sroot, has, err := q.nodeHashAt(ah[:], nil, s.Height)
		if err != nil {
			r.Err = "storage root: " + err.Error()
			return r
		}
		if has {
			storageHash = sroot
		}
	}
	got, err := walkProof(wantRoot, accNodes, accNib)
	if err != nil {
		r.Err = "account proof does not verify: " + err.Error()
		return r
	}
	if accLive {
		acct.Root = storageHash
		buf := make([]byte, acct.EncodingLengthForHashing())
		acct.EncodeForHashing(buf)
		if !bytes.Equal(got, buf) {
			r.Err = "account leaf mismatch between proof and leaf history"
			return r
		}
	} else if got != nil {
		r.Err = "expected absence, proof yields a value"
		return r
	}
	r.Live = accLive
	r.AccountMs = float64(time.Since(t0).Microseconds()) / 1000
	if accLive && storageHash != emptyTrieRoot {
		for _, slot := range s.Slots {
			sh := keccak(slot[:])
			sNib := nibblesOfBytes(sh[:])
			sNodes, err := q.proofPath(ah[:], sNib, s.Height)
			if err != nil {
				r.Err = fmt.Sprintf("slot %x proofPath: %v", slot[:4], err)
				return r
			}
			composite := append(append([]byte{}, ah[:]...), sh[:]...)
			sval, slive, err := q.leafFloor(true, composite, s.Height)
			if err != nil {
				r.Err = "slot leafFloor: " + err.Error()
				return r
			}
			sgot, err := walkProof(storageHash, sNodes, sNib)
			if err != nil {
				r.Err = fmt.Sprintf("slot %x proof does not verify: %v", slot[:4], err)
				return r
			}
			if slive {
				if !bytes.Equal(sgot, rlpStr(sval)) {
					r.Err = fmt.Sprintf("slot %x value mismatch", slot[:4])
					return r
				}
			} else if sgot != nil {
				r.Err = fmt.Sprintf("slot %x expected absence", slot[:4])
				return r
			}
		}
	}
	r.TotalMs = float64(time.Since(t0).Microseconds()) / 1000
	r.Recs, r.Folds, r.LeafReads = q.recs, q.folds, q.leafReads
	return r
}

func runBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (from build)")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir (root oracle)")
	csDir := fs.String("changesets", `D:/N42-eth1177/chain/freezer`, "acctcs/storcs freezer dir (sample source)")
	samples := fs.Int("samples", 200, "number of (address, height) queries")
	seed := fs.Int64("seed", 1, "sampling seed")
	maxSlots := fs.Int("slots", 2, "max storage slots per query (from the touching block's storcs)")
	mode := fs.String("mode", "mixed", "touch | random | mixed (see file header)")
	parallel := fs.Int("parallel", 1, "independent queriers running concurrently")
	jsonOut := fs.String("json", "", "write per-query results to this JSON file")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}

	logger := log.New()
	modulesInit()
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	hdrs, err := ethel.OpenHeaderCompact(*hdrDir)
	if err != nil {
		die("open headerc: %v", err)
	}
	defer hdrs.Close()
	acctTbl := openCS(*csDir, "acctcs")
	defer acctTbl.Close()
	storTbl := openCS(*csDir, "storcs")
	defer storTbl.Close()

	// Head from meta (one throwaway querier).
	tx0, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	q0, head, err := loadQuerier(tx0, *out, 0)
	if err != nil {
		die("%v", err)
	}
	fmt.Printf("DATC bench: head=%d accFold=%d stoFold=%d stoRootPerBlock=%v e0=%d samples=%d mode=%s parallel=%d\n",
		head, q0.accFold, q0.stoFold, q0.stoRootPerBlock, q0.sched.e[0], *samples, *mode, *parallel)
	tx0.Rollback()

	// Sample generation from the changesets.
	rng := rand.New(rand.NewSource(*seed))
	var set []benchSample
	for tries := 0; len(set) < *samples && tries < *samples*20; tries++ {
		n := rng.Uint64() % head
		blob, err := acctTbl.Retrieve(n)
		if err != nil || len(blob) == 0 {
			continue
		}
		entries, err := ethel.DecodeAccountChanges(blob)
		if err != nil || len(entries) == 0 {
			continue
		}
		s := benchSample{Addr: entries[rng.Intn(len(entries))].Address, Touched: n}
		if sblob, err := storTbl.Retrieve(n); err == nil && len(sblob) > 0 {
			if sentries, err := ethel.DecodeStorageChanges(sblob); err == nil {
				// Prefer the sampled address's slots; else switch to a storage-touching address.
				var same []types.Hash
				for _, e := range sentries {
					if bytes.Equal(e.CompositeKey[:20], s.Addr[:]) {
						var sl types.Hash
						copy(sl[:], e.CompositeKey[20:])
						same = append(same, sl)
					}
				}
				if len(same) == 0 && len(sentries) > 0 && rng.Intn(2) == 0 {
					e := sentries[rng.Intn(len(sentries))]
					copy(s.Addr[:], e.CompositeKey[:20])
					var sl types.Hash
					copy(sl[:], e.CompositeKey[20:])
					same = []types.Hash{sl}
				}
				rng.Shuffle(len(same), func(i, j int) { same[i], same[j] = same[j], same[i] })
				if len(same) > *maxSlots {
					same = same[:*maxSlots]
				}
				s.Slots = same
			}
		}
		switch *mode {
		case "touch":
			s.Height = n
		case "random":
			s.Height = n + rng.Uint64()%(head-n)
		default:
			if rng.Intn(2) == 0 {
				s.Height = n
			} else {
				s.Height = n + rng.Uint64()%(head-n)
			}
		}
		set = append(set, s)
	}
	if len(set) == 0 {
		die("no samples could be drawn from the changesets")
	}

	// Run.
	results := make([]benchResult, len(set))
	var wg sync.WaitGroup
	next := make(chan int, len(set))
	for i := range set {
		next <- i
	}
	close(next)
	tStart := time.Now()
	for w := 0; w < *parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := db.BeginRo(context.Background())
			if err != nil {
				die("begin: %v", err)
			}
			defer tx.Rollback()
			q, _, err := loadQuerier(tx, *out, 0)
			if err != nil {
				die("%v", err)
			}
			for i := range next {
				hdr, err := hdrs.ReadHeader(set[i].Height)
				if err != nil {
					results[i] = benchResult{benchSample: set[i], Err: "header: " + err.Error()}
					continue
				}
				results[i] = proveAt(q, set[i], hdr.Root)
			}
		}()
	}
	wg.Wait()
	wall := time.Since(tStart)

	// Report.
	var accMs, totMs []float64
	fails := 0
	for _, r := range results {
		if r.Err != "" {
			fails++
			continue
		}
		accMs = append(accMs, r.AccountMs)
		totMs = append(totMs, r.TotalMs)
	}
	pct := func(v []float64, p float64) float64 {
		if len(v) == 0 {
			return 0
		}
		s := append([]float64{}, v...)
		sort.Float64s(s)
		i := int(float64(len(s)-1) * p)
		return s[i]
	}
	under := func(v []float64, ms float64) float64 {
		if len(v) == 0 {
			return 0
		}
		c := 0
		for _, x := range v {
			if x <= ms {
				c++
			}
		}
		return 100 * float64(c) / float64(len(v))
	}
	fmt.Printf("\nqueries=%d ok=%d failed=%d wall=%s (%.1f q/s)\n", len(results), len(totMs), fails, wall.Round(time.Millisecond), float64(len(results))/wall.Seconds())
	fmt.Printf("account proof  ms: p50=%.1f p90=%.1f p99=%.1f max=%.1f  ≤100ms %.1f%%  ≤1s %.1f%%\n",
		pct(accMs, .5), pct(accMs, .9), pct(accMs, .99), pct(accMs, 1), under(accMs, 100), under(accMs, 1000))
	fmt.Printf("account+slots  ms: p50=%.1f p90=%.1f p99=%.1f max=%.1f  ≤100ms %.1f%%  ≤1s %.1f%%\n",
		pct(totMs, .5), pct(totMs, .9), pct(totMs, .99), pct(totMs, 1), under(totMs, 100), under(totMs, 1000))
	sort.Slice(results, func(i, j int) bool { return results[i].TotalMs > results[j].TotalMs })
	fmt.Println("slowest:")
	for i := 0; i < len(results) && i < 8; i++ {
		r := results[i]
		fmt.Printf("  %7.1f ms  addr=%x touched=%d height=%d slots=%d live=%v recs=%d folds=%d leafReads=%d %s\n",
			r.TotalMs, r.Addr[:6], r.Touched, r.Height, len(r.Slots), r.Live, r.Recs, r.Folds, r.LeafReads, r.Err)
	}
	if fails > 0 {
		fmt.Println("failures:")
		shown := 0
		for _, r := range results {
			if r.Err != "" && shown < 8 {
				fmt.Printf("  addr=%x height=%d: %s\n", r.Addr, r.Height, r.Err)
				shown++
			}
		}
	}
	if *jsonOut != "" {
		enc, _ := json.MarshalIndent(results, "", " ")
		if err := os.WriteFile(*jsonOut, enc, 0o644); err != nil {
			die("write json: %v", err)
		}
	}
	if fails > 0 {
		os.Exit(2)
	}
}
