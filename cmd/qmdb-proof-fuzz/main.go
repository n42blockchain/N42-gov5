// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// qmdb-proof-fuzz — large-scale randomized audit of the QMDB full-history proof
// layer (lib/qmdb/history.go ProofAtHeight) on a real --qmdb-history replay
// target. For N random (key, height, case) trials it reconstructs the root +
// membership proof at that height and checks, against the block HEADER root (the
// independent consensus oracle):
//
//   - root(reconstructed at h) == header.Root(h)             [reconstruction]
//   - if the key is live at h: qmdb.VerifyProof folds to it   [membership]
//   - and the wire codec round-trips: VerifyEncodedProof      [serialization]
//
// It deliberately exercises the "各种情况" (various cases) at arbitrary heights:
//   - live account/storage keys at heights where they exist  (found)
//   - real keys at heights BEFORE they first appeared         (temporal absence)
//   - random keys that never existed                          (pure absence)
//   - boundary heights (1, head, twig boundaries)
//
// and reports proof-generation latency percentiles, proof sizes, and throughput.
//
//	qmdb-proof-fuzz --db E:/n42-replay-v2-qmdb-staggered-hist/chaindata --n 100000
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	db := flag.String("db", "", "chaindata dir of a --tree qmdb --qmdb-history replay target")
	n := flag.Int("n", 100000, "number of random proof trials")
	poolSize := flag.Int("pool", 60000, "distinct real keys to pre-sample from the version index")
	mapGB := flag.Int("map.gb", 256, "MDBX map size GB")
	seed := flag.Int64("seed", 42, "PRNG seed (deterministic run)")
	progEvery := flag.Int("progress", 10000, "print progress every N trials")
	flag.Parse()
	if *db == "" {
		die("--db required")
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	kdb, err := mdbxkv.NewMDBX(log.New()).Path(*db).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer kdb.Close()
	tx, err := kdb.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	rc := commitment.NewQMDBRootComputer()
	t0 := time.Now()
	if err := rc.LoadFrom(tx); err != nil {
		die("LoadFrom: %v", err)
	}
	rc.SetCold(tx)
	rc.EnableHistory(tx) // read-only: ProofAtHeight only
	fmt.Printf("tree reloaded in %v: liveKeys=%d twigs=%d nextSlot=%d\n",
		time.Since(t0).Round(time.Millisecond), rc.Tree().LiveCount(), rc.Tree().NumTwigs(), rc.Tree().NextSlot())

	// Recorded height range from the per-block cursor table.
	lo, head, ok := blockPosRange(tx)
	if !ok || head == 0 {
		die("no qmdbBlockPos records — was the replay run with --qmdb-history?")
	}
	fmt.Printf("recorded height range: [%d, %d]\n", lo, head)

	rng := rand.New(rand.NewSource(*seed))

	// ---- Pre-sample a diverse pool of REAL keys (uniform over the keccak
	// keyspace) via random Seeks, so the timed loop never pays a sampling Seek.
	pool := samplePool(tx, rng, *poolSize)
	if len(pool) == 0 {
		die("no keys sampled from %s", modules.QMDBKeyVers)
	}
	fmt.Printf("pre-sampled %d distinct real keys\n", len(pool))

	// Header-root oracle cache (header read is I/O, kept OUT of proof timing).
	hdrCache := make(map[uint64]qmdb.Hash, 4096)
	headerRoot := func(h uint64) (qmdb.Hash, bool) {
		if v, ok := hdrCache[h]; ok {
			return v, v != (qmdb.Hash{}) || headerExists(tx, h)
		}
		hdr := rawdb.ReadHeaderByNumber(tx, h)
		if hdr == nil {
			hdrCache[h] = qmdb.Hash{}
			return qmdb.Hash{}, false
		}
		r := qmdb.Hash(hdr.Root)
		hdrCache[h] = r
		return r, true
	}

	// ---- Counters + latency samples.
	var (
		pass, fail                int
		found, notFound           int
		caseRealRand, caseRealLow int
		caseAbsent                int
		headerMissing             int
		verifyFail, encFail       int
		rootMismatch, errCount    int
		proofBytesSum             int64
		proofBytesMax             int
	)
	genFound := make([]time.Duration, 0, *n)
	genAbsent := make([]time.Duration, 0, *n)
	failSamples := make([]string, 0, 32)

	recordFail := func(msg string) {
		fail++
		if len(failSamples) < 32 {
			failSamples = append(failSamples, msg)
		}
	}

	// Verify one (key, height) trial. absentExpected=true means we chose a random
	// key that should never be live (pure-absence case).
	trial := func(kh qmdb.Hash, h uint64, absentExpected bool) {
		want, okH := headerRoot(h)
		if !okH {
			headerMissing++
			return
		}
		tStart := time.Now()
		proof, root, isFound, perr := rc.ProofAtHeight(kh, h)
		dur := time.Since(tStart)
		if perr != nil {
			errCount++
			recordFail(fmt.Sprintf("h=%d key=%x: ERR %v", h, kh[:6], perr))
			return
		}
		if root != want {
			rootMismatch++
			recordFail(fmt.Sprintf("h=%d key=%x: ROOT recon=%x header=%x", h, kh[:6], root[:8], want[:8]))
			return
		}
		if isFound {
			found++
			genFound = append(genFound, dur)
			if absentExpected {
				// A random 32B key resolving to a live entry is astronomically
				// unlikely — flag it (would indicate a sampling/logic bug).
				recordFail(fmt.Sprintf("h=%d key=%x: absent-expected key was FOUND", h, kh[:6]))
				return
			}
			if !qmdb.VerifyProof(want, proof) {
				verifyFail++
				recordFail(fmt.Sprintf("h=%d key=%x: VerifyProof failed", h, kh[:6]))
				return
			}
			blob := proof.Marshal()
			if !qmdb.VerifyEncodedProof(want, blob) {
				encFail++
				recordFail(fmt.Sprintf("h=%d key=%x: VerifyEncodedProof failed", h, kh[:6]))
				return
			}
			proofBytesSum += int64(len(blob))
			if len(blob) > proofBytesMax {
				proofBytesMax = len(blob)
			}
		} else {
			notFound++
			genAbsent = append(genAbsent, dur)
		}
		pass++
	}

	// ---- Edge heights: exercise the boundaries explicitly (once each, with a
	// live key and an absent key) so they're covered regardless of the RNG.
	twigSpanBlocks := head / uint64(maxInt(rc.Tree().NumTwigs(), 1))
	edges := []uint64{1, 2, lo, head, head - 1, twigSpanBlocks, head / 2}
	for _, h := range edges {
		if h == 0 || h > head {
			continue
		}
		trial(pool[rng.Intn(len(pool))], h, false)
		trial(randHash(rng), h, true)
	}

	// ---- Main randomized loop.
	fmt.Printf("running %d random trials (seed=%d)…\n", *n, *seed)
	loopStart := time.Now()
	for i := 0; i < *n; i++ {
		r := rng.Float64()
		switch {
		case r < 0.15: // pure absence: random key that never existed
			caseAbsent++
			trial(randHash(rng), 1+uint64(rng.Int63n(int64(head))), true)
		case r < 0.30: // temporal absence bias: real key at a LOW height
			caseRealLow++
			hi := head / 50
			if hi < 2 {
				hi = 2
			}
			trial(pool[rng.Intn(len(pool))], 1+uint64(rng.Int63n(int64(hi))), false)
		default: // real key at a fully random height (natural found/not-found mix)
			caseRealRand++
			trial(pool[rng.Intn(len(pool))], 1+uint64(rng.Int63n(int64(head))), false)
		}
		if *progEvery > 0 && (i+1)%*progEvery == 0 {
			el := time.Since(loopStart)
			fmt.Printf("  %d/%d  pass=%d fail=%d found=%d notFound=%d  %.0f proofs/s\n",
				i+1, *n, pass, fail, found, notFound, float64(i+1)/el.Seconds())
		}
	}
	wall := time.Since(loopStart)

	// ---- Report.
	fmt.Printf("\n=== QMDB any-height proof fuzz: %d trials in %v ===\n", *n, wall.Round(time.Millisecond))
	fmt.Printf("throughput        : %.0f proofs/s (single-thread)\n", float64(*n)/wall.Seconds())
	fmt.Printf("case mix          : realRand=%d realLow=%d absent=%d (+%d edge)\n",
		caseRealRand, caseRealLow, caseAbsent, len(edges)*2)
	fmt.Printf("outcomes          : PASS=%d FAIL=%d | found(membership)=%d notFound=%d headerMissing=%d\n",
		pass, fail, found, notFound, headerMissing)
	if found > 0 {
		fmt.Printf("proof size        : avg=%d B max=%d B (membership proofs)\n", proofBytesSum/int64(found), proofBytesMax)
	}
	fmt.Printf("latency (found)   : %s\n", pctl(genFound))
	fmt.Printf("latency (notFound): %s\n", pctl(genAbsent))

	if fail == 0 {
		fmt.Printf("\n✅ ALL %d PROOFS CORRECT — every reconstructed root equals the block header root; every membership proof folds to it and round-trips the wire codec.\n", pass)
		return
	}
	fmt.Printf("\n❌ %d FAILURES (breakdown: rootMismatch=%d verifyFail=%d encFail=%d err=%d):\n",
		fail, rootMismatch, verifyFail, encFail, errCount)
	for _, m := range failSamples {
		fmt.Printf("  %s\n", m)
	}
	os.Exit(1)
}

// samplePool draws up to want distinct real keys uniformly from QMDBKeyVers by
// seeking random 32-byte targets (keccak keys are uniform, so Seek→next key is a
// uniform sample of the live/historical keyspace).
func samplePool(tx kv.Tx, rng *rand.Rand, want int) []qmdb.Hash {
	c, err := tx.Cursor(modules.QMDBKeyVers)
	if err != nil {
		return nil
	}
	defer c.Close()
	seen := make(map[qmdb.Hash]struct{}, want)
	out := make([]qmdb.Hash, 0, want)
	attempts := 0
	for len(out) < want && attempts < want*8 {
		attempts++
		target := randHash(rng)
		k, _, err := c.Seek(target[:])
		if err != nil || k == nil {
			k, _, err = c.First() // wrapped past the end
			if err != nil || k == nil {
				break
			}
		}
		var kh qmdb.Hash
		copy(kh[:], k)
		if _, dup := seen[kh]; dup {
			continue
		}
		seen[kh] = struct{}{}
		out = append(out, kh)
	}
	return out
}

func randHash(rng *rand.Rand) qmdb.Hash {
	var h qmdb.Hash
	rng.Read(h[:])
	return h
}

func headerExists(tx kv.Tx, h uint64) bool { return rawdb.ReadHeaderByNumber(tx, h) != nil }

func blockPosRange(tx kv.Tx) (lo, hi uint64, ok bool) {
	c, err := tx.Cursor(modules.QMDBBlockPos)
	if err != nil {
		return 0, 0, false
	}
	defer c.Close()
	fk, _, _ := c.First()
	lk, _, _ := c.Last()
	if fk == nil || lk == nil {
		return 0, 0, false
	}
	return be8dec(fk), be8dec(lk), true
}

func be8dec(b []byte) uint64 {
	if len(b) < 8 {
		var v uint64
		for _, x := range b {
			v = v<<8 | uint64(x)
		}
		return v
	}
	return binary.BigEndian.Uint64(b)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// pctl sorts a copy and formats p50/p90/p99/max/avg.
func pctl(ds []time.Duration) string {
	if len(ds) == 0 {
		return "n=0"
	}
	cp := make([]time.Duration, len(ds))
	copy(cp, ds)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	var sum time.Duration
	for _, d := range cp {
		sum += d
	}
	at := func(p float64) time.Duration {
		idx := int(p * float64(len(cp)))
		if idx >= len(cp) {
			idx = len(cp) - 1
		}
		return cp[idx]
	}
	return fmt.Sprintf("n=%d avg=%s p50=%s p90=%s p99=%s max=%s",
		len(cp),
		(sum / time.Duration(len(cp))).Round(time.Microsecond),
		at(0.50).Round(time.Microsecond),
		at(0.90).Round(time.Microsecond),
		at(0.99).Round(time.Microsecond),
		cp[len(cp)-1].Round(time.Microsecond))
}
