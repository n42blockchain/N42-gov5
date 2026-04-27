// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// db-stats subcommand: print MDBX table stats + freezer segment stats
// for an N42 datadir. Mirrors `reth db stats` plus N42-specific extras.

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// NOTE — db-stats intentionally does NOT import the freezer package.
// Every Open/EnsureTable path in that package leads to alignOnResume
// (freezer.go:267-280), which auto-truncates tables down to the
// smallest item count. db-stats reads cidx + cdat directly via os.Open
// (O_RDONLY) so it can never trigger that destruction.

func runDBStats(c *cli.Context) error {
	datadir := c.String("datadir")
	if datadir == "" {
		return fmt.Errorf("--datadir is required")
	}
	hideEmpty := c.Bool("hide-empty")
	sortBy := c.String("sort")
	if sortBy == "" {
		sortBy = "size"
	}

	if !c.Bool("no-progress") {
		dbStatsPrintProgress(datadir)
		fmt.Println()
	}

	var freezerTotal uint64
	if !c.Bool("no-freezer") {
		var dirs []string
		if override := c.String("freezer"); override != "" {
			dirs = []string{override}
		} else {
			dirs = dbStatsDiscoverFreezerDirs(datadir)
		}
		if len(dirs) == 0 {
			fmt.Fprintln(os.Stderr, "freezer: no .cidx files found under datadir")
		}
		withDecoded := c.Bool("with-decoded")
		for _, d := range dirs {
			t, err := dbStatsPrintFreezer(d, hideEmpty, sortBy, withDecoded)
			if err != nil {
				fmt.Fprintf(os.Stderr, "freezer %s: %v\n", d, err)
			}
			freezerTotal += t
			fmt.Println()
		}
	}
	var mdbxFileSize, mdbxInTree uint64
	if !c.Bool("no-mdbx") {
		f, t, err := dbStatsPrintMDBX(datadir, hideEmpty, sortBy)
		if err != nil {
			return err
		}
		mdbxFileSize, mdbxInTree = f, t
		fmt.Println()
	}

	// Grand summary.
	fmt.Println("=== datadir summary ===")
	fmt.Printf("freezer on-disk:     %s\n", dbStatsHumanBytes(freezerTotal))
	fmt.Printf("mdbx file:           %s  (in-tree %s, free %s)\n",
		dbStatsHumanBytes(mdbxFileSize),
		dbStatsHumanBytes(mdbxInTree),
		dbStatsHumanBytes(mdbxFileSize-mdbxInTree))
	fmt.Printf("datadir total:       %s\n", dbStatsHumanBytes(freezerTotal+mdbxFileSize))
	return nil
}

// dbStatsDiscoverFreezerDirs walks datadir up to a small depth and returns
// every directory containing at least one *.cidx file. N42 stores the main
// changeset/witness tables in chain/freezer/ but also keeps codes (and
// potentially other tables in future) under chain/ directly, so a single
// hardcoded path misses real data. Scan depth=4 is enough to cover
// chain/freezer/, chain/, datadir/, plus one safety level.
func dbStatsDiscoverFreezerDirs(datadir string) []string {
	const maxDepth = 4
	seen := make(map[string]struct{})
	var roots []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		hasCidx := false
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if dbStatsIsCidxFile(n) {
				hasCidx = true
				break
			}
		}
		if hasCidx {
			abs, _ := filepath.Abs(dir)
			if _, ok := seen[abs]; !ok {
				seen[abs] = struct{}{}
				roots = append(roots, dir)
			}
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Skip mdbx-internal lock dirs.
			n := e.Name()
			if n == "mdbx-lck" || strings.HasPrefix(n, ".") {
				continue
			}
			walk(filepath.Join(dir, n), depth+1)
		}
	}
	walk(datadir, 0)
	sort.Strings(roots)
	return roots
}

// ---------- Progress ----------

func dbStatsPrintProgress(datadir string) {
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		Readonly().
		Accede().
		Open(context.Background())
	if err != nil {
		fmt.Printf("Progress: (mdbx unavailable: %v)\n", err)
		return
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return
	}
	defer tx.Rollback()

	keys := []string{"ethel-last-block", "Headers", "Bodies", "Senders", "Execution"}
	fmt.Println("Progress markers (table=SyncStage):")
	any := false
	for _, key := range keys {
		v, err := tx.GetOne(kv.SyncStageProgress, []byte(key))
		if err != nil || len(v) == 0 {
			continue
		}
		var n uint64
		if len(v) == 8 {
			n = binary.BigEndian.Uint64(v)
		}
		fmt.Printf("  %-24s = %s\n", key, dbStatsCommaInt(n))
		any = true
	}
	if !any {
		fmt.Println("  (no progress markers found)")
	}
}

// ---------- Freezer ----------

// dbStatsIsCidxFile reports whether `name` matches the freezer index
// file pattern: {table}.{ext}idx where ext is one letter (typically 'c').
// Examples that match: "headers.cidx", "storcs.cidx".
// Examples that don't: "abc.txt", "foo.idx" (no ext char).
func dbStatsIsCidxFile(name string) bool {
	if !strings.HasSuffix(name, "idx") || len(name) < 6 {
		return false
	}
	base := name[:len(name)-3]
	dot := strings.LastIndex(base, ".")
	return dot > 0 && dot == len(base)-2
}

// dbStatsDiscoverFreezerTables scans `dir` for *.cidx files and returns
// the table names (extension is "c" for normal tables; we don't yet
// support detecting compressed-only tables with a different ext).
func dbStatsDiscoverFreezerTables(dir string) []struct{ name, ext string } {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []struct{ name, ext string }
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !dbStatsIsCidxFile(n) {
			continue
		}
		base := n[:len(n)-3]
		dot := strings.LastIndex(base, ".")
		out = append(out, struct{ name, ext string }{base[:dot], base[dot+1:]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

type freezerRow struct {
	name string
	ext  string
	// cidxEntries is the raw cidx entry count (file size / per-table
	// entry size). For 6 B CompactTable tables this == block count;
	// for 8 B body/header_compact and 12 B SegmentStore tables it's the
	// segment count, with each entry covering blocksPerEntry blocks.
	cidxEntries uint64
	// items is the logical block-equivalent count (cidxEntries *
	// blocksPerEntry). For 1:1 tables this matches cidxEntries; for
	// segmented tables it's the (claimed) block coverage. db-stats
	// renders this in the "Items" column because the operator usually
	// thinks in blocks, not segments.
	items     uint64
	size      uint64
	avgSize   float64
	segments  int    // number of NNNN.{ext}dat segment files
	dictBytes uint64 // total size of zdict dictionaries (compressed tables)
	// avgSegBytes is total cdat bytes / segments — useful when a table's
	// per-cidx-entry size is misleading (e.g. shard tables where the cidx
	// is small but each entry is a fat shard).
	avgSegBytes uint64
	// Decoded fan-out estimate via sampling. For per-block list tables
	// (senders, bodies, receipts), this is total post-zstd element count
	// extrapolated from N samples. -1 = 1:1 table (no fan-out); 0 = could
	// not estimate (e.g. unsupported codec).
	decodedTotal int64
	decodedNote  string // unit label ("txs", "addrs", ...)
	// kind classifies the cidx semantics so the operator can read the row
	// without guessing: "block" (1 cidx = 1 block), "key" (1 cidx = 1
	// hash/key), "shard" (1 cidx = 1 RecSplit history shard).
	kind string
}

// dbStatsEstimateDecoded — REMOVED.
//
// The previous implementation called freezer.FreezerTable.Retrieve()
// to sample-decode batched blobs. Reaching that API requires opening
// the freezer through one of the constructors that goes through the
// RW path (`freezer.New`, `EnsureTable`, `EnsureTableCompressed`).
// All of those run `openFreezer.alignOnResume` which auto-truncates
// every opened table down to the smallest item count — destroying
// per-table data on datadirs where heights are intentionally
// staggered. We hit that on d:\N42-eth1.
//
// To re-enable a sampling estimator safely, build it on a strict
// O_RDONLY zstd-frame walker (see cmd/cdat-scan and cmd/cidx-inspect)
// that never goes through the freezer package's tables.

// dbStatsClassifyTable returns the cidx semantics for a known table.
// Affects how Block Range is rendered (per-block tables show 0..=N;
// shard/key tables show count only).
func dbStatsClassifyTable(name string) string {
	switch name {
	case "headers", "bodies", "receipts", "senders",
		"acctcs", "storcs", "witness":
		return "block"
	case "codes":
		return "key"
	case "accthist", "storhist", "txindex":
		return "shard"
	}
	return "unknown"
}

// dbStatsEntrySize returns the byte size of one cidx entry for the named
// table. Different writers use different layouts:
//
//   - 8 B: bodies / headers — produced by internal/ethel/{body,header}_compact.go
//     (encodeBodyIdx / encodeHeaderIdx, layout [fn:2 LE][rsvd:2][off:4 LE]).
//   - 12 B: accthist / storhist / txindex — produced by internal/cscompact/
//     segment_store.go (segIdxEntrySize=12, layout
//     [fn:2 LE][flags:2 LE][datOff:4 LE][riOff:4 LE]).
//   - 6 B: everything else (CompactTable / freezer.encodeIndex), layout
//     [fn:2 BE][off:4 BE].
//
// db-stats used to assume 6 universally — that produced size/6 = bogus
// "items" counts for the 8 B and 12 B tables (e.g. bodies.cidx 24 KB
// reported as 4000 entries when the real count is 24000/8 = 3000).
func dbStatsEntrySize(name string) int {
	switch name {
	case "bodies", "headers":
		return 8
	case "accthist", "storhist", "txindex":
		return 12
	}
	return 6
}

// dbStatsBlocksPerEntry returns the logical block coverage of one cidx
// entry. For block-tier columnar tables (bodies/headers) one entry
// indexes a full HeaderSegmentSize=8192 block segment. For shard-tier
// SegmentStore tables (accthist/storhist/txindex) one entry indexes a
// full 1,000,000-block history shard. For everything else (the CompactTable
// per-block tables) one entry is one block, so we return 1.
//
// Sources of the magic numbers:
//
//   - internal/ethel/header_compact.go:43 — HeaderSegmentSize = 8192
//   - internal/cscompact/history_segment.go:22 — HistSegmentSize = 1_000_000
//   - internal/txlookup/segment.go:29 — SegmentSize = 1_000_000
func dbStatsBlocksPerEntry(name string) uint64 {
	switch name {
	case "bodies", "headers":
		return 8192
	case "accthist", "storhist", "txindex":
		return 1_000_000
	}
	return 1
}

// dbStatsCountBodyTxs / dbStatsCountReceipts / dbStatsSampleAvg —
// REMOVED for the same reason as dbStatsEstimateDecoded above.

func dbStatsPrintFreezer(dir string, hideEmpty bool, sortBy string, withDecoded bool) (uint64, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, fmt.Errorf("freezer dir not accessible: %w", err)
	}
	specs := dbStatsDiscoverFreezerTables(dir)
	if len(specs) == 0 {
		return 0, fmt.Errorf("no freezer cidx files found in %s", dir)
	}

	// CRITICAL — db-stats DOES NOT open the freezer via any
	// `freezer.*` constructor. Both `freezer.New()` and the legacy code
	// path through `EnsureTable()` open the cidx files in O_RDWR. That
	// triggers `openFreezer`'s `alignOnResume` block (freezer.go:267-280)
	// which TRUNCATES every table down to the smallest per-table item
	// count of any opened table.
	//
	// On a datadir where tables LEGITIMATELY have different heights —
	// e.g. senders pre-built for 24M blocks while the executor only
	// processed 4K — that alignment DESTROYS the larger tables' cidx
	// files. We hit exactly that on d:\N42-eth1 today. Do not let
	// db-stats touch any RW freezer entry point.
	rows := make([]freezerRow, 0, len(specs))
	var totalBytes, totalDict uint64
	for _, spec := range specs {
		size, segs, dictBytes := dbStatsFreezerTableInfo(dir, spec.name, spec.ext)
		cidxEntries := dbStatsCountCidxEntries(dir, spec.name, spec.ext)
		blockEquiv := cidxEntries * dbStatsBlocksPerEntry(spec.name)

		totalBytes += size
		totalDict += dictBytes
		// avgSize is bytes per CIDX ENTRY (not per block). For shard /
		// block-segmented tables that ratio is always large by design;
		// we don't divide by blockEquiv because the cdat content is
		// chunked by segment, not by block.
		var avg float64
		if cidxEntries > 0 {
			avg = float64(size) / float64(cidxEntries)
		}
		var avgSeg uint64
		if segs > 0 {
			avgSeg = size / uint64(segs)
		}
		row := freezerRow{
			name:        spec.name,
			ext:         spec.ext,
			cidxEntries: cidxEntries,
			items:       blockEquiv,
			size:        size,
			avgSize:     avg,
			segments:    segs,
			dictBytes:   dictBytes,
			avgSegBytes: avgSeg,
			kind:        dbStatsClassifyTable(spec.name),
		}
		// --with-decoded is intentionally skipped here while we audit the
		// freezer-touch code paths. It used to call tbl.SetCompressed +
		// tbl.ForceBatchSize before sampling, which forces a per-table
		// header patch and could overwrite the originally-stored batch
		// size on tables that use a non-default value. Re-enable only
		// after we have a strictly-read-only Retrieve path.
		_ = withDecoded
		rows = append(rows, row)
	}

	if hideEmpty {
		filtered := rows[:0]
		for _, r := range rows {
			// items==0 means "no real data" — the cidx header file is
			// always 16 B even on a brand-new empty table, so size>0
			// alone is misleading. Use items as the truth for freezer.
			if r.items > 0 {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	sort.Slice(rows, func(i, j int) bool {
		switch sortBy {
		case "name":
			return rows[i].name < rows[j].name
		case "items", "entries":
			return rows[i].items > rows[j].items
		default: // "size"
			return rows[i].size > rows[j].size
		}
	})

	fmt.Printf("Freezer at %s\n", dir)
	header1 := "| Table       | Kind  | Items          | Block Range          | Segs | Avg/seg     | On-disk Size | Avg/cidx | Dict "
	header2 := "|-------------|-------|----------------|----------------------|------|-------------|--------------|----------|------"
	if withDecoded {
		header1 += "| Decoded est. (sampled) "
		header2 += "|------------------------"
	}
	fmt.Println(header1 + "|")
	fmt.Println(header2 + "|")
	for _, r := range rows {
		// Range string differs by kind:
		//   block  → 0..=items-1 (cidx is per-block)
		//   key    → "—" (cidx is per-key, no block semantics)
		//   shard  → "—" (cidx is per-shard; a shard covers many blocks
		//                 but the freezer doesn't store the boundary
		//                 here — RecSplit holds it separately)
		//   unknown → 0..=items-1 (best guess)
		rangeStr := "—"
		switch r.kind {
		case "block", "unknown":
			if r.items > 0 {
				rangeStr = fmt.Sprintf("0..=%s", dbStatsCommaInt(r.items-1))
			} else {
				rangeStr = "(empty)"
			}
		}
		dictStr := "-"
		if r.dictBytes > 0 {
			dictStr = dbStatsHumanBytes(r.dictBytes)
		}
		line := fmt.Sprintf("| %-11s | %-5s | %14s | %-20s | %4d | %11s | %12s | %8s | %4s ",
			r.name,
			r.kind,
			dbStatsCommaInt(r.items),
			rangeStr,
			r.segments,
			dbStatsHumanBytes(r.avgSegBytes),
			dbStatsHumanBytes(r.size),
			dbStatsHumanBytesF(r.avgSize),
			dictStr)
		if withDecoded {
			decStr := "—"
			switch {
			case r.decodedTotal == -1:
				decStr = "—" // 1:1 table (no fan-out)
			case r.decodedTotal > 0:
				decStr = fmt.Sprintf("~%s %s", dbStatsCommaInt(uint64(r.decodedTotal)), r.decodedNote)
			}
			line += fmt.Sprintf("| %-22s ", decStr)
		}
		fmt.Println(line + "|")
	}
	fmt.Println(header2 + "|")
	dictTotal := "-"
	if totalDict > 0 {
		dictTotal = dbStatsHumanBytes(totalDict)
	}
	totalLine := fmt.Sprintf("| %-11s | %-5s | %14s | %-20s | %4s | %11s | %12s | %8s | %4s ",
		"Total", "", "", "", "", "", dbStatsHumanBytes(totalBytes), "", dictTotal)
	if withDecoded {
		totalLine += fmt.Sprintf("| %-22s ", "")
	}
	fmt.Println(totalLine + "|")

	return totalBytes, nil
}

// dbStatsCountCidxEntries reports the cidx entry count by stat'ing the
// cidx file alone — STRICTLY READ-ONLY. Picks the right per-entry size
// based on the table name (see dbStatsEntrySize) and probes for the
// optional N42 16-byte 'CIDX' header.
//
// We do NOT use FreezerTable.Items() here because that requires opening
// the table, which on the writer-side path patches the cidx header.
func dbStatsCountCidxEntries(dir, name, ext string) uint64 {
	cidxPath := filepath.Join(dir, fmt.Sprintf("%s.%sidx", name, ext))
	f, err := os.OpenFile(cidxPath, os.O_RDONLY, 0)
	if err != nil {
		return 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0
	}
	size := info.Size()
	if size <= 0 {
		return 0
	}
	entrySize := int64(dbStatsEntrySize(name))
	// Probe for N42 magic 'CIDX' at byte 0; if present, skip the 16B header.
	// (Currently only used by 6 B CompactTable cidx; harmless to probe on
	// the wider entry sizes since their first 4 bytes are never "CIDX".)
	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err == nil && string(magic[:]) == "CIDX" {
		return uint64((size - 16) / entrySize)
	}
	return uint64(size / entrySize)
}

// dbStatsFreezerTableInfo returns total size, segment count, and zdict
// dictionary bytes for a freezer table. Files counted:
//   - {name}.{ext}idx       (1 cidx index file)
//   - {name}.NNNN.{ext}dat  (zero or more cdat data segments)
//   - {name}.NNNN.zdict     (per-segment zstd compression dictionary, if any)
func dbStatsFreezerTableInfo(dir, name, ext string) (size uint64, segments int, dictBytes uint64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, 0
	}
	cdatPrefix := name + "."
	cdatSuffix := "." + ext + "dat"
	cidxName := name + "." + ext + "idx"
	zdictSuffix := ".zdict"

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasPrefix(n, cdatPrefix) && n != cidxName {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		switch {
		case n == cidxName:
			size += uint64(info.Size())
		case strings.HasSuffix(n, cdatSuffix):
			size += uint64(info.Size())
			segments++
		case strings.HasSuffix(n, zdictSuffix):
			dictBytes += uint64(info.Size())
			// Dict bytes are NOT added to `size` — they're metadata, not
			// the actual data. Reported separately so an operator can spot
			// dictionary bloat (e.g. dict-per-segment regression).
		}
	}
	return size, segments, dictBytes
}

// ---------- MDBX ----------

type mdbxRow struct {
	name    string
	entries uint64
	branch  uint64
	leaf    uint64
	over    uint64
	size    uint64
	avgSize float64
}

func dbStatsPrintMDBX(datadir string, hideEmpty bool, sortBy string) (uint64, uint64, error) {
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		Readonly().
		Accede().
		Open(context.Background())
	if err != nil {
		return 0, 0, fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return 0, 0, fmt.Errorf("begin ro: %w", err)
	}
	defer tx.Rollback()

	mtx, ok := tx.(*mdbx.MdbxTx)
	if !ok {
		return 0, 0, fmt.Errorf("expected *mdbx.MdbxTx, got %T", tx)
	}

	var rows []mdbxRow
	var totalSize, totalEntries uint64

	tables := dbStatsKnownTables()
	sort.Strings(tables)

	const pageSize uint64 = 4096

	for _, name := range tables {
		if name == "freelist" || name == "gc" || name == "free_list" || name == "root" {
			continue
		}
		st, err := mtx.BucketStat(name)
		if err != nil || st == nil {
			continue
		}
		size := (st.BranchPages + st.LeafPages + st.OverflowPages) * pageSize
		totalSize += size
		totalEntries += st.Entries
		var avg float64
		if st.Entries > 0 {
			avg = float64(size) / float64(st.Entries)
		}
		rows = append(rows, mdbxRow{
			name:    name,
			entries: st.Entries,
			branch:  st.BranchPages,
			leaf:    st.LeafPages,
			over:    st.OverflowPages,
			size:    size,
			avgSize: avg,
		})
	}

	if hideEmpty {
		filtered := rows[:0]
		for _, r := range rows {
			if r.size > 0 || r.entries > 0 {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	sort.Slice(rows, func(i, j int) bool {
		switch sortBy {
		case "name":
			return rows[i].name < rows[j].name
		case "items", "entries":
			return rows[i].entries > rows[j].entries
		default: // "size"
			return rows[i].size > rows[j].size
		}
	})

	dbFile := filepath.Join(datadir, "mdbx.dat")
	var fileSize uint64
	if info, err := os.Stat(dbFile); err == nil {
		fileSize = uint64(info.Size())
		fmt.Printf("MDBX at %s (file size %s)\n", dbFile, dbStatsHumanBytes(fileSize))
	} else {
		fmt.Printf("MDBX at %s\n", datadir)
	}
	fmt.Println("| Table                      | # Entries      | Branch    | Leaf       | Overflow   | Total Size  | Avg/entry |")
	fmt.Println("|----------------------------|----------------|-----------|------------|------------|-------------|-----------|")
	for _, r := range rows {
		fmt.Printf("| %-26s | %14s | %9s | %10s | %10s | %11s | %9s |\n",
			r.name,
			dbStatsCommaInt(r.entries),
			dbStatsCommaInt(r.branch),
			dbStatsCommaInt(r.leaf),
			dbStatsCommaInt(r.over),
			dbStatsHumanBytes(r.size),
			dbStatsHumanBytesF(r.avgSize))
	}
	fmt.Println("|----------------------------|----------------|-----------|------------|------------|-------------|-----------|")
	fmt.Printf("| %-26s | %14s | %9s | %10s | %10s | %11s | %9s |\n",
		"Total (in-tree)",
		dbStatsCommaInt(totalEntries),
		"", "", "",
		dbStatsHumanBytes(totalSize),
		"")

	if gc, err := mtx.BucketStat("gc"); err == nil && gc != nil {
		gcSize := (gc.LeafPages + gc.BranchPages) * pageSize
		fmt.Printf("\nFree-list (gc): pages=%s, entries=%s, size=%s\n",
			dbStatsCommaInt(gc.LeafPages+gc.BranchPages),
			dbStatsCommaInt(gc.Entries),
			dbStatsHumanBytes(gcSize))
	}

	return fileSize, totalSize, nil
}

func dbStatsKnownTables() []string {
	out := make([]string, 0, len(kv.ChaindataTablesCfg))
	for name := range kv.ChaindataTablesCfg {
		out = append(out, name)
	}
	return out
}

// ---------- Formatting helpers ----------

func dbStatsHumanBytes(n uint64) string {
	return datasize.ByteSize(n).HumanReadable()
}

func dbStatsHumanBytesF(n float64) string {
	if n <= 0 {
		return "-"
	}
	return datasize.ByteSize(uint64(n)).HumanReadable()
}

// dbStatsCommaInt formats a uint64 with thousand-separator commas.
func dbStatsCommaInt(n uint64) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	parts := make([]byte, 0, len(s)+len(s)/3)
	pre := len(s) % 3
	if pre > 0 {
		parts = append(parts, s[:pre]...)
		if len(s) > pre {
			parts = append(parts, ',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		parts = append(parts, s[i:i+3]...)
		if i+3 < len(s) {
			parts = append(parts, ',')
		}
	}
	return string(parts)
}
