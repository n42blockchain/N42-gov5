// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42-ancient-seal converts a full qs-chain chaindata into the era
// layout (docs/QS_ANCIENT_ERA_DESIGN.md): sealed era files for every
// complete era behind the freeze threshold, plus (optionally) a
// born-compact hot MDBX that omits the sealed ranges.
//
// Two phases, independently runnable:
//
//	--seal      write era files (resume-safe: finished eras are skipped)
//	--emit-hot  build the hot chaindata copy (restarted from scratch if
//	            a previous attempt did not finish)
//
// The source database is never modified. Era files land in
// <out>/ancient-era, the hot DB in <out>/chaindata, and a summary in
// <out>/SEAL_DONE.json once --emit-hot completes.
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
)

// sealedTables are dropped from the hot DB for sealed ranges. All are
// keyed by an 8-byte big-endian block number prefix except BlockTx
// (sequence-keyed, handled via the base-tx cutoff).
var sealedTables = []string{
	modules.Headers, modules.BlockBody, modules.Receipts, modules.Log,
	modules.ConsensusEvidence, modules.BlockWitness,
	modules.AccountChangeSet, modules.StorageChangeSet,
}

func main() {
	src := flag.String("source", "", "source datadir or chaindata path (read-only)")
	out := flag.String("out", "", "output directory (gets ancient-era/ and chaindata/)")
	span := flag.Uint64("span", ancientera.DefaultSpan, "blocks per era (power-of-two multiple of 64)")
	threshold := flag.Uint64("threshold", 90_000, "blocks behind head that stay hot")
	doSeal := flag.Bool("seal", false, "phase 1: write era files")
	doHot := flag.Bool("emit-hot", false, "phase 2: build hot chaindata copy")
	creator := flag.String("creator", "n42-ancient-seal/1", "creator tag (descriptive only)")
	commitEvery := flag.Int("commit-rows", 2_000_000, "hot copy: commit interval in rows")
	flag.Parse()

	if *src == "" || *out == "" || (!*doSeal && !*doHot) {
		fmt.Fprintln(os.Stderr, "usage: n42-ancient-seal --source <dir> --out <dir> [--seal] [--emit-hot]")
		os.Exit(2)
	}
	srcPath := *src
	if st, err := os.Stat(filepath.Join(srcPath, "chaindata")); err == nil && st.IsDir() {
		srcPath = filepath.Join(srcPath, "chaindata")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	logger := log.New()

	srcDB, err := mdbx.NewMDBX(logger).Path(srcPath).Label(kv.ChainDB).
		MapSize(4 * datasize.TB).Accede().Readonly().Open(ctx)
	if err != nil {
		die("open source: %v", err)
	}
	defer srcDB.Close()

	info, err := inspect(ctx, srcDB, *span, *threshold)
	if err != nil {
		die("inspect: %v", err)
	}
	fmt.Printf("source: head=%d chainId=%d genesis=%s\n", info.head, info.chainID, hex.EncodeToString(info.genesis[:8]))
	fmt.Printf("span=%d threshold=%d → sealable eras: %d (blocks [0,%d))\n",
		*span, *threshold, info.numEras, info.sealedEnd)

	eraDir := filepath.Join(*out, ancientera.StoreDirName)
	if *doSeal {
		if err := sealEras(ctx, srcDB, eraDir, info, *span, *creator); err != nil {
			die("seal: %v", err)
		}
	}
	if *doHot {
		if err := emitHot(ctx, srcDB, filepath.Join(*out, "chaindata"), eraDir, info, *span, *commitEvery, logger); err != nil {
			die("emit-hot: %v", err)
		}
		summary := map[string]any{
			"source": srcPath, "head": info.head, "sealed_end": info.sealedEnd,
			"span": *span, "eras": info.numEras, "finished": time.Now().UTC().Format(time.RFC3339),
		}
		b, _ := json.MarshalIndent(summary, "", "  ")
		if err := os.WriteFile(filepath.Join(*out, "SEAL_DONE.json"), b, 0o644); err != nil {
			die("marker: %v", err)
		}
	}
	fmt.Println("done")
}

type chainInfo struct {
	head      uint64
	genesis   types.Hash
	chainID   uint64
	numEras   uint64
	sealedEnd uint64 // numEras * span
	cutoffTx  uint64 // BaseTxId of first unsealed block
}

func inspect(ctx context.Context, db kv.RoDB, span, threshold uint64) (*chainInfo, error) {
	info := &chainInfo{}
	err := db.View(ctx, func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.HeaderCanonical)
		if err != nil {
			return err
		}
		defer c.Close()
		k, _, err := c.Last()
		if err != nil || k == nil {
			return fmt.Errorf("empty CanonicalHeader (%v)", err)
		}
		info.head = binary.BigEndian.Uint64(k[:8])
		gh, err := tx.GetOne(modules.HeaderCanonical, modules.EncodeBlockNumber(0))
		if err != nil || len(gh) != 32 {
			return fmt.Errorf("genesis canonical hash missing (%v)", err)
		}
		info.genesis = types.BytesToHash(gh)
		cfg, err := rawdb.ReadChainConfig(tx, info.genesis)
		if err == nil && cfg != nil && cfg.ChainID != nil {
			info.chainID = cfg.ChainID.Uint64()
		}
		for (info.numEras+1)*span+threshold <= info.head {
			info.numEras++
		}
		info.sealedEnd = info.numEras * span
		if info.numEras > 0 {
			body, err := readStorageBody(tx, info.sealedEnd)
			if err != nil {
				return fmt.Errorf("body of first unsealed block %d: %w", info.sealedEnd, err)
			}
			info.cutoffTx = body.base
		}
		return nil
	})
	return info, err
}

type storageBody struct {
	base   uint64
	amount uint32
}

func readStorageBody(tx kv.Tx, num uint64) (storageBody, error) {
	hash, err := tx.GetOne(modules.HeaderCanonical, modules.EncodeBlockNumber(num))
	if err != nil || len(hash) != 32 {
		return storageBody{}, fmt.Errorf("no canonical hash for %d", num)
	}
	raw, err := tx.GetOne(modules.BlockBody, modules.BlockBodyKey(num, types.BytesToHash(hash)))
	if err != nil {
		return storageBody{}, err
	}
	if len(raw) != 12 {
		return storageBody{}, fmt.Errorf("body raw len %d != 12", len(raw))
	}
	return storageBody{base: binary.BigEndian.Uint64(raw[:8]), amount: binary.BigEndian.Uint32(raw[8:])}, nil
}

// sealEras writes era files for eras [0, info.numEras), skipping eras
// whose three files already exist (resume).
func sealEras(ctx context.Context, db kv.RoDB, eraDir string, info *chainInfo, span uint64, creator string) error {
	existing := map[string]bool{}
	if des, err := os.ReadDir(eraDir); err == nil {
		for _, de := range des {
			existing[de.Name()] = true
		}
	}
	eraComplete := func(era uint64) bool {
		// File names embed the first canonical hash, so match by prefix.
		found := 0
		for _, cl := range ancientera.Classes {
			prefix := fmt.Sprintf("%s-%08d-", cl, era)
			for name := range existing {
				if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ancientera.FileExt) {
					found++
					break
				}
			}
		}
		return found == len(ancientera.Classes)
	}

	tx, err := db.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for era := uint64(0); era < info.numEras; era++ {
		if eraComplete(era) {
			fmt.Printf("era %d: already sealed, skipping\n", era)
			continue
		}
		if err := sealOneEra(ctx, tx, eraDir, era, span, info, creator); err != nil {
			return fmt.Errorf("era %d: %w", era, err)
		}
	}
	// Manifest: rebuild from footers (authoritative) and save.
	m, err := ancientera.RebuildManifest(eraDir)
	if err != nil {
		return err
	}
	if _, err := m.Save(eraDir); err != nil {
		return err
	}
	fmt.Printf("manifest: %d entries\n", len(m.Entries))
	return nil
}

func sealOneEra(ctx context.Context, tx kv.Tx, eraDir string, era, span uint64, info *chainInfo, creator string) error {
	start := era * span
	t0 := time.Now()
	var writers [3]*ancientera.Writer
	classes := []ancientera.Class{ancientera.ClassChain, ancientera.ClassExec, ancientera.ClassAux}
	for i, cl := range classes {
		w, err := ancientera.NewWriter(eraDir, cl, era, span, info.chainID, info.genesis, creator)
		if err != nil {
			return err
		}
		writers[i] = w
	}
	abort := func() {
		for _, w := range writers {
			w.Abort()
		}
	}

	txCur, err := tx.Cursor(modules.BlockTx)
	if err != nil {
		abort()
		return err
	}
	defer txCur.Close()
	logCur, err := tx.Cursor(modules.Log)
	if err != nil {
		abort()
		return err
	}
	defer logCur.Close()
	storcsCur, err := tx.Cursor(modules.StorageChangeSet)
	if err != nil {
		abort()
		return err
	}
	defer storcsCur.Close()
	acctcsCur, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		abort()
		return err
	}
	defer acctcsCur.Close()

	var parentHash types.Hash
	if start > 0 {
		ph, err := tx.GetOne(modules.HeaderCanonical, modules.EncodeBlockNumber(start-1))
		if err != nil || len(ph) != 32 {
			abort()
			return fmt.Errorf("parent hash of era start: %v", err)
		}
		parentHash = types.BytesToHash(ph)
	}

	for n := start; n < start+span; n++ {
		if n%FrameProgress == 0 {
			select {
			case <-ctx.Done():
				abort()
				return ctx.Err()
			default:
			}
		}
		numKey := modules.EncodeBlockNumber(n)
		ch, err := tx.GetOne(modules.HeaderCanonical, numKey)
		if err != nil || len(ch) != 32 {
			abort()
			return fmt.Errorf("canonical hash %d: %v", n, err)
		}
		hash := types.BytesToHash(ch)

		// Class A: header + evidence.
		hdr, err := tx.GetOne(modules.Headers, modules.HeaderKey(n, hash))
		if err != nil || hdr == nil {
			abort()
			return fmt.Errorf("header %d: %v", n, err)
		}
		ev, err := tx.GetOne(modules.ConsensusEvidence, numKey)
		if err != nil {
			abort()
			return err
		}
		chainEntry := ancientera.BlockEntry{
			ancientera.TblCanonicalHash: ch,
			ancientera.TblHeader:        hdr,
		}
		if ev != nil {
			chainEntry[ancientera.TblEvidence] = ev
		}

		// Class B: body + txs + receipts + logs.
		bodyRaw, err := tx.GetOne(modules.BlockBody, modules.BlockBodyKey(n, hash))
		if err != nil || len(bodyRaw) != 12 {
			abort()
			return fmt.Errorf("body %d: len=%d err=%v", n, len(bodyRaw), err)
		}
		base := binary.BigEndian.Uint64(bodyRaw[:8])
		amount := binary.BigEndian.Uint32(bodyRaw[8:])
		if amount < 2 {
			abort()
			return fmt.Errorf("block %d: txAmount %d < 2 (missing system tx slots)", n, amount)
		}
		execEntry := ancientera.BlockEntry{ancientera.TblBody: bodyRaw}
		// Real txs live at [base+1, base+amount-1): one system tx slot at
		// each end of the block (mirrors rawdb.ReadBody).
		if real := amount - 2; real > 0 {
			txs := make([][]byte, 0, real)
			firstID := base + 1
			seekKey := modules.EncodeBlockNumber(firstID)
			for k, v, err := txCur.Seek(seekKey); k != nil && uint32(len(txs)) < real; k, v, err = txCur.Next() {
				if err != nil {
					abort()
					return err
				}
				id := binary.BigEndian.Uint64(k)
				if id != firstID+uint64(len(txs)) {
					abort()
					return fmt.Errorf("block %d: tx id gap: got %d want %d", n, id, firstID+uint64(len(txs)))
				}
				txs = append(txs, append([]byte(nil), v...))
			}
			if uint32(len(txs)) != real {
				abort()
				return fmt.Errorf("block %d: found %d of %d txs", n, len(txs), real)
			}
			execEntry[ancientera.TblTxs] = ancientera.EncodeU32List(txs)
		}
		if rc, err := tx.GetOne(modules.Receipts, numKey); err != nil {
			abort()
			return err
		} else if rc != nil {
			execEntry[ancientera.TblReceipts] = rc
		}
		var logs []ancientera.LogRec
		for k, v, err := logCur.Seek(numKey); k != nil; k, v, err = logCur.Next() {
			if err != nil {
				abort()
				return err
			}
			if binary.BigEndian.Uint64(k[:8]) != n {
				break
			}
			logs = append(logs, ancientera.LogRec{
				TxID: binary.BigEndian.Uint32(k[8:12]),
				Data: append([]byte(nil), v...),
			})
		}
		if len(logs) > 0 {
			execEntry[ancientera.TblLogs] = ancientera.EncodeLogs(logs)
		}

		// Class C: witness + changesets.
		auxEntry := ancientera.BlockEntry{}
		if wit, err := tx.GetOne(modules.BlockWitness, numKey); err != nil {
			abort()
			return err
		} else if wit != nil {
			auxEntry[ancientera.TblWitness] = wit
		}
		var acs [][]byte
		for k, v, err := acctcsCur.Seek(numKey); k != nil; k, v, err = acctcsCur.NextDup() {
			if err != nil {
				abort()
				return err
			}
			if binary.BigEndian.Uint64(k[:8]) != n {
				break
			}
			acs = append(acs, append([]byte(nil), v...))
		}
		if len(acs) > 0 {
			auxEntry[ancientera.TblAcctCS] = ancientera.EncodeU32List(acs)
		}
		var scs []ancientera.KVPair
		for k, v, err := storcsCur.Seek(numKey); k != nil; k, v, err = storcsCur.Next() {
			if err != nil {
				abort()
				return err
			}
			if binary.BigEndian.Uint64(k[:8]) != n {
				break
			}
			scs = append(scs, ancientera.KVPair{
				Suffix: append([]byte(nil), k[8:]...),
				Value:  append([]byte(nil), v...),
			})
		}
		if len(scs) > 0 {
			auxEntry[ancientera.TblStorCS] = ancientera.EncodeKVPairs(scs)
		}

		var h32 [32]byte
		copy(h32[:], ch)
		for i, e := range []ancientera.BlockEntry{chainEntry, execEntry, auxEntry} {
			if err := writers[i].AddBlock(e, h32); err != nil {
				abort()
				return err
			}
		}
	}
	for _, w := range writers {
		if _, meta, err := w.Finalize(parentHash); err != nil {
			abort()
			return err
		} else {
			fmt.Printf("era %d %s: sealed [%d,%d) payload=%s…\n",
				era, meta.Class, meta.StartBlock, meta.EndBlock, meta.PayloadBlake3[:14])
		}
	}
	fmt.Printf("era %d done in %s\n", era, time.Since(t0).Truncate(time.Second))
	return nil
}

// FrameProgress is how often the seal loop polls for cancellation.
const FrameProgress = 4096

// emitHot builds the hot chaindata copy: full copy of every bucket
// except the sealed block tables, which keep only genesis rows and
// rows >= sealedEnd (BlockTx: >= cutoffTx). Restarted from scratch if a
// previous attempt didn't finish (no SEAL_DONE.json).
func emitHot(ctx context.Context, srcDB kv.RoDB, dstPath, eraDir string, info *chainInfo, span uint64, commitEvery int, logger log.Logger) error {
	if _, err := os.Stat(dstPath); err == nil {
		fmt.Println("emit-hot: removing unfinished previous output")
		if err := os.RemoveAll(dstPath); err != nil {
			return err
		}
	}
	dstDB, err := mdbx.NewMDBX(logger).Path(dstPath).Label(kv.ChainDB).
		MapSize(4 * datasize.TB).Open(ctx)
	if err != nil {
		return err
	}
	defer dstDB.Close()

	sealedSet := map[string]bool{}
	for _, t := range sealedTables {
		sealedSet[t] = true
	}

	var buckets []string
	if err := srcDB.View(ctx, func(tx kv.Tx) error {
		var e error
		buckets, e = tx.(kv.BucketMigrator).ListBuckets()
		return e
	}); err != nil {
		// Fall back to the static table list.
		for name := range modules.N42TableCfg {
			buckets = append(buckets, name)
		}
	}

	for _, b := range buckets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cfg, known := modules.N42TableCfg[b]
		if !known {
			return fmt.Errorf("bucket %q not in N42TableCfg — refusing blind copy", b)
		}
		dup := cfg.Flags&kv.DupSort != 0
		var from []byte
		mode := "full"
		switch {
		case b == modules.BlockTx && info.numEras > 0:
			from = modules.EncodeBlockNumber(info.cutoffTx)
			mode = fmt.Sprintf("seq>=%d", info.cutoffTx)
		case sealedSet[b] && info.numEras > 0:
			from = modules.EncodeBlockNumber(info.sealedEnd)
			mode = fmt.Sprintf("genesis+num>=%d", info.sealedEnd)
		}
		n, err := copyBucket(ctx, srcDB, dstDB, b, dup, from, sealedSet[b] && info.numEras > 0, commitEvery)
		if err != nil {
			return fmt.Errorf("copy %s: %w", b, err)
		}
		fmt.Printf("copied %-22s %12d rows (%s)\n", b, n, mode)
	}

	// Anchor the era manifest + geometry in DbInfo.
	mh, err := ancientera.ManifestHash(eraDir)
	if err != nil {
		return fmt.Errorf("manifest hash: %w", err)
	}
	return dstDB.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.Put(modules.DatabaseInfo, []byte("eraManifestBlake3"), mh[:]); err != nil {
			return err
		}
		if err := tx.Put(modules.DatabaseInfo, []byte("eraSealedEnd"), modules.EncodeBlockNumber(info.sealedEnd)); err != nil {
			return err
		}
		return tx.Put(modules.DatabaseInfo, []byte("eraSpan"), modules.EncodeBlockNumber(span))
	})
}

// copyBucket copies src→dst. When from is non-nil the copy is ranged:
// genesis-prefixed rows (block 0) first if withGenesis, then everything
// at or after from. Uses Append/AppendDup (source cursor order is
// ascending), committing every commitEvery rows.
func copyBucket(ctx context.Context, srcDB kv.RoDB, dstDB kv.RwDB, bucket string, dup bool, from []byte, withGenesis bool, commitEvery int) (uint64, error) {
	srcTx, err := srcDB.BeginRo(ctx)
	if err != nil {
		return 0, err
	}
	defer srcTx.Rollback()
	sc, err := srcTx.Cursor(bucket)
	if err != nil {
		return 0, err
	}
	defer sc.Close()

	dstTx, err := dstDB.BeginRw(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if dstTx != nil {
			dstTx.Rollback()
		}
	}()

	var total uint64
	rows := 0

	// Cursor-per-batch pattern: keep one RwCursor open between commits.
	var dc kv.RwCursor
	var ddc kv.RwCursorDupSort
	openCursors := func() error {
		var e error
		if dup {
			ddc, e = dstTx.RwCursorDupSort(bucket)
		} else {
			dc, e = dstTx.RwCursor(bucket)
		}
		return e
	}
	closeCursors := func() {
		if dc != nil {
			dc.Close()
			dc = nil
		}
		if ddc != nil {
			ddc.Close()
			ddc = nil
		}
	}
	put := func(k, v []byte) error {
		if dup {
			return ddc.AppendDup(k, v)
		}
		return dc.Append(k, v)
	}
	maybeCommit := func() error {
		rows++
		total++
		if rows < commitEvery {
			return nil
		}
		rows = 0
		closeCursors()
		if err := dstTx.Commit(); err != nil {
			return err
		}
		var e error
		dstTx, e = dstDB.BeginRw(ctx)
		if e != nil {
			return e
		}
		return openCursors()
	}
	if err := openCursors(); err != nil {
		return 0, err
	}

	walk := func(start []byte, stop func(k []byte) bool) error {
		var k, v []byte
		var err error
		if start == nil {
			k, v, err = sc.First()
		} else {
			k, v, err = sc.Seek(start)
		}
		for ; k != nil; k, v, err = sc.Next() {
			if err != nil {
				return err
			}
			if stop != nil && stop(k) {
				return nil
			}
			if err := put(k, v); err != nil {
				return err
			}
			if err := maybeCommit(); err != nil {
				return err
			}
			if total%8_000_000 == 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}
		}
		return nil
	}

	if from == nil {
		if err := walk(nil, nil); err != nil {
			return total, err
		}
	} else {
		if withGenesis {
			zero := make([]byte, 8)
			if err := walk(zero, func(k []byte) bool {
				return len(k) < 8 || binary.BigEndian.Uint64(k[:8]) != 0
			}); err != nil {
				return total, err
			}
		}
		if err := walk(from, nil); err != nil {
			return total, err
		}
	}
	closeCursors()
	err = dstTx.Commit()
	dstTx = nil
	return total, err
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
