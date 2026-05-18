// n42-history-verify: replay CS from a freezer to build ground truth,
// then spot-check the history coldstore Reader against it.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/history"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func emit(format string, a ...interface{}) { fmt.Fprintf(os.Stderr, format, a...) }
func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	frDir := flag.String("freezer", "", "freezer dir with acctcs/storcs")
	storeDir := flag.String("store", "", "history coldstore dir")
	prefix := flag.String("prefix", "account", "domain prefix (account/storage/storage-grouped)")
	mphf := flag.Bool("mphf", false, "open as MPHF+fp store")
	startBlk := flag.Uint64("start", 0, "replay start (matches build)")
	endBlk := flag.Uint64("end", 100000, "replay end (matches build)")
	samples := flag.Int("samples", 200, "random (key, queryBlock) samples to check")
	seed := flag.Int64("seed", 0, "RNG seed (0=time)")
	flag.Parse()

	if *frDir == "" || *storeDir == "" {
		emit("usage: n42-history-verify --freezer <dir> --store <dir> --prefix DOM [--start N --end N --samples K]\n")
		os.Exit(1)
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))

	fr, err := freezer.NewReadOnly(*frDir)
	must(err, "open freezer")
	defer fr.Close()

	var tableName string
	switch *prefix {
	case "account":
		tableName = freezer.TableAccountChanges
	case "storage", "storage-grouped":
		tableName = freezer.TableStorageChanges
	default:
		fatal("unknown prefix: %s", *prefix)
	}
	tbl, err := fr.EnsureTableCompressed(tableName, "c")
	must(err, "open table")

	end := *endBlk
	if end > tbl.Items() {
		end = tbl.Items()
	}

	// Build ground truth: for each key, sorted list of (block, value).
	emit("Building ground truth from [%d, %d)...\n", *startBlk, end)
	t0 := time.Now()
	gt := make(map[string][]history.Change) // key string → sorted changes
	for blk := *startBlk; blk < end; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil || len(data) == 0 {
			continue
		}
		switch *prefix {
		case "account":
			cs, err := ethel.DecodeAccountChanges(data)
			if err != nil {
				continue
			}
			for _, c := range cs {
				k := string(c.Address[:])
				gt[k] = append(gt[k], history.Change{Block: blk, Value: append([]byte(nil), c.OldValue...)})
			}
		case "storage":
			cs, err := ethel.DecodeStorageChanges(data)
			if err != nil {
				continue
			}
			for _, c := range cs {
				k := string(c.CompositeKey)
				gt[k] = append(gt[k], history.Change{Block: blk, Value: append([]byte(nil), c.OldValue...)})
			}
		case "storage-grouped":
			cs, err := ethel.DecodeStorageChanges(data)
			if err != nil {
				continue
			}
			for _, c := range cs {
				k := string(c.CompositeKey)
				gt[k] = append(gt[k], history.Change{Block: blk, Value: append([]byte(nil), c.OldValue...)})
			}
		}
	}
	emit("Ground truth: %d unique keys in %s\n", len(gt), time.Since(t0).Truncate(time.Millisecond))

	if len(gt) == 0 {
		fatal("ground truth empty")
	}

	var (
		plainR *history.Reader
		mphfR  *history.MPHFReader
	)
	if *mphf {
		mr, err := history.OpenMPHF(*storeDir, *prefix)
		must(err, "open MPHF reader")
		defer mr.Close()
		mphfR = mr
		emit("Reader (MPHF): %d pages, %d keys\n", mr.PageCount(), mr.KeyCount())
	} else {
		r, err := history.Open(*storeDir, *prefix)
		must(err, "open history reader")
		defer r.Close()
		plainR = r
		emit("Reader: %d pages, keyLen=%d\n", r.PageCount(), r.KeyLen())
	}

	keys := make([]string, 0, len(gt))
	for k := range gt {
		keys = append(keys, k)
	}

	matches, miss, mism := 0, 0, 0
	for i := 0; i < *samples; i++ {
		k := keys[rng.Intn(len(keys))]
		want := gt[k]
		// Pick a random block in [start, end) and verify AsOf.
		queryBlock := *startBlk + uint64(rng.Int63n(int64(end-*startBlk)))

		var getKey []byte
		var get func([]byte) ([]byte, bool, error)
		if *mphf {
			get = mphfR.Get
		} else if plainR != nil {
			get = plainR.Get
		}
		switch *prefix {
		case "account", "storage":
			getKey = []byte(k)
			_ = getKey
			blob, ok, err := get([]byte(k))
			if err != nil {
				emit("  Get %x err: %v\n", k, err)
				mism++
				continue
			}
			if !ok {
				if len(want) == 0 {
					matches++
					continue
				}
				if miss < 5 {
					emit("  MISSING key=%x  (want %d changes)\n", k, len(want))
				}
				miss++
				continue
			}
			got, _, err := history.AsOf(blob, queryBlock)
			if err != nil {
				emit("  AsOf %x err: %v\n", k, err)
				mism++
				continue
			}
			wantVal := asOfGT(want, queryBlock)
			if !bytes.Equal(got, wantVal) {
				if mism < 5 {
					emit("  MISMATCH key=%x  block=%d  got=%x  want=%x\n", k, queryBlock, got, wantVal)
				}
				mism++
				continue
			}
			matches++

		case "storage-grouped":
			addr := []byte(k)[:20]
			slot := []byte(k)[20:]
			blob, ok, err := plainR.Get(addr)
			if err != nil {
				emit("  Get %x err: %v\n", addr, err)
				mism++
				continue
			}
			if !ok {
				if miss < 5 {
					emit("  MISSING addr=%x slot=%x\n", addr, slot)
				}
				miss++
				continue
			}
			got, _, err := history.AsOfGrouped(blob, 32, slot, queryBlock)
			if err != nil {
				emit("  AsOfGrouped err: %v\n", err)
				mism++
				continue
			}
			wantVal := asOfGT(want, queryBlock)
			if !bytes.Equal(got, wantVal) {
				if mism < 5 {
					emit("  MISMATCH addr=%x slot=%x block=%d got=%x want=%x\n",
						addr, slot, queryBlock, got, wantVal)
				}
				mism++
				continue
			}
			matches++
		}
	}

	emit("\n=== Verify ===\n")
	emit("  samples    : %d\n", *samples)
	emit("  matches    : %d (%.2f%%)\n", matches, float64(matches)*100/float64(*samples))
	emit("  missing    : %d\n", miss)
	emit("  mismatches : %d\n", mism)
	if miss > 0 || mism > 0 {
		os.Exit(1)
	}
	emit("\n  ALL %d LOOKUPS MATCHED ✓\n", matches)
}

// asOfGT returns the value the GT timeline gives for queryBlock
// (largest entry with Block <= queryBlock).
func asOfGT(timeline []history.Change, queryBlock uint64) []byte {
	var last []byte
	for _, c := range timeline {
		if c.Block > queryBlock {
			break
		}
		last = c.Value
	}
	return last
}

func must(err error, what string) {
	if err != nil {
		fatal("%s: %v", what, err)
	}
}
