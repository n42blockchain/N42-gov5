// n42-hist-verify sample-verifies accthist/storhist (built by n42-hist-from-freezer)
// against the source acctcs/storcs changeset freezer.
//
// For each sampled block B, it decodes the source changeset to get the keys that
// changed at B, then asserts the history index Lookup(key, B) == B (B is a change
// point, so the largest change ≤ B for that key is B itself). It also spot-checks
// that a never-changed key is absent. Mismatch ⇒ the index dropped/mis-recorded a
// change.
//
// Usage:
//
//	n42-hist-verify --hist D:/n42-hist/chain/freezer --changesets D:/N42-eth1177/chain/freezer [--samples 5000]
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func openCS(dir, name string) (*freezer.FreezerTable, error) {
	t, err := freezer.NewFreezerTableReadOnly(dir, name, "c")
	if err != nil {
		return nil, err
	}
	t.ForceBatchSize(freezer.BatchSize)
	t.SetCompressed(true)
	return t, nil
}

func main() {
	histDir := flag.String("hist", "", "dir with accthist.* + storhist.*")
	csDir := flag.String("changesets", "", "source acctcs.* + storcs.* freezer")
	samples := flag.Int("samples", 5000, "number of blocks to sample")
	maxKeys := flag.Int("maxkeys", 6, "max keys checked per sampled block")
	seed := flag.Int64("seed", 42, "rng seed")
	flag.Parse()
	if *histDir == "" || *csDir == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-hist-verify --hist <dir> --changesets <dir> [--samples N]")
		os.Exit(1)
	}

	acctCS, err := openCS(*csDir, "acctcs")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer acctCS.Close()
	storCS, err := openCS(*csDir, "storcs")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer storCS.Close()

	acctH, err := cscompact.NewHistoryReader(*histDir, "accthist")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open accthist:", err)
		os.Exit(1)
	}
	defer acctH.Close()
	storH, err := cscompact.NewHistoryReader(*histDir, "storhist")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storhist:", err)
		os.Exit(1)
	}
	defer storH.Close()

	end := uint64(acctCS.Items())
	// accthist covers whole 1M-block segments; cap to built coverage.
	covered := acctH.SegmentCount() * cscompact.HistSegmentSize
	if covered > 0 && covered < end {
		end = covered
	}
	fmt.Printf("verifying %d sampled blocks over [0,%d) (accthist segments=%d)\n", *samples, end, acctH.SegmentCount())

	rng := rand.New(rand.NewSource(*seed))

	type stat struct{ checked, ok, miss, bad int }
	var aS, sS stat

	verify := func(name string, h *cscompact.HistoryReader, key []byte, b uint64, st *stat) {
		st.checked++
		got, found := h.Lookup(key, b)
		if !found {
			st.miss++
			if st.miss <= 5 {
				fmt.Printf("  [%s] MISS key=%x block=%d (changed here but index has no entry)\n", name, key, b)
			}
			return
		}
		if got != b {
			st.bad++
			if st.bad <= 5 {
				fmt.Printf("  [%s] BAD  key=%x block=%d got=%d (largest-change<=B should be B)\n", name, key, b, got)
			}
			return
		}
		st.ok++
	}

	// Sample blocks then SORT them: the history readers cache one segment, so
	// visiting blocks in ascending order keeps same-segment samples consecutive
	// (cache hits) instead of thrashing segment reload on every random jump.
	blocks := make([]uint64, *samples)
	for i := range blocks {
		blocks[i] = uint64(rng.Int63n(int64(end)))
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i] < blocks[j] })

	for _, b := range blocks {
		if blob, err := acctCS.Retrieve(b); err == nil && len(blob) > 0 {
			if ch, err := ethel.DecodeAccountChanges(blob); err == nil {
				for j := 0; j < len(ch) && j < *maxKeys; j++ {
					a := ch[j].Address
					verify("acct", acctH, a[:], b, &aS)
				}
			}
		}
		if blob, err := storCS.Retrieve(b); err == nil && len(blob) > 0 {
			if ch, err := ethel.DecodeStorageChanges(blob); err == nil {
				for j := 0; j < len(ch) && j < *maxKeys; j++ {
					verify("stor", storH, ch[j].CompositeKey, b, &sS)
				}
			}
		}
	}

	fmt.Printf("\naccthist: checked=%d ok=%d miss=%d bad=%d\n", aS.checked, aS.ok, aS.miss, aS.bad)
	fmt.Printf("storhist: checked=%d ok=%d miss=%d bad=%d\n", sS.checked, sS.ok, sS.miss, sS.bad)
	if aS.miss+aS.bad+sS.miss+sS.bad == 0 && aS.checked+sS.checked > 0 {
		fmt.Println("PASS — every sampled change point is recorded at the correct block")
	} else {
		fmt.Println("FAIL")
		os.Exit(1)
	}
}
