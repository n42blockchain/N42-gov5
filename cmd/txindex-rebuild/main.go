// txindex-rebuild: rebuild the tx-hash → block txindex with a space-optimal
// RecSplit config, in two deployment profiles.
//
// Source: geth ancient bodies (freezer.TableBodies). Output: a cscompact
// SegmentStore named "txindex" the existing internal/txlookup.Service reads
// unchanged.
//
// Why rebuild: the on-disk txindex was built with Enums=false, which stores a
// fixed-width ~28-bit ordinal per key → ~33.7 bit/key (measured, 12.3 GB for
// 3.13 B txs). Enums=true replaces that with an Elias-Fano enumeration of the
// dense ordinals (~2.5 bit/key) → ~12 bit/key (LFP on). dat is already V2
// Elias-Fano block boundaries (~350 KB/seg), unchanged.
//
// Profiles:
//
//	archive  full 0..end (immutable history + tip). Service-compatible as-is.
//	window   only the recent --window-blocks (EIP-4444 Full node). Writes
//	         <out>/txindex.base so Service derives the right per-segment
//	         startBlock (base + i*SegmentSize).
//
// LessFalsePositives (--lfp, default true) keeps the 8-bit existence
// fingerprint. It is REQUIRED for correct multi-segment lookup as it stands:
// with it off every out-of-set hash phantom-hits (the MPHF always maps to
// [0,N)), so a newer segment falsely answers for a tx in an older one. The
// verify-and-continue lookup that makes the no-LFP index (~4.4 bit/key,
// ~1.9 GB archive) correct is implemented (txlookup.Service.SetVerifier:
// confirm the candidate block holds the hash, else keep probing). So --lfp=false
// is a valid, smaller build PROVIDED the reader installs a verifier; the tool
// logs a loud warning as a reminder.
//
// Size expectations (3.495 B txs to 25.2M):
//
//	archive  Enums+LFP   ~5.4 GB    Enums, no LFP  ~1.9 GB
//	window   Enums+LFP   ~1.0 GB    Enums, no LFP  ~0.36 GB
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	ancient := flag.String("ancient", "d:/geth/geth/chaindata/ancient/chain", "geth ancient chain dir (source bodies)")
	out := flag.String("out", "", "output dir for the rebuilt txindex (required)")
	profile := flag.String("profile", "archive", "archive | window")
	end := flag.Uint64("end", 0, "end block exclusive (0 = freezer frozen tip)")
	windowBlocks := flag.Uint64("window-blocks", 2_600_000, "window profile: blocks to retain (~1yr = 2.6M)")
	enums := flag.Bool("enums", true, "RecSplit Enums (EF enumeration of ordinals — the main space win)")
	lfp := flag.Bool("lfp", true, "RecSplit LessFalsePositives (8-bit existence fp; required for correct multi-segment lookup)")
	sample := flag.Uint64("sample", 0, "if >0, build only this many 1M-block segments from the start (size validation, not a usable index)")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "txindex-rebuild: --out is required")
		os.Exit(1)
	}
	if *profile != "archive" && *profile != "window" {
		fmt.Fprintln(os.Stderr, "txindex-rebuild: --profile must be archive or window")
		os.Exit(1)
	}
	if !*lfp {
		log.Warn("LessFalsePositives DISABLED (~4.4 bit/key) — every out-of-set hash phantom-hits. This index is correct ONLY when the consuming txlookup.Service has a verifier installed (Service.SetVerifier — verify-and-continue is implemented and unit-tested). Do not serve it from a reader without a verifier.")
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
		if *windowBlocks >= endBlock {
			startBlock = 0
		} else {
			startBlock = endBlock - *windowBlocks
		}
		// Align down to a segment boundary so segment 0 starts on a clean base.
		startBlock = (startBlock / txlookup.SegmentSize) * txlookup.SegmentSize
	}

	if *sample > 0 {
		capEnd := startBlock + *sample*txlookup.SegmentSize
		if capEnd < endBlock {
			endBlock = capEnd
		}
		log.Info("SAMPLE mode — building a few segments for size validation only", "segments", *sample)
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir out: %v\n", err)
		os.Exit(1)
	}

	log.Info("txindex-rebuild start",
		"profile", *profile,
		"source", *ancient,
		"out", *out,
		"range", fmt.Sprintf("%d..%d", startBlock, endBlock),
		"blocks", endBlock-startBlock,
		"enums", *enums, "lfp", *lfp)

	// Window profile records its base block so the reader derives the right
	// per-segment startBlock. Archive base is 0 (file omitted = 0).
	if *profile == "window" && startBlock > 0 {
		baseFile := filepath.Join(*out, "txindex.base")
		if err := os.WriteFile(baseFile, []byte(fmt.Sprintf("%d\n", startBlock)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write base file: %v\n", err)
			os.Exit(1)
		}
		log.Info("Wrote window base", "file", baseFile, "base", startBlock)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Warn("signal received — finishing current segment then stopping (resumable on re-run)")
		cancel()
	}()

	builder := txlookup.NewSegmentBuilder(fz, *out, ethelBodyTxHashes)
	builder.SetRecSplitTuning(*enums, *lfp)

	t0 := time.Now()
	if err := builder.BuildRange(ctx, startBlock, endBlock); err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n", err)
		os.Exit(1)
	}
	log.Info("Build done", "elapsed", time.Since(t0).Truncate(time.Second))

	reportStats(*out, startBlock)
}

// reportStats walks the output cdat files for total size and re-opens the store
// to sum per-segment tx counts (V2 dat header) → achieved bits/key.
func reportStats(out string, base uint64) {
	var totalBytes int64
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Only this store's files. --out is normally a SHARED freezer dir
		// (bodyc, receipts, senders live there too), so summing every .cdat
		// reports the whole freezer: the 2026-08-30 weekly run printed
		// "941.43 GB / 2031.78 bits/key" for a 15.24 GB index.
		name := e.Name()
		if !strings.HasPrefix(name, "txindex.") {
			continue
		}
		if filepath.Ext(name) == ".cdat" || filepath.Ext(name) == ".cidx" {
			if info, err := e.Info(); err == nil {
				totalBytes += info.Size()
			}
		}
	}

	store, err := cscompact.OpenSegmentStore(out, "txindex")
	if err != nil {
		log.Warn("reopen for stats failed", "err", err)
		return
	}
	defer store.Close()

	var totalTx uint64
	for i := uint64(0); i < store.SegmentCount(); i++ {
		data, err := store.ReadSegmentData(i)
		if err != nil || len(data) < 16 {
			continue
		}
		// V2 dat: [4]magic [4]blockCount [8]txCount ...
		if string(data[:4]) == "EFD2" {
			totalTx += uint64(data[8]) | uint64(data[9])<<8 | uint64(data[10])<<16 | uint64(data[11])<<24 |
				uint64(data[12])<<32 | uint64(data[13])<<40 | uint64(data[14])<<48 | uint64(data[15])<<56
		}
	}

	bitsPerKey := 0.0
	if totalTx > 0 {
		bitsPerKey = float64(totalBytes) * 8 / float64(totalTx)
	}
	log.Info("txindex-rebuild stats",
		"segments", store.SegmentCount(),
		"base", base,
		"totalTx", totalTx,
		"size", fmt.Sprintf("%.2f GB", float64(totalBytes)/1e9),
		"bits/key", fmt.Sprintf("%.2f", bitsPerKey))
}

// ethelBodyTxHashes decodes a geth-format stored body into its transaction
// hashes. Supplied to the txindex builder by the caller so internal/txlookup
// does not have to import internal/ethel (which would close an import cycle
// with the root internal package's live index).
func ethelBodyTxHashes(bodyData []byte) ([]types.Hash, error) {
	body, err := ethel.DecodeGethBody(bodyData)
	if err != nil {
		return nil, err
	}
	out := make([]types.Hash, len(body.Transactions))
	for i, tx := range body.Transactions {
		out[i] = tx.Hash()
	}
	return out, nil
}
