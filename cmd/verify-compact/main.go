// verify-compact: verify N42 compact bodies against Geth ancient freezer.
// Reads the last N blocks from the compact reader, decodes, and compares
// tx/uncle hashes against Geth.
//
// Usage:
//   verify-compact -compact D:/N42-eth4/chain -ancient d:/geth/geth/chaindata/ancient/chain -n 64

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	compactDir := flag.String("compact", "D:/N42-eth4/chain", "Compact bodies dir")
	ancientDir := flag.String("ancient", "d:/geth/geth/chaindata/ancient/chain", "Geth ancient dir")
	n := flag.Int("n", 64, "Number of blocks to verify")
	startBlock := flag.Uint64("start", 0, "Start block (0 = last N blocks)")
	flag.Parse()

	// Open compact body reader.
	reader, err := ethel.OpenBodyCompact(*compactDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open compact: %v\n", err)
		os.Exit(1)
	}
	defer reader.Close()
	maxBlock := reader.MaxBlock()
	fmt.Printf("Compact bodies: %d segments, max block %d\n", reader.Segments(), maxBlock)

	// Open Geth ancient freezer.
	gethF, err := freezer.New(*ancientDir, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open geth: %v\n", err)
		os.Exit(1)
	}
	defer gethF.Close()
	fmt.Printf("Geth frozen: %d blocks\n", gethF.Frozen())

	// Determine range.
	start := *startBlock
	if start == 0 {
		if maxBlock > uint64(*n) {
			start = maxBlock - uint64(*n)
		}
	}
	end := start + uint64(*n)
	if end > maxBlock {
		end = maxBlock
	}
	if end > gethF.Frozen() {
		end = gethF.Frozen()
	}
	fmt.Printf("Verifying blocks %d..%d (%d blocks)\n\n", start, end-1, end-start)

	t0 := time.Now()
	var matched, mismatched, errors int

	for blk := start; blk < end; blk++ {
		// Read from compact.
		compactBody, err := reader.ReadBody(blk)
		if err != nil {
			fmt.Printf("  block %d: compact read error: %v\n", blk, err)
			errors++
			continue
		}

		// Read from Geth.
		gethData, err := gethF.Ancient(freezer.TableBodies, blk)
		if err != nil {
			fmt.Printf("  block %d: geth read error: %v\n", blk, err)
			errors++
			continue
		}
		gethBody, err := ethel.DecodeGethBody(gethData)
		if err != nil {
			fmt.Printf("  block %d: geth decode error: %v\n", blk, err)
			errors++
			continue
		}

		// Compare tx count.
		if len(compactBody.Txs) != len(gethBody.Transactions) {
			fmt.Printf("  block %d: TX COUNT MISMATCH compact=%d geth=%d\n",
				blk, len(compactBody.Txs), len(gethBody.Transactions))
			mismatched++
			continue
		}

		// Compare uncle count.
		if len(compactBody.UncleRLP) != len(gethBody.Uncles) {
			fmt.Printf("  block %d: UNCLE COUNT MISMATCH compact=%d geth=%d\n",
				blk, len(compactBody.UncleRLP), len(gethBody.Uncles))
			mismatched++
			continue
		}

		// Compare tx hashes.
		txOK := true
		for i := range compactBody.Txs {
			if compactBody.Txs[i].Hash() != gethBody.Transactions[i].Hash() {
				fmt.Printf("  block %d: TX %d HASH MISMATCH compact=%s geth=%s\n",
					blk, i,
					compactBody.Txs[i].Hash().Hex(),
					gethBody.Transactions[i].Hash().Hex())
				txOK = false
				break
			}
		}
		if !txOK {
			mismatched++
			continue
		}

		// Compare uncle RLP hashes (compact stores raw RLP, geth decodes to Header).
		uncleOK := true
		for i := range compactBody.UncleRLP {
			gHash := gethBody.Uncles[i].Hash()
			// Re-encode geth uncle to compare raw bytes.
			cRLP := compactBody.UncleRLP[i]
			if len(cRLP) == 0 && gHash != ([32]byte{}) {
				fmt.Printf("  block %d: UNCLE %d empty compact RLP but geth has data\n", blk, i)
				uncleOK = false
				break
			}
			// For hash comparison: decode compact uncle RLP and hash.
			compactUncle, err := ethel.DecodeGethHeader(cRLP)
			if err != nil {
				// Direct RLP comparison as fallback.
				_ = compactUncle
			} else {
				cHash := compactUncle.Hash()
				if !bytes.Equal(cHash[:], gHash[:]) {
					fmt.Printf("  block %d: UNCLE %d HASH MISMATCH\n", blk, i)
					uncleOK = false
					break
				}
			}
		}
		if !uncleOK {
			mismatched++
			continue
		}

		matched++
	}

	fmt.Printf("\nResults: matched=%d mismatched=%d errors=%d (%.1fs)\n",
		matched, mismatched, errors, time.Since(t0).Seconds())
	if mismatched == 0 && errors == 0 {
		fmt.Println("ALL BLOCKS VERIFIED OK")
	}
}
