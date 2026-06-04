// blockhash-rebuild: build the cold blockHash → number index from geth ancient
// headers. Applies the hash self-verification pattern — the index stores no
// 32-byte hash, only MPHF(blockHash) → relBlock; the reader confirms by
// recomputing the resolved header's hash == the query.
//
// Profiles:
//
//	archive  full 0..end.
//	window   recent --window-blocks (EIP-4444 Full node); writes
//	         <out>/blockhash.base so the reader derives per-segment startBlock.
//
// --lfp (default false): with the existence fingerprint OFF (~3.25 B/key,
// ~80 MB for 25M blocks) the index is correct ONLY when the reader installs the
// header-hash verifier (api.SetBlockHashIndex does). =true keeps an 8-bit fp
// (~4.25 B/key) and is correct without a verifier.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/internal/blockhashindex"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	ancient := flag.String("ancient", "d:/geth/geth/chaindata/ancient/chain", "geth ancient chain dir (source headers)")
	out := flag.String("out", "", "output dir for the blockhash index (required)")
	profile := flag.String("profile", "archive", "archive | window")
	end := flag.Uint64("end", 0, "end block exclusive (0 = freezer frozen tip)")
	windowBlocks := flag.Uint64("window-blocks", 2_600_000, "window profile: blocks to retain (~1yr)")
	lfp := flag.Bool("lfp", false, "keep the 8-bit existence fingerprint (correct without a verifier; larger). Default off: smaller, needs the header-hash verifier the node installs.")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "blockhash-rebuild: --out is required")
		os.Exit(1)
	}
	if *profile != "archive" && *profile != "window" {
		fmt.Fprintln(os.Stderr, "blockhash-rebuild: --profile must be archive or window")
		os.Exit(1)
	}

	fz, err := freezer.NewReadOnly(*ancient)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open ancient %s: %v\n", *ancient, err)
		os.Exit(1)
	}
	defer fz.Close()

	frozen := fz.Frozen()
	endBlock := *end
	if endBlock == 0 || endBlock > frozen {
		endBlock = frozen
	}

	startBlock := uint64(0)
	if *profile == "window" {
		if *windowBlocks < endBlock {
			startBlock = endBlock - *windowBlocks
		}
		startBlock = (startBlock / blockhashindex.SegmentSize) * blockhashindex.SegmentSize
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir out: %v\n", err)
		os.Exit(1)
	}
	if *profile == "window" && startBlock > 0 {
		baseFile := filepath.Join(*out, "blockhash.base")
		if err := os.WriteFile(baseFile, []byte(fmt.Sprintf("%d\n", startBlock)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write base file: %v\n", err)
			os.Exit(1)
		}
		log.Info("Wrote window base", "file", baseFile, "base", startBlock)
	}

	log.Info("blockhash-rebuild start",
		"profile", *profile, "source", *ancient, "out", *out,
		"range", fmt.Sprintf("%d..%d", startBlock, endBlock), "blocks", endBlock-startBlock, "lfp", *lfp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Warn("signal received — finishing current segment then stopping (resumable)")
		cancel()
	}()

	t0 := time.Now()
	b := blockhashindex.NewBuilder(fz, *out, *lfp)
	if err := b.BuildRange(ctx, startBlock, endBlock); err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n", err)
		os.Exit(1)
	}

	var totalBytes int64
	entries, _ := os.ReadDir(*out)
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); ext == ".cdat" || ext == ".cidx" {
			if info, err := e.Info(); err == nil {
				totalBytes += info.Size()
			}
		}
	}
	keys := endBlock - startBlock
	bitsPerKey := 0.0
	if keys > 0 {
		bitsPerKey = float64(totalBytes) * 8 / float64(keys)
	}
	log.Info("blockhash-rebuild done",
		"elapsed", time.Since(t0).Truncate(time.Second),
		"keys", keys,
		"size", fmt.Sprintf("%.1f MB", float64(totalBytes)/1e6),
		"bits/key", fmt.Sprintf("%.2f", bitsPerKey))
}
