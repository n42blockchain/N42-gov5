// acctcs-extract-wipes: extract per-block list of contract addresses
// whose storage must be prefix-wiped (SELFDESTRUCT path) from an existing
// main-archive's acctcs + storcs tables. Writes a compact "addr-only"
// sidecar that rebuild-state applies via MDBX prefix-delete on the
// Storage table — covering the gap left by witness-replay's per-slot
// storcs (witness sees only slots the EVM read; SELFDESTRUCT'd contracts
// have many unread slots).
//
// Per-block payload format:
//
//	addr20 × N  (N = len(blob)/20; len(blob) % 20 == 0)
//
// Empty blocks have a zero-byte payload. The freezer is keyed by block
// number; cidx stays aligned with acctcs/storcs.
//
// Why both signals (acctcs + storcs threshold)
// ---------------------------------------------
// forward-replay calls stateWriter.CreateContract(addr) for the wipe
// from TWO paths in modules/state/intra_block_state.go:
//
//  1. shouldDelete (SELFDESTRUCT, no recreate same block)
//     → acctcs entry for addr has newValue=empty (account deleted)
//
//  2. needsWipe (SELFDESTRUCT + CREATE2 same block, then SSTORE)
//     → acctcs entry for addr has newValue!=empty (account replaced),
//       but storage was wiped between the two ops
//
// Path 1 is detectable from acctcs alone. Path 2 leaves a different
// fingerprint: storcs[N] has many (typically ≥ wipeBulkThreshold) entries
// for addr where newValue=empty AND oldValue!=empty (the pre-wipe rows
// emitted by collectPreWipeSlots), AFTER ChangeSet collapsed them with
// same-block SSTOREs that didn't touch those slots.
//
// We take the union: addr is in wipes[N] if EITHER signal fires. The
// threshold (default 3) trades off false positives — bulk-SSTORE-zero
// patterns (e.g. a "reset" function) below threshold are NOT flagged,
// so legitimate state isn't over-deleted at apply time.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	srcAcct := flag.String("acctcs-dir", "", "freezer dir containing main-archive acctcs.cdat (REQUIRED)")
	srcStor := flag.String("storcs-dir", "", "freezer dir containing main-archive storcs.cdat (defaults to acctcs-dir)")
	dst := flag.String("dst", "", "destination freezer dir for the addr-only wipes sidecar (REQUIRED)")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive); 0 = auto-resume from dst's existing items")
	toBlock := flag.Uint64("to", 0, "end block (exclusive); 0 = acctcs.Items()")
	wipeBulkThreshold := flag.Int("bulk-threshold", 3, "min count of newVal=empty storcs entries per addr to flag as path-2 wipe (SELFDESTRUCT+recreate); raise to reduce false positives, lower to catch tiny contracts")
	progressEvery := flag.Uint64("progress", 100000, "log progress every N blocks (0 = silent)")
	flag.Parse()

	if *srcAcct == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --acctcs-dir and --dst are required")
		os.Exit(2)
	}
	if *srcStor == "" {
		*srcStor = *srcAcct
	}

	acctTbl, err := freezer.NewFreezerTableCompressedReadOnly(*srcAcct, freezer.TableAccountChanges, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer acctTbl.Close()
	acctTbl.ForceBatchSize(freezer.BatchSize)

	stoTbl, err := freezer.NewFreezerTableCompressedReadOnly(*srcStor, freezer.TableStorageChanges, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer stoTbl.Close()
	stoTbl.ForceBatchSize(freezer.BatchSize)

	if err := os.MkdirAll(*dst, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir dst:", err)
		os.Exit(1)
	}
	dstTbl, err := freezer.NewFreezerTableCompressed(*dst, freezer.TableWipes, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open dst wipes:", err)
		os.Exit(1)
	}
	defer dstTbl.Close()

	enc, err := zstd.NewWriter(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zstd writer:", err)
		os.Exit(1)
	}
	defer enc.Close()

	end := *toBlock
	if items := acctTbl.Items(); end == 0 || end > items {
		end = items
	}
	start := *fromBlock
	if dstItems := dstTbl.Items(); dstItems > start {
		fmt.Fprintf(os.Stderr, "resuming: dst already has %d entries, --from set to %d\n", dstItems, dstItems)
		start = dstItems
	}
	if start >= end {
		fmt.Fprintf(os.Stderr, "nothing to do: start=%d >= end=%d\n", start, end)
		return
	}

	fmt.Printf("Extracting addr-only wipes from blocks [%d, %d)\n", start, end)
	fmt.Printf("acctcs=%s  storcs=%s  threshold=%d\n", *srcAcct, *srcStor, *wipeBulkThreshold)
	fmt.Printf("dst=%s/%s.cdat\n", *dst, freezer.TableWipes)

	var (
		acctWipes      uint64
		storcsWipes    uint64
		bothWipes      uint64
		nonEmptyBlocks uint64
		totalAddrsOut  uint64
		batchEntries   [][]byte
	)
	t0 := time.Now()

	flushBatch := func() error {
		if len(batchEntries) == 0 {
			return nil
		}
		encoded := freezer.EncodeBatch(batchEntries, enc)
		if err := freezer.WriteBatch(dstTbl, batchEntries, encoded); err != nil {
			return err
		}
		batchEntries = batchEntries[:0]
		return nil
	}

	// Reusable per-block scratch to avoid 24M-iteration allocations.
	wipes := make(map[types.Address]uint8, 64) // bit 0: acctcs, bit 1: storcs
	storCount := make(map[types.Address]int, 64)

	for b := start; b < end; b++ {
		acctData, err := acctTbl.Retrieve(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "block %d: retrieve acctcs: %v\n", b, err)
			os.Exit(1)
		}
		stoData, err := stoTbl.Retrieve(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "block %d: retrieve storcs: %v\n", b, err)
			os.Exit(1)
		}

		clear(wipes)
		clear(storCount)

		// Signal A: acctcs newValue=empty AND oldValue!=empty.
		// (Account deleted — path 1 in updateAccountWithWipe.)
		if len(acctData) > 0 {
			entries, err := ethel.DecodeAccountChanges(acctData)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARN block %d: decode acctcs: %v\n", b, err)
			} else {
				for _, e := range entries {
					if len(e.NewValue) == 0 && len(e.OldValue) > 0 {
						wipes[e.Address] |= 0x01
					}
				}
			}
		}

		// Signal B: storcs has ≥ threshold entries for same addr where
		// newValue=empty AND oldValue!=empty. The pre-wipe rows
		// collectPreWipeSlots emits land here, identifying the rarer
		// path 2 (SELFDESTRUCT + same-block CREATE2 + SSTORE).
		if len(stoData) > 0 {
			entries, err := ethel.DecodeStorageChanges(stoData)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARN block %d: decode storcs: %v\n", b, err)
			} else {
				for _, e := range entries {
					if len(e.NewValue) != 0 || len(e.OldValue) == 0 {
						continue
					}
					if len(e.CompositeKey) < 20 {
						continue
					}
					var addr types.Address
					copy(addr[:], e.CompositeKey[:20])
					storCount[addr]++
				}
				for addr, cnt := range storCount {
					if cnt >= *wipeBulkThreshold {
						wipes[addr] |= 0x02
					}
				}
			}
		}

		var blob []byte
		if len(wipes) > 0 {
			blob = make([]byte, 0, 20*len(wipes))
			for addr, src := range wipes {
				blob = append(blob, addr[:]...)
				switch src {
				case 0x01:
					acctWipes++
				case 0x02:
					storcsWipes++
				case 0x03:
					bothWipes++
				}
			}
			totalAddrsOut += uint64(len(wipes))
			nonEmptyBlocks++
		}
		batchEntries = append(batchEntries, blob)

		if len(batchEntries) >= freezer.BatchSize {
			if err := flushBatch(); err != nil {
				fmt.Fprintf(os.Stderr, "write batch at block %d: %v\n", b, err)
				os.Exit(1)
			}
		}
		if *progressEvery > 0 && b > start && (b-start)%*progressEvery == 0 {
			elapsed := time.Since(t0).Seconds()
			rate := float64(b-start) / elapsed
			fmt.Fprintf(os.Stderr, "  block %d  %.0f blk/s  addrs=%d (acctcs=%d storcs=%d both=%d)  nonEmptyBlocks=%d\n",
				b, rate, totalAddrsOut, acctWipes, storcsWipes, bothWipes, nonEmptyBlocks)
		}
	}
	if err := flushBatch(); err != nil {
		fmt.Fprintln(os.Stderr, "final batch:", err)
		os.Exit(1)
	}
	if err := dstTbl.Sync(); err != nil {
		fmt.Fprintln(os.Stderr, "sync dst:", err)
		os.Exit(1)
	}

	fmt.Printf("\n=== done ===\n")
	fmt.Printf("scanned blocks:        %d (%d..%d)\n", end-start, start, end-1)
	fmt.Printf("total wipe addrs:      %d\n", totalAddrsOut)
	fmt.Printf("  acctcs-only signal:  %d\n", acctWipes)
	fmt.Printf("  storcs-only signal:  %d\n", storcsWipes)
	fmt.Printf("  both signals:        %d\n", bothWipes)
	fmt.Printf("non-empty wipe blocks: %d / %d (%.4f%%)\n",
		nonEmptyBlocks, end-start, 100*float64(nonEmptyBlocks)/float64(end-start))
	fmt.Printf("elapsed: %v\n", time.Since(t0).Truncate(time.Second))
}
