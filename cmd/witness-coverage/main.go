// witness-coverage walks the witness table cidx alongside the body
// freezer and reports contiguous (good, bad) block ranges. A bad range
// is one where the witness payload is empty but the body has at least
// one transaction — exactly the failure mode introduced when ethexec
// was started with --no-witness past some checkpoint, or when the
// recorder crashed between commit intervals.
//
// Use this to decide what range to re-record: the bad ranges plus a
// few blocks of overlap on each side.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// gasUsedFromHeader decodes the geth-ancient header RLP and returns its
// GasUsed field. Returns (0, false) on any decode error so the caller can
// fall back to a body-length heuristic.
func gasUsedFromHeader(raw []byte) (uint64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	h, err := ethel.DecodeGethHeader(raw)
	if err != nil {
		return 0, false
	}
	return h.GasUsed, true
}

func main() {
	witDir := flag.String("witness-dir", "", "freezer dir containing witness.cidx (REQUIRED)")
	hbDir := flag.String("hb-dir", "", "freezer dir for headers+bodies (defaults to witness-dir)")
	from := flag.Uint64("from", 0, "start block (inclusive)")
	to := flag.Uint64("to", 0, "end block (inclusive); 0 = witness.Items()-1")
	progress := flag.Uint64("progress", 1_000_000, "log every N blocks")
	stride := flag.Uint64("stride", 1, "block stride — 1 = scan all, 10000 = sample every 10K. Use sampling for fast first-pass to find bad regions.")
	flag.Parse()
	if *witDir == "" {
		fmt.Fprintln(os.Stderr, "--witness-dir is required")
		os.Exit(2)
	}
	if *hbDir == "" {
		*hbDir = *witDir
	}

	wit, err := freezer.NewFreezerTableCompressedReadOnly(*witDir, freezer.TableBlockWitness, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open witness:", err)
		os.Exit(1)
	}
	defer wit.Close()
	wit.ForceBatchSize(freezer.BatchSize)

	// Open the bodies freezer via the high-level Freezer (auto-detects
	// batch size + table metadata from cidx headers). The earlier
	// direct-table open with NewFreezerTableCompressedReadOnly +
	// ForceBatchSize(64) silently returned 0 bytes for geth ancient
	// bodies (geth uses a different batch encoding), which made every
	// block look like "empty body = good even with empty witness".
	hb, err := freezer.NewReadOnly(*hbDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open bodies freezer at %s: %v\n", *hbDir, err)
		os.Exit(1)
	}
	defer hb.Close()

	end := *to
	if end == 0 {
		end = wit.Items() - 1
	}
	fmt.Printf("scanning blocks [%d, %d]  witness=%s  hb=%s  stride=%d\n",
		*from, end, *witDir, *hbDir, *stride)

	// State machine: "good" = witness non-empty OR body empty; "bad" =
	// witness empty AND body has txs.
	t0 := time.Now()
	type rangeT struct{ from, to uint64 }
	var bad []rangeT
	var inBad bool
	var badStart uint64
	var totalGood, totalBad, totalEmptyBlocks uint64
	for b := *from; b <= end; b += *stride {
		wd, err := wit.Retrieve(b)
		if err != nil {
			fmt.Printf("witness retrieve %d err: %v\n", b, err)
			break
		}
		hasWit := len(wd) > 0

		// Empty-witness is correct when the block has no execution (gasUsed
		// == 0). This covers genesis and post-Merge empty proposer blocks.
		// Body byte-length check (< 5) was too coarse: geth ancient encodes
		// post-Merge bodies with a tx list + uncles + withdrawals RLP that
		// runs 6+ bytes even when tx count is 0, producing false-positive
		// "bad" classifications.
		emptyBody := false
		if !hasWit {
			hdrRaw, _ := hb.Ancient(freezer.TableHeaders, b)
			gasUsed, ok := gasUsedFromHeader(hdrRaw)
			if ok && gasUsed == 0 {
				emptyBody = true
			} else if !ok {
				// Fall back to old heuristic when header decode fails.
				bodyRaw, _ := hb.Ancient(freezer.TableBodies, b)
				emptyBody = len(bodyRaw) < 5
			}
			if emptyBody {
				totalEmptyBlocks++
			}
		}

		good := hasWit || emptyBody
		if good {
			totalGood++
			if inBad {
				bad = append(bad, rangeT{from: badStart, to: b - 1})
				inBad = false
			}
		} else {
			totalBad++
			if !inBad {
				badStart = b
				inBad = true
			}
		}

		if *progress > 0 && b > 0 && b%*progress == 0 {
			fmt.Fprintf(os.Stderr, "  scanned to %d  good=%d  bad=%d  emptyBodies=%d  ranges=%d  elapsed=%v\n",
				b, totalGood, totalBad, totalEmptyBlocks, len(bad)+func() int {
					if inBad {
						return 1
					}
					return 0
				}(), time.Since(t0).Truncate(time.Second))
		}
	}
	if inBad {
		bad = append(bad, rangeT{from: badStart, to: end})
	}

	fmt.Printf("\n=== bad ranges (witness empty + body non-empty) ===\n")
	for _, r := range bad {
		fmt.Printf("  blocks [%d, %d]  count=%d\n", r.from, r.to, r.to-r.from+1)
	}
	fmt.Printf("\n=== summary ===\n")
	fmt.Printf("good blocks: %d\n", totalGood)
	fmt.Printf("bad blocks:  %d (need re-record)\n", totalBad)
	fmt.Printf("empty bodies (good by default): %d\n", totalEmptyBlocks)
	fmt.Printf("bad ranges:  %d\n", len(bad))
	fmt.Printf("elapsed:     %v\n", time.Since(t0).Truncate(time.Second))
}
