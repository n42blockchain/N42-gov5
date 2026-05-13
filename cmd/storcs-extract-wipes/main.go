// storcs-extract-wipes: scan an existing ethexec storcs freezer table and
// extract per-block "wipe" entries (newVal=0 ∧ oldVal≠0) into a sidecar
// freezer table. The output table has the same per-block index as the
// source storcs, but each blob contains only the wipe subset.
//
// DEPRECATED. The new sidecar format (cmd/acctcs-extract-wipes) is
// roughly 3000× smaller because it stores only the wipe ADDRESSES per
// block; rebuild-state expands each address into the slot set via an
// MDBX prefix-scan at apply time. Loading a sidecar produced by THIS
// tool against current rebuild-state errors out with "payload len is
// not a multiple of 20". Switch to acctcs-extract-wipes.
//
// Use case: witness-replay's WitnessCapturingWriter has no MDBX state to
// enumerate a SELFDESTRUCT'd contract's storage, so its storcs is
// structurally missing the pre-wipe entries that ethexec's
// BufferedPlainStateWriter.collectPreWipeSlots emits. Running this
// extractor against a known-good ethexec storcs (e.g. the
// reth-cross-validated D:/N42-eth1456-verified) produces a sidecar that
// witness-replay can load to fill those gaps without re-running ethexec.
//
// Note: the filter (newVal=0 ∧ oldVal≠0) also matches regular SSTORE x=0,
// which witness-replay would have produced on its own. The merger on the
// witness-replay side dedups by composite key so duplicates are harmless.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// sidecarTable mirrors freezer.TableWipes; aliased here to keep the
// flag-help and log lines stable if the registered constant ever moves.
var sidecarTable = freezer.TableWipes

func main() {
	src := flag.String("src", "", "source freezer dir (containing ethexec storcs.cdat) — REQUIRED")
	dst := flag.String("dst", "", "destination freezer dir for wipes sidecar — REQUIRED")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive); 0 = auto-resume from dst's existing items")
	toBlock := flag.Uint64("to", 0, "end block (exclusive); 0 = src.Items()")
	progressEvery := flag.Uint64("progress", 100000, "log progress every N blocks (0 = silent)")
	flag.Parse()

	if *src == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --src and --dst are required")
		os.Exit(2)
	}

	srcTbl, err := freezer.NewFreezerTableCompressedReadOnly(*src, freezer.TableStorageChanges, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open src storcs:", err)
		os.Exit(1)
	}
	defer srcTbl.Close()
	srcTbl.ForceBatchSize(freezer.BatchSize)

	if err := os.MkdirAll(*dst, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir dst:", err)
		os.Exit(1)
	}
	dstTbl, err := freezer.NewFreezerTableCompressed(*dst, sidecarTable, "c")
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

	srcItems := srcTbl.Items()
	end := *toBlock
	if end == 0 || end > srcItems {
		end = srcItems
	}

	// Auto-resume: dst already has N entries, start from there. Otherwise
	// honor the user-supplied --from.
	start := *fromBlock
	if dstItems := dstTbl.Items(); dstItems > start {
		fmt.Fprintf(os.Stderr, "resuming: dst already has %d entries, --from set to %d\n", dstItems, dstItems)
		start = dstItems
	}
	if start >= end {
		fmt.Fprintf(os.Stderr, "nothing to do: start=%d >= end=%d\n", start, end)
		return
	}

	fmt.Printf("Extracting wipes from blocks [%d, %d)\n", start, end)
	fmt.Printf("src=%s\ndst=%s/%s.cdat\n", *src, *dst, sidecarTable)

	var (
		totalEntries   uint64
		wipeEntries    uint64
		nonEmptyBlocks uint64
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

	// Per-block scratch state hoisted out so 24M iterations don't each
	// pay for a fresh ChangeSet + map allocation. extractWipes resets
	// them at entry.
	cs := changeset.NewStorageChangeSet()
	newVals := make(map[[52]byte][]byte, 32)

	for b := start; b < end; b++ {
		data, err := srcTbl.Retrieve(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "block %d: retrieve: %v\n", b, err)
			os.Exit(1)
		}
		wipeBlob, decoded, kept := extractWipes(data, cs, newVals)
		totalEntries += uint64(decoded)
		wipeEntries += uint64(kept)
		if len(wipeBlob) > 0 {
			nonEmptyBlocks++
		}
		// Even empty blobs must be appended to keep block-N indexing aligned.
		batchEntries = append(batchEntries, wipeBlob)

		if len(batchEntries) >= freezer.BatchSize {
			if err := flushBatch(); err != nil {
				fmt.Fprintf(os.Stderr, "write batch at block %d: %v\n", b, err)
				os.Exit(1)
			}
		}
		if *progressEvery > 0 && b > start && (b-start)%*progressEvery == 0 {
			elapsed := time.Since(t0).Seconds()
			rate := float64(b-start) / elapsed
			fmt.Fprintf(os.Stderr, "  block %d  %.0f blk/s  wipes=%d/%d (%.2f%%)  nonEmptyBlocks=%d\n",
				b, rate, wipeEntries, totalEntries,
				100*float64(wipeEntries)/float64(max(totalEntries, 1)), nonEmptyBlocks)
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
	fmt.Printf("scanned blocks: %d (%d → %d)\n", end-start, start, end-1)
	fmt.Printf("total storcs entries: %d\n", totalEntries)
	if totalEntries > 0 {
		fmt.Printf("wipe entries kept:    %d (%.2f%%)\n",
			wipeEntries, 100*float64(wipeEntries)/float64(totalEntries))
	}
	fmt.Printf("non-empty wipe blocks: %d / %d (%.2f%%)\n",
		nonEmptyBlocks, end-start, 100*float64(nonEmptyBlocks)/float64(end-start))
	fmt.Printf("elapsed: %v\n", time.Since(t0).Truncate(time.Second))
}

// extractWipes decodes one block's storcs blob, filters to wipe entries
// (newLen=0 ∧ oldLen>0), and re-encodes via the same path
// witness-replay/ethexec use so chunking + sorting are consistent.
// cs and newVals are caller-owned scratch buffers; both are reset on
// entry so the same instances can be reused across all blocks.
// Returns the new blob, total decoded entries, and kept wipe count.
func extractWipes(data []byte, cs *changeset.ChangeSet, newVals map[[52]byte][]byte) (blob []byte, total, kept int) {
	cs.Changes = cs.Changes[:0]
	clear(newVals)
	if len(data) == 0 {
		return nil, 0, 0
	}
	entries, err := ethel.DecodeStorageChanges(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: decode storcs: %v\n", err)
		return nil, 0, 0
	}
	total = len(entries)
	for _, e := range entries {
		if len(e.NewValue) != 0 || len(e.OldValue) == 0 {
			continue
		}
		if err := cs.Add(e.CompositeKey, e.OldValue); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: cs.Add: %v\n", err)
			continue
		}
		var k [52]byte
		copy(k[:], e.CompositeKey)
		newVals[k] = nil
		kept++
	}
	if kept == 0 {
		return nil, total, 0
	}
	blob = ethel.EncodeStorageChanges(cs, func(addr types.Address, slot types.Hash) []byte {
		var k [52]byte
		copy(k[:20], addr[:])
		copy(k[20:], slot[:])
		return newVals[k]
	})
	return blob, total, kept
}
